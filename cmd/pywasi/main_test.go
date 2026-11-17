package main

import (
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
	if err := cmd.ParseFlags([]string{"-d", "--home", "h", "--mount", "x:/y", "--net=false", "script.py", "--home", "z"}); err != nil {
		t.Fatal(err)
	}
	f := cmd.Flags()
	if deps, _ := f.GetBool("deps"); !deps {
		t.Error("-d not set")
	}
	if home, _ := f.GetString("home"); home != "h" {
		t.Errorf("home %q", home)
	}
	if args := f.Args(); !slices.Equal(args, []string{"script.py", "--home", "z"}) {
		t.Errorf("args %q", args)
	}
}
