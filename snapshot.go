package pywasi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/ChrisRx/wazero"
	"github.com/ChrisRx/wazero/imports/wasi_snapshot_preview1"
	"github.com/ChrisRx/wazero/sys"
	"go.chrisrx.dev/x/context"
)

// Pre-initialization of linked interpreters, like Wizer (which the build
// uses for interpreters built from source): python.wasm's
// wizer-initialize export starts CPython and imports the preload modules,
// and the resulting memory and globals become the module's initial state.
// pywasi-resume then replaces _start, so each run skips startup. See
// toolchain/support/pywasi_main.c.

const (
	initExport   = "wizer-initialize"
	resumeExport = "pywasi-resume"
	globalPrefix = "__pywasi_global_"
)

// Section ids.
const (
	secCustom    = 0
	secImport    = 2
	secMemory    = 5
	secGlobal    = 6
	secExport    = 7
	secStart     = 8
	secData      = 11
	secDataCount = 12
)

// snapshotWasm runs wasm's initialization with cfg (its WASI environment:
// the same mounts and paths as at runtime) and returns the pre-initialized
// module.
func snapshotWasm(ctx context.Context, r wazero.Runtime, wasm []byte, cfg wazero.ModuleConfig) ([]byte, error) {
	m, err := parseWasm(wasm)
	if err != nil {
		return nil, err
	}
	if !m.exports[initExport] || !m.exports[resumeExport] {
		return nil, errors.New("no snapshot entry points")
	}
	instrumented, err := m.encode(nil, nil)
	if err != nil {
		return nil, err
	}
	compiled, err := r.CompileModule(ctx, instrumented)
	if err != nil {
		return nil, err
	}
	defer compiled.Close(ctx)
	mod, err := r.InstantiateModule(ctx, compiled, cfg.WithName("").WithStartFunctions())
	if err != nil {
		return nil, err
	}
	defer mod.Close(ctx)
	if _, err := mod.ExportedFunction(initExport).Call(ctx); err != nil {
		var exitErr *sys.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("initialization exited with code %d", exitErr.ExitCode())
		}
		return nil, err
	}
	values := map[uint32]uint64{}
	for _, g := range m.globals {
		if g.mutable {
			values[g.index] = mod.ExportedGlobal(globalPrefix + strconv.Itoa(int(g.index))).Get()
		}
	}
	mem := mod.Memory()
	if mem == nil {
		return nil, errors.New("no memory")
	}
	data, ok := mem.Read(0, mem.Size())
	if !ok {
		return nil, errors.New("reading memory")
	}
	return m.encode(values, data)
}

type wasmSection struct {
	id   byte
	body []byte
}

type wasmGlobal struct {
	index   uint32 // in the global index space, after imported globals
	valtype byte
	mutable bool
	init    []byte // constant expression, with its end opcode
}

type wasmExport struct {
	name  string
	kind  byte
	index uint32
}

type wasmModule struct {
	sections []wasmSection
	globals  []wasmGlobal
	exports  map[string]bool
}

func parseWasm(b []byte) (*wasmModule, error) {
	if len(b) < 8 || !bytes.Equal(b[:8], []byte("\x00asm\x01\x00\x00\x00")) {
		return nil, errors.New("not a wasm module")
	}
	m := &wasmModule{exports: map[string]bool{}}
	r := &wasmReader{b: b[8:]}
	importedGlobals := uint32(0)
	for !r.done() {
		id := r.byte()
		body := r.bytes(r.u32())
		if r.err != nil {
			return nil, r.err
		}
		m.sections = append(m.sections, wasmSection{id, body})
		s := &wasmReader{b: body}
		switch id {
		case secImport:
			for range s.u32() {
				s.name()
				s.name()
				switch kind := s.byte(); kind {
				case 0: // function
					s.u32()
				case 1: // table
					s.byte()
					s.limits()
				case 2: // memory
					s.limits()
				case 3: // global
					s.byte()
					s.byte()
					importedGlobals++
				default:
					return nil, fmt.Errorf("unknown import kind %d", kind)
				}
			}
		case secGlobal:
			n := s.u32()
			for i := range n {
				g := wasmGlobal{index: importedGlobals + i, valtype: s.byte(), mutable: s.byte() == 1}
				g.init = s.constExpr()
				m.globals = append(m.globals, g)
			}
		case secExport:
			for _, e := range s.exports() {
				m.exports[e.name] = true
			}
		case secData:
			for range s.u32() {
				switch flag := s.u32(); flag {
				case 0:
					s.constExpr()
				case 2:
					s.u32()
					s.constExpr()
				default:
					return nil, fmt.Errorf("unsupported data segment kind %d", flag)
				}
				s.bytes(s.u32())
			}
		}
		if s.err != nil {
			return nil, fmt.Errorf("section %d: %w", id, s.err)
		}
	}
	return m, nil
}

// encode writes the module. Without data it adds an export for every
// mutable global, to read them after initialization. With data (the
// memory after initialization) and values (of mutable globals) it writes
// the snapshot: memory and globals start in that state, and the resume
// entry point is _start.
func (m *wasmModule) encode(values map[uint32]uint64, data []byte) ([]byte, error) {
	snapshot := data != nil
	out := []byte("\x00asm\x01\x00\x00\x00")
	for _, s := range m.sections {
		body := s.body
		switch s.id {
		case secMemory:
			if snapshot {
				r := &wasmReader{b: s.body}
				if n := r.u32(); n != 1 {
					return nil, fmt.Errorf("%d memories", n)
				}
				flags, _, maxPages := r.limits()
				if r.err != nil {
					return nil, r.err
				}
				body = appendU32(nil, 1)
				body = append(body, flags)
				body = appendU32(body, uint32(len(data)/65536))
				if flags&1 != 0 {
					body = appendU32(body, maxPages)
				}
			}
		case secGlobal:
			if snapshot {
				body = appendU32(nil, uint32(len(m.globals)))
				for _, g := range m.globals {
					body = append(body, g.valtype, boolByte(g.mutable))
					if !g.mutable {
						body = append(body, g.init...)
						continue
					}
					v := values[g.index]
					switch g.valtype {
					case 0x7f: // i32
						body = appendS64(append(body, 0x41), int64(int32(uint32(v))))
					case 0x7e: // i64
						body = appendS64(append(body, 0x42), int64(v))
					case 0x7d: // f32
						body = binary.LittleEndian.AppendUint32(append(body, 0x43), uint32(v))
					case 0x7c: // f64
						body = binary.LittleEndian.AppendUint64(append(body, 0x44), v)
					default:
						return nil, fmt.Errorf("mutable global of type 0x%x", g.valtype)
					}
					body = append(body, 0x0b)
				}
			}
		case secExport:
			r := &wasmReader{b: s.body}
			var exports []wasmExport
			for _, e := range r.exports() {
				switch {
				case !snapshot:
					exports = append(exports, e)
				case e.name == "_start" || e.name == initExport:
				case e.name == resumeExport:
					exports = append(exports, wasmExport{"_start", e.kind, e.index})
				default:
					exports = append(exports, e)
				}
			}
			if r.err != nil {
				return nil, r.err
			}
			if !snapshot {
				for _, g := range m.globals {
					if g.mutable {
						exports = append(exports, wasmExport{globalPrefix + strconv.Itoa(int(g.index)), 3, g.index})
					}
				}
			}
			body = appendU32(nil, uint32(len(exports)))
			for _, e := range exports {
				body = appendU32(body, uint32(len(e.name)))
				body = append(body, e.name...)
				body = append(body, e.kind)
				body = appendU32(body, e.index)
			}
		case secStart:
			if snapshot {
				continue // Initialization already ran.
			}
		case secDataCount:
			if snapshot {
				body = appendU32(nil, uint32(len(dataSegments(data))))
			}
		case secData:
			if snapshot {
				segs := dataSegments(data)
				body = appendU32(nil, uint32(len(segs)))
				for _, seg := range segs {
					body = append(body, 0, 0x41)
					body = appendS64(body, int64(int32(uint32(seg[0]))))
					body = append(body, 0x0b)
					body = appendU32(body, uint32(seg[1]-seg[0]))
					body = append(body, data[seg[0]:seg[1]]...)
				}
			}
		}
		out = append(out, s.id)
		out = appendU32(out, uint32(len(body)))
		out = append(out, body...)
	}
	return out, nil
}

// dataSegments returns the [start, end) ranges of data's nonzero bytes,
// merging ranges separated by short runs of zeros (each segment costs
// about 10 bytes).
func dataSegments(data []byte) [][2]int {
	const maxGap = 32
	var segs [][2]int
	i := 0
	for {
		for i < len(data) && data[i] == 0 {
			i++
		}
		if i == len(data) {
			return segs
		}
		start := i
		end := i
		for i < len(data) {
			if data[i] != 0 {
				i++
				end = i
				continue
			}
			j := i
			for j < len(data) && j-end < maxGap && data[j] == 0 {
				j++
			}
			if j == len(data) || data[j] == 0 {
				break
			}
			i = j
		}
		segs = append(segs, [2]int{start, end})
		i = end
	}
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// appendS64 appends v as signed LEB128 (not Go's zigzag varint).
func appendS64(b []byte, v int64) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && c&0x40 == 0) || (v == -1 && c&0x40 != 0) {
			return append(b, c)
		}
		b = append(b, c|0x80)
	}
}

func appendU32(b []byte, v uint32) []byte {
	return binary.AppendUvarint(b, uint64(v))
}

type wasmReader struct {
	b   []byte
	err error
}

func (r *wasmReader) done() bool { return len(r.b) == 0 || r.err != nil }

func (r *wasmReader) fail() {
	if r.err == nil {
		r.err = errors.New("malformed wasm")
	}
	r.b = nil
}

func (r *wasmReader) byte() byte {
	if len(r.b) == 0 {
		r.fail()
		return 0
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *wasmReader) bytes(n uint32) []byte {
	if uint64(n) > uint64(len(r.b)) {
		r.fail()
		return nil
	}
	b := r.b[:n]
	r.b = r.b[n:]
	return b
}

func (r *wasmReader) u32() uint32 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 || v > math.MaxUint32 {
		r.fail()
		return 0
	}
	r.b = r.b[n:]
	return uint32(v)
}

// s64 skips a signed LEB128.
func (r *wasmReader) s64() {
	for i := 0; i < len(r.b) && i < 10; i++ {
		if r.b[i]&0x80 == 0 {
			r.b = r.b[i+1:]
			return
		}
	}
	r.fail()
}

func (r *wasmReader) name() string { return string(r.bytes(r.u32())) }

func (r *wasmReader) limits() (flags byte, minPages, maxPages uint32) {
	flags = r.byte()
	if flags > 3 {
		r.fail() // memory64 or unknown
		return
	}
	minPages = r.u32()
	if flags&1 != 0 {
		maxPages = r.u32()
	}
	return
}

func (r *wasmReader) exports() []wasmExport {
	var out []wasmExport
	for range r.u32() {
		out = append(out, wasmExport{r.name(), r.byte(), r.u32()})
	}
	return out
}

// constExpr skips a constant expression and returns it, end included.
func (r *wasmReader) constExpr() []byte {
	start := r.b
	for !r.done() {
		switch op := r.byte(); op {
		case 0x0b: // end
			return start[:len(start)-len(r.b)]
		case 0x41, 0x42: // i32.const, i64.const
			r.s64()
		case 0x43: // f32.const
			r.bytes(4)
		case 0x44: // f64.const
			r.bytes(8)
		case 0x23, 0xd2: // global.get, ref.func
			r.u32()
		case 0xd0: // ref.null
			r.byte()
		case 0x6a, 0x6b, 0x6c, 0x7c, 0x7d, 0x7e: // extended const arithmetic
		case 0xfd: // v128.const
			if r.u32() != 12 {
				r.fail()
			}
			r.bytes(16)
		default:
			r.fail()
		}
	}
	r.fail()
	return nil
}

// newSnapshotRuntime is a runtime for snapshotWasm, with WASI.
func newSnapshotRuntime(ctx context.Context, cacheDir string) (wazero.Runtime, error) {
	cfg := wazero.NewRuntimeConfig().WithDebugInfoEnabled(false)
	if cacheDir != "" {
		cache, err := wazero.NewCompilationCacheWithDir(cacheDir)
		if err != nil {
			return nil, err
		}
		cfg = cfg.WithCompilationCache(cache)
	}
	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		r.Close(ctx)
		return nil, err
	}
	return r, nil
}
