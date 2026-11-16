package pywasi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.chrisrx.dev/pywasi"
)

// The default interpreter is snapshotted by New, with what's asked for
// preloaded; a module that fails to import there is left out.
func TestSnapshotImage(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("PYWASI_CACHE", cache)
	t.Setenv("TMPDIR", t.TempDir())
	check := pywasi.RunOptions{Args: []string{"-c", `
import sys
print("fractions" in sys.modules, "_pywasi" in sys.modules, "no_such_module" in sys.modules)
`}}
	py := archivePython(t, pywasi.WithPreload("fractions", "no_such_module"))
	if _, out, _ := run(t, py, check); strings.TrimSpace(out) != "True True False" {
		t.Fatalf("preloaded: %q", out)
	}
	images, _ := filepath.Glob(filepath.Join(cache, "images", "*.img"))
	if len(images) != 1 {
		t.Fatalf("images %v, want one", images)
	}

	// Each run starts from the snapshot, not from what the last one left.
	run(t, py, pywasi.RunOptions{Args: []string{"-c", "import fractions; fractions.marker = 1"}})
	if _, out, _ := run(t, py, pywasi.RunOptions{Args: []string{"-c", "import fractions; print(hasattr(fractions, 'marker'))"}}); strings.TrimSpace(out) != "False" {
		t.Fatalf("state carried over: %q", out)
	}

	// The same preloads reuse the image; others get their own.
	if _, out, _ := run(t, archivePython(t, pywasi.WithPreload("fractions", "no_such_module")), check); strings.TrimSpace(out) != "True True False" {
		t.Fatalf("cached image: %q", out)
	}
	if _, out, _ := run(t, archivePython(t), check); strings.TrimSpace(out) != "False True False" {
		t.Fatalf("no preloads: %q", out)
	}
	if images, _ := filepath.Glob(filepath.Join(cache, "images", "*.img")); len(images) != 2 {
		t.Fatalf("images %v, want two", images)
	}
}

// Pure-Python packages asked for are preloaded too.
func TestSnapshotImagePackages(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	py, err := stdlibPython(t, pywasi.WithPackages("tabulate"))
	if err != nil {
		t.Fatal(err)
	}
	_, out, _ := run(t, py, pywasi.RunOptions{Args: []string{"-c", `import sys; print("tabulate" in sys.modules)`}})
	if strings.TrimSpace(out) != "True" {
		t.Fatalf("tabulate preloaded: %q", out)
	}
}

// Runs from a snapshot aren't finalized, but exit as programs expect:
// statuses, tracebacks, atexit handlers and files left open.
func TestSnapshotExit(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	py := archivePython(t)
	for _, tc := range []struct {
		code     string
		status   int
		out, err string
	}{
		{"pass", 0, "", ""},
		{"import sys; sys.exit(3)", 3, "", ""},
		{"import sys; sys.exit('bye')", 1, "", "bye"},
		{"raise ValueError('boom')", 1, "", "ValueError: boom"},
		{"import atexit; atexit.register(print, 'at exit'); print('main')", 0, "main\nat exit", ""},
		{"import sys; sys.stdout.write('unflushed')", 0, "unflushed", ""},
	} {
		var stdout, stderr strings.Builder
		status, err := py.Run(t.Context(), pywasi.RunOptions{Args: []string{"-c", tc.code}, Stdout: &stdout, Stderr: &stderr})
		if err != nil {
			t.Fatalf("%s: %v", tc.code, err)
		}
		if status != tc.status || strings.TrimSpace(stdout.String()) != tc.out || !strings.Contains(stderr.String(), tc.err) {
			t.Errorf("%s: status %d, stdout %q, stderr %q", tc.code, status, stdout.String(), stderr.String())
		}
	}

	// A file left open is flushed at exit.
	dir := t.TempDir()
	run(t, py, pywasi.RunOptions{
		Args:   []string{"-c", "import pathlib; f = open('/out/a.txt', 'w'); f.write('a' * 10); g = pathlib.Path('/out/b.txt').open('w'); g.write('b')"},
		Mounts: []pywasi.Mount{{Host: dir, Guest: "/out"}},
	})
	for name, want := range map[string]string{"a.txt": "aaaaaaaaaa", "b.txt": "b"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != want {
			t.Errorf("%s: %q %v", name, b, err)
		}
	}
}
