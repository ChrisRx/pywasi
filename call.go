package pywasi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.chrisrx.dev/x/context"
)

// Func names a Python function for Call.
type Func struct {
	// Module is imported with importlib, e.g. "numpy.linalg" or a module on
	// a mounted path (add it to PYTHONPATH with RunOptions.Env).
	Module string
	// Name may be dotted to reach attributes, e.g. "Matrix.identity".
	Name   string
	Args   []any
	Kwargs map[string]any
}

// Error is a Python exception raised by Eval or Call.
type Error struct {
	// Type is the exception's qualified name, e.g. "ValueError" or
	// "numpy.linalg.LinAlgError".
	Type      string `json:"type"`
	Message   string `json:"message"`
	Traceback string `json:"traceback"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Type
	}
	return e.Type + ": " + e.Message
}

// Eval is (*Python).Eval on an interpreter created by New(ctx, options...),
// closed afterwards:
//
//	n, err := pywasi.Eval[int](ctx, "6 * 7")
//	inv, err := pywasi.Eval[[][]float64](ctx, `
//		import numpy as np
//		np.linalg.inv(np.eye(2))
//	`)
//
// Without options it uses $PYWASI_HOME or ./.pywasi if present, else an
// interpreter with only the standard library. Any Option works, e.g.
// WithModule or WithDependencies, and WithRunOptions sets the environment,
// mounts and stdio of the run. To run several evaluations, create an
// interpreter once with New.
func Eval[T any](ctx context.Context, code string, options ...Option) (T, error) {
	py, run, err := newOnce(ctx, options)
	if err != nil {
		var zero T
		return zero, err
	}
	defer py.Close()
	return py.Eval[T](ctx, code, run...)
}

// Call is (*Python).Call on an interpreter created by New(ctx, options...),
// closed afterwards. See the package-level Eval.
func Call[T any](ctx context.Context, fn Func, options ...Option) (T, error) {
	py, run, err := newOnce(ctx, options)
	if err != nil {
		var zero T
		return zero, err
	}
	defer py.Close()
	return py.Call[T](ctx, fn, run...)
}

func newOnce(ctx context.Context, options []Option) (*Python, []RunOptions, error) {
	var c config
	for _, o := range options {
		o(&c)
	}
	py, err := New(ctx, append(options, func(c *config) { c.run = nil })...)
	if err != nil {
		return nil, nil, err
	}
	if c.run == nil {
		return py, nil, nil
	}
	return py, []RunOptions{*c.run}, nil
}

// Eval runs code as a script in a fresh interpreter and returns the value of
// its final expression, if any, decoded as T (like a REPL or notebook cell):
//
//	n, err := py.Eval[int](ctx, `
//		import math
//		math.factorial(10)
//	`)
//
// The code is dedented first, so it can be indented to match the Go source.
//
// Values are passed as JSON. numpy arrays and scalars become lists and
// numbers, complex numbers {"real", "imag"}, bytes base64 (a Go []byte),
// and datetimes ISO 8601 strings. NaN and Infinity are an error. Use any for
// an untyped result (nil without a final expression) and json.RawMessage to
// decode later. A Python exception is returned as *Error, and any error
// comes with the zero value of T.
//
// The optional RunOptions adds environment, mounts and stdio. Its Args must
// be empty.
func (p *Python) Eval[T any](ctx context.Context, code string, opts ...RunOptions) (T, error) {
	return invoke[T](ctx, p, request{Mode: "eval", Code: dedent(code)}, opts)
}

// Call imports fn.Module, calls fn.Name with fn.Args and fn.Kwargs (encoded
// as JSON), and returns the result decoded as T (any to ignore it). See Eval
// for how values and errors are passed.
func (p *Python) Call[T any](ctx context.Context, fn Func, opts ...RunOptions) (T, error) {
	if fn.Module == "" || fn.Name == "" {
		var zero T
		return zero, errors.New("pywasi: Func.Module and Func.Name are required")
	}
	return invoke[T](ctx, p, callRequest(fn), opts)
}

func callRequest(fn Func) request {
	args := fn.Args
	if args == nil {
		args = []any{}
	}
	kwargs := fn.Kwargs
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	return request{Mode: "call", Module: fn.Module, Name: fn.Name, Args: args, Kwargs: kwargs}
}

// ioGuest is where the per-call request/response directory is mounted.
const ioGuest = guestRoot + "/io"

type request struct {
	Mode   string         `json:"mode"`
	Code   string         `json:"code,omitempty"`
	Module string         `json:"module,omitempty"`
	Name   string         `json:"name,omitempty"`
	Args   []any          `json:"args,omitempty"`
	Kwargs map[string]any `json:"kwargs,omitempty"`
}

type response struct {
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
	Error *Error          `json:"error"`
}

func invoke[T any](ctx context.Context, p *Python, req request, opts []RunOptions) (T, error) {
	var v T
	if err := p.invoke(ctx, req, &v, opts); err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

func (p *Python) invoke(ctx context.Context, req request, out any, opts []RunOptions) error {
	var o RunOptions
	switch len(opts) {
	case 0:
	case 1:
		o = opts[0]
	default:
		return errors.New("pywasi: at most one RunOptions")
	}
	if len(o.Args) > 0 {
		return errors.New("pywasi: RunOptions.Args is not used by Eval or Call")
	}
	if err := checkMounts(o.Mounts); err != nil {
		return err
	}
	return p.runModule(ctx, "_pywasi", req, out, o)
}

// runModule runs `python -m module DIR` with req in DIR/request.json and
// decodes the value of DIR/response.json into out. The module answers
// {"ok": true, "value": ...} or {"ok": false, "error": {...}}.
func (p *Python) runModule(ctx context.Context, module string, req, out any, o RunOptions) error {
	dir, err := os.MkdirTemp("", "pywasi-call-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("pywasi: encoding request: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "request.json"), data, 0o600); err != nil {
		return err
	}

	// Keep stderr for diagnostics if Python dies without a response.
	var stderr tailBuffer
	if o.Stderr != nil {
		o.Stderr = io.MultiWriter(o.Stderr, &stderr)
	} else {
		o.Stderr = &stderr
	}
	o.Mounts = append(o.Mounts[:len(o.Mounts):len(o.Mounts)], Mount{Host: dir, Guest: ioGuest})
	o.Args = []string{"-m", module, ioGuest}

	code, err := p.run(ctx, o)
	if err != nil {
		return err
	}
	data, err = os.ReadFile(filepath.Join(dir, "response.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("pywasi: interpreter exited with code %d without a result: %s", code, stderr.String())
		}
		return err
	}
	return decodeResponse(data, out)
}

// decodeResponse decodes {"ok": true, "value": ...} into out, or returns
// the *Error of {"ok": false, "error": ...}.
func decodeResponse(data []byte, out any) error {
	var resp response
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("pywasi: decoding response: %w", err)
	}
	if !resp.OK {
		if resp.Error == nil {
			return errors.New("pywasi: failed without an error")
		}
		return resp.Error
	}
	if err := json.Unmarshal(resp.Value, out); err != nil {
		return fmt.Errorf("pywasi: decoding result: %w", err)
	}
	return nil
}

// tailBuffer keeps the last few KiB written to it.
type tailBuffer struct{ buf bytes.Buffer }

const tailSize = 4 << 10

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.buf.Write(b)
	if extra := t.buf.Len() - tailSize; extra > 0 {
		t.buf.Next(extra)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	s := strings.TrimSpace(t.buf.String())
	if s == "" {
		return "(no stderr)"
	}
	return s
}
