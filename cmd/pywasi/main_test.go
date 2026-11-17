package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSplitPython(t *testing.T) {
	fs := newCommand().Flags()
	for _, tc := range []struct {
		args, flags, python []string
	}{
		{args: []string{"-m", "json.tool", "-h"}, flags: []string{}, python: []string{"-m", "json.tool", "-h"}},
		{args: []string{"--home", "x", "--cwd", "-m", "mod", "a"}, flags: []string{"--home", "x", "--cwd"}, python: []string{"-m", "mod", "a"}},
		{args: []string{"--home", "-m", "-m", "mod"}, flags: []string{"--home", "-m"}, python: []string{"-m", "mod"}},
		{args: []string{"script.py", "-m", "mod"}, flags: []string{"script.py", "-m", "mod"}},
		{args: []string{"--", "-m", "mod"}, flags: []string{"--", "-m", "mod"}},
		{args: []string{"-m"}, flags: []string{"-m"}},
		{args: []string{"--cwd=false", "-c", "print(1)", "-v"}, flags: []string{"--cwd=false"}, python: []string{"-c", "print(1)", "-v"}},
		{args: []string{"-c=pass"}, flags: []string{}, python: []string{"-c", "pass"}},
		{args: []string{"-cpass", "x"}, flags: []string{}, python: []string{"-c", "pass", "x"}},
		{args: []string{"--module=http.server", "8000"}, flags: []string{}, python: []string{"-m", "http.server", "8000"}},
		{args: []string{"-d", "--catalog", "u", "--mount", "a:/b"}, flags: []string{"-d", "--catalog", "u", "--mount", "a:/b"}},
	} {
		flags, python := splitPython(fs, tc.args)
		if !slices.Equal(flags, tc.flags) || !slices.Equal(python, tc.python) {
			t.Errorf("splitPython(%q) = %q, %q, want %q, %q", tc.args, flags, python, tc.flags, tc.python)
		}
	}
}

// pywasi's flags are POSIX ones, and Python's arguments after a script
// aren't pywasi's.
func TestFlags(t *testing.T) {
	cmd := newCommand()
	args := bareDeps(cmd.Flags(), []string{"--home", "h", "-d", "pandas,pyarrow==26.0.1", "--mount", "x:/y", "--net=false", "script.py", "--home", "z"})
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	f := cmd.Flags()
	if deps, _ := f.GetStringSlice("deps"); !slices.Equal(deps, []string{"pandas", "pyarrow==26.0.1"}) {
		t.Errorf("deps %q", deps)
	}
	if home, _ := f.GetString("home"); home != "h" {
		t.Errorf("home %q", home)
	}
	if args := f.Args(); !slices.Equal(args, []string{"script.py", "--home", "z"}) {
		t.Errorf("args %q", args)
	}
}

// -d alone (picking in the terminal) is told from -d with packages.
func TestBareDeps(t *testing.T) {
	fs := newCommand().Flags()
	script := filepath.Join(t.TempDir(), "script.py")
	if err := os.WriteFile(script, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ args, want []string }{
		{[]string{"-d"}, []string{"--deps="}},
		{[]string{"--deps", "--net=false"}, []string{"--deps=", "--net=false"}},
		{[]string{"--home", "x", "-d"}, []string{"--home", "x", "--deps="}},
		{[]string{"-d", "pandas"}, []string{"-d", "pandas"}},
		{[]string{"-d", "pandas", "-d"}, []string{"-d", "pandas", "--deps="}},
		{[]string{"-d", "--", "script.py"}, []string{"--deps=", "--", "script.py"}},
		{[]string{"script.py", "-d"}, []string{"script.py", "-d"}},
		{[]string{"-c", "pass", "-d"}, []string{"-c", "pass", "-d"}},
		// A file or directory after -d is the script, not packages.
		{[]string{"-d", script}, []string{"--deps=", script}},
		{[]string{"-d", "pandas", script}, []string{"-d", "pandas", script}},
	} {
		if got := bareDeps(fs, tc.args); !slices.Equal(got, tc.want) {
			t.Errorf("bareDeps(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
	cmd := newCommand()
	if err := cmd.ParseFlags(bareDeps(cmd.Flags(), []string{"-d"})); err != nil {
		t.Fatal(err)
	}
	if deps, _ := cmd.Flags().GetStringSlice("deps"); !cmd.Flags().Changed("deps") || len(deps) != 0 {
		t.Errorf("bare -d: changed %v, deps %q", cmd.Flags().Changed("deps"), deps)
	}
}
