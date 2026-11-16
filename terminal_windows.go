package pywasi

import (
	"os"

	"golang.org/x/sys/windows"
)

// termState is a console mode.
type termState uint32

func getTermState(fd uintptr) (termState, error) {
	var m uint32
	err := windows.GetConsoleMode(windows.Handle(fd), &m)
	return termState(m), err
}

func setTermState(fd uintptr, st termState) error {
	return windows.SetConsoleMode(windows.Handle(fd), uint32(st))
}

// applyTermMode returns the console input mode st with the modes m asks
// for. Keys arrive as VT sequences outside canonical mode, as on a Unix
// terminal.
func applyTermMode(st termState, m termMode) termState {
	v := uint32(st)
	set := func(on bool, bit uint32) {
		if on {
			v |= bit
		} else {
			v &^= bit
		}
	}
	canon := m.lflag&linuxICANON != 0
	set(canon, windows.ENABLE_LINE_INPUT)
	// The console only echoes in line input mode.
	set(canon && m.lflag&linuxECHO != 0, windows.ENABLE_ECHO_INPUT)
	set(m.signals(), windows.ENABLE_PROCESSED_INPUT)
	set(!canon, windows.ENABLE_VIRTUAL_TERMINAL_INPUT)
	return termState(v)
}

// setup turns on VT processing for the output consoles, since the guest
// writes ANSI escape sequences, and returns a func that turns it back off.
func (t *terminals) setup() func() {
	var undo []func()
	for _, f := range t.files[1:] {
		if f == nil {
			continue
		}
		h := windows.Handle(f.Fd())
		var m uint32
		if windows.GetConsoleMode(h, &m) != nil {
			continue
		}
		want := m | windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
		if want != m && windows.SetConsoleMode(h, want) == nil {
			undo = append(undo, func() { _ = windows.SetConsoleMode(h, m) })
		}
	}
	return func() {
		for _, u := range undo {
			u()
		}
	}
}

// modeFile is where modes for f are set: input modes belong to the console
// input, stdin.
func (t *terminals) modeFile(*os.File) *os.File { return t.files[0] }

// sizeFile is where f's window size is read: the console screen buffer,
// which output handles have.
func (t *terminals) sizeFile(f *os.File) *os.File {
	for _, o := range t.files[1:] {
		if o != nil {
			return o
		}
	}
	return f
}
