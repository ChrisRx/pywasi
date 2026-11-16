package pywasi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Reading python.wasm for pre-initialization (see image.go): its
// wizer-initialize export starts CPython and imports the preload modules,
// and pywasi-resume runs a program from that state. See
// toolchain/support/pywasi_main.c.

const (
	initExport   = "wizer-initialize"
	resumeExport = "pywasi-resume"
	globalPrefix = "__pywasi_global_"
)

// Section ids.
const (
	secCustom = 0
	secImport = 2
	secGlobal = 6
	secExport = 7
	secData   = 11
)

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
				case 1: // passive
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

// instrument writes the module with an export for every mutable global,
// so their values can be read after initialization and set when an image
// is restored (see image.go).
func (m *wasmModule) instrument() ([]byte, error) {
	out := []byte("\x00asm\x01\x00\x00\x00")
	for _, s := range m.sections {
		body := s.body
		if s.id == secExport {
			r := &wasmReader{b: s.body}
			exports := r.exports()
			if r.err != nil {
				return nil, r.err
			}
			for _, g := range m.globals {
				if g.mutable {
					exports = append(exports, wasmExport{globalPrefix + strconv.Itoa(int(g.index)), 3, g.index})
				}
			}
			body = appendU32(nil, uint32(len(exports)))
			for _, e := range exports {
				body = appendU32(body, uint32(len(e.name)))
				body = append(body, e.name...)
				body = append(body, e.kind)
				body = appendU32(body, e.index)
			}
		}
		out = append(out, s.id)
		out = appendU32(out, uint32(len(body)))
		out = append(out, body...)
	}
	return out, nil
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
