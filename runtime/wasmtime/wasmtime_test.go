package wasmtime_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.chrisrx.dev/pywasi"
	"go.chrisrx.dev/pywasi/runtime/wasmtime"
)

func newPython(t *testing.T) *pywasi.Python {
	t.Helper()
	t.Setenv("PYWASI_CACHE", t.TempDir())
	r, err := wasmtime.New()
	if err != nil {
		t.Fatal(err)
	}
	py, err := pywasi.New(t.Context(), pywasi.WithRunner(r))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { py.Close() })
	return py
}

func TestRun(t *testing.T) {
	py := newPython(t)
	var stdout, stderr bytes.Buffer
	code, err := py.Run(t.Context(), pywasi.RunOptions{
		Args:   []string{"-c", "import sys, os; print(sys.stdin.read().upper(), os.environ['X']); sys.exit(3)"},
		Env:    map[string]string{"X": "y"},
		Stdin:  strings.NewReader("hello"),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 || strings.TrimSpace(stdout.String()) != "HELLO y" {
		t.Errorf("code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

// Eval, Call and sessions go through the runner too.
func TestEvalCallSession(t *testing.T) {
	py := newPython(t)
	ctx := t.Context()
	if n, err := py.Eval[int](ctx, "import math\nmath.factorial(10)"); err != nil || n != 3628800 {
		t.Errorf("Eval: %v, %v", n, err)
	}
	if mean, err := py.Call[float64](ctx, pywasi.Func{Module: "statistics", Name: "mean", Args: []any{[]int{1, 2, 3, 4}}}); err != nil || mean != 2.5 {
		t.Errorf("Call: %v, %v", mean, err)
	}
	s, err := py.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Eval[any](ctx, "x = 41"); err != nil {
		t.Fatal(err)
	}
	if x, err := s.Eval[int](ctx, "x + 1"); err != nil || x != 42 {
		t.Errorf("session: %v, %v", x, err)
	}
}

// A run stops when its context is done.
func TestCancel(t *testing.T) {
	py := newPython(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := py.Run(ctx, pywasi.RunOptions{Args: []string{"-c", "while True: pass"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err %v", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("took %v to stop", d)
	}
}

// Packages linked into the interpreter (numpy's C extensions, pandas and
// its pure-Python dependencies) run in wasmtime too, and recursion as deep
// as Python allows by default.
func TestPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads packages")
	}
	t.Setenv("PYWASI_CACHE", t.TempDir())
	r, err := wasmtime.New()
	if err != nil {
		t.Fatal(err)
	}
	py, err := pywasi.New(t.Context(), pywasi.WithRunner(r), pywasi.WithPackages("pandas"))
	if err != nil {
		t.Fatal(err)
	}
	defer py.Close()
	if n, err := py.Eval[int](t.Context(), `import pandas as pd; int(pd.DataFrame({"a": [1, 2, 3]}).a.sum())`); err != nil || n != 6 {
		t.Errorf("pandas: %v, %v", n, err)
	}
	depth, err := py.Eval[int](t.Context(), `
import sys
def f(n): return n if n == 0 else 1 + f(n - 1)
f(sys.getrecursionlimit() - 50)`)
	if err != nil || depth < 900 {
		t.Errorf("recursion: %v, %v", depth, err)
	}
}
