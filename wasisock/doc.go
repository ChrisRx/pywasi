// Package wasisock gives a WebAssembly program sockets it can create, on
// top of WASI preview 1, which can only use sockets the host opened. The
// program is built with pywasi's C shim, toolchain/support/pywasi_socket.c,
// which implements socket, bind, listen, connect, getaddrinfo and the rest
// with host functions; this package is those functions, for wazero.
//
// pywasi's interpreter imports them from its "pywasi" host module. Another
// program builds the shim with -DWASISOCK_MODULE='"name"' and calls
// Instantiate with that name, or Export to add them to a host module of its
// own. The program reads and writes the sockets, accepts and polls with
// wazero's own WASI functions.
package wasisock
