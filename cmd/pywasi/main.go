// Command pywasi runs the WASI python.wasm under wazero: the one in --home,
// else $PYWASI_HOME or .pywasi if it exists, else the embedded standard
// library interpreter. The current directory is mounted at the same path and
// Python starts in it, like a native python (--cwd=false turns that off).
// Python can use the network (--net=false turns that off).
//
//	go run ./cmd/pywasi                        # interactive prompt
//	go run ./cmd/pywasi -d                     # pick packages, then a prompt with them
//	go run ./cmd/pywasi -d pandas,pyarrow      # a prompt with these packages
//	go run ./cmd/pywasi -d pandas script.py    # run a script with them
//	go run ./cmd/pywasi -c 'print(1 + 1)'
//	go run ./cmd/pywasi -m http.server 8000
//	go run ./cmd/pywasi --home examples/numpy-app/.pywasi -c 'import numpy; print(numpy.__version__)'
//	go run ./cmd/pywasi --home examples/numpy-app/.pywasi --mount examples/numpy-app:/app -- /app/main.py
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.chrisrx.dev/pywasi"
	"golang.org/x/term"
)

type mounts []pywasi.Mount

func (m *mounts) String() string { return fmt.Sprint(*m) }

func (m *mounts) Type() string { return "HOST:/GUEST" }

func (m *mounts) Set(v string) error {
	host, guest, ok := strings.Cut(v, ":")
	if !ok || host == "" || !strings.HasPrefix(guest, "/") {
		return fmt.Errorf("want HOST:/GUEST, got %q", v)
	}
	*m = append(*m, pywasi.Mount{Host: host, Guest: guest})
	return nil
}

// options are the command's flags.
type options struct {
	home, cache, catalog string
	cwd, net             bool
	// deps are the packages for --deps; picking them in the terminal if
	// it's given without any.
	deps    []string
	depsSet bool
	// reqFiles are requirements files (-r), for --deps too.
	reqFiles []string
	// project is a uv project to run with its dependencies.
	project string
	// export is where to write the interpreter instead of running it.
	export string
	// interactive is -i: a prompt rather than the project's app.
	interactive bool
	mounts      mounts
}

// exitError carries Python's exit status out of the command.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func main() {
	cmd := newCommand()
	flags, python := splitPython(cmd.Flags(), bareDeps(cmd.Flags(), os.Args[1:]))
	cmd.SetArgs(flags)
	if python != nil {
		cmd.SetContext(context.WithValue(context.Background(), pythonArgs{}, python))
	}
	if err := cmd.Execute(); err != nil {
		var code exitError
		if errors.As(err, &code) {
			os.Exit(int(code))
		}
		os.Exit(1)
	}
}

// pythonArgs is the context key for the arguments split off at -c or -m.
type pythonArgs struct{}

func newCommand() *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use: "pywasi [flags] [script [args...]]",
		Example: `  pywasi                         interactive prompt
  pywasi -d                      pick packages, then a prompt with them
  pywasi -d pandas,pyarrow       a prompt with these packages
  pywasi -d pandas script.py     run a script with them
  pywasi -r requirements.txt     a prompt with a requirements file's packages
  pywasi -p myapp                run a uv project's app, with its dependencies
  pywasi -p myapp serve --port 8000   run one of its [project.scripts]
  pywasi -p myapp -i             a prompt with the project importable
  pywasi -d pandas --export py   write the interpreter to py, for other runtimes (py/run)
  PYWASI_RUNTIME=wasmtime pywasi script.py   run it with wasmtime
  pywasi script.py arg
  pywasi -c 'print(1 + 1)'
  pywasi -m http.server 8000`,
		Short: "Run Python compiled to WebAssembly, in wazero",
		Long: `Run the WASI python.wasm under wazero: the one in --home, else
$PYWASI_HOME or .pywasi if it exists, else the embedded standard library
interpreter.

-c and -m end pywasi's flags, as in python: what follows goes to Python.

$PYWASI_RUNTIME picks the runtime that runs it: wazero (pywasi's own, the
default) or wasmtime (on PATH, or a path to it), which goes without
networking, terminals and pywasi's fast startup.`,
		Version:       pywasi.Version(),
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if python, ok := cmd.Context().Value(pythonArgs{}).([]string); ok {
				args = python
			}
			o.depsSet = cmd.Flags().Changed("deps")
			err := run(cmd.Context(), &o, args)
			if err != nil && !errors.As(err, new(exitError)) {
				fmt.Fprintln(os.Stderr, err)
			}
			return err
		},
	}
	cmd.SetVersionTemplate("pywasi {{.Version}}\n")
	f := cmd.Flags()
	// Python's own arguments after the script aren't pywasi's.
	f.SetInterspersed(false)
	f.StringVar(&o.home, "home", "", "Python install tree (bin/python.wasm, lib/python3.X), e.g. PROJECT/.pywasi; defaults to $PYWASI_HOME or .pywasi if it exists, else the embedded standard library interpreter")
	f.StringVar(&o.cache, "cache", pywasi.DefaultCacheDir(), "compiled code cache directory (empty disables)")
	f.BoolVar(&o.cwd, "cwd", true, "mount the current directory at the same path (/cwd on Windows) and start in it")
	f.BoolVar(&o.net, "net", true, "let Python use the network: listen, connect and resolve host names")
	f.VarP(&o.mounts, "mount", "", "mount a host directory (repeatable)")
	f.StringSliceVarP(&o.deps, "deps", "d", nil, "packages to link in (pandas,pyarrow==26.0.1), for a prompt with them or the script; without any, pick them from the catalog in the terminal")
	f.StringArrayVarP(&o.reqFiles, "requirement", "r", nil, "packages from a requirements file, as --deps does (repeatable)")
	f.StringVarP(&o.project, "project", "p", "", "a uv project (the directory with its pyproject.toml): its locked dependencies, its code on the import path, and without Python arguments its app: its one [project.scripts] command, its package's __main__, or its main.py")
	f.BoolVarP(&o.interactive, "interactive", "i", false, "a prompt, rather than --project's app")
	f.StringVar(&o.catalog, "catalog", pywasi.DefaultCatalog, "catalog of packages built for WASI, for --deps")
	f.StringVar(&o.export, "export", "", "write the interpreter, with its packages, to this directory instead of running it: a home that runs in other runtimes (DIR/run uses wasmtime)")
	// Documented here; splitPython hands them and what follows to Python.
	f.StringP("command", "c", "", "run a program passed in as a string, like python -c; the arguments after it go to the program")
	f.StringP("module", "m", "", "run library module as a script, like python -m; the arguments after it go to the module")
	return cmd
}

func run(ctx context.Context, o *options, args []string) error {
	opts := []pywasi.Option{cacheOption(o.cache), pywasi.WithCatalog(o.catalog)}
	if o.home != "" {
		opts = append(opts, pywasi.WithHome(o.home))
	}
	if o.net {
		opts = append(opts, pywasi.WithNetwork(nil))
	}
	runner, err := runtimeFromEnv()
	if err != nil {
		return err
	}
	if runner != nil {
		opts = append(opts, pywasi.WithRunner(runner))
	}
	if o.export != "" && len(args) > 0 {
		return errors.New("pywasi: --export writes the interpreter; it takes no Python arguments")
	}
	var py *pywasi.Python
	var reqs []string
	for _, d := range o.deps {
		if d = strings.TrimSpace(d); d != "" {
			reqs = append(reqs, d)
		}
	}
	for _, file := range o.reqFiles {
		more, notes, err := readRequirements(file)
		if err != nil {
			return fmt.Errorf("pywasi: %w", err)
		}
		for _, n := range notes {
			fmt.Fprintln(os.Stderr, "pywasi:", n)
		}
		reqs = append(reqs, more...)
	}
	deps := o.depsSet || len(o.reqFiles) > 0
	if o.interactive && len(args) > 0 {
		return errors.New("pywasi: -i starts a prompt; it takes no Python arguments")
	}
	// A project's app, as uv run has it: its script by name, or without
	// arguments, what it declares it runs as.
	if o.project != "" && !o.interactive {
		dir, err := filepath.Abs(o.project)
		if err != nil {
			return err
		}
		a, err := projectApp(dir)
		if err != nil {
			return err
		}
		if len(args) == 0 {
			if args, err = a.args(); err != nil {
				return err
			}
		} else if target, ok := a.scripts[args[0]]; ok && !isFile(args[0]) {
			// A file of that name is the script to run, but not a
			// directory: often the project's own, named as its command.
			args = scriptArgs(args[0], target, args[1:])
		}
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	// A prompt, Python's own or one started with --deps' aliases.
	prompt := len(args) == 0 && interactive
	mnts := o.mounts
	switch {
	case (deps || o.project != "") && o.home != "":
		return errors.New("pywasi: --deps, -r and --project link their own interpreter; they can't use --home")
	case deps && o.project != "":
		return errors.New("pywasi: --project has its dependencies in its lock; it can't take --deps or -r too (add them to the project)")
	case o.project != "":
		py, err = pywasi.New(ctx, append(opts, pywasi.WithProject(o.project))...)
	case deps && len(reqs) == 0 && len(o.reqFiles) > 0:
		return errors.New("pywasi: the requirements files have no packages")
	case deps && len(reqs) == 0:
		switch {
		case len(args) > 0:
			return errors.New("pywasi: --deps without packages picks them for a prompt; it takes no Python arguments (give the packages: -d pandas,pyarrow script.py)")
		case !interactive:
			return errors.New("pywasi: --deps without packages picks them in the terminal; give the packages instead: -d pandas,pyarrow")
		}
		py, args, err = pick(ctx, o.catalog, func(reqs []string, log io.Writer) (*pywasi.Python, error) {
			return pywasi.New(ctx, append(opts, pywasi.WithPackages(reqs...), pywasi.WithBuildLog(log))...)
		})
		if err == nil && py == nil {
			return nil // Quit without picking.
		}
	case deps:
		py, err = linkDeps(ctx, opts, reqs)
		if err == nil && prompt {
			var selected []item
			for _, r := range reqs {
				selected = append(selected, item{name: r})
			}
			args = sessionArgs(selected)
		}
	default:
		py, err = pywasi.New(ctx, opts...)
	}
	if err != nil {
		return err
	}
	defer py.Close()
	if o.export != "" {
		if err := py.Export(o.export); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "pywasi: wrote %s: run it with %s/run (wasmtime), or pywasi --home %s\n", o.export, o.export, o.export)
		return nil
	}

	env := map[string]string{}
	// What the REPL and colored output look at.
	for _, k := range []string{"TERM", "COLORTERM", "NO_COLOR", "FORCE_COLOR", "PYTHON_COLORS", "PYTHON_BASIC_REPL"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	if prompt {
		if file, ok := mountHistory(&mnts); ok {
			env["PYTHON_HISTORY"] = file
		}
	}
	var dir string
	if o.cwd {
		dir = mountWorkingDirectory(&mnts)
	}
	// A project's scripts are run by path, so it's mounted at its own path
	// unless the current directory's mount has it.
	if o.project != "" {
		if abs, err := filepath.Abs(o.project); err == nil {
			wd, _ := os.Getwd()
			if dir == "" || !within(abs, wd) {
				mnts = append(mnts, pywasi.Mount{Host: abs, Guest: filepath.ToSlash(abs)})
			}
		}
	}
	code, err := py.Run(ctx, pywasi.RunOptions{
		Args:   args,
		Env:    env,
		Dir:    dir,
		Mounts: mnts,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return exitError(code)
	}
	return nil
}

// linkDeps links an interpreter with reqs, saying so on a terminal while it
// does (the first time takes a while).
func linkDeps(ctx context.Context, opts []pywasi.Option, reqs []string) (*pywasi.Python, error) {
	status := term.IsTerminal(int(os.Stderr.Fd()))
	if status {
		fmt.Fprintf(os.Stderr, "pywasi: linking an interpreter with %s...", strings.Join(reqs, ", "))
	}
	py, err := pywasi.New(ctx, append(opts, pywasi.WithPackages(reqs...))...)
	if status {
		fmt.Fprint(os.Stderr, "\r\x1b[K")
	}
	return py, err
}

// bareDeps makes -d or --deps given without packages explicit, --deps=
// (picking them in the terminal, or with -r), since a flag's value can't be
// optional otherwise: last, before another flag, or before a file or
// directory, which is Python's (the script), as no package list is. Only
// pywasi's own flags are looked at, as splitPython does.
func bareDeps(fs *pflag.FlagSet, args []string) []string {
	out := slices.Clone(args)
	for i := 0; i < len(out); i++ {
		a := out[i]
		if a == "--" || a == "-" || !strings.HasPrefix(a, "-") {
			break
		}
		if a == "-d" || a == "--deps" {
			if i+1 == len(out) || strings.HasPrefix(out[i+1], "-") || exists(out[i+1]) {
				out[i] = "--deps="
			} else {
				i++ // Its packages.
			}
			continue
		}
		f, hasValue := lookupFlag(fs, a)
		if f == nil {
			continue
		}
		if f.Name == "command" || f.Name == "module" {
			break
		}
		if !hasValue && f.NoOptDefVal == "" {
			i++ // Skip the flag's value.
		}
	}
	return out
}

// exists is whether path is a file or directory.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// within is whether dir is base or under it.
func within(dir, base string) bool {
	rel, err := filepath.Rel(base, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// lookupFlag finds the flag an argument (--name, --name=v, -x, -xv or
// -x=v) is, and whether the argument holds its value.
func lookupFlag(fs *pflag.FlagSet, a string) (f *pflag.Flag, hasValue bool) {
	if long, ok := strings.CutPrefix(a, "--"); ok {
		name, _, hasValue := strings.Cut(long, "=")
		return fs.Lookup(name), hasValue
	}
	return fs.ShorthandLookup(a[1:2]), len(a) > 2
}

// historyGuest is where the REPL's history file is mounted.
const historyGuest = "/.pywasi-history"

// mountHistory mounts a directory for the REPL's history, kept with
// pywasi's cache ($PYWASI_CACHE, else the user cache directory), and
// returns the history file's guest path. The guest has no home directory
// for Python's usual ~/.python_history.
func mountHistory(mnts *mounts) (string, bool) {
	dir := os.Getenv("PYWASI_CACHE")
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", false
		}
		dir = filepath.Join(cache, "pywasi")
	}
	dir = filepath.Join(dir, "history")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false
	}
	*mnts = append(*mnts, pywasi.Mount{Host: dir, Guest: historyGuest})
	return historyGuest + "/python_history", true
}

func cacheOption(dir string) pywasi.Option {
	if dir == "" {
		return pywasi.WithoutCache()
	}
	return pywasi.WithCacheDir(dir)
}

// mountWorkingDirectory mounts the current directory at its own path (/cwd
// on Windows) and returns that guest path, or "" if it can't be mounted
// there: at the guest root or over the interpreter's own files.
func mountWorkingDirectory(mnts *mounts) string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	guest := wd
	if runtime.GOOS == "windows" {
		guest = "/cwd"
	}
	switch {
	case guest == "/", guest == "/bin", guest == "/lib", guest == "/.pywasi", strings.HasPrefix(guest, "/.pywasi/"):
		fmt.Fprintf(os.Stderr, "pywasi: not mounting the current directory %s, which would hide the interpreter's files\n", wd)
		return ""
	}
	*mnts = append(*mnts, pywasi.Mount{Host: wd, Guest: guest})
	return guest
}

// splitPython splits args at -c or -m, which like python's end the flags:
// the command or module name and everything after it go to Python, so their
// own flags aren't parsed as pywasi's. python is nil without either. Like
// python, it takes -c CMD, -cCMD and -c=CMD (and --command, --module).
func splitPython(fs *pflag.FlagSet, args []string) (flags, python []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || a == "-" || !strings.HasPrefix(a, "-") {
			break
		}
		var f *pflag.Flag
		var value string
		var hasValue bool
		if long, ok := strings.CutPrefix(a, "--"); ok {
			var name string
			name, value, hasValue = strings.Cut(long, "=")
			f = fs.Lookup(name)
		} else {
			f = fs.ShorthandLookup(a[1:2])
			value, hasValue = strings.TrimPrefix(a[2:], "="), len(a) > 2
		}
		if f == nil {
			continue
		}
		if f.Name == "command" || f.Name == "module" {
			short := "-" + f.Shorthand
			if hasValue {
				return args[:i], append([]string{short, value}, args[i+1:]...)
			}
			if i+1 == len(args) {
				break // Let the flag package report the missing value.
			}
			return args[:i], append([]string{short}, args[i+1:]...)
		}
		if !hasValue && f.NoOptDefVal == "" {
			i++ // Skip the flag's value.
		}
	}
	return args, nil
}
