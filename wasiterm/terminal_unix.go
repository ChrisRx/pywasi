//go:build linux || darwin

package wasiterm

import (
	"os"

	"golang.org/x/sys/unix"
)

// More of Linux's termios flags, as the guest sends them.
const (
	linuxECHOE  = 0o20
	linuxECHOK  = 0o40
	linuxECHONL = 0o100
	linuxIEXTEN = 0o100000

	linuxBRKINT = 0o2
	linuxINPCK  = 0o20
	linuxISTRIP = 0o40
	linuxINLCR  = 0o100
	linuxIGNCR  = 0o200
	linuxICRNL  = 0o400
	linuxIXON   = 0o2000
	linuxIXOFF  = 0o10000

	linuxOPOST = 0o1
	linuxONLCR = 0o4
)

type termState = unix.Termios

func getTermState(fd uintptr) (termState, error) {
	t, err := unix.IoctlGetTermios(int(fd), ioctlGetTermios)
	if err != nil {
		return termState{}, err
	}
	return *t, nil
}

func setTermState(fd uintptr, st termState) error {
	return unix.IoctlSetTermios(int(fd), ioctlSetTermios, &st)
}

// applyTermMode returns st with the modes m asks for.
func applyTermMode(st termState, m termMode) termState {
	st.Iflag = setFlags(st.Iflag, m.iflag, []flagBit[uint32]{
		{linuxBRKINT, unix.BRKINT},
		{linuxINPCK, unix.INPCK},
		{linuxISTRIP, unix.ISTRIP},
		{linuxINLCR, unix.INLCR},
		{linuxIGNCR, unix.IGNCR},
		{linuxICRNL, unix.ICRNL},
		{linuxIXON, unix.IXON},
		{linuxIXOFF, unix.IXOFF},
	})
	st.Oflag = setFlags(st.Oflag, m.oflag, []flagBit[uint32]{
		{linuxOPOST, unix.OPOST},
		{linuxONLCR, unix.ONLCR},
	})
	st.Lflag = setFlags(st.Lflag, m.lflag, []flagBit[uint32]{
		{linuxICANON, unix.ICANON},
		{linuxECHO, unix.ECHO},
		{linuxECHOE, unix.ECHOE},
		{linuxECHOK, unix.ECHOK},
		{linuxECHONL, unix.ECHONL},
		{linuxIEXTEN, unix.IEXTEN},
	})
	if m.signals() {
		st.Lflag |= unix.ISIG
	} else {
		st.Lflag &^= unix.ISIG
	}
	st.Cc[unix.VMIN] = m.vmin
	st.Cc[unix.VTIME] = m.vtime
	return st
}

// flagBit is a Linux flag and the host's.
type flagBit[T uint32 | uint64] struct {
	linux uint32
	host  T
}

// setFlags sets or clears each host flag in v as the guest's flags have it.
func setFlags[F ~uint32 | ~uint64, T uint32 | uint64](v F, guest uint32, bits []flagBit[T]) F {
	for _, b := range bits {
		if guest&b.linux != 0 {
			v |= F(b.host)
		} else {
			v &^= F(b.host)
		}
	}
	return v
}

func (t *terminals) setup() func() { return nil }

// modeFile is where modes for f are set: f itself.
func (t *terminals) modeFile(f *os.File) *os.File { return f }

// sizeFile is where f's window size is read: f itself.
func (t *terminals) sizeFile(f *os.File) *os.File { return f }
