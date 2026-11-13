/*
 * C++ code compiled with -fignore-exceptions still emits calls to the
 * throw half of the Itanium ABI. The no-exceptions libc++abi in wasi-sdk
 * doesn't provide it, and we avoid the wasm exception-handling proposal so
 * modules run on runtimes without it (wazero). A throw becomes an abort.
 */
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>

void *__cxa_allocate_exception(size_t size) {
    return malloc(size);
}

void __cxa_free_exception(void *ptr) {
    free(ptr);
}

_Noreturn void __cxa_throw(void *obj, void *tinfo, void (*dest)(void *)) {
    (void)obj;
    (void)tinfo;
    (void)dest;
    fputs("pywasi: C++ exception thrown (exceptions are disabled); aborting\n", stderr);
    abort();
}
