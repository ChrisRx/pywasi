package pywasi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"

	"go.chrisrx.dev/x/context"
)

// uvVersion is the uv release ResolverUV downloads when uv isn't on PATH.
const uvVersion = "0.13.7"

// uvReleases are the sha256s of uv's release archives, by GOOS/GOARCH.
var uvReleases = map[string]struct{ asset, sha256 string }{
	"linux/amd64":   {"uv-x86_64-unknown-linux-musl.tar.gz", "287491da03f2a785c70fa75b13e1f849918c2f2b5415fe84876b8214eeb792f9"},
	"linux/arm64":   {"uv-aarch64-unknown-linux-musl.tar.gz", "2d3ec076a0c4c0e8250bb6418c44080828b544f1a3be0b6e46adf9fb6923de45"},
	"darwin/amd64":  {"uv-x86_64-apple-darwin.tar.gz", "20c47e4c1f630b8abaff8c291fa9b9aa300fef92c4fbff95adf198b55287cda7"},
	"darwin/arm64":  {"uv-aarch64-apple-darwin.tar.gz", "81b811b57e4624df7ed3aac148e0359100d9962f3d9dce00aec533dc2e086731"},
	"windows/amd64": {"uv-x86_64-pc-windows-msvc.zip", "0cdc5b38fe5a8168ed6a25814f5ba43c21d313dd3187314fccb37b7cec46e021"},
	"windows/arm64": {"uv-aarch64-pc-windows-msvc.zip", "505d9e65e7692bac3ddf98c66754aa26c5050c409c184af30a881a0a72cc85e3"},
}

// uvMirrors are tried in order. The pywasi mirror holds the same files as
// uv's GitHub release.
var uvMirrors = []string{
	"https://pywasi.chrisrx.dev/uv/" + uvVersion + "/",
	"https://github.com/astral-sh/uv/releases/download/" + uvVersion + "/",
}

// findUV returns uv from PATH, or else the pinned release, downloaded into
// the cache and checked on first use.
func findUV(ctx context.Context) (string, error) {
	if uv, err := exec.LookPath("uv"); err == nil {
		return uv, nil
	}
	rel, ok := uvReleases[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("pywasi: no uv release for %s/%s; install uv on PATH", runtime.GOOS, runtime.GOARCH)
	}
	data, err := dataDir()
	if err != nil {
		return "", err
	}
	exe := "uv"
	if runtime.GOOS == "windows" {
		exe = "uv.exe"
	}
	bin := filepath.Join(data, "uv", uvVersion, exe)
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}

	var archive []byte
	var errs []error
	for _, mirror := range uvMirrors {
		archive, err = fetchChecked(ctx, mirror+rel.asset, rel.sha256)
		if err == nil {
			break
		}
		errs = append(errs, err)
	}
	if archive == nil {
		return "", fmt.Errorf("pywasi: downloading uv: %v", errs)
	}
	b, err := extractFile(rel.asset, archive, exe)
	if err != nil {
		return "", fmt.Errorf("pywasi: unpacking %s: %w", rel.asset, err)
	}
	if err := writeFileAtomic(bin, b); err != nil {
		return "", err
	}
	return bin, os.Chmod(bin, 0o755)
}

func fetchChecked(ctx context.Context, url, want string) ([]byte, error) {
	body, err := httpGet(ctx, url, "")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	b, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("%s: sha256 %x, expected %s", url, sum, want)
	}
	return b, nil
}

// extractFile returns the file named base from a .tar.gz or .zip.
func extractFile(asset string, archive []byte, base string) ([]byte, error) {
	if path.Ext(asset) == ".zip" {
		z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range z.File {
			if path.Base(f.Name) == base {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("no %s in archive", base)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("no %s in archive", base)
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == base {
			return io.ReadAll(tr)
		}
	}
}
