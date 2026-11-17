// Package wasmtime runs pywasi's interpreters in wasmtime, in-process
// (wasmtime-go), instead of pywasi's own wazero:
//
//	r, err := wasmtime.New()
//	py, err := pywasi.New(ctx, pywasi.WithRunner(r))
//	n, err := py.Eval[int](ctx, "1 + 1")
//
// It is its own module since wasmtime-go needs cgo. Runs go without what
// only pywasi's wazero provides: networking (sockets fail, ENOSYS),
// terminals, /dev, and the snapshots that start interpreters with their
// packages imported. Standard input and output go through pipes the guest
// opens as /dev/fd/N, so it runs on Linux and macOS.
package wasmtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/bytecodealliance/wasmtime-go/v50"

	"go.chrisrx.dev/pywasi"
)

// Runner runs interpreters in wasmtime (pywasi.Runner). Each run has its
// own engine, so that canceling one interrupts only it; compiled
// interpreters are kept, serialized, and loaded into them.
type Runner struct {
	mu       sync.Mutex
	compiled map[[32]byte][]byte // by the module's sha256
}

var _ pywasi.Runner = (*Runner)(nil)

// New returns a Runner.
func New() (*Runner, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("pywasi/runtime/wasmtime: not supported on Windows (standard streams go through /dev/fd)")
	}
	if _, err := config(); err != nil {
		return nil, err
	}
	return &Runner{compiled: map[[32]byte][]byte{}}, nil
}

// config is wasmtime's configuration for pywasi's interpreters: exception
// handling (C++ exceptions, setjmp/longjmp), as deep a stack as the C API
// allows (it caps it at its async stack size, 2 MiB, which it doesn't let
// change; wasmtime's command takes more), epochs to interrupt runs, and
// wasmtime's own cache
// of compiled code (its default configuration), which saves compiling an
// interpreter again in the next process.
func config() (*wasmtime.Config, error) {
	cfg := wasmtime.NewConfig()
	cfg.SetWasmExceptions(true)
	cfg.SetMaxWasmStack(2 << 20)
	cfg.SetEpochInterruption(true)
	if err := cfg.CacheConfigLoadDefault(); err != nil {
		return nil, fmt.Errorf("pywasi/runtime/wasmtime: wasmtime's cache: %w", err)
	}
	return cfg, nil
}

// Run implements pywasi.Runner.
func (r *Runner) Run(ctx context.Context, cmd *pywasi.Command) (int, error) {
	cfg, err := config()
	if err != nil {
		return 0, err
	}
	engine := wasmtime.NewEngineWithConfig(cfg)
	module, err := r.module(engine, cmd.Wasm)
	if err != nil {
		return 0, err
	}
	store := wasmtime.NewStore(engine)
	defer store.Close()

	// Interrupted when ctx is done: the engine is this run's alone.
	store.SetEpochDeadline(1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			engine.IncrementEpoch()
		case <-done:
		}
	}()

	wasi := wasmtime.NewWasiConfig()
	wasi.SetArgv(cmd.Args)
	var keys, values []string
	for _, kv := range cmd.Env {
		k, v, _ := strings.Cut(kv, "=")
		keys, values = append(keys, k), append(values, v)
	}
	wasi.SetEnv(keys, values)
	for _, d := range cmd.Dirs {
		if err := wasi.PreopenDir(d.Host, d.Guest, !d.ReadOnly); err != nil {
			return 0, fmt.Errorf("pywasi/runtime/wasmtime: %s at %s: %w", d.Host, d.Guest, err)
		}
	}
	streams, err := connect(wasi, cmd)
	if err != nil {
		return 0, err
	}
	defer streams.close()
	store.SetWasi(wasi)

	linker := wasmtime.NewLinker(engine)
	if err := linker.DefineWasi(); err != nil {
		return 0, err
	}
	stub, err := wasmtime.NewModule(engine, pywasi.HostStub)
	if err != nil {
		return 0, err
	}
	stubInstance, err := wasmtime.NewInstance(store, stub, nil)
	if err != nil {
		return 0, err
	}
	if err := linker.DefineInstance(store, "pywasi", stubInstance); err != nil {
		return 0, err
	}
	instance, err := linker.Instantiate(store, module)
	if err != nil {
		return 0, err
	}
	start := instance.GetFunc(store, "_start")
	if start == nil {
		return 0, errors.New("pywasi/runtime/wasmtime: the interpreter has no _start")
	}
	_, err = start.Call(store)
	// The guest's ends of the pipes are the store's: closed with it, so the
	// output is all read before returning.
	store.Close()
	copyErr := streams.wait()
	if err == nil {
		return 0, copyErr
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	var werr *wasmtime.Error
	if errors.As(err, &werr) {
		if code, ok := werr.ExitStatus(); ok {
			return int(code), nil
		}
	}
	return 0, err
}

// module is the interpreter at path compiled for engine.
func (r *Runner) module(engine *wasmtime.Engine, path string) (*wasmtime.Module, error) {
	wasm, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(wasm)
	r.mu.Lock()
	serialized, ok := r.compiled[key]
	r.mu.Unlock()
	if ok {
		return wasmtime.NewModuleDeserialize(engine, serialized)
	}
	module, err := wasmtime.NewModule(engine, wasm)
	if err != nil {
		return nil, fmt.Errorf("pywasi/runtime/wasmtime: compiling %s: %w", path, err)
	}
	if serialized, err := module.Serialize(); err == nil {
		r.mu.Lock()
		r.compiled[key] = serialized
		r.mu.Unlock()
	}
	return module, nil
}

// streams are the pipes standing in for a run's standard streams.
type streams struct {
	files   []*os.File // the host's ends, closed after the run
	copying sync.WaitGroup
	mu      sync.Mutex
	errs    []error
}

// failed records an error copying a stream.
func (s *streams) failed(err error) {
	s.mu.Lock()
	s.errs = append(s.errs, err)
	s.mu.Unlock()
}

// connect gives the guest pipes for cmd's standard streams, opened by
// path (/dev/fd/N), copied to and from them.
func connect(wasi *wasmtime.WasiConfig, cmd *pywasi.Command) (*streams, error) {
	s := &streams{}
	if cmd.Stdin != nil {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		if err := wasi.SetStdinFile(fdPath(r)); err != nil {
			r.Close()
			w.Close()
			return nil, err
		}
		r.Close() // The guest has its own.
		s.files = append(s.files, w)
		go func() {
			// Stopping when the guest closed its end, or the run ended and
			// closed ours, is how input ends.
			_, err := io.Copy(w, cmd.Stdin)
			if err != nil && !errors.Is(err, syscall.EPIPE) && !errors.Is(err, os.ErrClosed) {
				s.failed(fmt.Errorf("pywasi/runtime/wasmtime: standard input: %w", err))
			}
			w.Close()
		}()
	}
	for _, out := range []struct {
		w   io.Writer
		set func(string) error
	}{{cmd.Stdout, wasi.SetStdoutFile}, {cmd.Stderr, wasi.SetStderrFile}} {
		if out.w == nil {
			continue
		}
		r, w, err := os.Pipe()
		if err != nil {
			s.close()
			return nil, err
		}
		if err := out.set(fdPath(w)); err != nil {
			r.Close()
			w.Close()
			s.close()
			return nil, err
		}
		w.Close() // The guest has its own.
		s.files = append(s.files, r)
		s.copying.Add(1)
		go func(dst io.Writer) {
			defer s.copying.Done()
			if _, err := io.Copy(dst, r); err != nil {
				s.failed(fmt.Errorf("pywasi/runtime/wasmtime: copying output: %w", err))
			}
		}(out.w)
	}
	return s, nil
}

// wait waits for the output to be copied, once the guest's ends are
// closed, and returns what went wrong copying.
func (s *streams) wait() error {
	s.copying.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(s.errs...)
}

func (s *streams) close() {
	for _, f := range s.files {
		f.Close()
	}
}

func fdPath(f *os.File) string {
	return fmt.Sprintf("/dev/fd/%d", f.Fd())
}
