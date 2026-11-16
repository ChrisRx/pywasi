package pywasi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ChrisRx/wazero"
	"github.com/ChrisRx/wazero/api"
	"github.com/ChrisRx/wazero/sys"
	"github.com/klauspost/compress/zstd"
)

// Snapshots as data. An interpreter whose python.wasm can be pre-initialized
// (it exports wizer-initialize and pywasi-resume) is compiled once, with its
// mutable globals exported, and a snapshot of it is an image: the memory
// after initialization, as the differences from a fresh instance's, and the
// globals' values. A run instantiates the module without starting it,
// writes the image in and calls pywasi-resume. So snapshots with different
// modules preloaded (for each set of packages) share the compiled code,
// instead of each being a module to compile.

// imageRev changes the cache key of images when what they hold does.
const imageRev = "image-1"

// image is a pre-initialized interpreter's state, for one compiled module.
type image struct {
	pages   uint32 // memory size
	globals []imageGlobal
	diffs   []imageDiff
}

type imageGlobal struct {
	index uint32 // in the module's global index space
	value uint64
}

// imageDiff is a range of memory, at offset, that differs from a fresh
// instance's.
type imageDiff struct {
	offset uint32
	data   []byte
}

// resumable prepares wasm to be restored from images, if it can be
// pre-initialized: it returns the module with its mutable globals exported,
// and their indices. Otherwise it returns wasm unchanged and no globals.
func resumable(wasm []byte) ([]byte, []uint32, bool, error) {
	m, err := parseWasm(wasm)
	if err != nil {
		// Something this reader doesn't know: run it as it is.
		return wasm, nil, false, nil
	}
	if !m.exports[initExport] || !m.exports[resumeExport] {
		return wasm, nil, false, nil
	}
	var globals []uint32
	for _, g := range m.globals {
		if g.mutable {
			globals = append(globals, g.index)
		}
	}
	instrumented, err := m.instrument()
	return instrumented, globals, true, err
}

// takeImage pre-initializes an instance of compiled (resumable) with cfg,
// which sets the environment and PYWASI_PRELOAD as at run time, and returns
// its state.
func takeImage(ctx context.Context, r wazero.Runtime, compiled wazero.CompiledModule, globals []uint32, cfg wazero.ModuleConfig) (*image, error) {
	mod, err := r.InstantiateModule(ctx, compiled, cfg.WithName("").WithStartFunctions())
	if err != nil {
		return nil, err
	}
	defer mod.Close(ctx)
	mem := mod.Memory()
	if mem == nil {
		return nil, errors.New("no memory")
	}
	fresh, ok := mem.Read(0, mem.Size())
	if !ok {
		return nil, errors.New("reading memory")
	}
	fresh = bytes.Clone(fresh)
	if _, err := mod.ExportedFunction(initExport).Call(ctx); err != nil {
		var exitErr *sys.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("initialization exited with code %d", exitErr.ExitCode())
		}
		return nil, err
	}
	data, ok := mem.Read(0, mem.Size())
	if !ok {
		return nil, errors.New("reading memory")
	}
	img := &image{pages: mem.Size() / 65536, diffs: diffMemory(fresh, data)}
	for _, index := range globals {
		g := mod.ExportedGlobal(globalPrefix + strconv.Itoa(int(index)))
		if g == nil {
			return nil, fmt.Errorf("global %d isn't exported", index)
		}
		img.globals = append(img.globals, imageGlobal{index, g.Get()})
	}
	return img, nil
}

// restore writes the image into mod, a fresh, unstarted instance of the
// module it was taken from.
func (img *image) restore(mod api.Module) error {
	mem := mod.Memory()
	if mem == nil {
		return errors.New("no memory")
	}
	if have := mem.Size() / 65536; have < img.pages {
		if _, ok := mem.Grow(img.pages - have); !ok {
			return errors.New("growing memory")
		}
	}
	for _, d := range img.diffs {
		if !mem.Write(d.offset, d.data) {
			return errors.New("writing memory")
		}
	}
	for _, g := range img.globals {
		mg, ok := mod.ExportedGlobal(globalPrefix + strconv.Itoa(int(g.index))).(api.MutableGlobal)
		if !ok {
			return fmt.Errorf("global %d isn't exported and mutable", g.index)
		}
		mg.Set(g.value)
	}
	return nil
}

// diffMemory returns the ranges of after that differ from before (zero
// beyond its end), merging ranges separated by short equal runs (each range
// costs about 8 bytes and a write).
func diffMemory(before, after []byte) []imageDiff {
	const maxGap = 64
	differs := func(i int) bool {
		if i < len(before) {
			return before[i] != after[i]
		}
		return after[i] != 0
	}
	var diffs []imageDiff
	for i := 0; i < len(after); {
		if !differs(i) {
			i++
			continue
		}
		start, end := i, i+1
		for j := end; j < len(after) && j-end < maxGap; j++ {
			if differs(j) {
				end = j + 1
			}
		}
		diffs = append(diffs, imageDiff{uint32(start), bytes.Clone(after[start:end])})
		i = end
	}
	return diffs
}

var imageMagic = []byte("PYWASIMG1")

// encode writes the image, zstd-compressed.
func (img *image) encode(w io.Writer) error {
	zw, err := zstd.NewWriter(w)
	if err != nil {
		return err
	}
	var b []byte
	b = append(b, imageMagic...)
	b = binary.LittleEndian.AppendUint32(b, img.pages)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(img.globals)))
	for _, g := range img.globals {
		b = binary.LittleEndian.AppendUint32(b, g.index)
		b = binary.LittleEndian.AppendUint64(b, g.value)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(img.diffs)))
	if _, err := zw.Write(b); err != nil {
		return err
	}
	for _, d := range img.diffs {
		var h [8]byte
		binary.LittleEndian.PutUint32(h[:4], d.offset)
		binary.LittleEndian.PutUint32(h[4:], uint32(len(d.data)))
		if _, err := zw.Write(h[:]); err != nil {
			return err
		}
		if _, err := zw.Write(d.data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// decodeImage reads an image written by encode.
func decodeImage(r io.Reader) (*image, error) {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(b, imageMagic) {
		return nil, errors.New("not an image")
	}
	b = b[len(imageMagic):]
	u32 := func() uint32 {
		if len(b) < 4 {
			err = io.ErrUnexpectedEOF
			return 0
		}
		v := binary.LittleEndian.Uint32(b)
		b = b[4:]
		return v
	}
	img := &image{pages: u32()}
	for range u32() {
		index := u32()
		if len(b) < 8 {
			return nil, io.ErrUnexpectedEOF
		}
		img.globals = append(img.globals, imageGlobal{index, binary.LittleEndian.Uint64(b)})
		b = b[8:]
	}
	for n := u32(); n > 0 && err == nil; n-- {
		offset, size := u32(), u32()
		if uint32(len(b)) < size {
			return nil, io.ErrUnexpectedEOF
		}
		img.diffs = append(img.diffs, imageDiff{offset, b[:size:size]})
		b = b[size:]
	}
	return img, err
}

// snapshot returns the interpreter's image, with the modules this New asked
// for preloaded, taking it if it isn't cached. If it can't be taken, it
// records why next to where it would be and returns nil: runs then start
// from scratch.
func (p *Python) snapshot(ctx context.Context, opts config, site string) *image {
	data, err := dataDir()
	if err != nil {
		return nil
	}
	preload := p.preloads(opts, site)
	key := digestJSON([]any{imageRev, p.digest, preload, p.bundleSites, site})
	file := filepath.Join(data, "images", key+".img")
	if f, err := os.Open(file); err == nil {
		img, err := decodeImage(f)
		f.Close()
		if err == nil {
			return img
		}
	}
	if _, err := os.Stat(file + ".err"); err == nil {
		return nil // Failed before: don't retry each time.
	}
	var out tailBuffer
	img, err := p.takeSnapshot(ctx, preload, site, &out)
	if err != nil {
		if ctx.Err() == nil {
			_ = writeFileAtomic(file+".err", fmt.Appendf(nil, "pywasi: snapshot failed: %v\n%s", err, out.String()))
		}
		return nil
	}
	var buf bytes.Buffer
	if err := img.encode(&buf); err == nil {
		_ = writeFileAtomic(file, buf.Bytes())
	}
	return img
}

// takeSnapshot pre-initializes the interpreter with preload imported, in
// the file system layout and environment of a run (without RunOptions):
// the home at /, bundles' and pure packages' sites, and a /tmp.
func (p *Python) takeSnapshot(ctx context.Context, preload []string, site string, out io.Writer) (*image, error) {
	tmp, removeTmp, err := snapshotTempDir()
	if err != nil {
		return nil, err
	}
	defer removeTmp()
	fsCfg := wazero.NewFSConfig().WithReadOnlyDirMount(p.home, "/").WithDirMount(tmp, guestTemp)
	var path []string
	for i, dir := range p.bundleSites {
		fsCfg = fsCfg.WithReadOnlyDirMount(dir, bundleGuest(i))
		path = append(path, bundleGuest(i))
	}
	if site != "" {
		fsCfg = fsCfg.WithReadOnlyDirMount(site, guestRoot+"/site")
		path = append(path, guestRoot+"/site")
	}
	cfg := wazero.NewModuleConfig().
		WithArgs("python").
		WithEnv("PYTHONHOME", "/").
		WithEnv("PYTHONDONTWRITEBYTECODE", "1").
		WithEnv("HOME", guestTemp).
		WithEnv("PYWASI_PRELOAD", strings.Join(preload, ",")).
		WithFSConfig(fsCfg).
		WithStdout(out).
		WithStderr(out).
		WithSysWalltime().
		WithSysNanotime().
		WithSysNanosleep().
		// Real entropy, as at runtime: the snapshot keeps CPython's string
		// hash seed.
		WithRandSource(rand.Reader)
	if len(path) > 0 {
		cfg = cfg.WithEnv("PYTHONPATH", strings.Join(path, ":"))
	}
	return takeImage(ctx, p.runtime, p.compiled, p.globals, cfg)
}

// preloads lists the modules to import into the snapshot: _pywasi (for
// Eval and Call), WithPreload's, the top-level modules of linked-in
// bundles' extensions, and those of the packages asked for (every package
// in a WithPylock lock), looked up in their installed metadata.
func (p *Python) preloads(opts config, site string) []string {
	names := []string{"_pywasi"}
	add := func(name string) {
		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, m := range opts.preload {
		add(m)
	}
	for _, m := range p.bundleModules {
		add(m)
	}
	dirs := slices.Clone(p.bundleSites)
	if site != "" {
		dirs = append(dirs, site)
	}
	var wanted map[string]bool // nil: all
	if opts.pylock == nil {
		wanted = map[string]bool{}
		for _, req := range opts.packages {
			wanted[normalizeName(requirementName(req))] = true
		}
	}
	for _, dir := range dirs {
		infos, _ := filepath.Glob(filepath.Join(dir, "*.dist-info"))
		slices.Sort(infos)
		for _, info := range infos {
			name, _, _ := strings.Cut(filepath.Base(info), "-")
			if wanted == nil || wanted[normalizeName(name)] {
				for _, m := range topLevelModules(info) {
					add(m)
				}
			}
		}
	}
	return names
}

// requirementName is the package name in a PEP 508 requirement.
func requirementName(req string) string {
	name, _, _ := strings.Cut(req, ";")
	name = strings.TrimSpace(name)
	if end := strings.IndexAny(name, " [(<>=!~@"); end >= 0 {
		name = name[:end]
	}
	return name
}

// topLevelModules returns the importable top-level modules an installed
// distribution provides (sklearn for scikit-learn), from its top_level.txt
// or else its RECORD, leaving out private ones (_foo) unless that's all.
func topLevelModules(distInfo string) []string {
	var names []string
	add := func(name string) {
		if isIdentifier(name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	if b, err := os.ReadFile(filepath.Join(distInfo, "top_level.txt")); err == nil {
		for line := range strings.Lines(string(b)) {
			add(strings.TrimSpace(strings.ReplaceAll(line, "/", ".")))
		}
	} else if b, err := os.ReadFile(filepath.Join(distInfo, "RECORD")); err == nil {
		for line := range strings.Lines(string(b)) {
			file, _, _ := strings.Cut(strings.TrimSpace(line), ",")
			first, rest, nested := strings.Cut(file, "/")
			switch {
			case nested && rest == "__init__.py":
				add(first) // a package
			case !nested && strings.HasSuffix(first, ".py"):
				add(strings.TrimSuffix(first, ".py")) // a module
			}
		}
	}
	public := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return strings.HasPrefix(n, "_") })
	if len(public) > 0 {
		return public
	}
	return names
}

func isIdentifier(s string) bool {
	if s == "" || strings.Contains(s, ".") {
		return false
	}
	for i, r := range s {
		letter := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}
