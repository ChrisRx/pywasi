// Package resolver holds the Python side of pywasi's package resolution
// (_pywasi_resolve.py), with packaging and resolvelib vendored under
// _vendor (licenses in _vendor/licenses). It runs inside python.wasm.
package resolver

import (
	"embed"
	"io/fs"
)

//go:embed all:files
var files embed.FS

// FS is the import root: _pywasi_resolve.py and _vendor/.
func FS() fs.FS {
	sub, err := fs.Sub(files, "files")
	if err != nil {
		panic(err)
	}
	return sub
}
