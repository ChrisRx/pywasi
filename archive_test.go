package pywasi_test

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"go.chrisrx.dev/pywasi"
)

// stdlibArchive is the embedded interpreter, as a file.
var stdlibArchive = "internal/stdlib/python3.14-stdlib.tar.zst"

func TestArchiveFile(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	ctx := context.Background()

	for range 2 { // The second New reuses the unpacked home.
		py, err := pywasi.New(ctx, pywasi.WithArchive(pywasi.ArchiveFile(stdlibArchive)), pywasi.WithoutCache())
		if err != nil {
			t.Fatal(err)
		}
		v, err := py.Eval[string](ctx, "import sys\nsys.version.split()[0]")
		py.Close()
		if err != nil || !strings.HasPrefix(v, "3.14") {
			t.Fatalf("version=%q err=%v", v, err)
		}
	}
	homes, _ := filepath.Glob(filepath.Join(os.Getenv("PYWASI_CACHE"), "homes", "*"))
	if len(homes) != 1 {
		t.Fatalf("homes = %v, want one", homes)
	}
}

func TestDefaultArchive(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	t.Setenv("PYWASI_HOME", filepath.Join(t.TempDir(), "missing"))
	ctx := context.Background()
	n, err := pywasi.Eval[int](ctx, "import json\nlen(json.dumps([1, 2]))")
	if err != nil || n != 6 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestArchiveErrors(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	ctx := context.Background()
	var err error

	// Wrong digest.
	_, err = pywasi.New(ctx, pywasi.WithArchive(pywasi.ArchiveURL(
		"https://pywasi.chrisrx.dev/interpreters/python3.14-stdlib-f1dfcc0b2ee8.tar.zst",
		"sha256:"+strings.Repeat("0", 64))))
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("wrong digest: %v", err)
	}

	// Paths outside the home are rejected.
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := pywasi.New(ctx, pywasi.WithArchive(pywasi.ArchiveBytes(buf.Bytes()))); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Errorf("escape: %v", err)
	}

	if _, err := pywasi.New(ctx, pywasi.WithArchive(pywasi.ArchiveFile(stdlibArchive)), pywasi.WithHome("x")); err == nil {
		t.Error("WithArchive and WithHome together should fail")
	}
}
