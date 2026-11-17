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
		"numpy>=1.26", "python-dateutil>=2.8.2", "tzdata ; sys_platform == 'win32'",
		"pytest ; extra == 'test'", "numpy[extra]",
	})
	if strings.Join(got, ",") != "numpy,python-dateutil,tzdata" {
		t.Errorf("got %v", got)
	}
}
