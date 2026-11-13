package pywasi

import "strings"

// dedent removes the longest common leading whitespace from the non-blank
// lines of Python source, like textwrap.dedent, so code in a raw string
// literal can be indented along with the Go around it. Indentation is
// compared byte for byte, so tabs and spaces never match each other.
//
// A blank first or last line is dropped (the lines holding the backquotes).
// Whitespace-only lines become empty, CRLF becomes LF, and every line ends
// with a newline. Source that is already flush is unchanged apart from a
// trailing newline.
func dedent(source string) string {
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	if len(lines) > 0 && isBlank(lines[0]) {
		lines = lines[1:]
	}
	if len(lines) > 0 && isBlank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}

	var prefix string
	found := false
	for _, line := range lines {
		if isBlank(line) {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if !found {
			prefix, found = indent, true
			continue
		}
		prefix = commonPrefix(prefix, indent)
		if prefix == "" {
			break
		}
	}

	var b strings.Builder
	b.Grow(len(source))
	for _, line := range lines {
		if !isBlank(line) {
			b.WriteString(line[len(prefix):])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func isBlank(line string) bool {
	return strings.TrimLeft(line, " \t") == ""
}

func commonPrefix(a, b string) string {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}
