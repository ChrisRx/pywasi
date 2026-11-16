/* CPython's mmap module, which configure leaves out for WASI, built from
 * its own Modules/mmapmodule.c against wasi-libc's mmap emulation
 * (-D_WASI_EMULATED_MMAN, -lwasi-emulated-mman), and registered here.
 * The emulation copies a file into memory and writes nothing back, and
 * has no msync, so flush fails (ENOTSUP) rather than claim a write.
 */
#include <errno.h>
#include <stddef.h>

int msync(void *addr, size_t len, int flags) {
    (void)addr, (void)len, (void)flags;
    errno = ENOTSUP;
    return -1;
}

extern void *PyInit_mmap(void);
extern void pywasi_register(const char *name, void *(*init)(void));

__attribute__((constructor)) static void pywasi_register_mmap(void) {
    pywasi_register("mmap", PyInit_mmap);
}
