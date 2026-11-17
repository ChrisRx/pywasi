package pywasi

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WithProject installs the dependencies of a uv project (the directory
// with its pyproject.toml), as `uv run` would: those locked in its uv.lock,
// which uv creates if there is none, as it does. The project's own code
// is importable too, from its src directory if it has one, else from the
// project directory. As with WithPylock, each dependency must be pure
// Python or a package in the catalog (see WithPackages), and the project's
// code is used as it is, so it can't have C extensions of its own.
//
// uv comes from PATH, or is downloaded once (as for WithPackages).
func WithProject(dir string) Option {
	return func(c *config) { c.project = dir }
}

// exportProject is a uv project's locked dependencies as a pylock.toml,
// and the directory its code is imported from.
func exportProject(ctx context.Context, dir string) (lock []byte, src string, err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err != nil {
		return nil, "", fmt.Errorf("pywasi: %s isn't a project: no pyproject.toml", dir)
	}
	uv, err := findUV(ctx)
	if err != nil {
		return nil, "", err
	}
	tmp, err := os.MkdirTemp("", "pywasi-project-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "pylock.toml")
	args := []string{"export", "--quiet", "--project", dir, "--format", "pylock.toml", "--no-emit-project", "--output-file", out}
	if _, err := os.Stat(filepath.Join(dir, "uv.lock")); err == nil {
		args = append(args, "--frozen")
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, uv, args...)
	cmd.Stderr = &stderr
	cmd.Env = os.Environ()
	if os.Getenv("UV_PYTHON_INSTALL_DIR") == "" {
		data, err := dataDir()
		if err != nil {
			return nil, "", err
		}
		cmd.Env = append(cmd.Env, "UV_PYTHON_INSTALL_DIR="+filepath.Join(data, "uv", "python"))
	}
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("pywasi: uv export: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if lock, err = os.ReadFile(out); err != nil {
		return nil, "", err
	}
	src = dir
	if fi, err := os.Stat(filepath.Join(dir, "src")); err == nil && fi.IsDir() {
		src = filepath.Join(dir, "src")
	}
	return lock, src, nil
}
