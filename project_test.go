package pywasi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.chrisrx.dev/pywasi"
)

// A uv project runs with its locked dependencies, its code importable.
func TestProject(t *testing.T) {
	if testing.Short() {
		t.Skip("resolves and downloads packages")
	}
	t.Setenv("PYWASI_CACHE", t.TempDir())
	dir := t.TempDir()
	for name, content := range map[string]string{
		"pyproject.toml":       "[project]\nname = \"demo\"\nversion = \"0.1.0\"\nrequires-python = \">=3.14\"\ndependencies = [\"tabulate==0.9.0\"]\n",
		"src/demo/__init__.py": "from tabulate import tabulate\n\ndef table():\n    return tabulate([[1, 2]], headers=['a', 'b'], tablefmt='plain')\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	py, err := pywasi.New(t.Context(), pywasi.WithProject(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer py.Close()
	got, err := py.Eval[string](t.Context(), "import demo\ndemo.table()")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Fields(got)[0] != "a" {
		t.Errorf("got %q", got)
	}
	// uv locked it, as uv run would.
	if _, err := os.Stat(filepath.Join(dir, "uv.lock")); err != nil {
		t.Errorf("no uv.lock: %v", err)
	}
}
