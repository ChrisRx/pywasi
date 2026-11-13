package pywasi

import (
	"strings"
	"testing"
)

func TestDedent(t *testing.T) {
	long := strings.Repeat("x", 1<<20)
	for _, tt := range []struct{ in, want string }{
		// Already flush source is left alone.
		{"def f():\n    return 1\nf()", "def f():\n    return 1\nf()\n"},
		{"\n\tdef f():\n\t\treturn 1\n\n\tf()\n", "def f():\n\treturn 1\n\nf()\n"},
		{"\n\t\tif x:\n\t\t    y\n\t", "if x:\n    y\n"},
		// The common indent, not the first line's.
		{"\n    a\n  b\n", "  a\nb\n"},
		// Tabs and spaces don't match.
		{"\n\ta\n    b\n", "\ta\n    b\n"},
		{"\r\n\ta\r\n\t  b\r\n", "a\n  b\n"},
		{"\n\ta\n   \n\tb\n", "a\n\nb\n"},
		{"", ""},
		// No line length limit.
		{"\n\tdata = '" + long + "'\n\tprint(1)\n", "data = '" + long + "'\nprint(1)\n"},
	} {
		if got := dedent(tt.in); got != tt.want {
			t.Errorf("dedent(%.40q) = %.40q, want %.40q", tt.in, got, tt.want)
		}
	}
}
