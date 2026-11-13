// Command pywasi runs the WASI python.wasm under wazero: the one in -home,
// else $PYWASI_HOME or .pywasi if it exists, else the embedded standard
// library interpreter. The current directory is mounted at the same path and
// Python starts in it, like a native python (-cwd=false turns that off).
//
//	go run ./cmd/pywasi                        # interactive prompt
//	go run ./cmd/pywasi -- -c 'print(1 + 1)'
//	go run ./cmd/pywasi -home examples/numpy-app/.pywasi -- -c 'import numpy; print(numpy.__version__)'
//	go run ./cmd/pywasi -home examples/numpy-app/.pywasi -mount examples/numpy-app:/app -- /app/main.py
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
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
		home  = flag.String("home", "", "Python install tree (bin/python.wasm, lib/python3.X), e.g. PROJECT/.pywasi; defaults to $PYWASI_HOME or .pywasi if it exists, else the embedded standard library interpreter")
		cache = flag.String("cache", pywasi.DefaultCacheDir(), "compiled code cache directory (empty disables)")
		cwd   = flag.Bool("cwd", true, "mount the current directory at the same path (/cwd on Windows) and start in it")
		mnts  mounts
	)
	flag.Var(&mnts, "mount", "mount a host directory as HOST:/GUEST (repeatable)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: pywasi [flags] [--] [python args...]\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	ctx := context.Background()
	opts := []pywasi.Option{cacheOption(*cache)}
	if *home != "" {
		opts = append(opts, pywasi.WithHome(*home))
	}
	py, err := pywasi.New(ctx, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer py.Close()

	env := map[string]string{}
	if v, ok := os.LookupEnv("PYTHON_BASIC_REPL"); ok {
		env["PYTHON_BASIC_REPL"] = v
	} else {
		// CPython's newer REPL needs termios, which WASI lacks. Snapshotted
		// interpreters start the basic one themselves.
		env["PYTHON_BASIC_REPL"] = "1"
	}
	var dir string
	if *cwd {
		dir = mountWorkingDirectory(&mnts)
	}
	code, err := py.Run(ctx, pywasi.RunOptions{
		Args:   flag.Args(),
		Env:    env,
		Dir:    dir,
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

// mountWorkingDirectory mounts the current directory at its own path (/cwd
// on Windows) and returns that guest path, or "" if it can't be mounted
// there: at the guest root or over the interpreter's own files.
func mountWorkingDirectory(mnts *mounts) string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	guest := wd
	if runtime.GOOS == "windows" {
		guest = "/cwd"
	}
	switch {
	case guest == "/", guest == "/bin", guest == "/lib", guest == "/.pywasi", strings.HasPrefix(guest, "/.pywasi/"):
		fmt.Fprintf(os.Stderr, "pywasi: not mounting the current directory %s, which would hide the interpreter's files\n", wd)
		return ""
	}
	*mnts = append(*mnts, pywasi.Mount{Host: wd, Guest: guest})
	return guest
}
