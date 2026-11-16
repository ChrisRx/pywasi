//go:build linux || darwin

package pywasi

import (
	"errors"
	"os"
	"syscall"
)

// privateDir makes dir, or checks the one there, as a directory only this
// user can use. Its name is predictable and the OS temp directory is shared,
// so another user could have made it first, or put a symlink there.
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
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return errors.New("owned by another user")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}
