package pywasi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"go.chrisrx.dev/pywasi/internal/hoststub"
)

// HostStub is a WebAssembly module with pywasi's host functions (module
// "pywasi": sockets and terminals), all failing, for running pywasi's
// interpreters in other runtimes, which don't have them: preload it as
// "pywasi", e.g. wasmtime run --preload pywasi=stub.wasm. Python then sees
// sockets fail (ENOSYS) and no terminal (the basic REPL).
var HostStub = hoststub.Wasm

// Runner runs the interpreter in a WebAssembly runtime other than pywasi's
// own (wazero), for Run, Eval, Call and sessions: WithRunner. It gets the
// run as a Command: the module, its arguments and environment, and the
// host directories it sees. The runtime needs WASI preview 1, exception
// handling, and HostStub's functions.
type Runner interface {
	Run(ctx context.Context, cmd *Command) (exitCode int, err error)
}

// WithRunner runs the interpreter with r instead of wazero. pywasi still
// uses wazero itself, to resolve and link packages (WithPackages,
// WithProject), but runs don't get what only it provides: networking,
// terminals, /dev, and the snapshots that start them with packages already
// imported.
func WithRunner(r Runner) Option {
	return func(c *config) { c.runner = r }
}

// Command is a run of the interpreter, as a WASI runtime takes one (see
// Runner, Python.Command). Close removes the directories made for it.
type Command struct {
	// Wasm is python.wasm's host path.
	Wasm string
	// Args are its arguments, with argv[0].
	Args []string
	// Env is its environment, as KEY=VALUE.
	Env []string
	// Dirs are the host directories it sees (WASI preopens), the
	// interpreter's home at "/" first.
	Dirs []Dir

	Stdin          io.Reader
	Stdout, Stderr io.Writer

	temp []string
}

// Dir is a host directory a Command sees at a guest path.
type Dir struct {
	Host, Guest string
	ReadOnly    bool
}

// Close removes the directories made for the command's in-memory file
// systems (WithModule, sessions).
func (c *Command) Close() error {
	var errs []error
	for _, dir := range c.temp {
		errs = append(errs, os.RemoveAll(dir))
	}
	c.temp = nil
	return errors.Join(errs...)
}

// Command is the run opts make, for another runtime (see Runner): what
// WithRunner's runner gets. The caller closes it.
func (p *Python) Command(opts RunOptions) (*Command, error) {
	if err := checkMounts(opts.Mounts); err != nil {
		return nil, err
	}
	pl, err := p.plan(opts)
	if err != nil {
		return nil, err
	}
	return p.command(pl, opts)
}

func (p *Python) command(pl *runPlan, opts RunOptions) (*Command, error) {
	cmd := &Command{
		Wasm:   p.wasmPath,
		Args:   pl.args,
		Dirs:   []Dir{{Host: p.home, Guest: "/", ReadOnly: true}},
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
	}
	for _, kv := range pl.env {
		cmd.Env = append(cmd.Env, kv[0]+"="+kv[1])
	}
	for _, m := range pl.mounts {
		host := m.host
		if host == "" {
			// In memory or embedded: written out for the runtime.
			dir, err := os.MkdirTemp("", "pywasi-mount-")
			if err != nil {
				cmd.Close()
				return nil, err
			}
			cmd.temp = append(cmd.temp, dir)
			if err := copyFS(dir, m.fsys); err != nil {
				cmd.Close()
				return nil, err
			}
			host = dir
		}
		cmd.Dirs = append(cmd.Dirs, Dir{Host: host, Guest: m.guest, ReadOnly: m.readOnly})
	}
	return cmd, nil
}

// runPlan is a run: what wazero is configured with, or a Command made of.
type runPlan struct {
	args   []string
	env    [][2]string
	mounts []planMount
	// dev is whether /dev is pywasi's (devFS): not mounted over.
	dev bool
	// tmp is the host directory at /tmp, if any.
	tmp string
}

// planMount is a directory a run sees after the home at "/": a host
// directory, or a file system with none.
type planMount struct {
	guest    string
	host     string
	fsys     fs.FS
	readOnly bool
}

func (p *Python) plan(opts RunOptions) (*runPlan, error) {
	if opts.Dir != "" && !path.IsAbs(opts.Dir) {
		return nil, fmt.Errorf("pywasi: RunOptions.Dir %q must be an absolute guest path", opts.Dir)
	}
	pl := &runPlan{args: append([]string{"python"}, opts.Args...)}
	for _, m := range opts.Mounts {
		host, err := filepath.Abs(m.Host)
		if err != nil {
			return nil, err
		}
		pl.mounts = append(pl.mounts, planMount{guest: m.Guest, host: host, readOnly: m.ReadOnly})
	}
	tmp, err := p.tempMount(opts.Mounts)
	if err != nil {
		return nil, err
	}
	if tmp != "" {
		pl.tmp = tmp
		pl.mounts = append(pl.mounts, planMount{guest: guestTemp, host: tmp})
	}
	pl.dev = !slices.ContainsFunc(opts.Mounts, func(m Mount) bool { return path.Clean(m.Guest) == guestDev })
	var pythonPath []string
	for _, m := range p.modules {
		pl.mounts = append(pl.mounts, planMount{guest: m.guest, host: m.host, fsys: m.fsys, readOnly: true})
		pythonPath = append(pythonPath, m.guest)
	}
	for _, m := range opts.fsMounts {
		pl.mounts = append(pl.mounts, planMount{guest: m.guest, host: m.host, fsys: m.fsys, readOnly: true})
	}

	pl.env = [][2]string{
		{"PYTHONHOME", "/"},
		// No writable pycache inside the read-only home mount.
		{"PYTHONDONTWRITEBYTECODE", "1"},
	}
	keys := make([]string, 0, len(opts.Env))
	for k := range opts.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := opts.Env[k]
		if k == "PYTHONPATH" && len(pythonPath) > 0 {
			// The caller's entries come first.
			pythonPath = append([]string{v}, pythonPath...)
			continue
		}
		pl.env = append(pl.env, [2]string{k, v})
	}
	if len(pythonPath) > 0 {
		pl.env = append(pl.env, [2]string{"PYTHONPATH", strings.Join(pythonPath, ":")})
	}
	if _, ok := opts.Env["HOME"]; tmp != "" && !ok {
		// The temp space as the home directory too, for caches kept under ~
		// (matplotlib's, Hugging Face's). Without HOME, ~ doesn't expand:
		// WASI has no pwd module to look it up in.
		pl.env = append(pl.env, [2]string{"HOME", guestTemp})
	}
	if _, ok := opts.Env["PYWASI_VERSION"]; !ok {
		pl.env = append(pl.env, [2]string{"PYWASI_VERSION", Version()})
	}
	if opts.Dir != "" {
		// Read and removed by pywasi_main.c, which changes to it.
		pl.env = append(pl.env, [2]string{"PYWASI_CWD", path.Clean(opts.Dir)})
	}
	return pl, nil
}

// copyFS writes fsys's files into dir.
func copyFS(dir string, fsys fs.FS) error {
	return fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		src, err := fsys.Open(name)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			dst.Close()
			return err
		}
		return dst.Close()
	})
}
