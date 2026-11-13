// Package builder produces a pywasi interpreter for a list of Python
// requirements. pywasi.New calls it for pywasi.WithDependencies. Use it
// directly to build ahead of time (e.g. in go generate) and pass the result
// with pywasi.WithHome:
//
//	home, err := builder.Ensure(ctx, builder.Options{
//		Dependencies: []string{"numpy", "pyyaml==6.0.3"},
//	})
//
// It runs the pywasi build tool (scripts/build.sh --with ...), which needs a
// pywasi checkout and its Nix toolchain. The first build of a dependency set
// can take minutes (numpy compiles from source). Later calls with the same
// set return the cached interpreter.
package builder

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Options configures Ensure.
type Options struct {
	// Dependencies are PEP 508 requirements: a bare name ("numpy") or with
	// a version ("pyyaml==6.0.3", "tabulate>=0.9"). Unpinned versions are
	// resolved on the first build and then stay locked for this set. Empty
	// builds an interpreter with only the standard library.
	Dependencies []string

	// Preload lists modules (import names, e.g. "yaml" for pyyaml) to
	// import into the startup snapshot, making them free to import later.
	Preload []string

	// Root is the pywasi checkout. Defaults to $PYWASI_ROOT, then the
	// current directory.
	Root string

	// Log receives build output. Defaults to io.Discard.
	Log io.Writer
}

// Ensure builds (or finds the cached) interpreter for opts.Dependencies and
// returns its home directory, for pywasi.WithHome.
func Ensure(ctx context.Context, opts Options) (string, error) {
	root := opts.Root
	if root == "" {
		root = os.Getenv("PYWASI_ROOT")
	}
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	script := filepath.Join(root, "scripts", "build.sh")
	if _, err := os.Stat(script); err != nil {
		return "", fmt.Errorf("builder: %s not found (set Options.Root or PYWASI_ROOT to a pywasi checkout)", script)
	}

	// --env builds a set from the --with list, which may be empty (an
	// interpreter with only the standard library).
	args := []string{script, "--env"}
	for _, d := range opts.Dependencies {
		args = append(args, "--with", d)
	}
	for _, m := range opts.Preload {
		args = append(args, "--preload", m)
	}
	// Outside the dev shell, run inside it.
	if os.Getenv("WASI_SDK_PATH") == "" {
		args = append([]string{"nix", "develop", root, "--command"}, args...)
	}

	logw := opts.Log
	if logw == nil {
		logw = io.Discard
	}
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = root
	cmd.Stdout = &stdout
	cmd.Stderr = logw
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("builder: %s: %w", strings.Join(args, " "), err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	home := strings.TrimSpace(lines[len(lines)-1])
	if _, err := os.Stat(filepath.Join(home, "bin", "python.wasm")); err != nil {
		return "", fmt.Errorf("builder: no interpreter at %q: %w", home, err)
	}
	return home, nil
}
