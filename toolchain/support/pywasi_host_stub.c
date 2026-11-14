/* pywasi's host functions (module "pywasi", see sockets.go), all failing
 * with ENOSYS, for running a linked interpreter outside pywasi:
 *
 *   wasmtime run --preload pywasi=build/pywasi-host-stub.wasm ...
 *
 * Built by toolchain/pywasi_build.py. */
#define ENOSYS 52
#define STUB(name, ...) \
    __attribute__((export_name("sock_" #name))) int name(__VA_ARGS__) { return ENOSYS; }

STUB(open, int family, int type, int nonblock, int fd)
STUB(bind, int fd, int addr)
STUB(listen, int fd, int backlog)
STUB(connect, int fd, int addr)
STUB(addr, int fd, int peer, int addr)
STUB(getopt, int fd, int opt, int value)
STUB(setopt, int fd, int opt, int value)
STUB(resolve, int host, int host_len, int service, int service_len, int family, int flags, int addrs, int cap, int n)
STUB(lookup, int addr, int name, int cap, int len)
