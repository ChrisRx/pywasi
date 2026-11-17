package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestProjectApp(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  []string // the args, with the project directory as DIR
		err   string
	}{
		{
			name: "one script",
			files: map[string]string{"pyproject.toml": "[project]\nname = \"my-app\"\n[project.scripts]\nmy-app = \"my_app.cli:main\"\n",
				"src/my_app/__main__.py": "", "main.py": ""},
			want: []string{"-c", callScript, "my-app", "my_app.cli:main"},
		},
		{
			name:  "several scripts",
			files: map[string]string{"pyproject.toml": "[project]\nname = \"a\"\n[project.scripts]\nserve = \"a:serve\"\nmigrate = \"a:migrate\"\n"},
			err:   "several scripts, so give one: migrate, serve",
		},
		{
			name:  "poetry",
			files: map[string]string{"pyproject.toml": "[project]\nname = \"a\"\n[tool.poetry.scripts]\nrun = { reference = \"a:run\", type = \"console\" }\n"},
			want:  []string{"-c", callScript, "run", "a:run"},
		},
		{
			name:  "__main__",
			files: map[string]string{"pyproject.toml": "[project]\nname = \"My.App\"\n", "my_app/__main__.py": "", "main.py": ""},
			want:  []string{"-m", "my_app"},
		},
		{
			name:  "main.py",
			files: map[string]string{"pyproject.toml": "[project]\nname = \"a\"\n", "main.py": ""},
			want:  []string{"DIR/main.py"},
		},
		{
			name:  "nothing",
			files: map[string]string{"pyproject.toml": "[project]\nname = \"a\"\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeProject(t, tc.files)
			a, err := projectApp(dir)
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.args()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for i, w := range tc.want {
				tc.want[i] = strings.ReplaceAll(w, "DIR", filepath.ToSlash(dir))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
