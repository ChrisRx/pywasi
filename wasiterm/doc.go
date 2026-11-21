// Package wasiterm gives a WebAssembly program the terminal control WASI
// leaves out: tcgetattr, tcsetattr and the window size, on its standard
// descriptors when the host's are terminals. The program is built with
// pywasi's C shim, toolchain/support/pywasi_term.c, and its termios
// header; this package is the host functions the shim calls, for wazero.
//
// pywasi's interpreter imports them from its "pywasi" host module. Another
// program builds the shim with -DWASITERM_NO_PYTHON and
// -DWASITERM_MODULE='"name"', adds them to its host module with Export, and
// runs with the context With returns.
package wasiterm
