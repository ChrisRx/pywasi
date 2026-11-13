package pywasi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"go.chrisrx.dev/x/context"

	"go.chrisrx.dev/pywasi/internal/session"
)

// ErrSessionClosed is returned by a Session's Eval and Call after Close,
// after its interpreter exited, or after a call's context was canceled.
var ErrSessionClosed = errors.New("pywasi: session closed")

// Session is one long-lived interpreter that runs Eval and Call requests
// in turn, keeping its state between them: imports, and the globals that
// Eval code defines, like a notebook kernel. Requests skip interpreter
// startup, so each costs a few milliseconds plus the work itself, instead
// of a fresh interpreter's tens to hundreds of milliseconds.
//
// The trade-off is isolation: calls share one interpreter, so one call's
// changes (or memory) are visible to the next, and calls on a Session run
// one at a time. For concurrency, use several Sessions or the Python's own
// Eval and Call, which are isolated and can run in parallel.
//
// A Session is safe for use by multiple goroutines. Close it when done.
type Session struct {
	mu     sync.Mutex
	stdin  *io.PipeWriter
	frames chan []byte
	done   chan struct{}
	err    error // why the interpreter exited, once done is closed
	broken bool
}

// Session starts a long-lived interpreter. The optional RunOptions sets its
// environment, mounts and stderr, and where the code's own output (print)
// goes (Stdout). Its Args and Stdin must be empty: the session uses stdin
// for requests.
//
// The interpreter stops when the Session is closed or ctx is done. A call
// whose context is canceled while it runs makes the Session unusable,
// since the running code can't be interrupted.
func (p *Python) Session(ctx context.Context, opts ...RunOptions) (*Session, error) {
	var o RunOptions
	switch len(opts) {
	case 0:
	case 1:
		o = opts[0]
	default:
		return nil, errors.New("pywasi: at most one RunOptions")
	}
	if len(o.Args) > 0 || o.Stdin != nil {
		return nil, errors.New("pywasi: RunOptions.Args and Stdin are not used by Session")
	}
	if err := checkMounts(o.Mounts); err != nil {
		return nil, err
	}

	const guest = guestRoot + "/session"
	stdinR, stdinW := io.Pipe()
	s := &Session{
		stdin:  stdinW,
		frames: make(chan []byte, 1),
		done:   make(chan struct{}),
	}
	out := o.Stdout
	if out == nil {
		out = io.Discard
	}
	var stderr tailBuffer
	if o.Stderr != nil {
		o.Stderr = io.MultiWriter(o.Stderr, &stderr)
	} else {
		o.Stderr = &stderr
	}
	o.Stdin = stdinR
	o.Stdout = &frameWriter{out: out, frames: s.frames, done: s.done}
	o.Args = []string{"-m", "_pywasi_session"}
	o.fsMounts = append(o.fsMounts[:len(o.fsMounts):len(o.fsMounts)], fsMount{fsys: session.FS(), guest: guest})
	env := map[string]string{}
	for k, v := range o.Env {
		env[k] = v
	}
	if v := env["PYTHONPATH"]; v != "" {
		env["PYTHONPATH"] = guest + ":" + v
	} else {
		env["PYTHONPATH"] = guest
	}
	o.Env = env

	go func() {
		code, err := p.run(context.WithoutCancel(ctx), o)
		if err == nil {
			err = fmt.Errorf("pywasi: session interpreter exited with code %d: %s", code, stderr.String())
		}
		s.err = err
		stdinR.CloseWithError(ErrSessionClosed)
		close(s.done)
	}()
	stop := context.AfterFunc(ctx, func() { s.Close() })
	go func() {
		<-s.done
		stop()
	}()
	return s, nil
}

// Eval is (*Python).Eval in the session: code runs in the session's
// __main__, so names it defines stay defined for later requests.
func (s *Session) Eval[T any](ctx context.Context, code string) (T, error) {
	var v T
	err := s.do(ctx, request{Mode: "eval", Code: dedent(code)}, &v)
	if err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

// Call is (*Python).Call in the session. Imported modules stay imported.
func (s *Session) Call[T any](ctx context.Context, fn Func) (T, error) {
	var v T
	if fn.Module == "" || fn.Name == "" {
		return v, errors.New("pywasi: Func.Module and Func.Name are required")
	}
	err := s.do(ctx, callRequest(fn), &v)
	if err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

func (s *Session) do(ctx context.Context, req request, out any) error {
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("pywasi: encoding request: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return ErrSessionClosed
	}

	// The pipe hands the request to the interpreter as it reads it.
	written := make(chan error, 1)
	go func() {
		_, err := s.stdin.Write(append(line, '\n'))
		written <- err
	}()
	select {
	case frame := <-s.frames:
		return decodeResponse(frame, out)
	case <-s.done:
		s.broken = true
		if errors.Is(s.err, ErrSessionClosed) {
			return ErrSessionClosed
		}
		return fmt.Errorf("%w: %w", ErrSessionClosed, s.err)
	case <-ctx.Done():
		// The request may still be running and can't be stopped, and a
		// later answer would be taken for the next request's.
		s.broken = true
		s.stdin.CloseWithError(ErrSessionClosed)
		return ctx.Err()
	}
}

// Close stops the interpreter after any running request, and waits for it.
func (s *Session) Close() error {
	s.stdin.Close()
	<-s.done
	s.mu.Lock()
	s.broken = true
	s.mu.Unlock()
	return nil
}

// frameWriter splits the interpreter's stdout into the code's own output,
// passed to out, and responses: a record separator (0x1e), JSON and a
// newline.
type frameWriter struct {
	out     io.Writer
	frames  chan<- []byte
	done    <-chan struct{}
	frame   []byte
	inFrame bool
}

const recordSeparator = 0x1e

func (w *frameWriter) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		if !w.inFrame {
			i := bytes.IndexByte(b, recordSeparator)
			if i < 0 {
				_, err := w.out.Write(b)
				return n, err
			}
			if i > 0 {
				if _, err := w.out.Write(b[:i]); err != nil {
					return n, err
				}
			}
			b, w.inFrame, w.frame = b[i+1:], true, nil
			continue
		}
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			w.frame = append(w.frame, b...)
			return n, nil
		}
		w.frame = append(w.frame, b[:i]...)
		b, w.inFrame = b[i+1:], false
		select {
		case w.frames <- w.frame:
		case <-w.done:
		}
	}
	return n, nil
}
