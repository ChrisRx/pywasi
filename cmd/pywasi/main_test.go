package main

import (
	"flag"
	"slices"
	"testing"
)

func TestSplitPython(t *testing.T) {
	fs := flag.NewFlagSet("pywasi", flag.ContinueOnError)
	fs.String("home", "", "")
	fs.Bool("cwd", true, "")
	fs.String("m", "", "")
	fs.String("c", "", "")
	for _, tc := range []struct {
		args, flags, python []string
	}{
		{args: []string{"-m", "json.tool", "-h"}, flags: []string{}, python: []string{"-m", "json.tool", "-h"}},
		{args: []string{"-home", "x", "-cwd", "--m=mod", "a"}, flags: []string{"-home", "x", "-cwd"}, python: []string{"-m", "mod", "a"}},
		{args: []string{"-home", "-m", "-m", "mod"}, flags: []string{"-home", "-m"}, python: []string{"-m", "mod"}},
		{args: []string{"script.py", "-m", "mod"}, flags: []string{"script.py", "-m", "mod"}},
		{args: []string{"--", "-m", "mod"}, flags: []string{"--", "-m", "mod"}},
		{args: []string{"-m"}, flags: []string{"-m"}},
		{args: []string{"-cwd=false", "-c", "print(1)", "-v"}, flags: []string{"-cwd=false"}, python: []string{"-c", "print(1)", "-v"}},
		{args: []string{"-c=pass"}, flags: []string{}, python: []string{"-c", "pass"}},
	} {
		flags, python := splitPython(fs, tc.args)
		if !slices.Equal(flags, tc.flags) || !slices.Equal(python, tc.python) {
			t.Errorf("splitPython(%q) = %q, %q, want %q, %q", tc.args, flags, python, tc.flags, tc.python)
		}
	}
}
