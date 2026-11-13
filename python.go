// Package pywasi runs a WASI build of CPython (python.wasm, optionally with
// statically linked extension modules such as numpy) inside a Go program
// using wazero, without cgo.
//
// The interpreter is compiled once by New and every Run gets a fresh,
// isolated instance, so a single Python can be shared across goroutines.
package pywasi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
	"go.chrisrx.dev/x/context"

	"go.chrisrx.dev/pywasi/builder"
)

// Mount exposes a host directory to Python at a guest path.
type Mount struct {
	Host     string
	Guest    string
	ReadOnly bool
}

// RunOptions configures a single interpreter invocation.
type RunOptions struct {
	// Args are passed to python after argv[0], e.g. {"-c", "print(1)"}.
	Args   []string
	Env    map[string]string
	Mounts []Mount
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// fsMounts are pywasi's own mounts (not checked against reservedGuest).
	fsMounts []fsMount
}

type fsMount struct {
	fsys  fs.FS
	guest string
}

var _ io.Closer = (*Python)(nil)

// Python is a compiled interpreter that can be run many times.
type Python struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	home     string
	// modules are WithModule and WithModules file systems, linked-in
	// bundles' Python files and pure-Python packages, in PYTHONPATH order.
	modules []fsMount
	// digest identifies python.wasm (sha256 prefix), for cache keys.
	digest string
	// bundleSites are the Python files of linked-in bundles.
	bundleSites []string
}

// relink replaces the interpreter with one linked with layers' bundles.
func (p *Python) relink(ctx context.Context, layers layers, cacheDir string) error {
	home, sites, err := linkInterpreter(ctx, *layers.Linker, *layers.Kit, layers.Bundles, cacheDir)
	if err != nil {
		return err
	}
	wasm, err := os.ReadFile(filepath.Join(home, "bin", "python.wasm"))
	if err != nil {
		return err
	}
	compiled, err := p.runtime.CompileModule(ctx, wasm)
	if err != nil {
		return fmt.Errorf("pywasi: compiling linked interpreter: %w", err)
	}
	if err := p.compiled.Close(ctx); err != nil {
		return err
	}
	sum := sha256.Sum256(wasm)
	p.compiled, p.home, p.digest, p.bundleSites = compiled, home, hex.EncodeToString(sum[:16]), sites
	return nil
}

// New compiles python.wasm and prepares the WASI host functions.
func New(ctx context.Context, options ...Option) (*Python, error) {
	var opts config
	for _, o := range options {
		o(&opts)
	}
	sources := 0
	for _, set := range []bool{opts.home != "", len(opts.dependencies) > 0, opts.archive != nil} {
		if set {
			sources++
		}
	}
	switch {
	case opts.run != nil:
		return nil, errors.New("pywasi: WithRunOptions is for the package-level Eval and Call; pass RunOptions to Run, Eval or Call")
	case sources > 1:
		return nil, errors.New("pywasi: use only one of WithHome, WithDependencies and WithArchive")
	case len(opts.dependencies) > 0:
		built, err := build(ctx, opts)
		if err != nil {
			return nil, err
		}
		opts.home = built
	case opts.archive != nil:
		home, err := installArchive(ctx, opts.archive)
		if err != nil {
			return nil, err
		}
		opts.home = home
	case opts.home == "":
		// An existing interpreter ($PYWASI_HOME or ./.pywasi), else the
		// embedded standard library one.
		if home := DefaultHome(); isHome(home) {
			opts.home = home
			break
		}
		home, err := installArchive(ctx, DefaultArchive)
		if err != nil {
			return nil, fmt.Errorf("pywasi: unpacking the default interpreter: %w", err)
		}
		opts.home = home
	}
	modules, err := moduleFS(opts)
	if err != nil {
		return nil, err
	}
	if opts.cacheDir == "" && !opts.noCache {
		opts.cacheDir = DefaultCacheDir()
	}
	home, err := filepath.Abs(opts.home)
	if err != nil {
		return nil, err
	}
	wasmPath := opts.wasm
	if wasmPath == "" {
		wasmPath = filepath.Join(home, "bin", "python.wasm")
	}
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("pywasi: reading interpreter: %w", err)
	}

	// DWARF-based stack traces make every proc_exit (sys.exit with a
	// nonzero code, os._exit, the snapshot entry point) parse the debug
	// info of a ~40MB module: seconds per process. Function names from the
	// name section are still used.
	cfg := wazero.NewRuntimeConfig().WithDebugInfoEnabled(false)
	if opts.cacheDir != "" {
		cache, err := wazero.NewCompilationCacheWithDir(opts.cacheDir)
		if err != nil {
			return nil, fmt.Errorf("pywasi: compilation cache: %w", err)
		}
		cfg = cfg.WithCompilationCache(cache)
	}

	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("pywasi: instantiating WASI: %w", err)
	}
	compiled, err := r.CompileModule(ctx, wasm)
	if err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("pywasi: compiling interpreter: %w", err)
	}
	sum := sha256.Sum256(wasm)
	p := &Python{runtime: r, compiled: compiled, home: home, digest: hex.EncodeToString(sum[:16])}
	// Pure-Python packages are resolved by this interpreter, before the
	// modules are mounted.
	site, layers, err := p.packageLayer(ctx, opts)
	if err != nil {
		r.Close(ctx)
		return nil, err
	}
	if len(layers.Bundles) > 0 {
		// Packages with C extensions: switch to an interpreter linked
		// with them.
		if err := p.relink(ctx, layers, opts.cacheDir); err != nil {
			r.Close(ctx)
			return nil, err
		}
	}
	for i, fsys := range modules {
		p.modules = append(p.modules, fsMount{fsys, fmt.Sprintf("%s/modules/%d", guestRoot, i)})
	}
	for i, dir := range p.bundleSites {
		p.modules = append(p.modules, fsMount{os.DirFS(dir), bundleGuest(i)})
	}
	if site != "" {
		p.modules = append(p.modules, fsMount{os.DirFS(site), guestRoot + "/site"})
	}
	context.AddCleanup(ctx, func() {
		if err := p.Close(); err != nil {
			panic(err)
		}
	})
	return p, nil
}

func build(ctx context.Context, opts config) (string, error) {
	return builder.Ensure(ctx, builder.Options{
		Dependencies: opts.dependencies,
		Preload:      opts.preload,
		Root:         opts.root,
		Log:          opts.buildLog,
	})
}

// DefaultHome is where New looks for an interpreter without WithHome or
// WithDependencies: $PYWASI_HOME, or .pywasi in the current
// directory (where scripts/build.sh puts a project's interpreter).
func DefaultHome() string {
	if home := os.Getenv("PYWASI_HOME"); home != "" {
		return home
	}
	return ".pywasi"
}

// DefaultCacheDir is where compiled code is cached by default:
// os.UserCacheDir()/pywasi/wazero (or $PYWASI_CACHE/wazero), or "" (no
// cache) if there is no user cache directory.
func DefaultCacheDir() string {
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "wazero")
}

// Run executes the interpreter to completion and returns its exit code.
// A non-nil error means the module could not run or trapped; a Python
// exception that exits normally is reported through the exit code.
func (p *Python) Run(ctx context.Context, opts RunOptions) (int, error) {
	if err := checkMounts(opts.Mounts); err != nil {
		return 0, err
	}
	return p.run(ctx, opts)
}

// reservedGuest are guest paths a Mount may not cover: the interpreter's home
// (stdlib under /lib, python.wasm under /bin). Paths under guestRoot are
// reserved too.
var reservedGuest = []string{"/", "/bin", "/lib"}

// guestRoot holds pywasi's own mounts: modules and the Eval/Call directory.
const guestRoot = "/.pywasi"

func checkMounts(mounts []Mount) error {
	for _, m := range mounts {
		guest := path.Clean(m.Guest)
		if !path.IsAbs(guest) {
			return fmt.Errorf("pywasi: mount guest path %q must be absolute", m.Guest)
		}
		if guest == guestRoot || strings.HasPrefix(guest, guestRoot+"/") {
			return fmt.Errorf("pywasi: mount guest path %q is reserved for pywasi", m.Guest)
		}
		if slices.Contains(reservedGuest, guest) {
			return fmt.Errorf("pywasi: mount at %q would hide the interpreter's files; use another guest path such as /app", m.Guest)
		}
	}
	return nil
}

func (p *Python) run(ctx context.Context, opts RunOptions) (int, error) {
	fsCfg := wazero.NewFSConfig().WithReadOnlyDirMount(p.home, "/")
	for _, m := range opts.Mounts {
		host, err := filepath.Abs(m.Host)
		if err != nil {
			return 0, err
		}
		if m.ReadOnly {
			fsCfg = fsCfg.WithReadOnlyDirMount(host, m.Guest)
		} else {
			fsCfg = fsCfg.WithDirMount(host, m.Guest)
		}
	}
	var pythonPath []string
	for _, m := range p.modules {
		fsCfg = fsCfg.WithFSMount(m.fsys, m.guest)
		pythonPath = append(pythonPath, m.guest)
	}
	for _, m := range opts.fsMounts {
		fsCfg = fsCfg.WithFSMount(m.fsys, m.guest)
	}

	modCfg := wazero.NewModuleConfig().
		// Instances are anonymous so concurrent runs don't collide on name.
		WithName("").
		WithArgs(append([]string{"python"}, opts.Args...)...).
		WithEnv("PYTHONHOME", "/").
		// No writable pycache inside the read-only home mount.
		WithEnv("PYTHONDONTWRITEBYTECODE", "1").
		WithFSConfig(fsCfg).
		WithSysWalltime().
		WithSysNanotime().
		WithSysNanosleep().
		// wazero's default random source is deterministic; Python's
		// os.urandom, hash seeds and numpy's default_rng need real entropy.
		WithRandSource(rand.Reader)
	for k, v := range opts.Env {
		if k == "PYTHONPATH" && len(pythonPath) > 0 {
			// The caller's entries come first.
			pythonPath = append([]string{v}, pythonPath...)
			continue
		}
		modCfg = modCfg.WithEnv(k, v)
	}
	if len(pythonPath) > 0 {
		modCfg = modCfg.WithEnv("PYTHONPATH", strings.Join(pythonPath, ":"))
	}
	if opts.Stdin != nil {
		modCfg = modCfg.WithStdin(opts.Stdin)
	}
	if opts.Stdout != nil {
		modCfg = modCfg.WithStdout(opts.Stdout)
	}
	if opts.Stderr != nil {
		modCfg = modCfg.WithStderr(opts.Stderr)
	}

	mod, err := p.runtime.InstantiateModule(ctx, p.compiled, modCfg)
	if mod != nil {
		defer mod.Close(ctx)
	}
	var exitErr *sys.ExitError
	if errors.As(err, &exitErr) {
		return int(exitErr.ExitCode()), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}

// Close releases the runtime and its compiled code. Python implements
// io.Closer. The context passed to New isn't kept, since it is often
// request-scoped and may be canceled by the time Close runs. wazero only
// uses this context for optional hooks.
func (p *Python) Close() error {
	return p.runtime.Close(context.Background())
}
