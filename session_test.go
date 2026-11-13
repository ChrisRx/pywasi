package pywasi_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.chrisrx.dev/pywasi"
)

func newSession(t *testing.T, py *pywasi.Python, opts ...pywasi.RunOptions) *pywasi.Session {
	t.Helper()
	s, err := py.Session(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSession(t *testing.T) {
	py := newPython(t, pywasi.WithModule("counter", `
		n = 0

		def bump(by=1):
		    global n
		    n += by
		    return n
	`))
	var out bytes.Buffer
	s := newSession(t, py, pywasi.RunOptions{Stdout: &out})
	ctx := context.Background()

	// Globals and imports persist between requests.
	if _, err := s.Eval[any](ctx, "import numpy as np\nx = np.arange(5)"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Eval[int](ctx, "int(x.sum())"); err != nil || v != 10 {
		t.Fatalf("sum=%d err=%v", v, err)
	}
	// So does module state.
	for want := 1; want <= 3; want++ {
		if n, err := s.Call[int](ctx, pywasi.Func{Module: "counter", Name: "bump"}); err != nil || n != want {
			t.Fatalf("bump=%d err=%v, want %d", n, err, want)
		}
	}

	// Output passes through, in order, before the result.
	v, err := s.Eval[string](ctx, `
		print("first")
		print("no newline", end="")
		"result"
	`)
	if err != nil || v != "result" || out.String() != "first\nno newline" {
		t.Fatalf("v=%q out=%q err=%v", v, out.String(), err)
	}

	// Errors, even SystemExit, leave the session usable.
	var perr *pywasi.Error
	if _, err := s.Eval[any](ctx, "1/0"); !errors.As(err, &perr) || perr.Type != "ZeroDivisionError" {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Eval[any](ctx, "import sys\nsys.exit(3)"); !errors.As(err, &perr) || perr.Type != "SystemExit" {
		t.Fatalf("got %v", err)
	}
	if v, err := s.Eval[int](ctx, "len(x)"); err != nil || v != 5 {
		t.Fatalf("after errors: v=%d err=%v", v, err)
	}

	// Requests from several goroutines run one at a time.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.Call[int](ctx, pywasi.Func{Module: "counter", Name: "bump"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n, err := s.Eval[int](ctx, "import counter\ncounter.n"); err != nil || n != 11 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	start := time.Now()
	const runs = 20
	for range runs {
		if _, err := s.Eval[float64](ctx, "float(np.linalg.det(np.eye(3) * 2))"); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("numpy Eval in a session: %v each", time.Since(start)/runs)

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Eval[int](ctx, "1"); !errors.Is(err, pywasi.ErrSessionClosed) {
		t.Fatalf("after Close: %v", err)
	}
}

func TestSessionCancel(t *testing.T) {
	py := newPython(t)
	s := newSession(t, py)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// Busy for a second (time.sleep isn't supported under wazero yet).
	busy := "import time\nt = time.monotonic()\nwhile time.monotonic() - t < 1: pass"
	if _, err := s.Eval[any](ctx, busy); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Eval[int](context.Background(), "1"); !errors.Is(err, pywasi.ErrSessionClosed) {
		t.Fatalf("after cancel: %v", err)
	}
}
