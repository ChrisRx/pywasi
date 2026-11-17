module go.chrisrx.dev/pywasi/runtime/wasmtime

go 1.27.3

require (
	github.com/bytecodealliance/wasmtime-go/v50 v50.0.0
	go.chrisrx.dev/pywasi v0.3.0
)

require (
	github.com/ChrisRx/wazero v0.0.0-20261116205937-3f122248aff2 // indirect
	github.com/klauspost/compress v1.20.4 // indirect
	go.chrisrx.dev/x v0.0.0-20261103231709-6405755dad2b // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/term v0.43.0 // indirect
)

// Built against this repository's pywasi while working in it; users get the
// version required above.
replace go.chrisrx.dev/pywasi => ../..
