package pywasi

import (
	"context"
	"io"
	"io/fs"

	"github.com/ChrisRx/wazero/experimental"
)

// Option configures New.
type Option func(*config)

type config struct {
	home         string
	dependencies []string
	preload      []string
	root         string
	buildLog     io.Writer
	wasm         string
	cacheDir     string
	noCache      bool
	modules      map[string]string
	moduleFS     []fs.FS
	run          *RunOptions
	archive      Source
	packages     []string
	pylock       []byte
	resolver     Resolver
	index        string
	catalog      string
	catalogSet   bool
	boundsChecks bool
}

// WithHome uses a prebuilt interpreter: a directory containing
// bin/python.wasm and lib/python3.X (e.g. PROJECT/.pywasi from
// scripts/build.sh). It is mounted read-only at the guest root.
//
// Without WithHome or WithDependencies, New uses $PYWASI_HOME or .pywasi in
// the current directory if that exists, and otherwise builds an interpreter
// with only the standard library, the same way as WithDependencies.
func WithHome(dir string) Option {
	return func(c *config) { c.home = dir }
}

// WithDependencies builds an interpreter with these PEP 508 requirements
// ("numpy", "pyyaml==6.0.3", "tabulate>=0.9") using builder.Ensure.
// Repeated calls add to the list. This needs a pywasi checkout and its Nix
// toolchain (see WithRoot). The first build of a set can take minutes, and
// later ones are cached and locked.
func WithDependencies(requirements ...string) Option {
	return func(c *config) { c.dependencies = append(c.dependencies, requirements...) }
}

// WithPreload lists modules (import names, e.g. "yaml" for pyyaml) to import
// into the startup snapshot of a built interpreter. Repeated calls add to
// the list.
func WithPreload(modules ...string) Option {
	return func(c *config) { c.preload = append(c.preload, modules...) }
}

// WithRoot sets the pywasi checkout used to build interpreters. Defaults to
// $PYWASI_ROOT, then the current directory.
func WithRoot(dir string) Option {
	return func(c *config) { c.root = dir }
}

// WithBuildLog receives build output when an interpreter is built.
func WithBuildLog(w io.Writer) Option {
	return func(c *config) { c.buildLog = w }
}

// WithWasm overrides the interpreter module. Defaults to
// HOME/bin/python.wasm.
func WithWasm(path string) Option {
	return func(c *config) { c.wasm = path }
}

// WithCacheDir sets where compiled machine code is cached between
// processes. Defaults to DefaultCacheDir().
func WithCacheDir(dir string) Option {
	return func(c *config) { c.cacheDir, c.noCache = dir, false }
}

// WithoutCache disables the compiled code cache, so every process
// recompiles the module (seconds for a large interpreter).
func WithoutCache() Option {
	return func(c *config) { c.cacheDir, c.noCache = "", true }
}

// WithModule makes Python source importable under name in every Run, Eval
// and Call, without writing it to disk:
//
//	py, err := pywasi.New(ctx, pywasi.WithModule("geometry", `
//		def area(w, h):
//		    return w * h
//	`))
//	n, err := py.Call[int](ctx, pywasi.Func{Module: "geometry", Name: "area", Args: []any{2, 3}})
//
// The source is dedented, so it can be indented to match the Go source.
//
// A dotted name is a submodule ("geo.shapes" is geo/shapes.py), and a name
// that has submodules becomes its package's __init__.py. Repeated calls add
// modules, and a repeated name replaces the earlier source.
func WithModule(name, source string) Option {
	return func(c *config) {
		if c.modules == nil {
			c.modules = map[string]string{}
		}
		c.modules[name] = dedent(source)
	}
}

// WithModules adds a file system of Python modules and packages to the
// import path of every Run, Eval and Call, for example an embedded
// directory:
//
//	//go:embed pylib
//	var pylib embed.FS
//
//	sub, err := fs.Sub(pylib, "pylib")
//	py, err := pywasi.New(ctx, pywasi.WithModules(sub))
//
// Its root is the import root, so pylib/stats.py imports as stats. It is
// mounted read-only. Repeated calls add file systems, searched in order.
func WithModules(fsys fs.FS) Option {
	return func(c *config) { c.moduleFS = append(c.moduleFS, fsys) }
}

// WithRunOptions sets the environment, mounts and stdio for the
// package-level Eval and Call. New doesn't accept it: pass RunOptions to
// each Run, Eval or Call instead.
func WithRunOptions(opts RunOptions) Option {
	return func(c *config) { c.run = &opts }
}

// WithBoundsChecks compiles python.wasm with bounds checks on every memory
// access, so an out-of-bounds access traps as the WebAssembly spec requires.
//
// By default pywasi compiles without them (wazero's
// experimental.WithUncheckedMemoryAccess): each instance's memory is a
// fixed 8 GiB virtual reservation, of which only touched pages use physical
// memory, that every address python.wasm can compute falls inside, which
// makes pure-Python code 6-30% faster. An out-of-bounds access, which only
// a bug in C code could make, then reads or writes inside that instance's
// reservation instead of trapping. It can't reach the host's memory.
func WithBoundsChecks() Option {
	return func(c *config) { c.boundsChecks = true }
}

// compileContext is ctx for compiling wasm, with or without bounds checks.
func compileContext(ctx context.Context, boundsChecks bool) context.Context {
	if boundsChecks {
		return ctx
	}
	return experimental.WithUncheckedMemoryAccess(ctx)
}
