/*
 * Entry point for pywasi's python.wasm.
 *
 * Registers the statically linked extension modules and supports Wizer
 * pre-initialization. Extensions register themselves: every package's link
 * inputs include an object whose constructor calls pywasi_register() for
 * each of its modules (see toolchain/pywasi_build.py), so this file is the
 * same for any set of packages and needs no compiling per build.
 *
 *
 *   wizer-initialize  run once at build time under `wasmtime wizer`: start
 *                     the interpreter and import the preload modules. The
 *                     resulting memory is snapshotted into the module.
 *   pywasi-resume     becomes `_start` in the snapshot. Skips C constructors
 *                     (already run), refreshes per-process WASI state and
 *                     hands argv/environ to pywasi_snapshot.resume().
 *
 * Without a snapshot, main() is ordinary CPython startup.
 */
#include <Python.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <wasi/libc-environ.h>
#include <wasi/libc.h>

#include "pywasi_snapshot_py.h"

extern void __wasm_call_ctors(void);
extern int __main_void(void);
extern char **environ;

/* Extension modules registered by constructors, NULL-terminated. */
static struct _inittab *registered;
static size_t nregistered;

void pywasi_register(const char *name, PyObject *(*init)(void)) {
    struct _inittab *grown = realloc(registered, (nregistered + 2) * sizeof *registered);
    if (!grown) {
        abort();
    }
    registered = grown;
    registered[nregistered++] = (struct _inittab){name, init};
    registered[nregistered] = (struct _inittab){NULL, NULL};
}

static int register_builtins(void) {
    return nregistered ? PyImport_ExtendInittab(registered) : 0;
}

/* Pre-initializes CPython with mimalloc as the memory allocator unless
 * PYTHONMALLOC says otherwise. CPython only defaults to mimalloc in
 * free-threaded builds and leaves pymalloc out on WASI, so it would use
 * wasi-libc's dlmalloc for every object: mimalloc is 5-20% faster on
 * object-heavy code. argv is parsed as Python's command line (NULL for
 * none), so its options that affect pre-initialization still apply. */
static void preinitialize(int argc, char **argv) {
    PyPreConfig preconfig;
    PyPreConfig_InitPythonConfig(&preconfig);
    const char *env = getenv("PYTHONMALLOC");
    if (!env || !*env) {
        preconfig.allocator = PYMEM_ALLOCATOR_MIMALLOC;
    }
    PyStatus status = argv ? Py_PreInitializeFromBytesArgs(&preconfig, argc, argv) : Py_PreInitialize(&preconfig);
    if (PyStatus_Exception(status)) {
        Py_ExitStatusException(status);
    }
}

/* Set by wizer-initialize; non-NULL means we're running from a snapshot. */
static PyObject *resume_fn;

static _Noreturn void fatal(const char *what) {
    if (PyErr_Occurred()) {
        PyErr_Print();
    }
    fprintf(stderr, "pywasi: %s\n", what);
    abort();
}

__attribute__((export_name("wizer-initialize"))) void pywasi_initialize(void) {
    __wasm_call_ctors();
    if (register_builtins() < 0) {
        fatal("registering built-in extensions failed");
    }

    preinitialize(0, NULL);
    PyConfig config;
    PyConfig_InitPythonConfig(&config);
    PyStatus status = Py_InitializeFromConfig(&config);
    PyConfig_Clear(&config);
    if (PyStatus_Exception(status)) {
        Py_ExitStatusException(status);
    }

    PyObject *ns = PyDict_New();
    if (!ns || PyDict_SetItemString(ns, "__builtins__", PyEval_GetBuiltins()) < 0) {
        fatal("snapshot setup failed");
    }
    PyObject *r = PyRun_String(pywasi_snapshot_py, Py_file_input, ns, ns);
    if (!r) {
        fatal("loading pywasi_snapshot failed");
    }
    Py_DECREF(r);

    const char *preload = getenv("PYWASI_PRELOAD");
    PyObject *init = PyDict_GetItemString(ns, "snapshot_init");
    r = PyObject_CallFunction(init, "s", preload ? preload : "");
    if (!r) {
        fatal("preloading modules failed");
    }
    Py_DECREF(r);

    resume_fn = PyDict_GetItemString(ns, "resume");
    Py_XINCREF(resume_fn);
    if (!resume_fn) {
        fatal("pywasi_snapshot.resume missing");
    }
}

/* Changes to the working directory in $PYWASI_CWD, if set, and removes it
 * from the environment. WASI has no inherited working directory, so the host
 * passes one this way (pywasi.RunOptions.Dir). */
static void enter_working_directory(void) {
    const char *dir = getenv("PYWASI_CWD");
    if (!dir) {
        return;
    }
    if (*dir && chdir(dir) != 0) {
        fprintf(stderr, "pywasi: can't change to working directory %s\n", dir);
        exit(1);
    }
    unsetenv("PYWASI_CWD");
}

extern void __wasm_call_dtors(void);

/* Same as wasi-libc's _start, minus constructors (they ran before the
 * snapshot). proc_exit only for a nonzero status, as some runtimes make
 * exiting through it slow. */
__attribute__((export_name("pywasi-resume"))) void pywasi_resume_start(void) {
    int status = __main_void();
    __wasm_call_dtors();
    if (status != 0) {
        exit(status);
    }
}

int main(int argc, char **argv) {
    if (!resume_fn) {
        if (register_builtins() < 0) {
            return 1;
        }
        enter_working_directory();
        preinitialize(argc, argv);
        return Py_BytesMain(argc, argv);
    }

    /* The snapshot holds the build machine's preopened directories and
     * environment. Drop them so wasi-libc re-reads this run's. */
    __wasilibc_reset_preopens();
    __wasilibc_deinitialize_environ();
    __wasilibc_ensure_environ();
    enter_working_directory();

    PyObject *args = PyList_New(0);
    PyObject *env = PyDict_New();
    if (!args || !env) {
        fatal("out of memory");
    }
    for (int i = 0; i < argc; i++) {
        PyObject *a = PyUnicode_DecodeFSDefault(argv[i]);
        if (!a || PyList_Append(args, a) < 0) {
            fatal("decoding argv failed");
        }
        Py_DECREF(a);
    }
    for (char **e = environ; e && *e; e++) {
        const char *eq = strchr(*e, '=');
        if (!eq) {
            continue;
        }
        PyObject *k = PyBytes_FromStringAndSize(*e, eq - *e);
        PyObject *v = PyBytes_FromString(eq + 1);
        if (!k || !v || PyDict_SetItem(env, k, v) < 0) {
            fatal("reading environ failed");
        }
        Py_DECREF(k);
        Py_DECREF(v);
    }

    int code = 0;
    PyObject *r = PyObject_CallFunctionObjArgs(resume_fn, args, env, NULL);
    if (r) {
        Py_DECREF(r);
    } else {
        /* Exits the process for SystemExit, prints a traceback otherwise. */
        PyErr_Print();
        code = 1;
    }
    Py_DECREF(args);
    Py_DECREF(env);
    if (Py_FinalizeEx() < 0) {
        code = 120;
    }
    return code;
}
