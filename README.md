# pywasi

Run Python in Go, C extensions like numpy included. CPython is compiled to
WASI and runs in [wazero](https://wazero.io), so there's no cgo and nothing
to install: the default interpreter is embedded in the package.

## Quick start

```sh
go get go.chrisrx.dev/pywasi   # needs Go 1.27
```

```go
package main

import (
	"context"
	"fmt"
	"log"

	"go.chrisrx.dev/pywasi"
)

func main() {
	ctx := context.Background()

	// Evaluate code and get the value of its last expression.
	n, err := pywasi.Eval[int](ctx, `
		import math
		math.factorial(10)
	`)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(n) // 3628800

	// Call a function, here in a module defined inline.
	area, err := pywasi.Call[float64](ctx, pywasi.Func{
		Module: "shapes",
		Name:   "circle_area",
		Args:   []any{2.0},
	}, pywasi.WithModule("shapes", `
		import math

		def circle_area(r):
		    return math.pi * r * r
	`))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%.2f\n", area) // 12.57
}
```

The package-level `Eval` and `Call` create an interpreter, run, and close
it. To make several calls, create one with `New` and use its methods:

```go
py, err := pywasi.New(ctx, pywasi.WithPackages("numpy", "tabulate"))
if err != nil {
	log.Fatal(err)
}
defer py.Close()

inv, err := py.Eval[[][]float64](ctx, `
	import numpy as np
	np.linalg.inv(np.array([[4.0, 1.0], [2.0, 3.0]]))
`)
```

`New` compiles the interpreter once, and each `Run`, `Eval` or `Call` is a
fresh, isolated interpreter, safe to use concurrently. Packages come from
PyPI (pure Python) or from prebuilt bundles (numpy), so this needs no
compiler either. See [Pure-Python packages](#pure-python-packages-without-a-build)
and [Packages with C extensions](#packages-with-c-extensions-still-without-a-build).

There's also a command for running the interpreter directly, with an
interactive prompt when given no arguments:

```sh
go run go.chrisrx.dev/pywasi/cmd/pywasi@latest                       # REPL
go run go.chrisrx.dev/pywasi/cmd/pywasi@latest -- -c 'print(1 + 1)'
go run ./cmd/pywasi -mount .:/app -- /app/script.py                   # in a checkout
```

It uses `$PYWASI_HOME` or `./.pywasi` if there is one, else the embedded
interpreter (`-home` picks another). The prompt is CPython's basic one,
without line editing, since the newer one needs `termios`, which WASI lacks.

With no interpreter options, `New` uses `$PYWASI_HOME` or `./.pywasi` if
one exists, and otherwise the standard library interpreter embedded in the
package (`pywasi.DefaultArchive`, about 7 MiB). Everything is cached under
`os.UserCacheDir()/pywasi` (or `$PYWASI_CACHE`).

## Building your own interpreter

For packages that aren't prebuilt, build an interpreter from a uv project
with the Nix toolchain in this repository:

```sh
nix develop
uv init --app myapp && cd myapp
uv add numpy
cd .. && ./scripts/build.sh myapp   # -> myapp/.pywasi/{bin/python.wasm,lib/}
```

The first run builds CPython for WASI (a few minutes). Packages with a
`py3-none-any` wheel are installed as-is. Everything else is compiled from
its sdist and statically linked into `python.wasm`. Run it with the CLI, or
from Go with `WithHome`:

```sh
go run ./cmd/pywasi -home myapp/.pywasi -mount myapp:/app -- /app/main.py
```

Or let Go build it from a list of dependencies, either bare names or pinned
(`examples/inline`):

```go
py, err := pywasi.New(ctx,
	pywasi.WithDependencies("numpy", "pyyaml==6.0.3"), // any PEP 508 specifier
	pywasi.WithPreload("numpy", "yaml"),               // import names, snapshotted
)
```

`WithDependencies` builds through `builder.Ensure`, which runs
`scripts/build.sh --with ...`. This needs this checkout and its Nix
toolchain. It enters `nix develop` itself if needed, and `WithRoot` or
`PYWASI_ROOT` points it at the checkout. The first build of a dependency set
compiles anything with C code, which takes minutes for numpy. After that the
set is cached and locked, so a bare `numpy` stays on the version it first
resolved to. To ship a prebuilt interpreter, call `builder.Ensure` ahead of
time and pass its result to `WithHome`, or pack it as an archive.

## Interpreter archives

`scripts/build.sh --archive FILE` packs an interpreter into a reproducible
`.tar.zst`, and `scripts/publish.sh FILE interpreters` uploads it to R2 under
a content-addressed name. Use one from Go with:

```go
pywasi.New(ctx, pywasi.WithArchive(pywasi.ArchiveURL(url, "sha256:...")))
pywasi.New(ctx, pywasi.WithArchive(pywasi.ArchiveFile("python.tar.zst")))
```

Archives are unpacked once per digest. `Source` is an interface, so they can
come from anywhere.

The default interpreter is embedded from
`internal/stdlib/python3.14-stdlib.tar.zst`. To update it:

```sh
nix develop --command ./scripts/build.sh --env --archive internal/stdlib/python3.14-stdlib.tar.zst
```

## Pure-Python packages without a build

Packages with a pure-Python wheel can be added to any interpreter, with no
compiler, Nix or checkout:

```go
py, err := pywasi.New(ctx, pywasi.WithPackages("rich", "tabulate>=0.9"))
```

They are resolved for the interpreter's own environment (markers,
`requires-python`), downloaded once, checked against their sha256 and
mounted read-only on `sys.path`. A resolution is cached per interpreter and
requirement set, so unpinned names stay where they first resolved. There are
three ways to resolve:

- `ResolverBuiltin` (default): resolvelib running inside `python.wasm`, with
  Go fetching the index over HTTP. Nothing to install.
- `ResolverUV`: uv from PATH, or a pinned release downloaded from the R2
  mirror (or GitHub). uv also needs some native Python to lock with and
  fetches its own once if there's none.
- `WithPylock(data)`: a PEP 751 lock (`uv export --format pylock.toml`),
  used exactly.

Packages built into the interpreter (numpy in a numpy build) satisfy
requirements as they are.

## Packages with C extensions, still without a build

C extensions are statically linked into `python.wasm`, so they can't be
layered on like pure-Python packages. Instead, pywasi links a new
interpreter on the fly, with wasm-ld itself compiled to WASI and run in
wazero:

```go
py, err := pywasi.New(ctx, pywasi.WithPackages("numpy", "tabulate"))
```

A catalog at `https://pywasi.chrisrx.dev/index.json` (`WithCatalog` to change
or disable it) lists:

- the linker: LLVM's wasm-ld built for WASI (`nix build .#lld-wasi`, from the
  same LLVM as wasi-sdk, with YoWASP's WASI patch ported to LLVM 23 in
  `nix/patches/llvm-wasi.patch`);
- link kits: CPython's objects, libc, libc++ and the exact wasm-ld command
  line, plus the standard library;
- bundles: one per package built from source, with an object per extension
  module, an object that registers them at startup, and the package's Python
  files.

The resolver treats catalog bundles as installable versions, so numpy
resolves to a bundle and tabulate to a wheel. New then downloads what's
missing (all checked by sha256), links, and caches the interpreter by its
inputs. The first time takes about 20s (downloads, compiling the linker,
linking, and a startup snapshot with the bundles' packages imported, as in
[Fast startup](#fast-startup)). After that `New` takes about 0.3s and an
`Eval` using numpy about 30-40ms.

To publish more:

```sh
./scripts/build.sh --with numpy --with pyyaml --kit build/kit.tar.zst --bundles build/bundles
PYWASI_R2_ENV=... scripts/catalog.py --kit build/kit.tar.zst --bundle build/bundles/*.tar.zst
```

Bundles link only with a kit of the same ABI id (same CPython build and
toolchain). A package that is neither pure Python nor in the catalog is an
error that says so.

## Returning values and calling functions

`Eval` runs code and decodes the value of its last expression, like a
notebook cell. `Call` imports a module and calls a function. Both return
the result as the type parameter, and source code (in `Eval` and
`WithModule`) is dedented, so it can be indented along with the Go code:

```go
inv, err := py.Eval[[][]float64](ctx, `
	import numpy as np
	np.linalg.inv(np.array([[4.0, 1.0], [2.0, 3.0]]))
`)

type Stats struct{ Mean, Std float64 }
stats, err := py.Call[Stats](ctx, pywasi.Func{
	Module: "stats", // see Modules below
	Name:   "summarize",
	Args:   []any{[]float64{2, 4, 4, 5}},
	Kwargs: map[string]any{"ddof": 1},
})

// Or without creating an interpreter first. These take New's options,
// plus WithRunOptions for stdio, env and mounts:
n, err := pywasi.Eval[int](ctx, "6 * 7")
sq, err := pywasi.Call[int](ctx, pywasi.Func{Module: "sq", Name: "sq", Args: []any{7}},
	pywasi.WithModule("sq", "def sq(x): return x * x"))

var perr *pywasi.Error // Type ("numpy.linalg.LinAlgError"), Message, Traceback
if errors.As(err, &perr) { ... }
```

Arguments and results are JSON:
- numpy arrays and scalars become lists and numbers.
- complex numbers become `{"real", "imag"}`.
- bytes become base64, which decodes into a Go `[]byte`.
- datetimes become ISO 8601 strings.
- NaN and Infinity are an error, since JSON can't represent them.

### Modules

Your own Python modules can live in the Go program, either as strings or
as an embedded directory, and are importable by every `Run`, `Eval` and
`Call` (`examples/call`):

```go
//go:embed pylib
var pylib embed.FS

sub, _ := fs.Sub(pylib, "pylib") // pylib/stats.py imports as stats
py, err := pywasi.New(ctx,
	pywasi.WithModules(sub),
	pywasi.WithModule("geometry", `
		import numpy as np

		def rotate(points, degrees=90):
		    ...
	`),
)
```

They stay in memory and are mounted read-only under `/.pywasi/modules` on
`sys.path`, after any `PYTHONPATH` of the run. A dotted name such as
`"geo.shapes"` is a submodule.

The request and response go through a temporary directory mounted at
`/.pywasi/io`, so the code's own `print`s still go to `RunOptions.Stdout`, and
`python.wasm` stays a plain WASI program. Each call is a fresh interpreter:
about 20ms with a startup snapshot (40ms with numpy preloaded), and
60-200ms without one, depending on imports.

## Sessions

Each `Eval` and `Call` is a fresh interpreter, which keeps calls isolated
but costs startup every time. A `Session` is one interpreter that stays
running and handles requests in turn, keeping imports and globals between
them, like a notebook kernel:

```go
s, err := py.Session(ctx, pywasi.RunOptions{Stdout: os.Stdout})
if err != nil {
	log.Fatal(err)
}
defer s.Close()

_, err = s.Eval[any](ctx, "import numpy as np\nm = np.eye(3) * 2")
det, err := s.Eval[float64](ctx, "float(np.linalg.det(m))")             // m is still defined
n, err := s.Call[int](ctx, pywasi.Func{Module: "counter", Name: "bump"}) // module state persists
```

A small numpy `Eval` takes about 0.3ms in a session, against about 40ms
for a fresh interpreter with a snapshot and 150-200ms without one. Requests on
one session run one at a time (it's safe to share between goroutines), so
use several sessions for parallel work. A Python error, even `SystemExit`,
is returned and the session keeps going. Code can't be interrupted, so a
request whose context is canceled closes the session.

## Fast startup

The build pre-initializes the interpreter with Wizer: CPython starts, the
modules listed in `preload` are imported, and that memory is snapshotted into
`python.wasm`. Each run resumes from there:

```toml
[tool.pywasi]
preload = ["numpy"]
```

For the example app, this takes an `Eval` using numpy from 150-200ms to
about 40ms. Run as a new process with the wazero CLI, it saves less (0.45s
to 0.3s), since loading the compiled module dominates. The
snapshot interpreter accepts `-c`, `-m`, a script, or `-` (no other flags),
and its string hash seed is fixed at build time. Set `snapshot = false` or
pass `--no-snapshot` to turn it off.

Interpreters linked from Go (C-extension packages from the catalog) get the
same snapshot without Wizer: after linking, New runs the initialization in
wazero with the bundles' packages preloaded, and writes the resulting memory
and globals back into the module (`snapshot.go`). If that fails, the
interpreter still works without it, and the reason is in
`snapshot-error.txt` next to it in the cache.

## Example: PyYAML with libyaml

```sh
cd myapp && uv add pyyaml && cd ..
./scripts/build.sh myapp
```

```python
import yaml
print(yaml.__with_libyaml__)                             # True
print(yaml.load("a: [x, y]", Loader=yaml.CSafeLoader))  # {'a': ['x', 'y']}
```

PyYAML's C extension (`yaml._yaml`) is built from its sdist and links
libyaml, which is built for WASI in `nix/c-libs.nix`:

```nix
libyaml = mkWasiLib {
  pname = "libyaml";
  inherit (pkgs.libyaml) version src;
};
```

Any CMake-built C library can be added the same way. Entries are merged into
`$PYWASI_SYSROOT`, which every package build compiles and links against.

Per-package build options go in `pyproject.toml`:

```toml
[tool.pywasi.packages.somepackage]
config-settings.setup-args = ["-Dfoo=false"]
```

See `toolchain/recipes.toml` for the built-in ones.

## Performance

Everything is compiled with wasm SIMD128, and numpy uses OpenBLAS (its
`WASM128_GENERIC` SIMD kernels, a register-blocked DGEMM kernel, plus LAPACK)
from `nix/openblas-wasi.nix`, and `np.sort` uses Highway's vectorized
quicksort (`toolchain/patches/numpy-wasm-highway-sort.patch`). CPython
allocates with mimalloc (`PYTHONMALLOC` overrides it), which is 5-15% faster
than wasi-libc's malloc on object-heavy code.

pywasi uses a fork of wazero
([github.com/ChrisRx/wazero](https://github.com/ChrisRx/wazero), branch
`pywasi`) that saves its trap context once per function frame instead of at
every memory access, and compiles python.wasm without memory bounds checks:
each instance's memory is a fixed 8 GiB virtual reservation (only touched
pages use physical memory) that every address python.wasm can compute falls
inside. Together these make pure-Python code 10-50% faster. An
out-of-bounds access, which only a bug in C code could make, then stays
inside that instance's memory instead of trapping. `WithBoundsChecks()`
turns the checks back on.

Versus native CPython 3.14 with numpy 2.5.4 and OpenBLAS on x86-64, one
thread (`OMP_NUM_THREADS=1`), pinned to one performance core, best of 3 runs:

| | wasmtime | pywasi (wazero) |
|---|---|---|
| Pure Python | ~1.2-2x slower | ~1.5-2.7x slower |
| numpy elementwise, reductions, FFT | ~1-1.8x | ~1.3-2.2x |
| `linalg.solve` 1000x1000 | ~2.5x | ~6.5x |
| matmul 1000x1000 | ~3x | ~7.5x |
| `np.sort` | ~4x | ~6x |

Native numpy uses AVX2/AVX-512 kernels for matmul and sort, and wasm SIMD is
128 bits wide.

## Limits

- No Rust extensions (maturin) yet.
- C libraries a package needs must be added to `nix/c-libs.nix` first. zlib,
  libyaml and OpenBLAS are there now.
- No threads (WASI preview 1), so BLAS runs single-threaded.
- C++ exceptions abort instead of unwinding.
- wasm has no floating-point exception flags. numpy's explicit errors work
  through a patch (singular matrices raise `LinAlgError`, and integer division
  by zero respects `np.errstate`), but float operations like `1.0 / 0.0` don't
  warn.
- Your app's own code isn't packaged unless the project has a
  `[build-system]`. Mount it instead.
