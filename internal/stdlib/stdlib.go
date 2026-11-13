// Package stdlib embeds pywasi's default interpreter: CPython for WASI with
// only the standard library (scripts/build.sh --env --archive). It is also
// published at https://pywasi.chrisrx.dev/interpreters/.
package stdlib

import _ "embed"

// Archive is the interpreter archive (.tar.zst).
//
//go:embed python3.14-stdlib.tar.zst
var Archive []byte
