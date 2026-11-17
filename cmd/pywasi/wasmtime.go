package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.chrisrx.dev/pywasi"
)

// runtimeFromEnv is the runtime $PYWASI_RUNTIME picks: nil for pywasi's
// own (wazero, the default), or wasmtime's command ("wasmtime", found on
// PATH, or a path to it).
func runtimeFromEnv() (pywasi.Runner, error) {
	v := os.Getenv("PYWASI_RUNTIME")
	switch {
	case v == "" || v == "wazero":
		return nil, nil
	case v == "wasmtime":
		bin, err := exec.LookPath("wasmtime")
		if err != nil {
			return nil, errors.New("pywasi: PYWASI_RUNTIME=wasmtime, but there is no wasmtime on PATH")
		}
		return wasmtimeCommand{bin}, nil
	case strings.HasPrefix(filepath.Base(v), "wasmtime"):
		if _, err := os.Stat(v); err != nil {
			return nil, fmt.Errorf("pywasi: PYWASI_RUNTIME: %w", err)
		}
		return wasmtimeCommand{v}, nil
	default:
		return nil, fmt.Errorf("pywasi: PYWASI_RUNTIME=%s: want wazero, wasmtime, or a path to wasmtime", v)
	}
}

// wasmtimeCommand runs the interpreter with wasmtime's command (wasmtime
// run), pywasi's host functions failing (pywasi.HostStub).
type wasmtimeCommand struct{ bin string }

func (w wasmtimeCommand) Run(ctx context.Context, cmd *pywasi.Command) (int, error) {
	stub, err := hostStub()
	if err != nil {
		return 0, err
	}
	args := []string{
		"run", "--preload", "pywasi=" + stub,
		"--wasm", "exceptions=y", "--wasm", "max-wasm-stack=16777216",
	}
	for _, d := range cmd.Dirs {
		args = append(args, "--dir", d.Host+"::"+d.Guest)
	}
	for _, kv := range cmd.Env {
		args = append(args, "--env", kv)
	}
	args = append(args, "--argv0", cmd.Args[0], cmd.Wasm)
	args = append(args, cmd.Args[1:]...)
	c := exec.CommandContext(ctx, w.bin, args...)
	c.Stdin, c.Stdout, c.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	err = c.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		return exit.ExitCode(), nil
	}
	return 0, err
}

// hostStub writes pywasi.HostStub to the cache, once, for --preload.
func hostStub() (string, error) {
	sum := sha256.Sum256(pywasi.HostStub)
	dir, err := pickerDir()
	if err != nil {
		return "", err
	}
	file := filepath.Join(filepath.Dir(dir), "host-stub-"+hex.EncodeToString(sum[:6])+".wasm")
	if _, err := os.Stat(file); err == nil {
		return file, nil
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, pywasi.HostStub, 0o644); err != nil {
		return "", err
	}
	return file, os.Rename(tmp, file)
}
