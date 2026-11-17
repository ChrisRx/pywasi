package pywasi

import (
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
)

const modulePath = "go.chrisrx.dev/pywasi"

// Version is the version of pywasi in this program, from its build info:
// a release tag (v0.4.0), or the short commit hash for a commit between
// them, "+dirty" if built from a modified checkout; "devel" if there is no
// telling (a replace directive pointing at a checkout, or no build info).
// Runs get it as $PYWASI_VERSION, and the REPL shows it in its banner.
var Version = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	if info.Main.Path == modulePath {
		settings := map[string]string{}
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
		v := shortVersion(info.Main.Version)
		if v == "devel" && settings["vcs.revision"] != "" {
			v = settings["vcs.revision"][:min(7, len(settings["vcs.revision"]))]
			if settings["vcs.modified"] == "true" {
				v += "+dirty"
			}
		}
		return v
	}
	for _, dep := range info.Deps {
		if dep.Path == modulePath {
			if dep.Replace != nil {
				return shortVersion(dep.Replace.Version)
			}
			return shortVersion(dep.Version)
		}
	}
	return "devel"
})

// pseudoVersion matches a Go pseudo-version's timestamp and commit.
var pseudoVersion = regexp.MustCompile(`[-.]\d{14}-([0-9a-f]{12})(\+dirty)?$`)

// shortVersion makes a module version readable: a tag stays as is, a
// pseudo-version becomes its commit's short hash.
func shortVersion(v string) string {
	switch {
	case v == "" || v == "(devel)":
		return "devel"
	case pseudoVersion.MatchString(v):
		m := pseudoVersion.FindStringSubmatch(v)
		return m[1][:7] + m[2]
	default:
		return strings.TrimSuffix(v, "+incompatible")
	}
}
