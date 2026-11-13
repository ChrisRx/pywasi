package pywasi

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
	"go.chrisrx.dev/x/context"
)

// DefaultCatalog lists the prebuilt pieces WithPackages can link packages
// with C extensions from (numpy, for example): a linker, link kits and
// package bundles, each with a sha256. See WithCatalog.
const DefaultCatalog = "https://pywasi.chrisrx.dev/index.json"

// WithCatalog sets the catalog of prebuilt packages for WithPackages, or ""
// for none (only pure-Python packages). Defaults to DefaultCatalog.
//
// When a requirement resolves to a package with C extensions that the
// catalog has, New downloads its bundle and a link kit and links an
// interpreter with them, with wasm-ld running in wazero, so no compiler or
// Nix is needed. Linked interpreters are cached by their inputs, with a
// startup snapshot that has the bundles' packages imported.
func WithCatalog(url string) Option {
	return func(c *config) { c.catalog, c.catalogSet = url, true }
}

type artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

func (a artifact) source() Source { return ArchiveURL(a.URL, "sha256:"+a.SHA256) }

type kitEntry struct {
	artifact
	ABI    string `json:"abi"`
	Python string `json:"python"`
}

type bundleEntry struct {
	artifact
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	ABI      string   `json:"abi"`
	Requires []string `json:"requires"`
}

type catalog struct {
	Version int           `json:"version"`
	Linker  artifact      `json:"linker"`
	Kits    []kitEntry    `json:"kits"`
	Bundles []bundleEntry `json:"bundles"`
}

// catalogTTL is how long a downloaded catalog is used before refetching.
const catalogTTL = 10 * time.Minute

// loadCatalog fetches the catalog at url, cached for catalogTTL. A stale
// copy is used if it can't be fetched.
func loadCatalog(ctx context.Context, url string) (*catalog, error) {
	data, err := dataDir()
	if err != nil {
		return nil, err
	}
	file := filepath.Join(data, "catalogs", digestJSON(url)+".json")
	read := func() (*catalog, error) {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var c catalog
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("pywasi: catalog %s: %w", url, err)
		}
		return &c, nil
	}
	if st, err := os.Stat(file); err == nil && time.Since(st.ModTime()) < catalogTTL {
		return read()
	}
	body, err := httpGet(ctx, url, "application/json")
	if err == nil {
		var b []byte
		b, err = io.ReadAll(body)
		body.Close()
		if err == nil && json.Valid(b) {
			err = writeFileAtomic(file, b)
		} else if err == nil {
			err = errors.New("not JSON")
		}
	}
	if err != nil {
		if c, rerr := read(); rerr == nil {
			return c, nil
		}
		return nil, fmt.Errorf("pywasi: fetching catalog %s: %w", url, err)
	}
	return read()
}

// prebuilt is what the catalog can link into an interpreter for python
// ("3.X"): the newest kit for it and the bundles built against that kit.
func (c *catalog) prebuilt(python string) (kitEntry, map[string][]bundleEntry, bool) {
	var kit kitEntry
	found := false
	for _, k := range c.Kits {
		if strings.HasPrefix(k.Python, python+".") {
			kit, found = k, true // Later entries are newer.
		}
	}
	if !found {
		return kit, nil, false
	}
	bundles := map[string][]bundleEntry{}
	for _, b := range c.Bundles {
		if b.ABI == kit.ABI {
			bundles[b.Name] = append(bundles[b.Name], b)
		}
	}
	return kit, bundles, true
}

// linkInterpreter links python.wasm from a kit and bundles with wasm-ld
// running in wazero, and returns the new interpreter home (cached by its
// inputs) and the bundles' site-packages directories, to mount at
// bundleGuest(i).
func linkInterpreter(ctx context.Context, linker artifact, kit kitEntry, bundles []bundleEntry, cacheDir string) (string, []string, error) {
	data, err := dataDir()
	if err != nil {
		return "", nil, err
	}
	// A fixed order, since the snapshot records where each bundle is.
	bundles = slices.SortedFunc(slices.Values(bundles), func(a, b bundleEntry) int { return strings.Compare(a.Name, b.Name) })
	var sites, bundleDirs []string
	for _, b := range bundles {
		if b.ABI != kit.ABI {
			return "", nil, fmt.Errorf("pywasi: bundle %s %s was built for another kit", b.Name, b.Version)
		}
		dir, err := unpackCached(ctx, b.source(), "bundles", "bundle.json")
		if err != nil {
			return "", nil, fmt.Errorf("pywasi: bundle %s %s: %w", b.Name, b.Version, err)
		}
		bundleDirs = append(bundleDirs, dir)
		sites = append(sites, filepath.Join(dir, "site"))
	}

	ids := []string{snapshotRev, linker.SHA256, kit.SHA256}
	for _, b := range bundles {
		ids = append(ids, b.SHA256)
	}
	home := filepath.Join(data, "linked", digestJSON(ids))
	if isHome(home) {
		return home, sites, nil
	}

	kitDir, err := unpackCached(ctx, kit.source(), "kits", "link.json")
	if err != nil {
		return "", nil, fmt.Errorf("pywasi: link kit: %w", err)
	}
	var link struct {
		ABI  string   `json:"abi"`
		Args []string `json:"args"`
	}
	b, err := os.ReadFile(filepath.Join(kitDir, "link.json"))
	if err == nil {
		err = json.Unmarshal(b, &link)
	}
	if err != nil {
		return "", nil, fmt.Errorf("pywasi: link kit: %w", err)
	}
	lld, err := linkerWasm(ctx, linker)
	if err != nil {
		return "", nil, err
	}

	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(home), ".link-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "bin"), 0o755); err != nil {
		return "", nil, err
	}

	fsCfg := wazero.NewFSConfig().
		WithReadOnlyDirMount(kitDir, "/kit").
		WithDirMount(filepath.Join(tmp, "bin"), "/out")
	var inputs []string
	preload := []string{"_pywasi"}
	for i, dir := range bundleDirs {
		guest := fmt.Sprintf("/b%d", i)
		fsCfg = fsCfg.WithReadOnlyDirMount(dir, guest)
		var meta struct {
			Modules []struct {
				Name   string `json:"name"`
				Object string `json:"object"`
			} `json:"modules"`
		}
		b, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
		if err == nil {
			err = json.Unmarshal(b, &meta)
		}
		if err != nil {
			return "", nil, fmt.Errorf("pywasi: bundle %s: %w", bundles[i].Name, err)
		}
		for _, m := range meta.Modules {
			inputs = append(inputs, guest+"/"+m.Object)
			if top, _, _ := strings.Cut(m.Name, "."); !slices.Contains(preload, top) {
				preload = append(preload, top)
			}
		}
		inputs = append(inputs, guest+"/registration.o")
	}
	args := []string{"wasm-ld", "-flavor", "wasm"}
	for _, a := range link.Args {
		switch a {
		case "{inputs}":
			args = append(args, inputs...)
		case "{output}":
			args = append(args, "/out/python.wasm")
		default:
			args = append(args, a)
		}
	}
	if err := runWasm(ctx, lld, args, fsCfg, cacheDir); err != nil {
		return "", nil, fmt.Errorf("pywasi: linking interpreter: %w", err)
	}
	if err := os.CopyFS(filepath.Join(tmp, "lib"), os.DirFS(filepath.Join(kitDir, "lib"))); err != nil {
		return "", nil, err
	}
	// Without a snapshot the interpreter still works, only slower to start.
	if err := snapshotLinked(ctx, tmp, sites, preload, cacheDir); err != nil {
		msg := []byte(fmt.Sprintf("pywasi: snapshot failed: %v\n", err))
		if err := os.WriteFile(filepath.Join(tmp, "snapshot-error.txt"), msg, 0o644); err != nil {
			return "", nil, err
		}
	}
	if err := os.Rename(tmp, home); err != nil && !isHome(home) {
		return "", nil, err
	}
	return home, sites, nil
}

// linkerWasm returns wasm-ld for WASI (zstd-compressed in the catalog),
// downloaded and checked once.
func linkerWasm(ctx context.Context, linker artifact) ([]byte, error) {
	data, err := dataDir()
	if err != nil {
		return nil, err
	}
	file := filepath.Join(data, "linker", linker.SHA256, "lld.wasm")
	if b, err := os.ReadFile(file); err == nil {
		return b, nil
	}
	compressed, err := fetchChecked(ctx, linker.URL, linker.SHA256)
	if err != nil {
		return nil, fmt.Errorf("pywasi: downloading the linker: %w", err)
	}
	zr, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("pywasi: unpacking the linker: %w", err)
	}
	return b, writeFileAtomic(file, b)
}

// runWasm runs a WASI command to completion.
func runWasm(ctx context.Context, wasm []byte, args []string, fsCfg wazero.FSConfig, cacheDir string) error {
	cfg := wazero.NewRuntimeConfig().WithDebugInfoEnabled(false)
	if cacheDir != "" {
		cache, err := wazero.NewCompilationCacheWithDir(cacheDir)
		if err != nil {
			return err
		}
		cfg = cfg.WithCompilationCache(cache)
	}
	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	defer r.Close(context.Background())
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		return err
	}
	var stderr tailBuffer
	_, err := r.InstantiateWithConfig(ctx, wasm, wazero.NewModuleConfig().
		WithName("").
		WithArgs(args...).
		WithFSConfig(fsCfg).
		WithStdout(&stderr).
		WithStderr(&stderr).
		WithSysWalltime().
		WithSysNanotime())
	var exitErr *sys.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 0 {
			return nil
		}
		return fmt.Errorf("exit code %d: %s", exitErr.ExitCode(), stderr.String())
	}
	return err
}

// snapshotRev changes the cache key of linked interpreters when how they
// are snapshotted changes.
const snapshotRev = "snapshot-1"

// bundleGuest is where the Python files of a linked interpreter's i-th
// bundle are mounted. It's fixed, since the snapshot imports from there.
func bundleGuest(i int) string {
	return fmt.Sprintf("%s/bundles/%d", guestRoot, i)
}

// snapshotLinked pre-initializes home's python.wasm with the preload
// modules imported, in the same file system layout as at runtime.
func snapshotLinked(ctx context.Context, home string, sites, preload []string, cacheDir string) error {
	wasmPath := filepath.Join(home, "bin", "python.wasm")
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return err
	}
	r, err := newSnapshotRuntime(ctx, cacheDir)
	if err != nil {
		return err
	}
	defer r.Close(context.Background())
	fsCfg := wazero.NewFSConfig().WithReadOnlyDirMount(home, "/")
	var path []string
	for i, dir := range sites {
		fsCfg = fsCfg.WithReadOnlyDirMount(dir, bundleGuest(i))
		path = append(path, bundleGuest(i))
	}
	var out tailBuffer
	cfg := wazero.NewModuleConfig().
		WithArgs("python").
		WithEnv("PYTHONHOME", "/").
		WithEnv("PYTHONDONTWRITEBYTECODE", "1").
		WithEnv("PYTHONPATH", strings.Join(path, ":")).
		WithEnv("PYWASI_PRELOAD", strings.Join(preload, ",")).
		WithFSConfig(fsCfg).
		WithStdout(&out).
		WithStderr(&out).
		WithSysWalltime().
		WithSysNanotime().
		// Real entropy, as at runtime: the snapshot keeps CPython's string
		// hash seed.
		WithRandSource(rand.Reader)
	snap, err := snapshotWasm(ctx, r, wasm, cfg)
	if err != nil {
		return fmt.Errorf("%w: %s", err, out.String())
	}
	return writeFileAtomic(wasmPath, snap)
}
