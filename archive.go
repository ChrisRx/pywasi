package pywasi

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"go.chrisrx.dev/x/context"

	"go.chrisrx.dev/pywasi/internal/stdlib"
)

// Source is an interpreter archive: a .tar.zst of an interpreter home, as
// written by scripts/build.sh --archive. New unpacks it once into the user
// cache directory, keyed by its sha256, and reuses it after that.
//
// ArchiveURL, ArchiveBytes and ArchiveFile cover the common cases. Implement
// Source to fetch archives from somewhere else.
type Source interface {
	// Digest is the archive's "sha256:<hex>", or "" if it isn't known
	// ahead of time (it is then computed while reading). A known digest
	// lets New skip Open once the archive is unpacked, and is checked
	// against the data.
	Digest() string
	// Open returns the archive's bytes.
	Open(ctx context.Context) (io.ReadCloser, error)
}

// WithArchive uses the interpreter in an archive. See Source.
func WithArchive(src Source) Option {
	return func(c *config) { c.archive = src }
}

// ArchiveURL is an archive downloaded over HTTP(S) and checked against
// digest ("sha256:<hex>"), which is required. It is only downloaded the
// first time.
func ArchiveURL(url, digest string) Source {
	return urlSource{url: url, digest: digest}
}

// ArchiveBytes is an archive in memory, such as one embedded with go:embed.
func ArchiveBytes(data []byte) Source {
	sum := sha256.Sum256(data)
	return bytesSource{data: data, digest: "sha256:" + hex.EncodeToString(sum[:])}
}

// ArchiveFile is an archive on disk.
func ArchiveFile(name string) Source {
	return fileSource(name)
}

type urlSource struct{ url, digest string }

func (s urlSource) Digest() string { return s.digest }

func (s urlSource) Open(ctx context.Context) (io.ReadCloser, error) {
	if s.digest == "" {
		return nil, fmt.Errorf("pywasi: ArchiveURL %s needs a digest", s.url)
	}
	return httpGet(ctx, s.url, "")
}

type bytesSource struct {
	data   []byte
	digest string
}

func (s bytesSource) Digest() string { return s.digest }

func (s bytesSource) Open(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

type fileSource string

func (s fileSource) Digest() string { return "" }

func (s fileSource) Open(context.Context) (io.ReadCloser, error) {
	return os.Open(string(s))
}

// dataDir holds unpacked interpreters, downloads and package layers:
// os.UserCacheDir()/pywasi, or $PYWASI_CACHE.
func dataDir() (string, error) {
	if dir := os.Getenv("PYWASI_CACHE"); dir != "" {
		return dir, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("pywasi: no cache directory for interpreters (set PYWASI_CACHE): %w", err)
	}
	return filepath.Join(dir, "pywasi"), nil
}

// installArchive unpacks src into the cache, if it isn't already, and
// returns the interpreter home.
func installArchive(ctx context.Context, src Source) (string, error) {
	return unpackCached(ctx, src, "homes", "bin/python.wasm")
}

// unpackCached unpacks the archive src into dataDir/kind/<sha256>, if it
// isn't already, and returns that directory. marker is a file every valid
// archive of this kind has.
func unpackCached(ctx context.Context, src Source, kind, marker string) (string, error) {
	data, err := dataDir()
	if err != nil {
		return "", err
	}
	homes := filepath.Join(data, kind)
	isHome := func(dir string) bool {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(marker)))
		return err == nil
	}
	want, err := parseDigest(src.Digest())
	if err != nil {
		return "", err
	}
	if want != "" {
		if home := filepath.Join(homes, want); isHome(home) {
			return home, nil
		}
	}
	if err := os.MkdirAll(homes, 0o755); err != nil {
		return "", err
	}

	// Download (or copy) and hash before unpacking anything.
	rc, err := src.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("pywasi: opening archive: %w", err)
	}
	defer rc.Close()
	tmp, err := os.CreateTemp(homes, ".download-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), rc); err != nil {
		return "", fmt.Errorf("pywasi: reading archive: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if want != "" && got != want {
		return "", fmt.Errorf("pywasi: archive digest is sha256:%s, expected sha256:%s", got, want)
	}
	home := filepath.Join(homes, got)
	if isHome(home) {
		return home, nil
	}

	dir, err := os.MkdirTemp(homes, ".unpack-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := untarZstd(tmp, dir); err != nil {
		return "", fmt.Errorf("pywasi: unpacking archive: %w", err)
	}
	if !isHome(dir) {
		return "", fmt.Errorf("pywasi: archive has no %s", marker)
	}
	if err := os.Rename(dir, home); err != nil {
		// Another process unpacked the same archive first.
		if isHome(home) {
			return home, nil
		}
		return "", err
	}
	return home, nil
}

func isHome(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "bin", "python.wasm"))
	return err == nil
}

// parseDigest returns the hex of "sha256:<hex>", or "" for "".
func parseDigest(digest string) (string, error) {
	if digest == "" {
		return "", nil
	}
	hexsum, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hexsum) != sha256.Size*2 {
		return "", fmt.Errorf("pywasi: digest %q is not sha256:<64 hex digits>", digest)
	}
	if _, err := hex.DecodeString(hexsum); err != nil {
		return "", fmt.Errorf("pywasi: digest %q: %w", digest, err)
	}
	return strings.ToLower(hexsum), nil
}

// untarZstd unpacks regular files and directories, rejecting paths that
// would land outside dir.
func untarZstd(r io.Reader, dir string) error {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(hdr.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("unsafe path %q", hdr.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported entry %q (type %c)", hdr.Name, hdr.Typeflag)
		}
	}
}

func httpGet(ctx context.Context, url, accept string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

// DefaultArchive is the interpreter New uses without WithHome,
// WithDependencies or WithArchive when there is none at DefaultHome: CPython
// with only the standard library, embedded in this package (about 7 MiB),
// so it works offline and without a download. It is unpacked into the
// user cache directory on first use.
var DefaultArchive Source = ArchiveBytes(stdlib.Archive)
