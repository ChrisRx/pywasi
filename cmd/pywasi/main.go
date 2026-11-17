// Command pywasi runs the WASI python.wasm under wazero: the one in --home,
// else $PYWASI_HOME or .pywasi if it exists, else the embedded standard
// library interpreter. The current directory is mounted at the same path and
// Python starts in it, like a native python (--cwd=false turns that off).
// Python can use the network (--net=false turns that off).
//
//	go run ./cmd/pywasi                        # interactive prompt
//	go run ./cmd/pywasi -d                     # pick packages, then a prompt with them
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
	cwd, net, deps       bool
	mounts               mounts
}

// exitError carries Python's exit status out of the command.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func main() {
	cmd := newCommand()
	flags, python := splitPython(cmd.Flags(), os.Args[1:])
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
  pywasi script.py arg
  pywasi -c 'print(1 + 1)'
  pywasi -m http.server 8000`,
		Short: "Run Python compiled to WebAssembly, in wazero",
		Long: `Run the WASI python.wasm under wazero: the one in --home, else
$PYWASI_HOME or .pywasi if it exists, else the embedded standard library
interpreter.

-c and -m end pywasi's flags, as in python: what follows goes to Python.`,
		Version:       pywasi.Version(),
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if python, ok := cmd.Context().Value(pythonArgs{}).([]string); ok {
				args = python
			}
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
	f.BoolVarP(&o.deps, "deps", "d", false, "pick packages from the catalog in the terminal, then start a prompt with them (linked on the fly)")
	f.StringVar(&o.catalog, "catalog", pywasi.DefaultCatalog, "catalog of packages built for WASI, for --deps")
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
	var py *pywasi.Python
	var err error
	if o.deps {
		switch {
		case o.home != "":
			return errors.New("pywasi: --deps links its own interpreter; it can't use --home")
		case len(args) > 0:
			return errors.New("pywasi: --deps starts a prompt; it takes no Python arguments")
		case !term.IsTerminal(int(os.Stdin.Fd())):
			return errors.New("pywasi: --deps needs a terminal")
		}
		py, args, err = pick(ctx, o.catalog, func(reqs []string, log io.Writer) (*pywasi.Python, error) {
			return pywasi.New(ctx, append(opts, pywasi.WithPackages(reqs...), pywasi.WithBuildLog(log))...)
		})
		if err == nil && py == nil {
			return nil // Quit without picking.
		}
	} else {
		py, err = pywasi.New(ctx, opts...)
	}
	if err != nil {
		return err
	}
	defer py.Close()

	env := map[string]string{}
	// What the REPL and colored output look at.
	for _, k := range []string{"TERM", "COLORTERM", "NO_COLOR", "FORCE_COLOR", "PYTHON_COLORS", "PYTHON_BASIC_REPL"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	mnts := o.mounts
	if (len(args) == 0 || o.deps) && term.IsTerminal(int(os.Stdin.Fd())) {
		if file, ok := mountHistory(&mnts); ok {
			env["PYTHON_HISTORY"] = file
		}
	}
	var dir string
	if o.cwd {
		dir = mountWorkingDirectory(&mnts)
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
