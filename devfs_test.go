package pywasi_test

import (
	"strings"
	"testing"

	"go.chrisrx.dev/pywasi"
)

// Runs have a /dev with null, zero and urandom, and what pytest needs of
// os that WASI leaves out.
func TestDevAndOSShims(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	py := archivePython(t)
	_, out, errOut := run(t, py, pywasi.RunOptions{Args: []string{"-c", `
import os
with open(os.devnull, "w") as f:
    f.write("discarded")
print(open(os.devnull, "rb").read() == b"")
print(open("/dev/zero", "rb").read(4) == bytes(4), len(open("/dev/urandom", "rb").read(16)))
print(sorted(os.listdir("/dev")))
print(os.getuid(), os.geteuid(), os.stat("/tmp").st_uid, os.umask(0o077), os.umask(0o022))
`}})
	want := "True\nTrue 16\n['null', 'urandom', 'zero']\n0 0 0 18 63"
	if got := strings.TrimSpace(out); got != want {
		t.Errorf("got %q, want %q; stderr %s", got, want, errOut)
	}

	// A Mount at /dev takes its place.
	dir := t.TempDir()
	_, out, _ = run(t, py, pywasi.RunOptions{
		Args:   []string{"-c", "import os; print(os.listdir('/dev'))"},
		Mounts: []pywasi.Mount{{Host: dir, Guest: "/dev"}},
	})
	if got := strings.TrimSpace(out); got != "[]" {
		t.Errorf("mounted /dev: %q", got)
	}
}
