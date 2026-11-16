//go:build linux || darwin

package pywasi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateDir(t *testing.T) {
	base := t.TempDir()

	// Made private, and an existing one with loose permissions tightened.
	dir := filepath.Join(base, "space")
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v, want 0700", fi.Mode().Perm())
	}

	// Not through a symlink someone else could have put there.
	if err := os.Symlink(t.TempDir(), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(filepath.Join(base, "link")); err == nil {
		t.Fatal("symlink accepted")
	}
}
