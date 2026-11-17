package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// readRequirements reads the requirements in a requirements file, as pip
// writes and reads them: one per line, with comments, line continuations,
// --hash options (from pip-compile and uv pip compile) and nested -r files.
// Options pywasi has no use for (index URLs, say) are skipped, each with a
// note; editable installs (-e) are an error, as are constraints files.
func readRequirements(file string) (reqs, notes []string, err error) {
	reqs, err = readRequirementsSeen(file, &notes, map[string]bool{})
	return reqs, notes, err
}

func readRequirementsSeen(file string, notes *[]string, seen map[string]bool) ([]string, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	if seen[abs] {
		return nil, nil
	}
	seen[abs] = true
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\\\n", " ")
	var reqs []string
	for n, line := range strings.Split(text, "\n") {
		// A comment starts a line or follows whitespace (a # in a URL
		// fragment doesn't).
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		where := fmt.Sprintf("%s:%d", file, n+1)
		if opt, value, ok := option(fields); ok {
			switch opt {
			case "-r", "--requirement":
				nested := value
				if !filepath.IsAbs(nested) {
					nested = filepath.Join(filepath.Dir(file), nested)
				}
				more, err := readRequirementsSeen(nested, notes, seen)
				if err != nil {
					return nil, err
				}
				reqs = append(reqs, more...)
			case "-e", "--editable":
				return nil, fmt.Errorf("%s: editable installs (-e) aren't supported; use --project for a uv project", where)
			case "-c", "--constraint":
				return nil, fmt.Errorf("%s: constraints files (-c) aren't supported", where)
			default:
				*notes = append(*notes, fmt.Sprintf("%s: ignoring %s", where, opt))
			}
			continue
		}
		// The requirement, without per-requirement options (--hash).
		var req []string
		for _, f := range fields {
			if strings.HasPrefix(f, "--") {
				break
			}
			req = append(req, f)
		}
		if len(req) > 0 {
			reqs = append(reqs, strings.Join(req, " "))
		}
	}
	return reqs, nil
}

// option is a line's option (-r file, --requirement=file, --index-url
// URL), if it's one.
func option(fields []string) (opt, value string, ok bool) {
	f := fields[0]
	if !strings.HasPrefix(f, "-") {
		return "", "", false
	}
	if name, v, found := strings.Cut(f, "="); found {
		return name, v, true
	}
	if len(f) > 2 && f[1] != '-' {
		return f[:2], f[2:], true // -rfile
	}
	if len(fields) > 1 {
		return f, fields[1], true
	}
	return f, "", true
}
