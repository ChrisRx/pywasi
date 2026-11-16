//go:build linux

package pywasi_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"go.chrisrx.dev/pywasi"
)

// openPty returns a pseudo-terminal's controlling side and its terminal,
// rows by cols.
func openPty(t *testing.T, rows, cols uint16) (ptm, pts *os.File) {
	t.Helper()
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals: %v", err)
	}
	t.Cleanup(func() { ptm.Close() })
	if err := unix.IoctlSetPointerInt(int(ptm.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetUint32(int(ptm.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	pts, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pts.Close() })
	if err := unix.IoctlSetWinsize(int(pts.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols}); err != nil {
		t.Fatal(err)
	}
	return ptm, pts
}

func TestTerminal(t *testing.T) {
	ctx := context.Background()
	t.Setenv("PYWASI_HOME", t.TempDir()) // The embedded interpreter.
	py, err := pywasi.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer py.Close()

	ptm, pts := openPty(t, 30, 100)
	before, err := unix.IoctlGetTermios(int(pts.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(ptm) // EIO once the terminal side is closed.
		read <- b
	}()

	// Raw mode turns off output processing, so a newline written then
	// isn't turned into \r\n as it is before and after.
	var stderr bytes.Buffer
	code, err := py.Run(ctx, pywasi.RunOptions{
		Args: []string{"-c", `
import os, sys, termios, tty
assert os.isatty(0) and os.isatty(1)
print("size", termios.tcgetwinsize(1))
cooked = termios.tcgetattr(0)
tty.setraw(0)
assert not termios.tcgetattr(0)[3] & termios.ICANON
os.write(1, b"raw\n")
termios.tcsetattr(0, termios.TCSANOW, cooked)
os.write(1, b"cooked\n")
tty.setraw(0)  # Left for pywasi to restore.
`},
		Stdin:  pts,
		Stdout: pts,
		Stderr: &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf("exit %d, %v\nstderr:\n%s", code, err, stderr.String())
	}
	after, err := unix.IoctlGetTermios(int(pts.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	pts.Close()
	out := string(<-read)
	for _, want := range []string{"size (30, 100)\r\n", "raw\ncooked\r\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q doesn't have %q", out, want)
		}
	}
	if after.Lflag != before.Lflag || after.Iflag != before.Iflag || after.Oflag != before.Oflag {
		t.Errorf("terminal not restored: lflag %#o, want %#o", after.Lflag, before.Lflag)
	}

	// Not a terminal.
	code, _, errOut := run(t, py, pywasi.RunOptions{Args: []string{"-c", `
import termios
try:
    termios.tcgetattr(0)
except termios.error as e:
    raise SystemExit(0 if e.args[0] == 59 else 1)  # ENOTTY
raise SystemExit(1)
`}})
	if code != 0 {
		t.Fatalf("tcgetattr on a pipe: exit %d\n%s", code, errOut)
	}
}
