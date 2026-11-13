package pywasi

import (
	"bytes"
	"reflect"
	"testing"
)

func TestAppendS64(t *testing.T) {
	// Signed LEB128, as in the wasm spec.
	for _, tc := range []struct {
		v    int64
		want []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{-1, []byte{0x7f}},
		{63, []byte{0x3f}},
		{64, []byte{0xc0, 0x00}},
		{-64, []byte{0x40}},
		{-65, []byte{0xbf, 0x7f}},
		{624485, []byte{0xe5, 0x8e, 0x26}},
		{-2147483648, []byte{0x80, 0x80, 0x80, 0x80, 0x78}},
	} {
		got := appendS64(nil, tc.v)
		if !bytes.Equal(got, tc.want) {
			t.Errorf("appendS64(%d) = %x, want %x", tc.v, got, tc.want)
		}
		r := &wasmReader{b: append(got, 0xaa)}
		if r.s64(); r.err != nil || !bytes.Equal(r.b, []byte{0xaa}) {
			t.Errorf("s64 after %d: %x %v", tc.v, r.b, r.err)
		}
	}
}

func TestDataSegments(t *testing.T) {
	data := make([]byte, 200)
	data[3], data[4] = 1, 1
	data[20] = 1  // gap of 15 zeros: merged
	data[100] = 1 // gap of 79: new segment
	data[199] = 1 // last byte
	got := dataSegments(data)
	want := [][2]int{{3, 21}, {100, 101}, {199, 200}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := dataSegments(make([]byte, 64)); len(got) != 0 {
		t.Errorf("zeros: %v", got)
	}
}
