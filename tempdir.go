package pywasi

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
)

// guestTemp is where an interpreter's temp space is mounted.
const guestTemp = "/tmp"

// WithTempDir sets the host directory mounted read-write at /tmp, where
// tempfile and libraries that keep caches there (matplotlib, for one) write.
//
// By default each interpreter gets one under the OS temp directory,
// pywasi-<uid>-<digest> in os.TempDir(), where digest identifies its
// python.wasm (a linked interpreter has its own). It's kept between runs,
// so caches carry over from one to the next, and every run of that
// interpreter by the same OS user shares it, concurrent ones included, as
// processes on a machine share /tmp. Give runs that mustn't see each other's
// files a directory of their own, or "" for no /tmp at all. A Mount at /tmp
// takes its place.
//
// Unless RunOptions.Env sets HOME, it's the home directory too, so caches
// kept under ~ (matplotlib's font cache, Hugging Face's downloads) carry
// over as well.
func WithTempDir(dir string) Option {
	return func(c *config) { c.tempDir, c.tempDirSet = dir, true }
}

// tempMount returns the host directory to mount at /tmp for a run with
// mounts, or "" for none.
func (p *Python) tempMount(mounts []Mount) (string, error) {
	for _, m := range mounts {
		if path.Clean(m.Guest) == guestTemp {
			return "", nil
		}
	}
	if p.tempDirSet {
		if p.tempDir == "" {
			return "", nil
		}
		dir, err := filepath.Abs(p.tempDir)
		if err != nil {
			return "", err
		}
		return dir, os.MkdirAll(dir, 0o700)
	}
	name := "pywasi-" + p.digest
	if uid := os.Getuid(); uid >= 0 {
		name = "pywasi-" + strconv.Itoa(uid) + "-" + p.digest
	}
	dir := filepath.Join(os.TempDir(), name)
	if err := privateDir(dir); err != nil {
		return "", fmt.Errorf("pywasi: temp space %s: %w", dir, err)
	}
	return dir, nil
}

// snapshotTempDir is a throwaway /tmp for taking a snapshot, so that one
// taken while importing modules that look for a temp directory (tempfile
// caches its choice) finds /tmp as runs will. remove deletes it.
func snapshotTempDir() (dir string, remove func(), err error) {
	dir, err = os.MkdirTemp("", "pywasi-snapshot-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}
