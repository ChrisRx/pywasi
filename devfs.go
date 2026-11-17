package pywasi

import (
	"crypto/rand"
	"io/fs"
	"slices"
	"strings"

	experimentalsys "github.com/ChrisRx/wazero/experimental/sys"
	wazerosys "github.com/ChrisRx/wazero/sys"
)

// guestDev is where runs find devFS.
const guestDev = "/dev"

// devFS is a /dev with the devices programs open by name: null (os.devnull,
// which pytest and logging configurations write to), zero and urandom.
// WASI has no device files of its own.
type devFS struct {
	experimentalsys.UnimplementedFS
}

var devices = []string{"null", "urandom", "zero"}

func (devFS) OpenFile(path string, _ experimentalsys.Oflag, _ fs.FileMode) (experimentalsys.File, experimentalsys.Errno) {
	path = devPath(path)
	if path == "" {
		return &devDir{}, 0
	}
	if !slices.Contains(devices, path) {
		return nil, experimentalsys.ENOENT
	}
	return &devFile{name: path}, 0
}

func (d devFS) Stat(path string) (wazerosys.Stat_t, experimentalsys.Errno) {
	path = devPath(path)
	if path == "" {
		return wazerosys.Stat_t{Mode: fs.ModeDir | 0o755, Nlink: 2}, 0
	}
	if !slices.Contains(devices, path) {
		return wazerosys.Stat_t{}, experimentalsys.ENOENT
	}
	return deviceStat(), 0
}

func (d devFS) Lstat(path string) (wazerosys.Stat_t, experimentalsys.Errno) { return d.Stat(path) }

// devPath is path relative to /dev, "" for /dev itself.
func devPath(path string) string {
	path = strings.Trim(path, "/")
	if path == "." {
		return ""
	}
	return path
}

func deviceStat() wazerosys.Stat_t {
	return wazerosys.Stat_t{Mode: fs.ModeDevice | fs.ModeCharDevice | 0o666, Nlink: 1}
}

// devDir is /dev itself, listing the devices.
type devDir struct {
	experimentalsys.UnimplementedFile
	read bool
}

func (*devDir) IsDir() (bool, experimentalsys.Errno) { return true, 0 }

func (*devDir) Stat() (wazerosys.Stat_t, experimentalsys.Errno) {
	return wazerosys.Stat_t{Mode: fs.ModeDir | 0o755, Nlink: 2}, 0
}

func (d *devDir) Readdir(int) ([]experimentalsys.Dirent, experimentalsys.Errno) {
	if d.read {
		return nil, 0
	}
	d.read = true
	var out []experimentalsys.Dirent
	for _, name := range devices {
		out = append(out, experimentalsys.Dirent{Name: name, Type: fs.ModeDevice | fs.ModeCharDevice})
	}
	return out, 0
}

func (*devDir) Close() experimentalsys.Errno { return 0 }

// devFile is an open device: writes are discarded, and reads give nothing
// (null), zeros (zero) or random bytes (urandom).
type devFile struct {
	experimentalsys.UnimplementedFile
	name string
}

func (*devFile) IsDir() (bool, experimentalsys.Errno) { return false, 0 }

func (*devFile) Stat() (wazerosys.Stat_t, experimentalsys.Errno) { return deviceStat(), 0 }

func (f *devFile) Read(buf []byte) (int, experimentalsys.Errno) {
	switch f.name {
	case "zero":
		clear(buf)
		return len(buf), 0
	case "urandom":
		if _, err := rand.Read(buf); err != nil {
			return 0, experimentalsys.EIO
		}
		return len(buf), 0
	}
	return 0, 0
}

func (f *devFile) Pread(buf []byte, _ int64) (int, experimentalsys.Errno) { return f.Read(buf) }

func (*devFile) Write(buf []byte) (int, experimentalsys.Errno) { return len(buf), 0 }

func (*devFile) Pwrite(buf []byte, _ int64) (int, experimentalsys.Errno) { return len(buf), 0 }

func (*devFile) Truncate(int64) experimentalsys.Errno { return 0 }

func (*devFile) Sync() experimentalsys.Errno { return 0 }

func (*devFile) Datasync() experimentalsys.Errno { return 0 }

func (*devFile) Close() experimentalsys.Errno { return 0 }
