package pywasi

import "testing"

func TestShortVersion(t *testing.T) {
	for in, want := range map[string]string{
		"v0.4.0":                                     "v0.4.0",
		"v0.0.0-20261116205937-3f122248aff2":         "3f12224",
		"v0.4.1-0.20261116205937-3f122248aff2":       "3f12224",
		"v0.4.1-0.20261116205937-3f122248aff2+dirty": "3f12224+dirty",
		"(devel)": "devel",
		"":        "devel",
	} {
		if got := shortVersion(in); got != want {
			t.Errorf("shortVersion(%q) = %q, want %q", in, got, want)
		}
	}
	if Version() == "" {
		t.Error("no version")
	}
}
