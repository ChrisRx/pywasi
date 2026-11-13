package pywasi_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.chrisrx.dev/pywasi"
)

// stdlibPython is the embedded standard library interpreter,
// with its own cache.
func stdlibPython(t *testing.T, options ...pywasi.Option) (*pywasi.Python, error) {
	t.Helper()
	if testing.Short() {
		t.Skip("downloads packages")
	}
	py, err := pywasi.New(context.Background(), append([]pywasi.Option{
		pywasi.WithArchive(pywasi.DefaultArchive),
	}, options...)...)
	if err == nil {
		t.Cleanup(func() { py.Close() })
	}
	return py, err
}

const richCheck = `
import io, rich, tabulate
from rich.console import Console
out = io.StringIO()
Console(file=out, width=40).print("[bold]hi[/bold]")
[out.getvalue().strip(), tabulate.tabulate([[1, 2]])]
`

func TestPackagesBuiltin(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	py, err := stdlibPython(t, pywasi.WithPackages("rich", "tabulate>=0.9"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := py.Eval[[]string](context.Background(), richCheck)
	if err != nil || got[0] != "hi" || !strings.Contains(got[1], "1  2") {
		t.Fatalf("got %q err=%v", got, err)
	}
	// The resolution is cached: a second interpreter needs no index.
	res, _ := filepath.Glob(filepath.Join(os.Getenv("PYWASI_CACHE"), "resolutions", "*.json"))
	if len(res) != 1 {
		t.Fatalf("resolutions = %v", res)
	}
}

func TestPackagesPylock(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	lock, err := os.ReadFile("testdata/pylock.toml")
	if err != nil {
		t.Fatal(err)
	}
	py, err := stdlibPython(t, pywasi.WithPylock(lock))
	if err != nil {
		t.Fatal(err)
	}
	v, err := py.Eval[string](context.Background(), "import importlib.metadata as m\nm.version('rich')")
	if err != nil || v != "15.0.0" {
		t.Fatalf("rich %q err=%v", v, err)
	}
}

func TestPackagesUV(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	t.Setenv("PATH", "") // Use the pinned uv download.
	py, err := stdlibPython(t, pywasi.WithPackages("rich", "tabulate"), pywasi.WithResolver(pywasi.ResolverUV))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := py.Eval[[]string](context.Background(), richCheck); err != nil || got[0] != "hi" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestPackagesErrors(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	_, err := stdlibPython(t, pywasi.WithPackages("numpy"), pywasi.WithCatalog(""))
	if err == nil || !strings.Contains(err.Error(), "no pure-Python wheel or prebuilt bundle for numpy") {
		t.Errorf("numpy: %v", err)
	}
	_, err = stdlibPython(t, pywasi.WithPackages("rich==0.0.0.1"))
	if err == nil || !strings.Contains(err.Error(), "no versions satisfy") {
		t.Errorf("missing version: %v", err)
	}
}

func TestPackagesBuiltIn(t *testing.T) {
	// numpy is built into this interpreter, so it satisfies requirements
	// for it without a download.
	home := filepath.Join(appDir, ".pywasi")
	if _, err := os.Stat(filepath.Join(home, "bin", "python.wasm")); err != nil {
		t.Skip("examples/numpy-app/.pywasi not built")
	}
	if testing.Short() {
		t.Skip("downloads packages")
	}
	t.Setenv("PYWASI_CACHE", t.TempDir())
	py := newPython(t, pywasi.WithPackages("numpy", "tabulate"))
	got, err := py.Eval[string](context.Background(), "import numpy, tabulate\ntabulate.tabulate(numpy.eye(2, dtype=int))")
	if err != nil || !strings.Contains(got, "1  0") {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestPackagesLinked(t *testing.T) {
	// numpy has no pure wheel, so it comes from the catalog's prebuilt
	// bundle, linked into a new interpreter by wasm-ld running in wazero.
	t.Setenv("PYWASI_CACHE", t.TempDir())
	py, err := stdlibPython(t, pywasi.WithPackages("numpy", "tabulate"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := py.Eval[string](context.Background(), `
		import numpy as np, tabulate, time
		time.sleep(0.01)
		tabulate.tabulate(np.linalg.inv(np.array([[4.0, 1.0], [2.0, 3.0]])).round(2))
	`)
	if err != nil || !strings.Contains(got, "0.3") || !strings.Contains(got, "-0.2") {
		t.Fatalf("got %q err=%v", got, err)
	}
}
