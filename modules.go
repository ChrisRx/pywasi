package pywasi

import (
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing/fstest"
)

var moduleName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// moduleFS returns the file systems for WithModule and WithModules, in
// import path order: inline modules first.
func moduleFS(opts config) ([]fs.FS, error) {
	var out []fs.FS
	if len(opts.modules) > 0 {
		inline := fstest.MapFS{}
		names := slices.Collect(maps.Keys(opts.modules))
		for name, source := range opts.modules {
			if !moduleName.MatchString(name) {
				return nil, fmt.Errorf("pywasi: WithModule: %q is not a module name", name)
			}
			file := strings.ReplaceAll(name, ".", "/") + ".py"
			// A module with submodules is a package.
			if slices.ContainsFunc(names, func(other string) bool { return strings.HasPrefix(other, name+".") }) {
				file = strings.ReplaceAll(name, ".", "/") + "/__init__.py"
			}
			if _, ok := inline[file]; ok {
				return nil, fmt.Errorf("pywasi: WithModule: %q and another module both map to %s", name, file)
			}
			inline[file] = &fstest.MapFile{Data: []byte(source), Mode: 0o444}
		}
		out = append(out, inline)
	}
	for _, fsys := range opts.moduleFS {
		if fsys == nil {
			return nil, fmt.Errorf("pywasi: WithModules: nil file system")
		}
		out = append(out, fsys)
	}
	return out, nil
}
