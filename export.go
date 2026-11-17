package pywasi

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Export writes the interpreter to dir, which must not exist or be empty,
// as one self-contained home: python.wasm and the standard library, with
// every package (WithPackages, WithProject, linked-in bundles) and module
// (WithModule, WithModules) in its site-packages. It runs in other
// runtimes without pywasi: HostStub is bin/pywasi-host-stub.wasm, and the
// run script starts it with wasmtime (dir/run -c 'print(1)'). It is also
// a home for WithHome (pywasi --home dir), where it starts from scratch
// rather than linking again.
func (p *Python) Export(dir string) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("pywasi: export: %s isn't empty", dir)
	}
	if err := copyDir(dir, p.home); err != nil {
		return fmt.Errorf("pywasi: export: %w", err)
	}
	wasm := filepath.Join(dir, "bin", "python.wasm")
	if filepath.Clean(p.wasmPath) != filepath.Join(p.home, "bin", "python.wasm") {
		if err := copyFile(wasm, p.wasmPath); err != nil {
			return fmt.Errorf("pywasi: export: %w", err)
		}
	}
	version, err := p.pythonVersion()
	if err != nil {
		return err
	}
	site := filepath.Join(dir, "lib", "python"+version, "site-packages")
	if err := os.MkdirAll(site, 0o755); err != nil {
		return err
	}
	// In import order reversed, so what comes first on the path wins.
	for i := len(p.modules) - 1; i >= 0; i-- {
		m := p.modules[i]
		var err error
		if m.host != "" {
			err = copyDir(site, m.host)
		} else {
			err = copyFS(site, m.fsys)
		}
		if err != nil {
			return fmt.Errorf("pywasi: export: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "pywasi-host-stub.wasm"), HostStub, 0o644); err != nil {
		return err
	}
	script := strings.ReplaceAll(runScript, "{version}", Version())
	return os.WriteFile(filepath.Join(dir, "run"), []byte(script), 0o755)
}

// runScript starts an exported interpreter with wasmtime.
const runScript = `#!/bin/sh
# Runs this Python, exported by pywasi ({version}), with wasmtime (or
# $WASMTIME), taking python's arguments:
#
#   ./run -c 'print(1 + 1)'
#   ./run script.py
#
# The current directory is mounted at its own path, and Python starts in
# it. /tmp, also HOME, is a directory kept per user. Sockets fail and the
# prompt is the basic one: those are pywasi's (bin/pywasi-host-stub.wasm
# stands in). The directory is a pywasi home too: pywasi --home DIR.
set -e
here=$(cd "$(dirname "$0")" && pwd)
tmp="${TMPDIR:-/tmp}/pywasi-export-$(id -u)"
mkdir -p "$tmp"
set -- --argv0 python "$here/bin/python.wasm" "$@"
if [ "$PWD" != / ]; then
  set -- --dir "$PWD::$PWD" --env "PYWASI_CWD=$PWD" "$@"
fi
exec "${WASMTIME:-wasmtime}" run \
  --preload "pywasi=$here/bin/pywasi-host-stub.wasm" \
  --wasm exceptions=y --wasm max-wasm-stack=16777216 \
  --dir "$here::/" --dir "$tmp::/tmp" \
  --env PYTHONHOME=/ --env PYTHONDONTWRITEBYTECODE=1 --env HOME=/tmp \
  --env PYWASI_VERSION={version} \
  "$@"
`

// copyDir copies src's files into dst (made if missing), following
// symbolic links.
func copyDir(dst, src string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(target, path)
	})
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
