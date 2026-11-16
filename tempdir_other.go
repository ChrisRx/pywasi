//go:build !linux && !darwin

package pywasi

import (
	"errors"
	"os"
)

// privateDir makes dir, or checks the one there is a directory (not a
// symlink). The OS temp directory is per user on Windows.
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("not a directory")
	}
	return nil
}
