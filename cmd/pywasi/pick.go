package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"go.chrisrx.dev/pywasi"
)

// pick lists the catalog's packages in the terminal (-pick), plus some
// pure-Python favorites from PyPI and any requirement typed in, links an
// interpreter with the ones chosen (with link) and returns it, with the
// Python arguments for a REPL that starts with their usual aliases (np, pd,
// pa, plt) imported. The last selection is remembered. A nil Python means
// the user quit.
func pick(ctx context.Context, catalogURL string, link func(reqs []string, log io.Writer) (*pywasi.Python, error)) (*pywasi.Python, []string, error) {
	c, err := fetchCatalog(ctx, catalogURL)
	if err != nil {
		return nil, nil, err
	}
	list, python := items(c)
	list = restoreSelection(list)

	m := newModel(list, python, strings.TrimPrefix(strings.TrimPrefix(catalogURL, "https://"), "http://"))
	m.link = link
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	m.send = p.Send
	if _, err := p.Run(); err != nil {
		return nil, nil, err
	}
	// Reported once the terminal is back: only remembering it failed.
	if m.saveErr != nil {
		fmt.Fprintln(os.Stderr, "pywasi: remembering the selection:", m.saveErr)
	}
	if m.py == nil {
		return nil, nil, nil
	}
	return m.py, sessionArgs(m.selected()), nil
}

// aliases are what a session with these packages usually starts with:
// the names they bind, and the code.
var aliases = map[string][2]string{
	"numpy":        {"np", "import numpy as np"},
	"pandas":       {"pd", "import pandas as pd"},
	"pyarrow":      {"pa, pc", "import pyarrow as pa, pyarrow.compute as pc"},
	"matplotlib":   {"plt", "import matplotlib; matplotlib.use('Agg'); import matplotlib.pyplot as plt"},
	"scipy":        {"scipy", "import scipy"},
	"scikit-learn": {"sklearn", "import sklearn"},
	"duckdb":       {"duckdb", "import duckdb"},
	"polars":       {"pl", "import polars as pl"},
	"sympy":        {"sp", "import sympy as sp"},
	"networkx":     {"nx", "import networkx as nx"},
}

// session starts the REPL from a -c program, after the aliases: pywasi's
// REPL doesn't read PYTHONSTARTUP.
const session = `
import json, os, sys
_imported = []
for _name, _code in json.loads(sys.argv[1]):
    try:
        exec(_code)
        _imported.append(_name)
    except Exception as _e:
        print(f"{_code}: {type(_e).__name__}: {_e}", file=sys.stderr)
print(f"Python {sys.version.split()[0]} in WebAssembly (pywasi {os.environ.get('PYWASI_VERSION', 'devel')}), with {sys.argv[2] or 'the standard library'}", file=sys.stderr)
if _imported:
    print("imported: " + ", ".join(_imported), file=sys.stderr)
del json, _imported
sys.argv = [""]
if os.environ.get("PYTHON_BASIC_REPL"):
    import code
    code.interact(banner="", local=globals(), exitmsg="")
else:
    from _pyrepl.main import interactive_console
    import __main__
    interactive_console(__main__)
`

// sessionArgs are the Python arguments for a REPL with these packages.
func sessionArgs(selected []item) []string {
	imports := [][2]string{}
	var names []string
	for _, it := range selected {
		names = append(names, it.name)
		if a, ok := aliases[requirementName(it.name)]; ok {
			imports = append(imports, a)
		}
	}
	importsJSON, _ := json.Marshal(imports)
	return []string{"-c", session, string(importsJSON), strings.Join(names, ", ")}
}

// pickerDir is where the picker keeps the last selection, in pywasi's
// cache ($PYWASI_CACHE, else the user cache directory).
func pickerDir() (string, error) {
	dir := os.Getenv("PYWASI_CACHE")
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "pywasi")
	}
	dir = filepath.Join(dir, "picker")
	return dir, os.MkdirAll(dir, 0o700)
}

func selectionFile() string {
	dir, err := pickerDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "selection.json")
}

func saveSelection(selected []item) error {
	file := selectionFile()
	if file == "" {
		return nil
	}
	var names []string
	for _, it := range selected {
		names = append(names, it.name)
	}
	b, err := json.Marshal(names)
	if err != nil {
		return err
	}
	return os.WriteFile(file, b, 0o600)
}

// restoreSelection selects what was selected last time, adding the
// requirements that were typed in.
func restoreSelection(list []item) []item {
	file := selectionFile()
	if file == "" {
		return list
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return list
	}
	var names []string
	if json.Unmarshal(b, &names) != nil {
		return list
	}
	for _, name := range names {
		i := slices.IndexFunc(list, func(it item) bool { return it.name == name })
		if i >= 0 {
			list[i].selected = true
		} else {
			list = append(list, item{name: name, desc: "added", kind: custom, selected: true})
		}
	}
	return list
}
