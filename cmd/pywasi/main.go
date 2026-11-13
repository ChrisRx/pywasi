// Command pywasi runs the WASI python.wasm under wazero.
//
//	go run ./cmd/pywasi -home examples/numpy-app/.pywasi -- -c 'import numpy; print(numpy.__version__)'
//	go run ./cmd/pywasi -home examples/numpy-app/.pywasi -mount examples/numpy-app:/app -- /app/main.py
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"go.chrisrx.dev/pywasi"
)

type mounts []pywasi.Mount

func (m *mounts) String() string { return fmt.Sprint(*m) }

func (m *mounts) Set(v string) error {
	host, guest, ok := strings.Cut(v, ":")
	if !ok || host == "" || !strings.HasPrefix(guest, "/") {
		return fmt.Errorf("want HOST:/GUEST, got %q", v)
	}
	*m = append(*m, pywasi.Mount{Host: host, Guest: guest})
	return nil
}

func main() {
	var (
		home  = flag.String("home", pywasi.DefaultHome(), "Python install tree (bin/python.wasm, lib/python3.X), e.g. PROJECT/.pywasi; defaults to $PYWASI_HOME or .pywasi")
		cache = flag.String("cache", pywasi.DefaultCacheDir(), "compiled code cache directory (empty disables)")
		mnts  mounts
	)
	flag.Var(&mnts, "mount", "mount a host directory as HOST:/GUEST (repeatable)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: pywasi [flags] [--] [python args...]\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	ctx := context.Background()
	py, err := pywasi.New(ctx, pywasi.WithHome(*home), cacheOption(*cache))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer py.Close()

	code, err := py.Run(ctx, pywasi.RunOptions{
		Args:   flag.Args(),
		Mounts: mnts,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	py.Close()
	os.Exit(code)
}

func cacheOption(dir string) pywasi.Option {
	if dir == "" {
		return pywasi.WithoutCache()
	}
	return pywasi.WithCacheDir(dir)
}
