/* libffi for CPython's _ctypes on wasm32-wasip1 (toolchain/support/ffi/
 * ffi.h), and the registration of _ctypes, which configure leaves out for
 * WASI and toolchain/pywasi_build.py builds from CPython's Modules/_ctypes.
 *
 * Types are described as libffi does, which is all ctypes needs for
 * structures, unions, arrays and pointers. A wasm function can only be
 * called with its exact signature, known at compile time, so calls are
 * limited to those spelled out here: up to 8 arguments that are integers
 * of 32 bits or less, or pointers, returning nothing, one of those, a
 * 64-bit integer, a float or a double. That covers ctypes' own memmove,
 * memset and string_at. ffi_prep_cif refuses other signatures, and
 * variadic calls, so ctypes raises instead of calling. Closures (Python
 * callbacks as C function pointers) can't be made.
 */
#include <stdint.h>
#include <stdlib.h>

#include "ffi.h"

ffi_type ffi_type_void = {1, 1, FFI_TYPE_VOID, NULL};
ffi_type ffi_type_uint8 = {1, 1, FFI_TYPE_UINT8, NULL};
ffi_type ffi_type_sint8 = {1, 1, FFI_TYPE_SINT8, NULL};
ffi_type ffi_type_uint16 = {2, 2, FFI_TYPE_UINT16, NULL};
ffi_type ffi_type_sint16 = {2, 2, FFI_TYPE_SINT16, NULL};
ffi_type ffi_type_uint32 = {4, 4, FFI_TYPE_UINT32, NULL};
ffi_type ffi_type_sint32 = {4, 4, FFI_TYPE_SINT32, NULL};
ffi_type ffi_type_uint64 = {8, 8, FFI_TYPE_UINT64, NULL};
ffi_type ffi_type_sint64 = {8, 8, FFI_TYPE_SINT64, NULL};
ffi_type ffi_type_float = {4, 4, FFI_TYPE_FLOAT, NULL};
ffi_type ffi_type_double = {8, 8, FFI_TYPE_DOUBLE, NULL};
ffi_type ffi_type_longdouble = {16, 16, FFI_TYPE_LONGDOUBLE, NULL};
ffi_type ffi_type_pointer = {4, 4, FFI_TYPE_POINTER, NULL};

enum { RET_VOID, RET_WORD, RET_I64, RET_F32, RET_F64 };

#define MAX_ARGS 8

/* An argument passed as a 32-bit wasm value. */
static int word_arg(const ffi_type *t) {
    switch (t->type) {
    case FFI_TYPE_INT:
    case FFI_TYPE_UINT8:
    case FFI_TYPE_SINT8:
    case FFI_TYPE_UINT16:
    case FFI_TYPE_SINT16:
    case FFI_TYPE_UINT32:
    case FFI_TYPE_SINT32:
    case FFI_TYPE_POINTER:
        return 1;
    default:
        return 0;
    }
}

static int return_kind(const ffi_type *t) {
    switch (t->type) {
    case FFI_TYPE_VOID:
        return RET_VOID;
    case FFI_TYPE_UINT64:
    case FFI_TYPE_SINT64:
        return RET_I64;
    case FFI_TYPE_FLOAT:
        return RET_F32;
    case FFI_TYPE_DOUBLE:
        return RET_F64;
    default:
        return word_arg(t) ? RET_WORD : -1;
    }
}

ffi_status ffi_prep_cif(ffi_cif *cif, ffi_abi abi, unsigned int nargs, ffi_type *rtype, ffi_type **atypes) {
    if (abi != FFI_DEFAULT_ABI) return FFI_BAD_ABI;
    if (nargs > MAX_ARGS) return FFI_BAD_ARGTYPE;
    for (unsigned i = 0; i < nargs; i++) {
        if (!word_arg(atypes[i])) return FFI_BAD_ARGTYPE;
    }
    int kind = return_kind(rtype);
    if (kind < 0) return FFI_BAD_TYPEDEF;
    cif->abi = abi;
    cif->nargs = nargs;
    cif->arg_types = atypes;
    cif->rtype = rtype;
    cif->bytes = nargs * 4;
    cif->flags = (unsigned)kind;
    return FFI_OK;
}

ffi_status ffi_prep_cif_var(ffi_cif *cif, ffi_abi abi, unsigned int nfixedargs, unsigned int ntotalargs,
                            ffi_type *rtype, ffi_type **atypes) {
    /* wasm passes variadic arguments in memory, through a hidden pointer. */
    if (ntotalargs != nfixedargs) return FFI_BAD_ARGTYPE;
    return ffi_prep_cif(cif, abi, nfixedargs, rtype, atypes);
}

typedef uint32_t w;

#define CALLS(R, fn, a, out)                                                                       \
    switch (n) {                                                                                   \
    case 0: out ((R(*)(void))fn)(); break;                                                         \
    case 1: out ((R(*)(w))fn)(a[0]); break;                                                        \
    case 2: out ((R(*)(w, w))fn)(a[0], a[1]); break;                                               \
    case 3: out ((R(*)(w, w, w))fn)(a[0], a[1], a[2]); break;                                      \
    case 4: out ((R(*)(w, w, w, w))fn)(a[0], a[1], a[2], a[3]); break;                             \
    case 5: out ((R(*)(w, w, w, w, w))fn)(a[0], a[1], a[2], a[3], a[4]); break;                    \
    case 6: out ((R(*)(w, w, w, w, w, w))fn)(a[0], a[1], a[2], a[3], a[4], a[5]); break;           \
    case 7: out ((R(*)(w, w, w, w, w, w, w))fn)(a[0], a[1], a[2], a[3], a[4], a[5], a[6]); break;  \
    default:                                                                                       \
        out((R(*)(w, w, w, w, w, w, w, w))fn)(a[0], a[1], a[2], a[3], a[4], a[5], a[6], a[7]);     \
        break;                                                                                     \
    }

void ffi_call(ffi_cif *cif, void (*fn)(void), void *rvalue, void **avalue) {
    w a[MAX_ARGS] = {0};
    unsigned n = cif->nargs;
    for (unsigned i = 0; i < n; i++) {
        switch (cif->arg_types[i]->type) {
        case FFI_TYPE_UINT8: a[i] = *(uint8_t *)avalue[i]; break;
        case FFI_TYPE_SINT8: a[i] = (w)(int32_t)(*(int8_t *)avalue[i]); break;
        case FFI_TYPE_UINT16: a[i] = *(uint16_t *)avalue[i]; break;
        case FFI_TYPE_SINT16: a[i] = (w)(int32_t)(*(int16_t *)avalue[i]); break;
        default: a[i] = *(w *)avalue[i]; break;
        }
    }
    switch (cif->flags) {
    case RET_VOID: CALLS(void, fn, a, ) break;
    /* Integers narrower than ffi_arg are returned widened to it. */
    case RET_WORD: CALLS(w, fn, a, *(ffi_arg *)rvalue =) break;
    case RET_I64: CALLS(uint64_t, fn, a, *(uint64_t *)rvalue =) break;
    case RET_F32: CALLS(float, fn, a, *(float *)rvalue =) break;
    case RET_F64: CALLS(double, fn, a, *(double *)rvalue =) break;
    }
}

void *ffi_closure_alloc(size_t size, void **code) {
    void *closure = calloc(1, size);
    *code = closure;
    return closure;
}

void ffi_closure_free(void *closure) {
    free(closure);
}

ffi_status ffi_prep_closure_loc(ffi_closure *closure, ffi_cif *cif,
                                void (*fun)(ffi_cif *, void *, void **, void *), void *user_data, void *codeloc) {
    (void)closure, (void)cif, (void)fun, (void)user_data, (void)codeloc;
    return FFI_BAD_ABI;
}

ffi_status ffi_prep_closure(ffi_closure *closure, ffi_cif *cif,
                            void (*fun)(ffi_cif *, void *, void **, void *), void *user_data) {
    return ffi_prep_closure_loc(closure, cif, fun, user_data, closure);
}

extern void *PyInit__ctypes(void);
extern void pywasi_register(const char *name, void *(*init)(void));

__attribute__((constructor)) static void pywasi_register_ctypes(void) {
    pywasi_register("_ctypes", PyInit__ctypes);
}
