package pywasi

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestPrebuiltKitHostRevision(t *testing.T) {
	kit := func(sha string, host int) kitEntry {
		return kitEntry{artifact: artifact{SHA256: sha}, ABI: sha, Python: "3.14.10", Host: host}
	}
	// As scripts/catalog.py orders them: highest revision first, then
	// oldest first.
	c := &catalog{
		Kits: []kitEntry{
			kit("future", hostRev+1),
			kit("current-old", hostRev),
			kit("current-new", hostRev),
			kit("legacy-old", 0),
			kit("legacy-new", 0),
			kit("other", hostRev),
		},
		Bundles: []bundleEntry{{Name: "numpy", ABI: "current-new"}, {Name: "numpy", ABI: "legacy-new"}},
	}
	c.Kits[5].Python = "3.13.9"
	got, bundles, ok := c.prebuilt("3.14")
	if !ok || got.SHA256 != "current-new" {
		t.Fatalf("picked %q, want current-new", got.SHA256)
	}
	if len(bundles["numpy"]) != 1 {
		t.Fatalf("bundles %v, want the one for current-new", bundles)
	}

	// pywasi before host revisions took the last kit for its Python.
	var last kitEntry
	for _, k := range c.Kits {
		if k.Python == "3.14.10" {
			last = k
		}
	}
	if last.SHA256 != "legacy-new" {
		t.Fatalf("old pywasi would pick %q", last.SHA256)
	}
}

// Libraries (objects modules of a bundle share) go first, before any
// bundle's modules; older bundles have none.
func TestLinkInputs(t *testing.T) {
	var metas []bundleMeta
	for _, b := range []string{
		`{"modules": [{"name": "numpy._core._multiarray_umath", "object": "objects/a.o"}]}`,
		`{"modules": [{"name": "pyarrow.lib", "object": "objects/b.o"}, {"name": "pyarrow._compute", "object": "objects/c.o"}],
		  "libraries": ["objects/lib.o"]}`,
	} {
		var m bundleMeta
		if err := json.Unmarshal([]byte(b), &m); err != nil {
			t.Fatal(err)
		}
		metas = append(metas, m)
	}
	want := []string{
		"/b1/objects/lib.o",
		"/b0/objects/a.o", "/b0/registration.o",
		"/b1/objects/b.o", "/b1/objects/c.o", "/b1/registration.o",
	}
	if got := linkInputs(metas); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
