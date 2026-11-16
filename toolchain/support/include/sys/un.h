/* UNIX-domain socket addresses for wasm32-wasip1. wasi-libc's sockaddr_un
 * has no sun_path, since WASI has no such sockets; this one has it, so code
 * that mentions them compiles. Using one still fails (EAFNOSUPPORT). */
#ifndef PYWASI_SYS_UN_H
#define PYWASI_SYS_UN_H

#include <sys/socket.h>

/* Instead of wasi-libc's. */
#define __wasilibc___struct_sockaddr_un_h
struct sockaddr_un {
    sa_family_t sun_family;
    char sun_path[108];
};

#endif
