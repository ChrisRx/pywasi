package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testModel() *model {
	return newModel([]item{
		{name: "numpy", version: "2.5.5", desc: "arrays", kind: bundle},
		{name: "pandas", version: "3.1.1", desc: "data frames", kind: bundle},
		{name: "httpx", desc: "HTTP client", kind: pure},
	}, "3.14.10", "example.com/index.json")
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case " ":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m.Update(msg)
	}
}

func names(items []item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.name)
	}
	return out
}

func TestFilterAndSelect(t *testing.T) {
	m := testModel()
	press(m, "/", "p", "a", "n", "enter", " ")
	if got := names(m.selected()); strings.Join(got, ",") != "pandas" {
		t.Fatalf("selected %v, want pandas", got)
	}
	// Down past the filtered list stays on its last item; clearing the
	// filter shows everything again, the cursor still on pandas.
	press(m, "j", "j", "/", "esc", "k", " ")
	if got := names(m.selected()); strings.Join(got, ",") != "numpy,pandas" {
		t.Fatalf("selected %v, want numpy,pandas", got)
	}
	if v := m.View(); !strings.Contains(v, "start (2)") || !strings.Contains(v, "Pure Python") {
		t.Errorf("view:\n%s", v)
	}
}

func TestAddRequirement(t *testing.T) {
	m := testModel()
	press(m, "+", "r", "i", "c", "h", "enter")
	got := m.selected()
	if len(got) != 1 || got[0].requirement() != "rich" || got[0].kind != custom {
		t.Fatalf("selected %+v", got)
	}
	bundled := item{name: "numpy", version: "2.5.5", kind: bundle}
	if r := bundled.requirement(); r != "numpy==2.5.5" {
		t.Errorf("bundle requirement %q", r)
	}
}

func TestDependencies(t *testing.T) {
	got := dependencies([]string{
		"numpy>=1.26", "python_dateutil>=2.8.2", "tzdata ; sys_platform == 'win32'",
		"pytest ; extra == 'test'", "numpy[extra]", "typing-extensions ; python_version < '3.15'",
		"uvloop ; platform_python_implementation == 'PyPy'", "Jinja2 @ https://example.com/j.whl",
	})
	if strings.Join(got, ",") != "numpy,python-dateutil,typing-extensions,jinja2" {
		t.Errorf("got %v", got)
	}
}

func TestImplied(t *testing.T) {
	known := map[string][]string{
		"pandas":          {"numpy", "python-dateutil"},
		"python-dateutil": {"six"},
		"six":             nil,
		"numpy":           nil,
		"scikit-learn":    {"numpy", "scipy"},
	}
	by, missing := implied([]string{"pandas", "scikit-learn", "numpy"}, known, map[string]bool{})
	if strings.Join(by["python-dateutil"], ",") != "pandas" || strings.Join(by["six"], ",") != "pandas" {
		t.Errorf("by %v", by)
	}
	if _, ok := by["numpy"]; ok {
		t.Errorf("numpy is selected, not implied: %v", by)
	}
	if strings.Join(missing, ",") != "scipy" {
		t.Errorf("missing %v", missing)
	}
}

// Packages the selection pulls in get a lighter mark, and are counted.
func TestImpliedMarks(t *testing.T) {
	m := newModel([]item{
		{name: "numpy", version: "2.5.5", desc: "arrays", kind: bundle},
		{name: "pandas", version: "3.1.1", desc: "data frames", kind: bundle, requires: []string{"numpy", "python-dateutil"}},
		{name: "fastapi", desc: "web APIs", kind: pure},
		{name: "pydantic-core", version: "2.50.4", desc: "core", kind: bundle},
	}, "3.14.10", "example.com/index.json")
	press(m, "j", " ") // pandas
	m.Update(depsMsg{name: "python-dateutil", deps: []string{"six"}})
	m.Update(depsMsg{name: "six"})
	if got := strings.Join(m.neededBy(m.items[0]), ","); got != "pandas" {
		t.Errorf("numpy needed by %q", got)
	}
	press(m, "j", " ") // fastapi
	m.Update(depsMsg{name: "fastapi", deps: []string{"pydantic", "starlette"}})
	m.Update(depsMsg{name: "pydantic", deps: []string{"pydantic-core"}})
	if got := strings.Join(m.neededBy(m.items[3]), ","); got != "fastapi" {
		t.Errorf("pydantic-core needed by %q", got)
	}
	if v := m.View(); !strings.Contains(v, "start (2 + 6 deps)") {
		t.Errorf("view:\n%s", v)
	}
	// Selecting a package that came along makes it selected.
	press(m, "g", " ")
	if len(m.neededBy(m.items[0])) != 0 || !m.items[0].selected {
		t.Error("numpy not selected")
	}
}
