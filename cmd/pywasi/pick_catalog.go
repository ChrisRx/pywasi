package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The catalog's JSON (see linker.go in pywasi), as far as the picker needs.
type catalog struct {
	Kits []struct {
		ABI    string `json:"abi"`
		Python string `json:"python"`
		Host   int    `json:"host"`
	} `json:"kits"`
	Bundles []struct {
		URL      string   `json:"url"`
		SHA256   string   `json:"sha256"`
		Name     string   `json:"name"`
		Version  string   `json:"version"`
		ABI      string   `json:"abi"`
		Requires []string `json:"requires"`
	} `json:"bundles"`
}

func fetchCatalog(ctx context.Context, url string) (*catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: %s", url, resp.Status)
	}
	var c catalog
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	return &c, nil
}

type kind int

const (
	bundle kind = iota // built from source, linked into the interpreter
	pure               // a wheel from PyPI, installed as is
	custom             // a requirement typed in
)

type item struct {
	name, version, desc string
	kind                kind
	requires            []string
	cached              bool
	selected            bool
}

// requirement is what goes to pywasi.WithPackages.
func (it item) requirement() string {
	if it.kind == bundle {
		return it.name + "==" + it.version
	}
	return it.name
}

// items lists the bundles pywasi would link (those of the newest kit of
// the highest host revision, as it picks), then pure-Python suggestions.
func items(c *catalog) (list []item, python string) {
	var abi string
	best := -1
	for _, k := range c.Kits {
		if k.Host >= best {
			best, abi, python = k.Host, k.ABI, k.Python
		}
	}
	cache := bundleCache()
	seen := map[string]bool{}
	for _, b := range c.Bundles {
		if b.ABI != abi || seen[b.Name] {
			continue
		}
		seen[b.Name] = true
		_, err := os.Stat(filepath.Join(cache, b.SHA256))
		list = append(list, item{
			name: b.Name, version: b.Version, desc: descriptions[b.Name], kind: bundle,
			requires: dependencies(b.Requires), cached: err == nil,
		})
	}
	slices.SortFunc(list, func(a, b item) int { return strings.Compare(a.name, b.name) })
	for _, p := range suggestions {
		if !seen[p.name] {
			list = append(list, item{name: p.name, desc: p.desc, kind: pure})
		}
	}
	return list, python
}

// bundleCache is where pywasi keeps downloaded bundles, by sha256.
func bundleCache() string {
	if dir := os.Getenv("PYWASI_CACHE"); dir != "" {
		return filepath.Join(dir, "bundles")
	}
	dir, _ := os.UserCacheDir()
	return filepath.Join(dir, "pywasi", "bundles")
}

var descriptions = map[string]string{
	"cffi":          "C foreign function interface",
	"contourpy":     "contour lines and filled contours (matplotlib)",
	"cryptography":  "cryptographic recipes and primitives",
	"duckdb":        "in-process analytical SQL database",
	"jiter":         "fast JSON parser (pydantic, openai)",
	"kiwisolver":    "constraint solver (matplotlib)",
	"lxml":          "XML and HTML with libxml2",
	"markupsafe":    "escaping for HTML (jinja2)",
	"matplotlib":    "plotting, to PNG, SVG and PDF",
	"msgpack":       "MessagePack serialization",
	"numpy":         "arrays and numerical computing",
	"orjson":        "fast JSON",
	"pandas":        "data frames",
	"pillow":        "images",
	"pyarrow":       "Apache Arrow: columnar data, Parquet, compute",
	"pydantic-core": "pydantic's validation core",
	"pyyaml":        "YAML",
	"regex":         "regular expressions, beyond re",
	"rpds-py":       "persistent data structures (jsonschema)",
	"safetensors":   "tensor files for ML models",
	"scikit-learn":  "machine learning",
	"scipy":         "scientific computing",
	"sentencepiece": "subword tokenizer",
	"tiktoken":      "OpenAI's BPE tokenizer",
	"tokenizers":    "Hugging Face tokenizers",
	"xxhash":        "fast hashing",
}

// suggestions are pure-Python packages worth a session, installed from
// PyPI (pywasi resolves their dependencies, linking any bundles they need).
var suggestions = []struct{ name, desc string }{
	{"anthropic", "Anthropic's API SDK"},
	{"beautifulsoup4", "HTML scraping"},
	{"fastapi", "web APIs (with pydantic)"},
	{"httpx", "HTTP client"},
	{"jinja2", "templates"},
	{"networkx", "graphs"},
	{"openai", "OpenAI's API SDK"},
	{"pydantic", "data validation"},
	{"requests", "HTTP client"},
	{"rich", "rich text in the terminal"},
	{"sympy", "symbolic math"},
	{"tabulate", "text tables"},
}
