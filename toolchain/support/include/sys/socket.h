/* wasi-libc's <sys/socket.h>, plus the functions it leaves out on
 * wasm32-wasip1, which pywasi_socket.c provides. */
#ifndef PYWASI_SYS_SOCKET_H
#define PYWASI_SYS_SOCKET_H

#include_next <sys/socket.h>

#ifdef __cplusplus
extern "C" {
#endif

#ifdef __wasip1__
/* Options wasi-libc only defines for wasip2, with its values. */
#ifndef SO_REUSEADDR
#define SO_REUSEADDR 2
#define SO_ERROR 4
#define SO_BROADCAST 6
#define SO_SNDBUF 7
#define SO_RCVBUF 8
#define SO_KEEPALIVE 9
#define SO_REUSEPORT 15
#define SO_ACCEPTCONN 30
#define SO_DOMAIN 39
#endif
#ifndef SOMAXCONN
#define SOMAXCONN 128
#endif

int socket(int, int, int);
int socketpair(int, int, int, int[2]);
int bind(int, const struct sockaddr *, socklen_t);
int listen(int, int);
int connect(int, const struct sockaddr *, socklen_t);
int getsockname(int, struct sockaddr *__restrict, socklen_t *__restrict);
int getpeername(int, struct sockaddr *__restrict, socklen_t *__restrict);
int setsockopt(int, int, int, const void *, socklen_t);
#endif

#ifdef __cplusplus
}
#endif

#endif
