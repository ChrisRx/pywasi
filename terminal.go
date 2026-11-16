package pywasi

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"sync"

	"github.com/ChrisRx/wazero/api"
	"golang.org/x/term"
)

// Terminals: WASI has no termios, so the interpreter's
// toolchain/support/pywasi_term.c implements tcgetattr, tcsetattr and the
// window size ioctl with the host functions here, in the "pywasi" module.
// Only the guest's standard descriptors can be terminals: those whose
// RunOptions Stdin, Stdout or Stderr is an *os.File on a terminal. That is
// what CPython's REPL (_pyrepl) needs to edit lines itself, with
// completion, history and colors.
//
// Modes arrive in Linux's values (pywasi_term.c's <termios.h>), and each
// platform applies those it can. A run restores the terminals it changed
// when it ends.

// Linux's termios flags that every platform looks at, as the guest sends
// them (terminal_unix.go has the rest).
const (
	linuxISIG   = 0o1
	linuxICANON = 0o2
	linuxECHO   = 0o10
)

const errnoNotty int32 = 59

// termMode is what a guest's tcsetattr asks for.
type termMode struct {
	iflag, oflag, lflag uint32
	vmin, vtime         uint8
}

// signals reports whether the terminal should turn interrupt keys into
// signals. Signals never reach the guest, only pywasi, so in
// non-canonical mode, where a program reads keys one at a time (as
// _pyrepl does), they are passed through instead: Ctrl-C arrives as a
// byte rather than ending pywasi.
func (m termMode) signals() bool {
	return m.lflag&linuxISIG != 0 && m.lflag&linuxICANON != 0
}

// terminals are a run's standard descriptors that are terminals.
type terminals struct {
	mu sync.Mutex
	// files are the host files of guest descriptors 0, 1 and 2, nil where
	// that isn't a terminal.
	files [3]*os.File
	// saved are the states to restore, by host descriptor.
	saved map[uintptr]termState
	// undo restores what setup changed (Windows).
	undo func()
}

type terminalsKey struct{}

// withTerminals adds the terminals among stdin, stdout and stderr to ctx
// for the host functions. restore puts back the states the guest changed.
func withTerminals(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) (_ context.Context, restore func()) {
	t := &terminals{saved: map[uintptr]termState{}}
	found := false
	for i, s := range []any{stdin, stdout, stderr} {
		if f, ok := s.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			t.files[i] = f
			found = true
		}
	}
	if !found {
		return ctx, func() {}
	}
	t.undo = t.setup()
	return context.WithValue(ctx, terminalsKey{}, t), t.restore
}

func (t *terminals) restore() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for fd, st := range t.saved {
		_ = setTermState(fd, st)
	}
	clear(t.saved)
	if t.undo != nil {
		t.undo()
		t.undo = nil
	}
}

// terminalFile returns the host file of a guest descriptor that is a
// terminal.
func terminalFile(ctx context.Context, fd int32) (*terminals, *os.File) {
	t, _ := ctx.Value(terminalsKey{}).(*terminals)
	if t == nil || fd < 0 || int(fd) >= len(t.files) || t.files[fd] == nil {
		return nil, nil
	}
	return t, t.files[fd]
}

// term_get(fd) -> errno: 0 if fd is a terminal.
func termGet(ctx context.Context, _ api.Module, stack []uint64) {
	if _, f := terminalFile(ctx, int32(stack[0])); f == nil {
		stack[0] = uint64(uint32(errnoNotty))
		return
	}
	stack[0] = 0
}

// term_set(fd, when, iflag, oflag, lflag, vmin_vtime) -> errno
func termSet(ctx context.Context, _ api.Module, stack []uint64) {
	fd := int32(stack[0])
	m := termMode{
		iflag: uint32(stack[2]),
		oflag: uint32(stack[3]),
		lflag: uint32(stack[4]),
		vmin:  uint8(stack[5]),
		vtime: uint8(stack[5] >> 8),
	}
	stack[0] = uint64(uint32(func() int32 {
		t, f := terminalFile(ctx, fd)
		if f == nil {
			return errnoNotty
		}
		f = t.modeFile(f)
		if f == nil {
			return errnoNotty
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		hfd := f.Fd()
		orig, ok := t.saved[hfd]
		if !ok {
			st, err := getTermState(hfd)
			if err != nil {
				return errnoIO
			}
			orig = st
		}
		if err := setTermState(hfd, applyTermMode(orig, m)); err != nil {
			return errnoIO
		}
		t.saved[hfd] = orig
		return 0
	}()))
}

// term_size(fd, size) -> errno: writes rows and columns as two uint16.
func termSize(ctx context.Context, mod api.Module, stack []uint64) {
	fd, ptr := int32(stack[0]), uint32(stack[1])
	stack[0] = uint64(uint32(func() int32 {
		t, f := terminalFile(ctx, fd)
		if f == nil {
			return errnoNotty
		}
		f = t.sizeFile(f)
		cols, rows, err := term.GetSize(int(f.Fd()))
		if err != nil {
			return errnoIO
		}
		var b [4]byte
		binary.LittleEndian.PutUint16(b[0:], uint16(rows))
		binary.LittleEndian.PutUint16(b[2:], uint16(cols))
		if !mod.Memory().Write(ptr, b[:]) {
			return errnoInval
		}
		return 0
	}()))
}
