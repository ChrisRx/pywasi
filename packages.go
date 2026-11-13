package pywasi

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.chrisrx.dev/x/context"

	"go.chrisrx.dev/pywasi/internal/resolver"
)

// Resolver chooses how WithPackages requirements are resolved to wheels.
type Resolver string

const (
	// ResolverBuiltin resolves inside python.wasm itself, with resolvelib,
	// reading the package index over HTTP from Go. It needs nothing
	// installed. This is the default.
	ResolverBuiltin Resolver = "builtin"
	// ResolverUV resolves with uv: the one on PATH, or else a pinned
	// release downloaded (and checked) on first use. uv also needs a
	// native Python, and downloads one once if the system has none.
	ResolverUV Resolver = "uv"
)

// DefaultIndex is the package index for ResolverBuiltin.
const DefaultIndex = "https://pypi.org/simple"

// WithPackages adds pure-Python packages (PEP 508 requirements such as
// "rich", "tabulate>=0.9") on top of the interpreter, from py3-none-any
// wheels. No compiler or Nix is needed. Packages the interpreter already
// has (e.g. numpy in a numpy build) satisfy requirements as they are, and a
// package with C extensions and no pure wheel is an error.
//
// Requirements are resolved once per interpreter and requirement set, then
// cached, so unpinned names stay on the versions they first resolved to.
// Wheels are downloaded once, checked against their sha256 and unpacked
// into a cached directory mounted read-only on sys.path. Repeated calls add
// requirements.
func WithPackages(requirements ...string) Option {
	return func(c *config) { c.packages = append(c.packages, requirements...) }
}

// WithPylock installs the packages of a PEP 751 lock file (pylock.toml)
// that apply to the interpreter, e.g. from
//
//	uv export --format pylock.toml
//	uv pip compile --universal --format pylock.toml requirements.in
//
// Locked wheels are used exactly, without resolving. As with WithPackages,
// each must be pure Python or built into the interpreter.
func WithPylock(data []byte) Option {
	return func(c *config) { c.pylock = data }
}

// WithResolver picks how WithPackages is resolved (default
// ResolverBuiltin).
func WithResolver(r Resolver) Option {
	return func(c *config) { c.resolver = r }
}

// WithIndex sets the package index (PEP 503/691 "simple" API) for
// ResolverBuiltin. Defaults to DefaultIndex.
func WithIndex(url string) Option {
	return func(c *config) { c.index = url }
}

// wheel is one resolved distribution.
type wheel struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	// Bundle is set for a prebuilt package from the catalog (no wheel).
	Bundle bool `json:"bundle,omitempty"`
}

// layers is what packageLayer resolved: a site directory of pure wheels,
// and bundles (packages with C extensions) to link with kit.
type layers struct {
	Wheels  []wheel       `json:"wheels"`
	Bundles []bundleEntry `json:"bundles,omitempty"`
	Kit     *kitEntry     `json:"kit,omitempty"`
	Linker  *artifact     `json:"linker,omitempty"`
}

// packageLayer resolves opts' packages and installs the pure ones,
// returning their site directory ("" for none) and any bundles to link.
func (p *Python) packageLayer(ctx context.Context, opts config) (string, layers, error) {
	var l layers
	if len(opts.packages) == 0 && opts.pylock == nil {
		return "", l, nil
	}
	if len(opts.packages) > 0 && opts.pylock != nil {
		return "", l, errors.New("pywasi: use WithPackages or WithPylock, not both")
	}
	data, err := dataDir()
	if err != nil {
		return "", l, err
	}
	resolverKind := cmpOr(opts.resolver, ResolverBuiltin)
	index := cmpOr(opts.index, DefaultIndex)
	catalogURL := DefaultCatalog
	if opts.catalogSet {
		catalogURL = opts.catalog
	}

	reqs := slices.Clone(opts.packages)
	slices.Sort(reqs)
	reqs = slices.Compact(reqs)
	key := digestJSON([]any{"v2", p.digest, reqs, string(opts.pylock), resolverKind, index, catalogURL})
	cached := filepath.Join(data, "resolutions", key+".json")

	if b, err := os.ReadFile(cached); err == nil {
		if err := json.Unmarshal(b, &l); err != nil {
			return "", l, fmt.Errorf("pywasi: reading cached resolution %s: %w", cached, err)
		}
	} else {
		// Packages with C extensions that can be linked in. Only for an
		// interpreter without extensions of its own, since a linked one
		// has only the kit's.
		prebuilt := map[string][]map[string]any{}
		var available map[string][]bundleEntry
		var cat *catalog
		if catalogURL != "" && p.extensions() == 0 {
			if cat, err = loadCatalog(ctx, catalogURL); err != nil {
				return "", l, err
			}
			version, err := p.pythonVersion()
			if err != nil {
				return "", l, err
			}
			if kit, bundles, ok := cat.prebuilt(version); ok {
				l.Kit, l.Linker, available = &kit, &cat.Linker, bundles
				for name, entries := range bundles {
					for _, e := range entries {
						prebuilt[name] = append(prebuilt[name], map[string]any{"version": e.Version, "requires": e.Requires})
					}
				}
			}
		}

		var picked []wheel
		switch {
		case opts.pylock != nil:
			picked, err = p.selectLock(ctx, opts.pylock, prebuilt)
		case resolverKind == ResolverBuiltin:
			err = p.resolver(ctx, map[string]any{"mode": "resolve", "requirements": reqs, "index": index, "prebuilt": prebuilt}, &picked)
		case resolverKind == ResolverUV:
			picked, err = p.resolveUV(ctx, reqs, prebuilt)
		default:
			err = fmt.Errorf("pywasi: unknown resolver %q", resolverKind)
		}
		if err != nil {
			return "", l, err
		}
		for _, w := range picked {
			if !w.Bundle {
				l.Wheels = append(l.Wheels, w)
				continue
			}
			i := slices.IndexFunc(available[w.Name], func(b bundleEntry) bool { return b.Version == w.Version })
			if i < 0 {
				return "", l, fmt.Errorf("pywasi: resolved %s %s, which the catalog doesn't have", w.Name, w.Version)
			}
			l.Bundles = append(l.Bundles, available[w.Name][i])
		}
		if len(l.Bundles) == 0 {
			l.Kit, l.Linker = nil, nil
		}
		if err := writeFileAtomic(cached, mustJSON(l)); err != nil {
			return "", l, err
		}
	}
	if len(l.Wheels) == 0 {
		return "", l, nil
	}
	site, err := installWheels(ctx, data, l.Wheels)
	return site, l, err
}

// extensions is how many extension modules the interpreter has built in,
// from its manifest (-1 if unknown).
func (p *Python) extensions() int {
	b, err := os.ReadFile(filepath.Join(p.home, "pywasi.json"))
	if err != nil {
		return -1
	}
	var m struct {
		Extensions []string `json:"extensions"`
	}
	if json.Unmarshal(b, &m) != nil {
		return -1
	}
	return len(m.Extensions)
}

func (p *Python) selectLock(ctx context.Context, pylock []byte, prebuilt any) ([]wheel, error) {
	var wheels []wheel
	err := p.resolver(ctx, map[string]any{"mode": "lock", "pylock": string(pylock), "prebuilt": prebuilt}, &wheels)
	return wheels, err
}

// resolver runs _pywasi_resolve in the interpreter, serving its downloads.
func (p *Python) resolver(ctx context.Context, req, out any) error {
	data, err := dataDir()
	if err != nil {
		return err
	}
	httpDir := filepath.Join(data, "http")
	if err := os.MkdirAll(httpDir, 0o755); err != nil {
		return err
	}
	ch := &fetchChannel{ctx: ctx, dir: httpDir}
	const resolverGuest = guestRoot + "/resolver"
	o := RunOptions{
		Mounts: []Mount{{Host: httpDir, Guest: guestRoot + "/http", ReadOnly: true}},
		Env:    map[string]string{"PYTHONPATH": resolverGuest},
		Stdin:  ch,
		Stdout: ch,
		fsMounts: []fsMount{
			{fsys: resolver.FS(), guest: resolverGuest},
		},
	}
	if err := p.runModule(ctx, "_pywasi_resolve", req, out, o); err != nil {
		var perr *Error
		if errors.As(err, &perr) {
			return fmt.Errorf("pywasi: resolving packages: %s", perr.Message)
		}
		return err
	}
	return nil
}

// fetchChannel serves the resolver's download requests: a JSON line on its
// stdout is answered on its stdin. Python blocks reading the answer, and
// wazero calls Write and Read on the same goroutine, so the answer is
// always ready before it's read.
type fetchChannel struct {
	ctx  context.Context
	dir  string
	line []byte
	in   bytes.Buffer
}

func (c *fetchChannel) Write(b []byte) (int, error) {
	c.line = append(c.line, b...)
	for {
		i := bytes.IndexByte(c.line, '\n')
		if i < 0 {
			return len(b), nil
		}
		reply := c.handle(c.line[:i])
		c.in.Write(append(mustJSON(reply), '\n'))
		c.line = c.line[i+1:]
	}
}

func (c *fetchChannel) Read(b []byte) (int, error) {
	if c.in.Len() == 0 {
		return 0, io.EOF
	}
	return c.in.Read(b)
}

// Index pages change, so they are refetched after this long.
const indexTTL = 10 * time.Minute

func (c *fetchChannel) handle(line []byte) map[string]string {
	var req struct {
		Fetch     string `json:"fetch"`
		Accept    string `json:"accept"`
		Immutable bool   `json:"immutable"`
	}
	if err := json.Unmarshal(line, &req); err != nil || req.Fetch == "" {
		return map[string]string{"error": fmt.Sprintf("bad request %q", line)}
	}
	if !strings.HasPrefix(req.Fetch, "https://") && !strings.HasPrefix(req.Fetch, "http://") {
		return map[string]string{"error": "only http(s) URLs can be fetched"}
	}
	name := digestJSON([]string{req.Fetch, req.Accept})
	file := filepath.Join(c.dir, name)
	if st, err := os.Stat(file); err == nil && (req.Immutable || time.Since(st.ModTime()) < indexTTL) {
		return map[string]string{"path": guestRoot + "/http/" + name}
	}
	body, err := httpGet(c.ctx, req.Fetch, req.Accept)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	defer body.Close()
	b, err := io.ReadAll(body)
	if err == nil {
		err = writeFileAtomic(file, b)
	}
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	return map[string]string{"path": guestRoot + "/http/" + name}
}

// installWheels downloads and unpacks wheels into a cached site directory,
// keyed by their hashes, and returns it.
func installWheels(ctx context.Context, data string, wheels []wheel) (string, error) {
	var ids []string
	for _, w := range wheels {
		ids = append(ids, w.SHA256+" "+w.Filename)
	}
	slices.Sort(ids)
	site := filepath.Join(data, "sites", digestJSON(ids))
	if _, err := os.Stat(site); err == nil {
		return site, nil
	}
	sites := filepath.Dir(site)
	if err := os.MkdirAll(sites, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(sites, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	for _, w := range wheels {
		file, err := downloadWheel(ctx, data, w)
		if err != nil {
			return "", err
		}
		if err := unpackWheel(file, tmp); err != nil {
			return "", fmt.Errorf("pywasi: installing %s: %w", w.Filename, err)
		}
	}
	if err := os.Rename(tmp, site); err != nil && !isDir(site) {
		return "", err
	}
	return site, nil
}

func isDir(name string) bool {
	st, err := os.Stat(name)
	return err == nil && st.IsDir()
}

func downloadWheel(ctx context.Context, data string, w wheel) (string, error) {
	if path.Base(w.Filename) != w.Filename || !strings.HasSuffix(w.Filename, ".whl") {
		return "", fmt.Errorf("pywasi: bad wheel name %q", w.Filename)
	}
	file := filepath.Join(data, "wheels", w.SHA256, w.Filename)
	if _, err := os.Stat(file); err == nil {
		return file, nil
	}
	body, err := httpGet(ctx, w.URL, "")
	if err != nil {
		return "", fmt.Errorf("pywasi: downloading %s: %w", w.Filename, err)
	}
	defer body.Close()
	b, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("pywasi: downloading %s: %w", w.Filename, err)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != w.SHA256 {
		return "", fmt.Errorf("pywasi: %s has sha256 %x, expected %s", w.Filename, sum, w.SHA256)
	}
	return file, writeFileAtomic(file, b)
}

// unpackWheel installs a pure wheel into site: its files, plus
// <name>.data/purelib and platlib. Scripts, headers and data files have
// nowhere to go in the guest and are dropped.
func unpackWheel(file, site string) error {
	z, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, f := range z.File {
		name := path.Clean(f.Name)
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if first, rest, ok := strings.Cut(name, "/"); ok && strings.HasSuffix(first, ".data") {
			scheme, rel, ok := strings.Cut(rest, "/")
			if !ok || (scheme != "purelib" && scheme != "platlib") {
				continue
			}
			name = rel
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("unsafe path %q", f.Name)
		}
		target := filepath.Join(site, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err == nil {
			_, err = io.Copy(out, rc)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
		}
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// resolveUV locks reqs with uv for the interpreter's Python, keeping
// packages the interpreter already has at their versions, then selects
// wheels from that lock.
func (p *Python) resolveUV(ctx context.Context, reqs []string, prebuilt any) ([]wheel, error) {
	var have map[string]string
	if err := p.resolver(ctx, map[string]any{"mode": "installed"}, &have); err != nil {
		return nil, err
	}
	uv, err := findUV(ctx)
	if err != nil {
		return nil, err
	}
	version, err := p.pythonVersion()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "pywasi-uv-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var constraints []string
	for name, v := range have {
		constraints = append(constraints, name+"=="+v)
	}
	slices.Sort(constraints)
	in, cons, lock := filepath.Join(dir, "requirements.in"), filepath.Join(dir, "constraints.txt"), filepath.Join(dir, "pylock.toml")
	if err := os.WriteFile(in, []byte(strings.Join(reqs, "\n")+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cons, []byte(strings.Join(constraints, "\n")+"\n"), 0o600); err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, uv, "pip", "compile", "--quiet", "--no-header", "--universal",
		"--python-version", version, "--format", "pylock.toml", "--constraint", cons, "--output-file", lock, in)
	cmd.Stderr = &stderr
	// uv needs a native Python to lock with (any 3.X, it isn't run for the
	// target). Without one on the system it downloads its own once, kept in
	// pywasi's cache unless uv is configured otherwise.
	cmd.Env = os.Environ()
	if os.Getenv("UV_PYTHON_INSTALL_DIR") == "" {
		data, err := dataDir()
		if err != nil {
			return nil, err
		}
		cmd.Env = append(cmd.Env, "UV_PYTHON_INSTALL_DIR="+filepath.Join(data, "uv", "python"))
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pywasi: uv pip compile: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	b, err := os.ReadFile(lock)
	if err != nil {
		return nil, err
	}
	return p.selectLock(ctx, b, prebuilt)
}

// pythonVersion is the interpreter's "3.X", from its lib/python3.X.
func (p *Python) pythonVersion() (string, error) {
	libs, _ := filepath.Glob(filepath.Join(p.home, "lib", "python3.*"))
	if len(libs) != 1 {
		return "", fmt.Errorf("pywasi: can't tell the Python version of %s", p.home)
	}
	return strings.TrimPrefix(filepath.Base(libs[0]), "python"), nil
}

func digestJSON(v any) string {
	sum := sha256.Sum256(mustJSON(v))
	return hex.EncodeToString(sum[:16])
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func cmpOr[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

func writeFileAtomic(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}
