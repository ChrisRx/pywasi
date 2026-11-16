package pywasi_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.chrisrx.dev/pywasi"
)

func archivePython(t *testing.T, options ...pywasi.Option) *pywasi.Python {
	t.Helper()
	py, err := pywasi.New(context.Background(), append([]pywasi.Option{pywasi.WithArchive(pywasi.DefaultArchive)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { py.Close() })
	return py
}

func TestTempDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	py := archivePython(t)

	// Runs share the interpreter's /tmp, which tempfile uses.
	_, out, _ := run(t, py, pywasi.RunOptions{Args: []string{"-c", `
import os, tempfile
open("/tmp/kept", "w").write("1")
print(tempfile.gettempdir(), os.path.expanduser("~"))
`}})
	if strings.TrimSpace(out) != "/tmp /tmp" {
		t.Fatalf("gettempdir() and ~ = %q, want /tmp /tmp", out)
	}
	_, out, _ = run(t, py, pywasi.RunOptions{Args: []string{"-c", `print(open("/tmp/kept").read())`}})
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("second run read %q, want 1", out)
	}

	// On the host, it's a private directory under the OS temp directory.
	dirs, _ := filepath.Glob(filepath.Join(tmp, "pywasi-*"))
	if len(dirs) != 1 {
		t.Fatalf("temp spaces %v, want one", dirs)
	}
	fi, err := os.Stat(dirs[0])
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("temp space %s: %v, mode %v", dirs[0], err, fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "kept")); err != nil {
		t.Fatal(err)
	}

	// Another interpreter of the same python.wasm shares it.
	_, out, _ = run(t, archivePython(t), pywasi.RunOptions{Args: []string{"-c", `print(open("/tmp/kept").read())`}})
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("other interpreter read %q, want 1", out)
	}
}

func TestTempDirOptions(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	write := pywasi.RunOptions{Args: []string{"-c", `
import os
print(os.path.isdir("/tmp"))
if os.path.isdir("/tmp"):
    open("/tmp/f", "w").write("x")
`}}

	// "" turns it off.
	if _, out, _ := run(t, archivePython(t, pywasi.WithTempDir("")), write); strings.TrimSpace(out) != "False" {
		t.Fatalf("WithTempDir(\"\"): /tmp exists: %q", out)
	}

	// A directory of the caller's.
	own := filepath.Join(t.TempDir(), "own")
	run(t, archivePython(t, pywasi.WithTempDir(own)), write)
	if _, err := os.Stat(filepath.Join(own, "f")); err != nil {
		t.Fatalf("WithTempDir(dir): %v", err)
	}

	// A Mount at /tmp takes its place.
	mounted := t.TempDir()
	opts := write
	opts.Mounts = []pywasi.Mount{{Host: mounted, Guest: "/tmp"}}
	run(t, archivePython(t), opts)
	if _, err := os.Stat(filepath.Join(mounted, "f")); err != nil {
		t.Fatalf("Mount at /tmp: %v", err)
	}
}
