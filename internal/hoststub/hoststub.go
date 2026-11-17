// Package hoststub embeds the module providing pywasi's host functions
// (module "pywasi": sockets and terminals) all failing, built from
// toolchain/support/pywasi_host_stub.c (task host-stub), for runtimes
// other than pywasi's wazero.
package hoststub

import _ "embed"

//go:embed pywasi-host-stub.wasm
var Wasm []byte
