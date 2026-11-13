// Package session holds the Python side of pywasi's Session
// (_pywasi_session.py), which runs inside python.wasm.
package session

import (
	"embed"
	"io/fs"
)

//go:embed all:files
var files embed.FS

// FS is the import root holding _pywasi_session.py.
func FS() fs.FS {
	sub, err := fs.Sub(files, "files")
	if err != nil {
		panic(err)
	}
	return sub
}
