/* POSIX functions wasi-libc leaves out, for C libraries that call them
 * (DuckDB), with what they mean in a single wasm process:
 *
 * - madvise: advice, which memory with nothing behind it can ignore.
 * - mlock, munlock: wasm memory is never paged out, so it's locked.
 * - pthread_atfork: there is no fork, so the handlers never run.
 */
#include <stddef.h>

int madvise(void *addr, size_t len, int advice) {
    (void)addr, (void)len, (void)advice;
    return 0;
}

int mlock(const void *addr, size_t len) {
    (void)addr, (void)len;
    return 0;
}

int munlock(const void *addr, size_t len) {
    (void)addr, (void)len;
    return 0;
}

int pthread_atfork(void (*prepare)(void), void (*parent)(void), void (*child)(void)) {
    (void)prepare, (void)parent, (void)child;
    return 0;
}
