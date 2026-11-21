/* Sockets for wasm32-wasip1: the calls wasi-libc leaves out there (socket,
 * bind, listen, connect, sendto, recvfrom, name resolution, ...),
 * implemented by pywasi's host functions (module "pywasi", see
 * wasisock/sockets.go).
 * accept, recv, send, shutdown and poll stay WASI calls, which work on the
 * descriptors these return, TCP and UDP.
 *
 * Built with -DPYWASI_SOCKET_STUB, the host functions fail with ENOSYS
 * instead, for an interpreter that runs without pywasi (such as under
 * wasmtime during the build).
 */
#include <arpa/inet.h>
#include <errno.h>
#include <ifaddrs.h>
#include <net/if.h>
#include <fcntl.h>
#include <netdb.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <wasi/api.h>

/* An address as the host functions take it. */
typedef struct {
    uint16_t family; /* 4 or 6 */
    uint16_t port;
    uint32_t flowinfo;
    uint32_t scope_id;
    uint8_t addr[16];
} wire_addr;

/* Socket options, by the host's numbering. */
enum {
    OPT_REUSEADDR = 1,
    OPT_KEEPALIVE,
    OPT_NODELAY,
    OPT_RCVBUF,
    OPT_SNDBUF,
    OPT_ERROR,
    OPT_TYPE,
    OPT_DOMAIN,
    OPT_ACCEPTCONN,
    OPT_REUSEPORT,
    OPT_BROADCAST,
    OPT_V6ONLY,
};

/* resolve flags */
enum { RESOLVE_NUMERICHOST = 1, RESOLVE_PASSIVE = 2, RESOLVE_NUMERICSERV = 4 };

/* resolve and lookup results, other than 0 and errno values */
enum { RESOLVE_NONAME = 1000, RESOLVE_AGAIN, RESOLVE_FAIL, RESOLVE_SERVICE, RESOLVE_FAMILY };

/* The host module the functions come from: pywasi's, unless a program
 * that is not the interpreter builds the shim with its own, which the Go
 * package go.chrisrx.dev/pywasi/wasisock gives it (wasisock.Instantiate). */
#ifndef WASISOCK_MODULE
#define WASISOCK_MODULE "pywasi"
#endif

#ifdef PYWASI_SOCKET_STUB
#define HOST(name, params) static int32_t host_##name params { return ENOSYS; }
#else
#define HOST(name, params) \
    __attribute__((import_module(WASISOCK_MODULE), import_name("sock_" #name))) int32_t host_##name params;
#endif

HOST(open, (int32_t family, int32_t type, int32_t nonblock, int32_t *fd))
HOST(bind, (int32_t fd, const wire_addr *addr))
HOST(listen, (int32_t fd, int32_t backlog))
HOST(connect, (int32_t fd, const wire_addr *addr))
HOST(addr, (int32_t fd, int32_t peer, wire_addr *addr))
HOST(getopt, (int32_t fd, int32_t opt, int32_t *value))
HOST(setopt, (int32_t fd, int32_t opt, int32_t value))
HOST(resolve, (const char *host, int32_t host_len, const char *service, int32_t service_len,
               int32_t family, int32_t flags, wire_addr *addrs, int32_t cap, int32_t *n))
HOST(lookup, (const wire_addr *addr, char *name, int32_t cap, int32_t *len))
HOST(pair, (int32_t family, int32_t nonblock, int32_t *fds))
HOST(sendto, (int32_t fd, const void *buf, int32_t len, int32_t flags, const wire_addr *addr, int32_t *sent))
HOST(recvfrom, (int32_t fd, void *buf, int32_t len, int32_t flags, wire_addr *addr, int32_t *n))

/* Flags of sock_sendto and sock_recvfrom. */
enum {
    DGRAM_PEEK = 1,
    DGRAM_DONTWAIT = 2,
};

static int fail(int32_t err) {
    errno = err;
    return -1;
}

static int to_wire(const struct sockaddr *sa, socklen_t len, wire_addr *w) {
    memset(w, 0, sizeof *w);
    if (!sa) return EFAULT;
    if (sa->sa_family == AF_INET && len >= sizeof(struct sockaddr_in)) {
        const struct sockaddr_in *in = (const struct sockaddr_in *)sa;
        w->family = 4;
        w->port = ntohs(in->sin_port);
        memcpy(w->addr, &in->sin_addr, 4);
        return 0;
    }
    if (sa->sa_family == AF_INET6 && len >= sizeof(struct sockaddr_in6)) {
        const struct sockaddr_in6 *in6 = (const struct sockaddr_in6 *)sa;
        w->family = 6;
        w->port = ntohs(in6->sin6_port);
        w->flowinfo = in6->sin6_flowinfo;
        w->scope_id = in6->sin6_scope_id;
        memcpy(w->addr, &in6->sin6_addr, 16);
        return 0;
    }
    return sa->sa_family == AF_INET || sa->sa_family == AF_INET6 ? EINVAL : EAFNOSUPPORT;
}

/* Writes w to sa as far as *len allows, and sets *len to its full size. */
static void from_wire(const wire_addr *w, struct sockaddr *sa, socklen_t *len) {
    struct sockaddr_storage ss;
    socklen_t n;
    memset(&ss, 0, sizeof ss);
    if (w->family == 6) {
        struct sockaddr_in6 *in6 = (struct sockaddr_in6 *)&ss;
        in6->sin6_family = AF_INET6;
        in6->sin6_port = htons(w->port);
        in6->sin6_flowinfo = w->flowinfo;
        in6->sin6_scope_id = w->scope_id;
        memcpy(&in6->sin6_addr, w->addr, 16);
        n = sizeof *in6;
    } else {
        struct sockaddr_in *in = (struct sockaddr_in *)&ss;
        in->sin_family = AF_INET;
        in->sin_port = htons(w->port);
        memcpy(&in->sin_addr, w->addr, 4);
        n = sizeof *in;
    }
    if (sa && len) memcpy(sa, &ss, *len < n ? *len : n);
    if (len) *len = n;
}

int socket(int domain, int type, int protocol) {
    int nonblock = (type & SOCK_NONBLOCK) != 0;
    int32_t family, kind, fd, err;
    type &= ~(SOCK_NONBLOCK | SOCK_CLOEXEC);
    switch (domain) {
    case AF_INET: family = 4; break;
    case AF_INET6: family = 6; break;
    default: return fail(EAFNOSUPPORT);
    }
    switch (type) {
    case SOCK_STREAM:
        if (protocol != 0 && protocol != IPPROTO_TCP) return fail(EPROTONOSUPPORT);
        kind = 1;
        break;
    case SOCK_DGRAM:
        kind = 2;
        break;
    default: return fail(EPROTONOSUPPORT);
    }
    if ((err = host_open(family, kind, nonblock, &fd))) return fail(err);
    return fd;
}

/* A connected pair, for wakeups within the process (asyncio's self-pipe).
 * The host makes it from loopback TCP, which needs no network access, so
 * AF_UNIX gets one too. */
int socketpair(int domain, int type, int protocol, int fds[2]) {
    int nonblock = (type & SOCK_NONBLOCK) != 0;
    int32_t out[2], err;
    type &= ~(SOCK_NONBLOCK | SOCK_CLOEXEC);
    if (domain != AF_UNIX && domain != AF_INET && domain != AF_INET6) return fail(EAFNOSUPPORT);
    if (type != SOCK_STREAM || (protocol != 0 && protocol != IPPROTO_TCP)) return fail(EPROTONOSUPPORT);
    if ((err = host_pair(domain == AF_INET6 ? 6 : 4, nonblock, out))) return fail(err);
    fds[0] = out[0];
    fds[1] = out[1];
    return 0;
}

int bind(int fd, const struct sockaddr *addr, socklen_t len) {
    wire_addr w;
    int32_t err = to_wire(addr, len, &w);
    if (!err) err = host_bind(fd, &w);
    return err ? fail(err) : 0;
}

int listen(int fd, int backlog) {
    int32_t err = host_listen(fd, backlog);
    return err ? fail(err) : 0;
}

int connect(int fd, const struct sockaddr *addr, socklen_t len) {
    wire_addr w;
    int32_t err = to_wire(addr, len, &w);
    if (!err) err = host_connect(fd, &w);
    return err ? fail(err) : 0;
}

static int sock_addr(int fd, int peer, struct sockaddr *addr, socklen_t *len) {
    wire_addr w;
    int32_t err;
    if (!len) return fail(EFAULT);
    if ((err = host_addr(fd, peer, &w))) return fail(err);
    from_wire(&w, addr, len);
    return 0;
}

/* The flags for sock_sendto or sock_recvfrom: the host waits unless the
 * descriptor is non-blocking or the call says not to. */
static int dgram_flags(int fd, int flags, int32_t *out) {
    int fl;
    if (flags & ~(MSG_PEEK | MSG_DONTWAIT | MSG_NOSIGNAL)) return EOPNOTSUPP;
    *out = flags & MSG_PEEK ? DGRAM_PEEK : 0;
    if ((fl = fcntl(fd, F_GETFL)) < 0) return errno;
    if ((flags & MSG_DONTWAIT) || (fl & O_NONBLOCK)) *out |= DGRAM_DONTWAIT;
    return 0;
}

ssize_t sendto(int fd, const void *buf, size_t len, int flags, const struct sockaddr *addr, socklen_t addrlen) {
    wire_addr w;
    int32_t f, sent, err;
    if (!addr) return send(fd, buf, len, flags);
    if ((err = to_wire(addr, addrlen, &w)) || (err = dgram_flags(fd, flags, &f))) return fail(err);
    if ((err = host_sendto(fd, buf, len, f, &w, &sent))) return fail(err);
    return sent;
}

ssize_t recvfrom(int fd, void *__restrict buf, size_t len, int flags, struct sockaddr *__restrict addr,
                 socklen_t *__restrict addrlen) {
    wire_addr w;
    int32_t f, n, err;
    if ((err = dgram_flags(fd, flags, &f))) return fail(err);
    if ((err = host_recvfrom(fd, buf, len, f, &w, &n))) return fail(err);
    if (addr && addrlen) from_wire(&w, addr, addrlen);
    return n;
}

int getsockname(int fd, struct sockaddr *__restrict addr, socklen_t *__restrict len) {
    return sock_addr(fd, 0, addr, len);
}

int getpeername(int fd, struct sockaddr *__restrict addr, socklen_t *__restrict len) {
    return sock_addr(fd, 1, addr, len);
}

/* Replaces wasi-libc's accept and accept4 (which share an object file),
 * to fill in the peer address. */
int accept4(int fd, struct sockaddr *__restrict addr, socklen_t *__restrict len, int flags) {
    __wasi_fd_t conn;
    __wasi_errno_t err = __wasi_sock_accept(fd, (flags & SOCK_NONBLOCK) ? __WASI_FDFLAGS_NONBLOCK : 0, &conn);
    if (err) return fail(err);
    if (addr && len && sock_addr(conn, 1, addr, len) != 0) *len = 0;
    return conn;
}

int accept(int fd, struct sockaddr *__restrict addr, socklen_t *__restrict len) {
    return accept4(fd, addr, len, 0);
}

static int32_t option(int level, int name) {
    if (level == SOL_SOCKET) {
        switch (name) {
        case SO_REUSEADDR: return OPT_REUSEADDR;
        case SO_KEEPALIVE: return OPT_KEEPALIVE;
        case SO_RCVBUF: return OPT_RCVBUF;
        case SO_SNDBUF: return OPT_SNDBUF;
        case SO_ERROR: return OPT_ERROR;
        case SO_TYPE: return OPT_TYPE;
        case SO_DOMAIN: return OPT_DOMAIN;
        case SO_ACCEPTCONN: return OPT_ACCEPTCONN;
#ifdef SO_REUSEPORT
        case SO_REUSEPORT: return OPT_REUSEPORT;
#endif
#ifdef SO_BROADCAST
        case SO_BROADCAST: return OPT_BROADCAST;
#endif
        }
    } else if (level == IPPROTO_TCP && name == TCP_NODELAY) {
        return OPT_NODELAY;
    } else if (level == IPPROTO_IPV6 && name == IPV6_V6ONLY) {
        return OPT_V6ONLY;
    }
    return 0;
}

/* Replaces wasi-libc's getsockopt, which only knows SO_TYPE. */
int getsockopt(int fd, int level, int name, void *__restrict value, socklen_t *__restrict len) {
    int32_t opt = option(level, name), v, err;
    if (!opt) return fail(ENOPROTOOPT);
    if (!value || !len || *len < sizeof(int)) return fail(EINVAL);
    if ((err = host_getopt(fd, opt, &v))) return fail(err);
    switch (opt) {
    case OPT_TYPE: v = v == 2 ? SOCK_DGRAM : SOCK_STREAM; break;
    case OPT_DOMAIN: v = v == 6 ? AF_INET6 : AF_INET; break;
    }
    *(int *)value = v;
    *len = sizeof(int);
    return 0;
}

int setsockopt(int fd, int level, int name, const void *value, socklen_t len) {
    int32_t opt = option(level, name), err;
    if (!opt) return fail(ENOPROTOOPT);
    if (!value || len < sizeof(int)) return fail(EINVAL);
    if ((err = host_setopt(fd, opt, *(const int *)value))) return fail(err);
    return 0;
}

/* Name resolution */

int h_errno;

static int gai_error(int32_t err) {
    switch (err) {
    case RESOLVE_NONAME: return EAI_NONAME;
    case RESOLVE_AGAIN: return EAI_AGAIN;
    case RESOLVE_SERVICE: return EAI_SERVICE;
    case RESOLVE_FAMILY: return EAI_FAMILY;
    case RESOLVE_FAIL: return EAI_FAIL;
    }
    errno = err;
    return EAI_SYSTEM;
}

#define MAX_ADDRS 32

int getaddrinfo(const char *__restrict node, const char *__restrict service,
                const struct addrinfo *__restrict hints, struct addrinfo **__restrict res) {
    wire_addr addrs[MAX_ADDRS];
    int32_t family = 0, flags = 0, n = 0, err;
    int socktype = SOCK_STREAM, protocol = 0, hint_flags = hints ? hints->ai_flags : 0;
    struct addrinfo *head = NULL, **tail = &head;

    if (!node && !service) return EAI_NONAME;
    if (hints) {
        switch (hints->ai_family) {
        case AF_UNSPEC: break;
        case AF_INET: family = 4; break;
        case AF_INET6: family = 6; break;
        default: return EAI_FAMILY;
        }
        if (hints->ai_socktype) socktype = hints->ai_socktype;
        protocol = hints->ai_protocol;
    }
    if (socktype != SOCK_STREAM && socktype != SOCK_DGRAM) return EAI_SOCKTYPE;
    if (!protocol) protocol = socktype == SOCK_STREAM ? IPPROTO_TCP : IPPROTO_UDP;
    if (hint_flags & AI_NUMERICHOST) flags |= RESOLVE_NUMERICHOST;
    if (hint_flags & AI_PASSIVE) flags |= RESOLVE_PASSIVE;
    if (hint_flags & AI_NUMERICSERV) flags |= RESOLVE_NUMERICSERV;

    err = host_resolve(node, node ? strlen(node) : 0, service, service ? strlen(service) : 0,
                       family, flags, addrs, MAX_ADDRS, &n);
    if (err) return gai_error(err);
    if (n == 0) return EAI_NONAME;

    for (int32_t i = 0; i < n; i++) {
        /* One allocation holds the entry and its address. */
        struct {
            struct addrinfo ai;
            struct sockaddr_storage ss;
        } *e = calloc(1, sizeof *e);
        socklen_t len = sizeof e->ss;
        if (!e) {
            freeaddrinfo(head);
            return EAI_MEMORY;
        }
        from_wire(&addrs[i], (struct sockaddr *)&e->ss, &len);
        e->ai.ai_family = addrs[i].family == 6 ? AF_INET6 : AF_INET;
        e->ai.ai_socktype = socktype;
        e->ai.ai_protocol = protocol;
        e->ai.ai_addrlen = len;
        e->ai.ai_addr = (struct sockaddr *)&e->ss;
        if (i == 0 && (hint_flags & AI_CANONNAME) && node && !(e->ai.ai_canonname = strdup(node))) {
            free(e);
            freeaddrinfo(head);
            return EAI_MEMORY;
        }
        *tail = &e->ai;
        tail = &e->ai.ai_next;
    }
    *res = head;
    return 0;
}

void freeaddrinfo(struct addrinfo *ai) {
    while (ai) {
        struct addrinfo *next = ai->ai_next;
        free(ai->ai_canonname);
        free(ai);
        ai = next;
    }
}

const char *gai_strerror(int err) {
    switch (err) {
    case EAI_BADFLAGS: return "Invalid flags";
    case EAI_NONAME: return "Name or service not known";
    case EAI_AGAIN: return "Temporary failure in name resolution";
    case EAI_FAIL: return "Non-recoverable failure in name resolution";
    case EAI_NODATA: return "No address associated with hostname";
    case EAI_FAMILY: return "Address family not supported";
    case EAI_SOCKTYPE: return "Socket type not supported";
    case EAI_SERVICE: return "Servname not supported for ai_socktype";
    case EAI_MEMORY: return "Memory allocation failure";
    case EAI_SYSTEM: return "System error";
    case EAI_OVERFLOW: return "Argument buffer overflow";
    }
    return "Unknown error";
}

int getnameinfo(const struct sockaddr *__restrict sa, socklen_t salen, char *__restrict host,
                socklen_t hostlen, char *__restrict serv, socklen_t servlen, int flags) {
    wire_addr w;
    int32_t err = to_wire(sa, salen, &w), len;
    if (err) return err == EAFNOSUPPORT ? EAI_FAMILY : EAI_FAIL;
    if (host && hostlen) {
        int named = 0;
        if (!(flags & NI_NUMERICHOST)) {
            err = host_lookup(&w, host, hostlen - 1, &len);
            if (!err) {
                host[len] = 0;
                named = 1;
            } else if (err != RESOLVE_NONAME) {
                return gai_error(err);
            }
        }
        if (!named) {
            if (flags & NI_NAMEREQD) return EAI_NONAME;
            if (!inet_ntop(w.family == 6 ? AF_INET6 : AF_INET, w.addr, host, hostlen)) return EAI_OVERFLOW;
        }
    }
    if (serv && servlen) {
        int n = snprintf(serv, servlen, "%u", (unsigned)w.port);
        if (n < 0 || (socklen_t)n >= servlen) return EAI_OVERFLOW;
    }
    return 0;
}

/* gethostbyname and gethostbyaddr fill in one static hostent, like
 * glibc's. CPython serializes calls to them. */

static struct {
    struct hostent h;
    char name[NI_MAXHOST + 1];
    char *aliases[1];
    char *addr_list[MAX_ADDRS + 1];
    struct in6_addr addrs[MAX_ADDRS];
} host_result;

struct hostent *gethostbyname(const char *name) {
    struct addrinfo hints = {.ai_family = AF_INET, .ai_socktype = SOCK_STREAM}, *res, *ai;
    int n = 0, err = getaddrinfo(name, NULL, &hints, &res);
    if (err) {
        h_errno = err == EAI_AGAIN ? TRY_AGAIN : HOST_NOT_FOUND;
        return NULL;
    }
    memset(&host_result, 0, sizeof host_result);
    snprintf(host_result.name, sizeof host_result.name, "%s", name);
    for (ai = res; ai && n < MAX_ADDRS; ai = ai->ai_next) {
        memcpy(&host_result.addrs[n], &((struct sockaddr_in *)ai->ai_addr)->sin_addr, 4);
        host_result.addr_list[n] = (char *)&host_result.addrs[n];
        n++;
    }
    freeaddrinfo(res);
    host_result.h.h_name = host_result.name;
    host_result.h.h_aliases = host_result.aliases;
    host_result.h.h_addrtype = AF_INET;
    host_result.h.h_length = 4;
    host_result.h.h_addr_list = host_result.addr_list;
    return &host_result.h;
}

struct servent *getservbyname(const char *name, const char *proto) {
    static struct servent se;
    static char *aliases[1];
    static char se_name[NI_MAXSERV], se_proto[8];
    wire_addr w;
    int32_t n = 0;
    if (!name || host_resolve(NULL, 0, name, strlen(name), 4, RESOLVE_PASSIVE, &w, 1, &n) || n != 1) return NULL;
    snprintf(se_name, sizeof se_name, "%s", name);
    snprintf(se_proto, sizeof se_proto, "%s", proto ? proto : "tcp");
    se.s_name = se_name;
    se.s_aliases = aliases;
    se.s_port = htons(w.port);
    se.s_proto = se_proto;
    return &se;
}

struct hostent *gethostbyaddr(const void *addr, socklen_t len, int type) {
    wire_addr w;
    int32_t n, err;
    memset(&w, 0, sizeof w);
    if (type == AF_INET && len == 4) {
        w.family = 4;
    } else if (type == AF_INET6 && len == 16) {
        w.family = 6;
    } else {
        h_errno = NO_RECOVERY;
        return NULL;
    }
    memcpy(w.addr, addr, len);
    memset(&host_result, 0, sizeof host_result);
    if ((err = host_lookup(&w, host_result.name, NI_MAXHOST, &n))) {
        h_errno = err == RESOLVE_AGAIN ? TRY_AGAIN : HOST_NOT_FOUND;
        return NULL;
    }
    host_result.name[n] = 0;
    memcpy(&host_result.addrs[0], addr, len);
    host_result.addr_list[0] = (char *)&host_result.addrs[0];
    host_result.h.h_name = host_result.name;
    host_result.h.h_aliases = host_result.aliases;
    host_result.h.h_addrtype = type;
    host_result.h.h_length = len;
    host_result.h.h_addr_list = host_result.addr_list;
    return &host_result.h;
}

/* Network interfaces: the guest can't see the host's, so there are none
 * (getifaddrs and if_nameindex list nothing, lookups find nothing). */

int getifaddrs(struct ifaddrs **ifap) {
    *ifap = NULL;
    return 0;
}

void freeifaddrs(struct ifaddrs *ifa) {
    (void)ifa;
}

unsigned int if_nametoindex(const char *name) {
    (void)name;
    errno = ENXIO;
    return 0;
}

char *if_indextoname(unsigned int index, char *name) {
    (void)index, (void)name;
    errno = ENXIO;
    return NULL;
}

struct if_nameindex *if_nameindex(void) {
    return calloc(1, sizeof(struct if_nameindex));
}

void if_freenameindex(struct if_nameindex *list) {
    free(list);
}
