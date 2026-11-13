package pywasi_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.chrisrx.dev/pywasi"
)

const appDir = "examples/numpy-app"

func newPython(t *testing.T, options ...pywasi.Option) *pywasi.Python {
	t.Helper()
	home := filepath.Join(appDir, ".pywasi")
	if _, err := os.Stat(filepath.Join(home, "bin", "python.wasm")); err != nil {
		t.Skip("examples/numpy-app/.pywasi not built; run scripts/build.sh")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	py, err := pywasi.New(ctx, append([]pywasi.Option{pywasi.WithHome(home), pywasi.WithCacheDir(filepath.Join(cache, "pywasi", "wazero"))}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { py.Close() })
	return py
}

func run(t *testing.T, py *pywasi.Python, opts pywasi.RunOptions) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts.Stdout, opts.Stderr = &stdout, &stderr
	code, err := py.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v\nstderr:\n%s", err, stderr.String())
	}
	return code, stdout.String(), stderr.String()
}

func TestNumpyDemo(t *testing.T) {
	py := newPython(t)
	code, out, errOut := run(t, py, pywasi.RunOptions{
		Args:   []string{"/app/main.py"},
		Mounts: []pywasi.Mount{{Host: appDir, Guest: "/app", ReadOnly: true}},
	})
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, errOut)
	}
	for _, want := range []string{
		"numpy 2.5.4",
		"eigvals = [2.+0.j 5.+0.j]",
		"solve = [0.1 0.6]",
		"fft peak bin = 5",
		"markupsafe escape: &lt;b&gt;&amp;&lt;/b&gt; speedups: built-in",
		"sum          66",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestExitCodeAndStdin(t *testing.T) {
	py := newPython(t)
	code, out, _ := run(t, py, pywasi.RunOptions{
		Args:  []string{"-c", "import sys; print(sys.stdin.read().upper()); sys.exit(3)"},
		Stdin: strings.NewReader("hello"),
	})
	if code != 3 || strings.TrimSpace(out) != "HELLO" {
		t.Fatalf("got code=%d out=%q", code, out)
	}
}

func TestEntropy(t *testing.T) {
	py := newPython(t)
	draw := func() string {
		_, out, _ := run(t, py, pywasi.RunOptions{Args: []string{"-c", "import os; print(os.urandom(8).hex())"}})
		return out
	}
	if a, b := draw(), draw(); a == b {
		t.Fatalf("os.urandom repeated across runs: %s", a)
	}
}

func TestConcurrentRuns(t *testing.T) {
	py := newPython(t)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			code, out, errOut := run(t, py, pywasi.RunOptions{
				Args: []string{"-c", "import numpy as np, sys; print(int(np.arange(int(sys.argv[1])).sum()))", "1000"},
			})
			if code != 0 || strings.TrimSpace(out) != "499500" {
				t.Errorf("run %d: code=%d out=%q stderr=%s", i, code, out, errOut)
			}
		})
	}
	wg.Wait()
}

// The snapshot captures the build machine's process state; each run must
// see its own environment, argv and working mounts.
func TestPerRunState(t *testing.T) {
	py := newPython(t)
	for _, v := range []string{"one", "two"} {
		code, out, errOut := run(t, py, pywasi.RunOptions{
			Args:   []string{"-c", "import os, sys; print(os.environ.get('PYWASI_TEST'), sys.argv[1:], 'main.py' in os.listdir('/app'))", v},
			Env:    map[string]string{"PYWASI_TEST": v},
			Mounts: []pywasi.Mount{{Host: appDir, Guest: "/app", ReadOnly: true}},
		})
		if want := fmt.Sprintf("%s ['%s'] True", v, v); code != 0 || strings.TrimSpace(out) != want {
			t.Fatalf("code=%d out=%q want %q stderr=%s", code, out, want, errOut)
		}
	}
}

func TestModuleAndUnsupportedFlag(t *testing.T) {
	py := newPython(t)
	code, out, _ := run(t, py, pywasi.RunOptions{Args: []string{"-m", "json.tool"}, Stdin: strings.NewReader(`{"a":1}`)})
	if code != 0 || !strings.Contains(out, `"a": 1`) {
		t.Fatalf("-m json.tool: code=%d out=%q", code, out)
	}
	// -X/-O and friends can't apply to an already-initialized interpreter.
	if code, _, _ := run(t, py, pywasi.RunOptions{Args: []string{"-O", "-c", "pass"}}); code == 0 {
		t.Fatal("-O should be rejected by the snapshot interpreter")
	}
}

func TestOptionsHomeOrDependencies(t *testing.T) {
	ctx := context.Background()
	// Neither is valid: see TestDefaultHome and TestStdlibOnly.
	if _, err := pywasi.New(ctx, pywasi.WithHome("x"), pywasi.WithDependencies("numpy")); err == nil {
		t.Error("expected an error with both Home and Dependencies")
	}
}

// Builds through Options.Dependencies (cached after the first run).
func TestDependencies(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an interpreter")
	}
	if os.Getenv("WASI_SDK_PATH") == "" {
		t.Skip("needs the nix dev shell")
	}
	ctx := context.Background()
	py, err := pywasi.New(ctx, pywasi.WithDependencies("tabulate>=0.9"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { py.Close() })
	code, out, _ := run(t, py, pywasi.RunOptions{Args: []string{"-c", "from tabulate import tabulate; print(tabulate([[1, 2]]))"}})
	if code != 0 || !strings.Contains(out, "1  2") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestDefaultHome(t *testing.T) {
	ctx := context.Background()
	t.Setenv("PYWASI_HOME", filepath.Join(appDir, ".pywasi"))
	if _, err := os.Stat(filepath.Join(appDir, ".pywasi", "bin", "python.wasm")); err != nil {
		t.Skip("examples/numpy-app/.pywasi not built")
	}
	py, err := pywasi.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer py.Close()
	if n, err := py.Eval[int](ctx, "6 * 7"); err != nil || n != 42 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
