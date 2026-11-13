package pywasi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"go.chrisrx.dev/pywasi"
)

func TestEval(t *testing.T) {
	py := newPython(t)
	ctx := context.Background()

	if n, err := py.Eval[int](ctx, "import math\nmath.factorial(10)"); err != nil || n != 3628800 {
		t.Fatalf("factorial: n=%d err=%v", n, err)
	}

	// numpy arrays decode as nested slices.
	code := `
import numpy as np

m = np.array([[4.0, 1.0], [2.0, 3.0]])
np.linalg.inv(m).round(2)
`
	inv, err := py.Eval[[][]float64](ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]float64{{0.3, -0.1}, {-0.2, 0.4}}; !equal2(inv, want) {
		t.Fatalf("inv = %v, want %v", inv, want)
	}

	// No final expression: nothing to decode, output still goes to Stdout.
	var stdout bytes.Buffer
	v, err := py.Eval[any](ctx, "print('hi')\nx = 1", pywasi.RunOptions{Stdout: &stdout})
	if err != nil {
		t.Fatal(err)
	}
	if v != nil || stdout.String() != "hi\n" {
		t.Fatalf("v=%v stdout=%q", v, stdout.String())
	}

	// Complex, bytes and numpy scalars.
	type mixedValues struct {
		C     struct{ Real, Imag float64 }
		B     []byte
		Count int64
	}
	mixed, err := py.Eval[mixedValues](ctx, `import numpy as np; {"C": 1+2j, "B": b"\x00\x01", "Count": np.int64(7)}`)
	if err != nil {
		t.Fatal(err)
	}
	if mixed.C.Real != 1 || mixed.C.Imag != 2 || !bytes.Equal(mixed.B, []byte{0, 1}) || mixed.Count != 7 {
		t.Fatalf("mixed = %+v", mixed)
	}
}

func TestEvalErrors(t *testing.T) {
	py := newPython(t)
	ctx := context.Background()

	var perr *pywasi.Error
	_, err := py.Eval[any](ctx, "x = 1\n1 / 0")
	if !errors.As(err, &perr) || perr.Type != "ZeroDivisionError" {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(perr.Traceback, "1 / 0") || strings.Contains(perr.Traceback, "_pywasi") {
		t.Fatalf("traceback should show the user's line and not _pywasi:\n%s", perr.Traceback)
	}

	_, err = py.Eval[any](ctx, "import numpy as np\nnp.linalg.inv(np.zeros((2, 2)))")
	if !errors.As(err, &perr) || perr.Type != "numpy.linalg.LinAlgError" {
		t.Fatalf("got %v", err)
	}

	_, err = py.Eval[any](ctx, "float('nan')")
	if !errors.As(err, &perr) || !strings.Contains(perr.Message, "nan") {
		t.Fatalf("NaN should be a clear error, got %v", err)
	}

	_, err = py.Eval[any](ctx, "object()")
	if !errors.As(err, &perr) || perr.Type != "TypeError" {
		t.Fatalf("unserializable value: got %v", err)
	}

	_, err = py.Eval[any](ctx, "def f(:")
	if !errors.As(err, &perr) || perr.Type != "SyntaxError" {
		t.Fatalf("got %v", err)
	}

	// Exiting without a result is reported with stderr.
	_, err = py.Eval[any](ctx, "import sys; sys.exit(3)")
	if err == nil || !strings.Contains(err.Error(), "code 3") {
		t.Fatalf("got %v", err)
	}
}

func TestCall(t *testing.T) {
	py := newPython(t)
	ctx := context.Background()

	if mean, err := py.Call[float64](ctx, pywasi.Func{Module: "statistics", Name: "mean", Args: []any{[]int{1, 2, 3, 4}}}); err != nil || mean != 2.5 {
		t.Fatalf("mean=%v err=%v", mean, err)
	}

	lib := pywasi.RunOptions{
		Mounts: []pywasi.Mount{{Host: "testdata/pylib", Guest: "/pylib", ReadOnly: true}},
		Env:    map[string]string{"PYTHONPATH": "/pylib"},
	}
	type summary struct {
		Mean, Std float64
		N         int
	}
	stats, err := py.Call[summary](ctx, pywasi.Func{
		Module: "stats",
		Name:   "summarize",
		Args:   []any{[]float64{2, 4, 4, 4, 5, 5, 7, 9}},
		Kwargs: map[string]any{"ddof": 0},
	}, lib)
	if err != nil || stats.Mean != 5 || stats.Std != 2 || stats.N != 8 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}

	if eye, err := py.Call[[][]float64](ctx, pywasi.Func{Module: "stats", Name: "Matrix.identity", Args: []any{2}}, lib); err != nil || !equal2(eye, [][]float64{{1, 0}, {0, 1}}) {
		t.Fatalf("eye=%v err=%v", eye, err)
	}

	var perr *pywasi.Error
	if _, err := py.Call[any](ctx, pywasi.Func{Module: "stats", Name: "fail"}, lib); !errors.As(err, &perr) || perr.Type != "numpy.linalg.LinAlgError" || perr.Message != "Singular matrix" {
		t.Fatalf("got %v", err)
	}
	if _, err := py.Call[any](ctx, pywasi.Func{Module: "no_such_module", Name: "f"}); !errors.As(err, &perr) || perr.Type != "ModuleNotFoundError" {
		t.Fatalf("got %v", err)
	}

	// Raw JSON for callers that decode later.
	if raw, err := py.Call[json.RawMessage](ctx, pywasi.Func{Module: "json", Name: "loads", Args: []any{`{"a": [1, 2]}`}}); err != nil || string(raw) != `{"a": [1, 2]}` {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
}

func TestConcurrentEval(t *testing.T) {
	py := newPython(t)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			if got, err := py.Eval[int](context.Background(), "import sys\nsum(range(100))"); err != nil || got != 4950 {
				t.Errorf("eval %d: got=%d err=%v", i, got, err)
			}
		})
	}
	wg.Wait()
}

func equal2(a, b [][]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

func TestReservedMounts(t *testing.T) {
	py := newPython(t)
	ctx := context.Background()
	for _, guest := range []string{"/", "/lib", "/lib/", "/bin", "/.pywasi", "/.pywasi/modules/0"} {
		opts := pywasi.RunOptions{Mounts: []pywasi.Mount{{Host: "testdata/pylib", Guest: guest}}}
		if _, err := py.Eval[any](ctx, "1", opts); err == nil || !strings.Contains(err.Error(), "pywasi: mount") {
			t.Errorf("Eval with mount at %s: got %v", guest, err)
		}
		opts.Args = []string{"-c", "pass"}
		if _, err := py.Run(ctx, opts); err == nil {
			t.Errorf("Run with mount at %s: expected an error", guest)
		}
	}
	if _, err := py.Eval[any](ctx, "1", pywasi.RunOptions{Mounts: []pywasi.Mount{{Host: "testdata/pylib", Guest: "lib"}}}); err == nil {
		t.Error("relative guest path should be rejected")
	}
}

func TestPackageEvalAndCall(t *testing.T) {
	if _, err := os.Stat(filepath.Join(appDir, ".pywasi", "bin", "python.wasm")); err != nil {
		t.Skip("examples/numpy-app/.pywasi not built")
	}
	t.Setenv("PYWASI_HOME", filepath.Join(appDir, ".pywasi"))
	ctx := context.Background()

	trace, err := pywasi.Eval[float64](ctx, "import numpy as np\nnp.trace(np.eye(3))")
	if err != nil || trace != 3 {
		t.Fatalf("trace=%v err=%v", trace, err)
	}
	eye, err := pywasi.Eval[[][]int](ctx, "import numpy as np\nnp.eye(2, dtype=int)")
	if want := [][]int{{1, 0}, {0, 1}}; err != nil || !reflect.DeepEqual(eye, want) {
		t.Fatalf("eye=%v err=%v", eye, err)
	}
	type point struct{ X, Y float64 }
	p, err := pywasi.Eval[point](ctx, `{"X": 1.5, "Y": -2}`)
	if err != nil || p != (point{1.5, -2}) {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	// Errors return the zero value.
	if n, err := pywasi.Eval[int](ctx, "'not a number'"); err == nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if mean, err := pywasi.Call[float64](ctx, pywasi.Func{Module: "statistics", Name: "mean", Args: []any{[]int{2, 4}}}); err != nil || mean != 3 {
		t.Fatalf("mean=%v err=%v", mean, err)
	}
	sq, err := pywasi.Call[int](ctx, pywasi.Func{Module: "sq", Name: "sq", Args: []any{7}}, pywasi.WithModule("sq", `
		def sq(x):
		    return x * x
	`))
	if err != nil || sq != 49 {
		t.Fatalf("sq=%d err=%v", sq, err)
	}
	var out bytes.Buffer
	if v, err := pywasi.Eval[string](ctx, `
		import sq
		print(sq.sq(3))
		sq.__name__
	`, pywasi.WithModule("sq", "def sq(x): return x * x"), pywasi.WithRunOptions(pywasi.RunOptions{Stdout: &out})); err != nil || v != "sq" || out.String() != "9\n" {
		t.Fatalf("v=%q out=%q err=%v", v, out.String(), err)
	}
	if _, err := pywasi.New(ctx, pywasi.WithRunOptions(pywasi.RunOptions{})); err == nil {
		t.Fatal("New should reject WithRunOptions")
	}
	var perr *pywasi.Error
	if _, err := pywasi.Eval[any](ctx, "1/0"); !errors.As(err, &perr) || perr.Type != "ZeroDivisionError" {
		t.Fatalf("got %v", err)
	}
}

func TestModules(t *testing.T) {
	py := newPython(t,
		pywasi.WithModule("geo", "from geo.shapes import area\nUNIT = 'm'\n"),
		pywasi.WithModule("geo.shapes", "import numpy as np\n\ndef area(w, h):\n    return np.prod([w, h])\n"),
		pywasi.WithModule("broken", `
			def f():
			    raise ValueError('nope')
		`),
		pywasi.WithModules(os.DirFS("testdata/pylib")),
	)
	ctx := context.Background()

	if area, err := py.Call[int](ctx, pywasi.Func{Module: "geo", Name: "area", Args: []any{2, 3}}); err != nil || area != 6 {
		t.Fatalf("area=%d err=%v", area, err)
	}
	if unit, err := py.Eval[string](ctx, "import geo\ngeo.UNIT"); err != nil || unit != "m" {
		t.Fatalf("unit=%q err=%v", unit, err)
	}
	if stats, err := py.Call[struct{ Mean float64 }](ctx, pywasi.Func{Module: "stats", Name: "summarize", Args: []any{[]float64{1, 3}}}); err != nil || stats.Mean != 2 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}

	// Tracebacks show the inline source.
	var perr *pywasi.Error
	if _, err := py.Call[any](ctx, pywasi.Func{Module: "broken", Name: "f"}); !errors.As(err, &perr) || !strings.Contains(perr.Traceback, "raise ValueError('nope')") {
		t.Fatalf("got %v, traceback %q", err, perr.Traceback)
	}

	// Modules are importable from Run, and a caller's PYTHONPATH comes first.
	code, stdout, stderr := run(t, py, pywasi.RunOptions{
		Args: []string{"-c", "import geo, stats, sys; print(geo.area(4, 5), sys.path.index('/x') < sys.path.index('/.pywasi/modules/0'))"},
		Env:  map[string]string{"PYTHONPATH": "/x"},
	})
	if code != 0 || strings.TrimSpace(stdout) != "20 True" {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, stdout, stderr)
	}

	if _, err := pywasi.New(ctx, pywasi.WithHome(filepath.Join(appDir, ".pywasi")), pywasi.WithModule("not-valid", "")); err == nil {
		t.Error("invalid module name should be rejected")
	}
}
