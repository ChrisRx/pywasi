package pywasi_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.chrisrx.dev/pywasi"
)

// Command describes a run for another runtime: in-memory modules are
// written out, and the interpreter's home is first, at /.
func TestCommand(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	py := archivePython(t, pywasi.WithModule("greet", "def hi(): return 'hi'"))
	dir := t.TempDir()
	cmd, err := py.Command(pywasi.RunOptions{
		Args:   []string{"-c", "pass"},
		Env:    map[string]string{"X": "1"},
		Mounts: []pywasi.Mount{{Host: dir, Guest: "/data", ReadOnly: true}},
		Dir:    "/data",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cmd.Args, []string{"python", "-c", "pass"}) {
		t.Errorf("args %q", cmd.Args)
	}
	if cmd.Dirs[0].Guest != "/" || !cmd.Dirs[0].ReadOnly {
		t.Errorf("first dir %+v", cmd.Dirs[0])
	}
	var module string
	for _, d := range cmd.Dirs {
		if d.Guest == "/data" && (d.Host != dir || !d.ReadOnly) {
			t.Errorf("mount %+v", d)
		}
		if strings.Contains(d.Guest, "/modules/") {
			module = d.Host
		}
	}
	if b, err := os.ReadFile(filepath.Join(module, "greet.py")); err != nil || !strings.Contains(string(b), "def hi") {
		t.Errorf("module written to %q: %q %v", module, b, err)
	}
	for _, kv := range []string{"X=1", "PYTHONHOME=/", "PYWASI_CWD=/data"} {
		if !slices.Contains(cmd.Env, kv) {
			t.Errorf("env %q lacks %s", cmd.Env, kv)
		}
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(module); !os.IsNotExist(err) {
		t.Errorf("%s not removed: %v", module, err)
	}
}

// An exported interpreter is a home of its own: its modules are in
// site-packages, next to the host stub and the run script.
func TestExport(t *testing.T) {
	t.Setenv("PYWASI_CACHE", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	py := archivePython(t, pywasi.WithModule("greet", "def hi(): return 'hi'"))
	dir := filepath.Join(t.TempDir(), "py")
	if err := py.Export(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bin/python.wasm", "bin/pywasi-host-stub.wasm", "run"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Error(err)
		}
	}
	if err := py.Export(dir); err == nil {
		t.Error("exported over a directory that isn't empty")
	}
	exported, err := pywasi.New(t.Context(), pywasi.WithHome(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer exported.Close()
	if got, err := exported.Eval[string](t.Context(), "import greet; greet.hi()"); err != nil || got != "hi" {
		t.Errorf("exported: %q, %v", got, err)
	}
}
