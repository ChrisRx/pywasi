package pywasi

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestDiffMemory(t *testing.T) {
	before := make([]byte, 300)
	before[10] = 7 // unchanged
	before[50] = 9 // zeroed after
	after := bytes.Clone(before)
	after[50] = 0
	after[52] = 1                  // 2 bytes from the change at 50: merged
	after[200] = 3                 // far: its own range
	after = append(after, 0, 0, 5) // grown, nonzero at the end
	got := diffMemory(before, after)
	want := []imageDiff{
		{50, after[50:53]},
		{200, after[200:201]},
		{302, []byte{5}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := diffMemory(before, bytes.Clone(before)); len(got) != 0 {
		t.Errorf("unchanged: %v", got)
	}
}

func TestImageEncoding(t *testing.T) {
	img := &image{
		pages:   3,
		globals: []imageGlobal{{0, 65536}, {7, 1 << 40}},
		diffs:   []imageDiff{{16, []byte("hello")}, {70000, []byte{1, 2, 3}}},
	}
	var buf bytes.Buffer
	if err := img.encode(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := decodeImage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, img) {
		t.Errorf("got %+v, want %+v", got, img)
	}
	if _, err := decodeImage(bytes.NewReader([]byte("nope"))); err == nil {
		t.Error("garbage decoded")
	}
}

func TestPreloadNames(t *testing.T) {
	site := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(site, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// From RECORD: packages and modules, private ones only if that's all.
	write("scikit_learn-1.9.1.dist-info/RECORD", "sklearn/__init__.py,sha256=x,1\nsklearn/base.py,,\n"+
		"_sklearn_helper.py,,\nscikit_learn-1.9.1.dist-info/METADATA,,\n../../bin/tool,,\n")
	write("typing_extensions-4.16.0.dist-info/RECORD", "typing_extensions.py,,\n")
	write("cffi-2.1.1.dist-info/RECORD", "_cffi_backend.py,,\n")
	// top_level.txt wins.
	write("PyYAML-6.0.3.dist-info/top_level.txt", "_yaml\nyaml\n")
	write("PyYAML-6.0.3.dist-info/RECORD", "other/__init__.py,,\n")
	// Not asked for.
	write("unwanted-1.0.dist-info/RECORD", "unwanted.py,,\n")

	p := &Python{}
	got := p.preloads(config{
		packages: []string{"scikit-learn>=1.5", "typing-extensions", "cffi", "pyyaml; python_version>'3'"},
		preload:  []string{"json"},
	}, site)
	// _pywasi and WithPreload's first, then the packages' in site order.
	want := []string{"_pywasi", "json", "yaml", "_cffi_backend", "sklearn", "typing_extensions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// A lock: everything installed.
	if got := p.preloads(config{pylock: []byte("lock")}, site); !slices.Contains(got, "unwanted") {
		t.Errorf("lock: %v", got)
	}
}

func TestRequirementName(t *testing.T) {
	for in, want := range map[string]string{
		"numpy":                              "numpy",
		"scikit-learn>=1.5":                  "scikit-learn",
		"rich[jupyter] ; python_version>'3'": "rich",
		"pkg @ https://example.com/x.whl":    "pkg",
		" spaced == 1":                       "spaced",
	} {
		if got := requirementName(in); got != want {
			t.Errorf("requirementName(%q) = %q, want %q", in, got, want)
		}
	}
}
