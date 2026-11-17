package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var nameSeparators = regexp.MustCompile(`[-_.]+`)

// normalize is a package name as PEP 503 compares them.
func normalize(name string) string {
	return strings.ToLower(nameSeparators.ReplaceAllString(name, "-"))
}

// notOnWASI matches environment markers that are false on WASI: other
// platforms, or extras (optional dependencies).
var notOnWASI = regexp.MustCompile(`extra\s*==|(sys_platform|platform_system|os_name)\s*==\s*["'](win32|cygwin|darwin|linux|Windows|Darwin|Linux|nt)["']|platform_python_implementation\s*==\s*["']PyPy["']`)

// dependencies are the names a package's Requires-Dist needs on WASI,
// normalized.
func dependencies(requires []string) []string {
	var out []string
	for _, r := range requires {
		if _, marker, ok := strings.Cut(r, ";"); ok && notOnWASI.MatchString(marker) {
			continue
		}
		name := strings.FieldsFunc(r, func(c rune) bool { return strings.ContainsRune(" ;<>=!~[(@", c) })
		if len(name) > 0 && !slices.Contains(out, normalize(name[0])) {
			out = append(out, normalize(name[0]))
		}
	}
	return out
}

// depsMsg is a package's dependencies, fetched from PyPI.
type depsMsg struct {
	name string
	deps []string
}

// pypiDependencies fetches what the latest release of a package (a
// requirement, as typed: its name is all that's looked up) needs, from
// PyPI's JSON API. A package it can't find needs nothing, as far as the
// picker shows.
func pypiDependencies(name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		url := fmt.Sprintf("https://pypi.org/pypi/%s/json", name)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return depsMsg{name: name}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return depsMsg{name: name}
		}
		defer resp.Body.Close()
		var meta struct {
			Info struct {
				RequiresDist []string `json:"requires_dist"`
			} `json:"info"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&meta) != nil {
			return depsMsg{name: name}
		}
		return depsMsg{name: name, deps: dependencies(meta.Info.RequiresDist)}
	}
}

// requirementName is the package name in a requirement as typed (rich,
// rich==14.2.0, rich[jupyter]>=14), normalized.
func requirementName(req string) string {
	if f := strings.FieldsFunc(req, func(c rune) bool { return strings.ContainsRune(" ;<>=!~[(@", c) }); len(f) > 0 {
		return normalize(f[0])
	}
	return normalize(req)
}

// implied walks the selection's dependencies: for each package it pulls in
// (not itself selected), the selected packages that need it. Packages
// whose dependencies aren't known yet, and not being fetched, are returned
// to fetch.
func implied(selected []string, known map[string][]string, fetching map[string]bool) (by map[string][]string, missing []string) {
	by = map[string][]string{}
	for _, root := range selected {
		seen := map[string]bool{root: true}
		queue := []string{root}
		for len(queue) > 0 {
			name := queue[0]
			queue = queue[1:]
			deps, ok := known[name]
			if !ok {
				if !fetching[name] && !slices.Contains(missing, name) {
					missing = append(missing, name)
				}
				continue
			}
			for _, d := range deps {
				if seen[d] {
					continue
				}
				seen[d] = true
				queue = append(queue, d)
				if !slices.Contains(selected, d) && !slices.Contains(by[d], root) {
					by[d] = append(by[d], root)
				}
			}
		}
	}
	return by, missing
}
