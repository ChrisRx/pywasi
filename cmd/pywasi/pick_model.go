package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"go.chrisrx.dev/pywasi"
)

type state int

const (
	picking state = iota
	filtering
	adding
	linking
	failed
)

var (
	accent    = lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#B4A2FF"}
	green     = lipgloss.AdaptiveColor{Light: "#1E7A3C", Dark: "#7EE2A0"}
	muted     = lipgloss.AdaptiveColor{Light: "#6B6B76", Dark: "#8A8A96"}
	red       = lipgloss.AdaptiveColor{Light: "#B3261E", Dark: "#FF8A80"}
	title     = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dim       = lipgloss.NewStyle().Foreground(muted)
	section   = lipgloss.NewStyle().Bold(true).MarginTop(1)
	current   = lipgloss.NewStyle().Bold(true)
	checked   = lipgloss.NewStyle().Foreground(accent)
	comesWith = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#A8A0CC", Dark: "#5E5679"})
	cachedOK  = lipgloss.NewStyle().Foreground(green)
	errStyle  = lipgloss.NewStyle().Foreground(red)
	key       = lipgloss.NewStyle().Foreground(accent)
)

type logMsg string

type linkedMsg struct {
	py  *pywasi.Python
	err error
}

type model struct {
	items   []item
	shown   []int // indices of items matching the filter
	cursor  int   // in shown
	python  string
	catalog string

	state   state
	input   textinput.Model
	spinner spinner.Model
	width   int
	height  int

	// known are packages' dependencies (normalized names): bundles' from
	// the catalog, others' fetched from PyPI as the selection needs them.
	known    map[string][]string
	fetching map[string]bool
	// by is, for each package the selection pulls in, what needs it.
	by map[string][]string

	send    func(tea.Msg)
	link    func(reqs []string, log io.Writer) (*pywasi.Python, error)
	started time.Time
	logs    []string
	err     error
	saveErr error
	py      *pywasi.Python
}

func newModel(list []item, python, catalogURL string) *model {
	in := textinput.New()
	in.Prompt = ""
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(accent)
	m := &model{
		items: list, python: python, catalog: catalogURL, input: in, spinner: sp,
		known: map[string][]string{}, fetching: map[string]bool{},
	}
	for _, it := range list {
		if it.kind == bundle {
			m.known[normalize(it.name)] = it.requires
		}
	}
	m.refilter()
	return m
}

func (m *model) Init() tea.Cmd { return m.refreshDeps() }

// refreshDeps works out what the selection pulls in, fetching the
// dependencies of packages that aren't in the catalog from PyPI.
func (m *model) refreshDeps() tea.Cmd {
	var selected []string
	for _, it := range m.selected() {
		selected = append(selected, requirementName(it.name))
	}
	by, missing := implied(selected, m.known, m.fetching)
	m.by = by
	var cmds []tea.Cmd
	for _, name := range missing {
		m.fetching[name] = true
		cmds = append(cmds, pypiDependencies(name))
	}
	return tea.Batch(cmds...)
}

// neededBy is what in the selection needs it, if it isn't selected itself.
func (m *model) neededBy(it item) []string {
	if it.selected {
		return nil
	}
	return m.by[requirementName(it.name)]
}

func (m *model) selected() []item {
	var out []item
	for _, it := range m.items {
		if it.selected {
			out = append(out, it)
		}
	}
	return out
}

// refilter shows the items whose name or description contain the filter.
func (m *model) refilter() {
	f := strings.ToLower(strings.TrimSpace(m.input.Value()))
	if m.state != filtering && m.state != picking {
		f = ""
	}
	// The cursor stays on its item if it's still shown.
	at := -1
	if m.cursor < len(m.shown) {
		at = m.shown[m.cursor]
	}
	m.shown = m.shown[:0]
	m.cursor = 0
	for i, it := range m.items {
		if f == "" || strings.Contains(it.name, f) || strings.Contains(strings.ToLower(it.desc), f) {
			if i == at {
				m.cursor = len(m.shown)
			}
			m.shown = append(m.shown, i)
		}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case spinner.TickMsg:
		if m.state != linking {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case logMsg:
		m.logs = append(m.logs, string(msg))
		return m, nil
	case depsMsg:
		m.known[msg.name] = msg.deps
		delete(m.fetching, msg.name)
		return m, m.refreshDeps()
	case linkedMsg:
		if msg.err != nil {
			m.state, m.err = failed, msg.err
			return m, nil
		}
		m.py = msg.py
		return m, tea.Quit
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case filtering, adding:
		switch msg.String() {
		case "esc":
			if m.state == filtering {
				m.input.SetValue("")
			}
			m.state = picking
			m.input.Blur()
			m.refilter()
			return m, nil
		case "enter":
			if m.state == adding {
				if req := strings.TrimSpace(m.input.Value()); req != "" {
					m.items = append(m.items, item{name: req, desc: "added", kind: custom, selected: true})
				}
				m.input.SetValue("")
			}
			m.state = picking
			m.input.Blur()
			m.refilter()
			return m, m.refreshDeps()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.state == filtering {
			m.refilter()
		}
		return m, cmd
	case linking:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	case failed:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		default:
			m.state, m.err, m.logs = picking, nil, nil
			return m, nil
		}
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.shown)-1, 0))
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = max(len(m.shown)-1, 0)
	case " ", "x":
		if len(m.shown) > 0 {
			it := &m.items[m.shown[m.cursor]]
			it.selected = !it.selected
		}
		return m, m.refreshDeps()
	case "/":
		m.state = filtering
		m.input.Placeholder = "filter"
		m.input.SetValue("")
		return m, m.input.Focus()
	case "+", "a":
		m.state = adding
		m.input.Placeholder = "requirement, e.g. polars or rich==14.2.0"
		m.input.SetValue("")
		return m, m.input.Focus()
	case "c":
		for i := range m.items {
			m.items[i].selected = false
		}
		return m, m.refreshDeps()
	case "enter":
		return m, m.start()
	}
	return m, nil
}

// start links the interpreter for the selected packages (none is fine:
// the standard library alone).
func (m *model) start() tea.Cmd {
	var reqs []string
	for _, it := range m.selected() {
		reqs = append(reqs, it.requirement())
	}
	m.saveErr = saveSelection(m.selected())
	m.state, m.started, m.logs = linking, time.Now(), nil
	send, link := m.send, m.link
	build := func() tea.Msg {
		py, err := link(reqs, &lineWriter{send: send})
		return linkedMsg{py, err}
	}
	return tea.Batch(m.spinner.Tick, build)
}

func (m *model) View() string {
	var b strings.Builder
	n := len(m.selected())
	b.WriteString(title.Render("pywasi") + dim.Render("  ·  Python "+m.python+" in WebAssembly") + "\n")
	b.WriteString(dim.Render(fmt.Sprintf("%d packages from %s", len(m.items), m.catalog)) + "\n")

	if m.state == linking || m.state == failed {
		return b.String() + m.linkingView(n)
	}

	// The list, scrolled to keep the cursor in view.
	rows := max(m.height-12, 5)
	start := 0
	if m.cursor >= rows {
		start = m.cursor - rows + 1
	}
	last := kind(-1)
	for i := start; i < min(start+rows, len(m.shown)); i++ {
		it := m.items[m.shown[i]]
		if it.kind != last {
			b.WriteString(section.Render(map[kind]string{
				bundle: "Built for WASI",
				pure:   "Pure Python, from PyPI",
				custom: "Added",
			}[it.kind]) + "\n")
			last = it.kind
		}
		b.WriteString(m.row(it, i == m.cursor) + "\n")
	}
	if len(m.shown) == 0 {
		b.WriteString(dim.Render("\n  nothing matches") + "\n")
	}

	// What the cursor is on.
	b.WriteString("\n")
	if len(m.shown) > 0 {
		it := m.items[m.shown[m.cursor]]
		info := it.desc
		if by := m.neededBy(it); len(by) > 0 {
			info += " · comes with " + strings.Join(by, ", ")
		}
		if deps := m.known[requirementName(it.name)]; len(deps) > 0 {
			info += " · needs " + strings.Join(deps, ", ")
		}
		if it.cached {
			info += " · downloaded"
		}
		b.WriteString(dim.Render("  "+info) + "\n")
	}

	switch m.state {
	case filtering:
		b.WriteString(key.Render("  / ") + m.input.View() + dim.Render("   enter keep · esc clear"))
	case adding:
		b.WriteString(key.Render("  + ") + m.input.View() + dim.Render("   enter add · esc cancel"))
	default:
		count := fmt.Sprint(n)
		if d := len(m.by); d > 0 {
			count += fmt.Sprintf(" + %d deps", d)
		}
		b.WriteString(help("space", "select") + help("/", "filter") + help("+", "add") + help("c", "clear") +
			help("enter", "start ("+count+")") + help("q", "quit"))
	}
	return b.String()
}

func help(k, what string) string { return "  " + key.Render(k) + " " + dim.Render(what) }

func (m *model) row(it item, here bool) string {
	cursor, box := "  ", "[ ]"
	if here {
		cursor = key.Render("▸ ")
	}
	switch {
	case it.selected:
		box = checked.Render("[✓]")
	case len(m.neededBy(it)) > 0:
		box = comesWith.Render("[✓]")
	}
	name := fmt.Sprintf("%-16s", it.name)
	if here {
		name = current.Render(name)
	}
	mark := " "
	if it.cached {
		mark = cachedOK.Render("●")
	}
	version := fmt.Sprintf("%-11s", it.version)
	desc := it.desc
	if w := m.width - 40; w > 10 && len(desc) > w {
		desc = desc[:w-1] + "…"
	}
	return fmt.Sprintf("%s%s %s %s %s %s", cursor, box, name, dim.Render(version), mark, dim.Render(desc))
}

func (m *model) linkingView(n int) string {
	var b strings.Builder
	b.WriteString("\n")
	what := "the standard library"
	if n > 0 {
		var names []string
		for _, it := range m.selected() {
			names = append(names, it.name)
		}
		what = strings.Join(names, ", ")
	}
	if m.state == failed {
		b.WriteString(errStyle.Render("  Couldn't start: ") + m.err.Error() + "\n\n")
		b.WriteString(help("any key", "back") + help("q", "quit"))
		return b.String()
	}
	elapsed := time.Since(m.started).Truncate(100 * time.Millisecond)
	fmt.Fprintf(&b, "  %s Linking an interpreter with %s  %s\n\n", m.spinner.View(), what, dim.Render(elapsed.String()))
	b.WriteString(dim.Render("  The first time downloads, links and snapshots it (about a minute for a\n  big set); after that it's cached.") + "\n\n")
	for _, l := range m.logs[max(len(m.logs)-6, 0):] {
		if w := m.width - 6; w > 10 && len(l) > w {
			l = l[:w-1] + "…"
		}
		b.WriteString(dim.Render("  │ "+l) + "\n")
	}
	return b.String()
}

// lineWriter sends what's written to it to the program, a line at a time.
type lineWriter struct {
	send func(tea.Msg)
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			return len(p), nil
		}
		if line := strings.TrimSpace(string(w.buf[:i])); line != "" && w.send != nil {
			w.send(logMsg(line))
		}
		w.buf = w.buf[i+1:]
	}
}

var _ io.Writer = (*lineWriter)(nil)
