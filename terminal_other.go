//go:build !(linux || darwin || windows)

package pywasi

import (
	"errors"
	"os"
)

// Terminal modes aren't supported here: tcsetattr fails, and _pyrepl falls
// back to the basic REPL.
type termState struct{}

func getTermState(uintptr) (termState, error) { return termState{}, errors.ErrUnsupported }

func setTermState(uintptr, termState) error { return errors.ErrUnsupported }

func applyTermMode(st termState, _ termMode) termState { return st }

func (t *terminals) setup() func() { return nil }

func (t *terminals) modeFile(f *os.File) *os.File { return f }

func (t *terminals) sizeFile(f *os.File) *os.File { return f }
