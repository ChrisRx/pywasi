package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// app is what a project declares it runs as, as uv run and installed
// commands have it.
type app struct {
	// scripts are [project.scripts] (and Poetry's [tool.poetry.scripts]):
	// command names and the functions they call, as module:function.
	scripts map[string]string
	// mainModule is a package with a __main__.py, to run with -m.
	mainModule string
	// mainFile is a main.py at the project's root.
	mainFile string
}

// projectApp reads what the uv project in dir runs as.
func projectApp(dir string) (*app, error) {
	var pyproject struct {
		Project struct {
			Name    string            `toml:"name"`
			Scripts map[string]string `toml:"scripts"`
		} `toml:"project"`
		Tool struct {
			Poetry struct {
				Scripts map[string]any `toml:"scripts"`
			} `toml:"poetry"`
		} `toml:"tool"`
	}
	if _, err := toml.DecodeFile(filepath.Join(dir, "pyproject.toml"), &pyproject); err != nil {
		return nil, fmt.Errorf("pywasi: %s: %w", filepath.Join(dir, "pyproject.toml"), err)
	}
	a := &app{scripts: map[string]string{}}
	for name, target := range pyproject.Project.Scripts {
		a.scripts[name] = target
	}
	for name, v := range pyproject.Tool.Poetry.Scripts {
		// A string, or a table naming it (reference) for a console script.
		switch v := v.(type) {
		case string:
			a.scripts[name] = v
		case map[string]any:
			if ref, ok := v["reference"].(string); ok && v["type"] != "file" {
				a.scripts[name] = ref
			}
		}
	}
	if pkg := strings.ReplaceAll(normalize(pyproject.Project.Name), "-", "_"); pkg != "" {
		for _, root := range []string{filepath.Join(dir, "src"), dir} {
			if isFile(filepath.Join(root, pkg, "__main__.py")) {
				a.mainModule = pkg
				break
			}
		}
	}
	if main := filepath.Join(dir, "main.py"); isFile(main) {
		a.mainFile = main
	}
	return a, nil
}

// args are the Python arguments that run the app, for a run without any,
// or nil if it declares nothing to run.
func (a *app) args() ([]string, error) {
	switch {
	case len(a.scripts) == 1:
		for name, target := range a.scripts {
			return scriptArgs(name, target, nil), nil
		}
	case len(a.scripts) > 1:
		names := make([]string, 0, len(a.scripts))
		for name := range a.scripts {
			names = append(names, name)
		}
		slices.Sort(names)
		return nil, fmt.Errorf("pywasi: the project has several scripts, so give one: %s (or -i for a prompt)", strings.Join(names, ", "))
	case a.mainModule != "":
		return []string{"-m", a.mainModule}, nil
	case a.mainFile != "":
		return []string{filepath.ToSlash(a.mainFile)}, nil
	}
	return nil, nil
}

// scriptArgs are the Python arguments that call a [project.scripts]
// target as its installed command would: with sys.argv[0] the command's
// name, exiting with what it returns.
func scriptArgs(name, target string, args []string) []string {
	return append([]string{"-c", callScript, name, target}, args...)
}

const callScript = `
import sys
from importlib import import_module

name, target = sys.argv[1], sys.argv[2]
target = target.split("[")[0].strip()  # no extras
module, _, attrs = target.partition(":")
fn = import_module(module.strip())
for attr in filter(None, attrs.strip().split(".")):
    fn = getattr(fn, attr)
sys.argv = [name, *sys.argv[3:]]
sys.exit(fn())
`

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
