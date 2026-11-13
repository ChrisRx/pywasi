// Call shows calling Python functions from Go: a library function directly,
// and functions in a module whose source lives in this file.
//
//	go run ./examples/call
//
// The first run builds the interpreter (numpy compiles from source, a few
// minutes). Later runs reuse it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"go.chrisrx.dev/pywasi"
)

// A Python module defined inline, importable as geometry.
const geometry = `
import numpy as np


def fit_line(xs, ys):
    """Least-squares line through the points."""
    A = np.vstack([xs, np.ones(len(xs))]).T
    (slope, intercept), residuals, *_ = np.linalg.lstsq(A, ys, rcond=None)
    return {"slope": slope, "intercept": intercept, "residual": residuals.sum()}


def rotate(points, degrees=90):
    """Rotate 2D points counterclockwise about the origin."""
    t = np.radians(degrees)
    r = np.array([[np.cos(t), -np.sin(t)], [np.sin(t), np.cos(t)]])
    return (np.asarray(points) @ r.T).round(6)


def invert(matrix):
    return np.linalg.inv(matrix)
`

type Line struct {
	Slope     float64 `json:"slope"`
	Intercept float64 `json:"intercept"`
	Residual  float64 `json:"residual"`
}

func main() {
	ctx := context.Background()

	py, err := pywasi.New(ctx,
		pywasi.WithDependencies("numpy"),
		pywasi.WithPreload("numpy"),
		pywasi.WithModule("geometry", geometry),
		pywasi.WithBuildLog(os.Stderr),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer py.Close()

	// 1. A library function, no module of our own needed.
	det, err := py.Call[float64](ctx, pywasi.Func{
		Module: "numpy.linalg",
		Name:   "det",
		Args:   []any{[][]float64{{4, 1}, {2, 3}}},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("det = %.1f\n", det)

	// 2. Functions in the inline module.
	line, err := py.Call[Line](ctx, pywasi.Func{
		Module: "geometry",
		Name:   "fit_line",
		Args:   []any{[]float64{0, 1, 2, 3}, []float64{1, 3, 5, 7}},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("fit_line: y = %.1fx + %.1f (residual %.1g)\n", line.Slope, line.Intercept, line.Residual)

	rotated, err := py.Call[[][]float64](ctx, pywasi.Func{
		Module: "geometry",
		Name:   "rotate",
		Args:   []any{[][]float64{{1, 0}, {0, 2}}},
		Kwargs: map[string]any{"degrees": 90},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("rotate:", rotated)

	// 3. A Python exception comes back as *pywasi.Error.
	_, err = py.Call[any](ctx, pywasi.Func{
		Module: "geometry",
		Name:   "invert",
		Args:   []any{[][]float64{{1, 2}, {2, 4}}},
	})
	var perr *pywasi.Error
	if errors.As(err, &perr) {
		fmt.Printf("invert: %s: %s\n", perr.Type, perr.Message)
	}
}
