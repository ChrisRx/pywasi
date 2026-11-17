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

	"github.com/ChrisRx/wazero"
	"github.com/ChrisRx/wazero/api"
	"github.com/ChrisRx/wazero/experimental"
	"github.com/ChrisRx/wazero/experimental/sysfs"
	"github.com/ChrisRx/wazero/imports/wasi_snapshot_preview1"
	"github.com/ChrisRx/wazero/sys"
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
	// Dir is the working directory inside the guest, an absolute guest
	// path such as a Mount's (default "/"). It needs an interpreter built by
	// pywasi; a plain CPython build ignores it.
	Dir string

	// fsMounts are pywasi's own mounts (not checked against reservedGuest).
	fsMounts []fsMount
}

type fsMount struct {
	fsys  fs.FS
	guest string
	// host is the host directory fsys is, if it is one (for Command).
	host string
}

var _ io.Closer = (*Python)(nil)

// Python is a compiled interpreter that can be run many times.
type Python struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	home     string
	// wasmPath is python.wasm's host path.
	wasmPath string
	// runner is WithRunner's: runs go to it instead of wazero.
	runner Runner
	// modules are WithModule and WithModules file systems, linked-in
	// bundles' Python files and pure-Python packages, in PYTHONPATH order.
	modules []fsMount
	// digest identifies python.wasm (sha256 prefix), for cache keys.
	digest string
	// bundleSites are the Python files of linked-in bundles, and
	// bundleModules the top-level modules of their extensions.
	bundleSites   []string
	bundleModules []string
	// boundsChecks is WithBoundsChecks.
	boundsChecks bool
	// tempDir is WithTempDir's directory, if tempDirSet.
	tempDir    string
	tempDirSet bool
	// resumable is set if compiled can be pre-initialized, with globals its
	// mutable globals' indices, and then image is its snapshot, if one could
	// be taken (see image.go).
	resumable bool
	globals   []uint32
	image     *image
}

// load compiles wasm as the interpreter, ready to be restored from images
// if it can be pre-initialized.
func (p *Python) load(ctx context.Context, wasm []byte) error {
	code, globals, ok, err := resumable(wasm)
	if err != nil {
		return fmt.Errorf("pywasi: reading interpreter: %w", err)
	}
	compiled, err := p.runtime.CompileModule(compileContext(ctx, p.boundsChecks), code)
	if err != nil {
		return fmt.Errorf("pywasi: compiling interpreter: %w", err)
	}
	if p.compiled != nil {
		if err := p.compiled.Close(ctx); err != nil {
			return err
		}
	}
	sum := sha256.Sum256(wasm)
	p.compiled, p.digest, p.resumable, p.globals, p.image = compiled, hex.EncodeToString(sum[:16]), ok, globals, nil
	return nil
}

// relink replaces the interpreter with one linked with layers' bundles.
func (p *Python) relink(ctx context.Context, layers layers, cacheDir string) error {
	home, sites, modules, err := linkInterpreter(ctx, *layers.Linker, *layers.Kit, layers.Bundles, cacheDir, p.boundsChecks)
	if err != nil {
		return err
	}
	wasm, err := os.ReadFile(filepath.Join(home, "bin", "python.wasm"))
	if err != nil {
		return err
	}
	if err := p.load(ctx, wasm); err != nil {
		return err
	}
	p.home, p.bundleSites, p.bundleModules = home, sites, modules
	p.wasmPath = filepath.Join(home, "bin", "python.wasm")
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

	cfg := newRuntimeConfig()
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
	if err := instantiateHost(ctx, r, opts.network); err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("pywasi: instantiating host functions: %w", err)
	}
	p := &Python{
		runtime: r, home: home, wasmPath: wasmPath, boundsChecks: opts.boundsChecks,
		tempDir: opts.tempDir, tempDirSet: opts.tempDirSet,
	}
	if err := p.load(ctx, wasm); err != nil {
		r.Close(ctx)
		return nil, err
	}
	// A uv project: its lock, and its code as modules.
	var projectSrc string
	if opts.project != "" {
		if len(opts.packages) > 0 || opts.pylock != nil {
			r.Close(ctx)
			return nil, errors.New("pywasi: use WithProject, WithPackages or WithPylock, not more than one")
		}
		lock, src, err := exportProject(ctx, opts.project)
		if err != nil {
			r.Close(ctx)
			return nil, err
		}
		opts.pylock = lock
		projectSrc = src
	}
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
		p.modules = append(p.modules, fsMount{fsys: fsys, guest: fmt.Sprintf("%s/modules/%d", guestRoot, i)})
	}
	for i, dir := range p.bundleSites {
		p.modules = append(p.modules, fsMount{fsys: os.DirFS(dir), guest: bundleGuest(i), host: dir})
	}
	if site != "" {
		p.modules = append(p.modules, fsMount{fsys: os.DirFS(site), guest: guestRoot + "/site", host: site})
	}
	if projectSrc != "" {
		// First on the import path, as uv run has it.
		p.modules = append([]fsMount{{fsys: os.DirFS(projectSrc), guest: guestRoot + "/project", host: projectSrc}}, p.modules...)
	}
	// Only wazero starts from snapshots, and pywasi's own runs while making
	// the interpreter (resolving packages) need it, its networking above all,
	// so the runner takes over from here.
	if opts.runner != nil {
		p.runner = opts.runner
	} else if p.resumable {
		p.image = p.snapshot(ctx, opts, site)
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
	pl, err := p.plan(opts)
	if err != nil {
		return 0, err
	}
	if p.runner != nil {
		cmd, err := p.command(pl, opts)
		if err != nil {
			return 0, err
		}
		defer cmd.Close()
		return p.runner.Run(ctx, cmd)
	}

	fsCfg := wazero.NewFSConfig().WithReadOnlyDirMount(p.home, "/")
	for _, m := range pl.mounts {
		switch {
		case m.host == "":
			fsCfg = fsCfg.WithFSMount(m.fsys, m.guest)
		case m.readOnly:
			fsCfg = fsCfg.WithReadOnlyDirMount(m.host, m.guest)
		default:
			fsCfg = fsCfg.WithDirMount(m.host, m.guest)
		}
	}
	if pl.dev {
		fsCfg = fsCfg.(sysfs.FSConfig).WithSysFSMount(devFS{}, guestDev)
	}
	modCfg := wazero.NewModuleConfig().
		// Instances are anonymous so concurrent runs don't collide on name.
		WithName("").
		WithArgs(pl.args...).
		WithFSConfig(fsCfg).
		WithSysWalltime().
		WithSysNanotime().
		WithSysNanosleep().
		// wazero's default random source is deterministic; Python's
		// os.urandom, hash seeds and numpy's default_rng need real entropy.
		WithRandSource(rand.Reader)
	for _, kv := range pl.env {
		modCfg = modCfg.WithEnv(kv[0], kv[1])
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

	ctx, restore := withTerminals(ctx, opts.Stdin, opts.Stdout, opts.Stderr)
	defer restore()
	var mod api.Module
	if p.resumable {
		// Started here: from the snapshot, if there is one.
		mod, err = p.runtime.InstantiateModule(ctx, p.compiled, modCfg.WithStartFunctions())
		if err == nil {
			entry := "_start"
			if p.image != nil {
				entry = resumeExport
				err = p.image.restore(mod)
			}
			if err == nil {
				_, err = mod.ExportedFunction(entry).Call(ctx)
			}
		}
	} else {
		mod, err = p.runtime.InstantiateModule(ctx, p.compiled, modCfg)
	}
	if mod != nil {
		defer mod.Close(ctx)
	}
	var exitErr *sys.ExitError
	if errors.As(err, &exitErr) {
		switch code := exitErr.ExitCode(); code {
		case sys.ExitCodeContextCanceled, sys.ExitCodeDeadlineExceeded:
			// Stopped waiting (on a socket, or sleeping) because ctx is
			// done.
			return 0, ctx.Err()
		default:
			return int(code), nil
		}
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

// newRuntimeConfig is the wazero configuration pywasi runs modules with.
//
// DWARF-based stack traces make every proc_exit (sys.exit with a nonzero
// code, os._exit, the snapshot entry point) parse the debug info of a
// ~40MB module: seconds per process, so they're off. Function names from
// the name section are still used. Exception handling is on: extension
// modules use it for C++ exceptions and setjmp/longjmp (toolchain/wasi-cc).
func newRuntimeConfig() wazero.RuntimeConfig {
	return wazero.NewRuntimeConfig().
		WithDebugInfoEnabled(false).
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling)
}
