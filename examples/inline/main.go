// Inline runs Python embedded in Go. The code is a string and its
// dependencies are a list. No project files or mounts are needed.
//
//	go run ./examples/inline
//
// The first run builds the interpreter (numpy compiles from source, a few
// minutes). Later runs reuse it.
package main

import (
	"context"
	"log"
	"os"

	"go.chrisrx.dev/pywasi"
)

const code = `
import numpy as np
import yaml

m = np.array([[4.0, 1.0], [2.0, 3.0]])
print(yaml.safe_dump({
    "eigenvalues": sorted(np.linalg.eigvals(m).real.tolist()),
    "inverse": np.linalg.inv(m).round(2).tolist(),
}), end="")
`

func main() {
	ctx := context.Background()

	py, err := pywasi.New(ctx,
		pywasi.WithDependencies("numpy", "pyyaml==6.0.3"),
		pywasi.WithPreload("numpy", "yaml"),
		pywasi.WithBuildLog(os.Stderr),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer py.Close()

	if _, err := py.Run(ctx, pywasi.RunOptions{
		Args:   []string{"-c", code},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}); err != nil {
		log.Fatal(err)
	}
}
