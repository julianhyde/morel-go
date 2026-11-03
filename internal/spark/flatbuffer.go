// Licensed to Julian Hyde under one or more contributor license
// agreements.  See the NOTICE file distributed with this work
// for additional information regarding copyright ownership.
// Julian Hyde licenses this file to you under the Apache
// License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License.  You may obtain a
// copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied.  See the License for the specific
// language governing permissions and limitations under the
// License.

package spark

import (
	"encoding/binary"
	"errors"
	"slices"
)

// Flatbuffers, as much of them as the Arrow IPC format needs: a
// reader of tables, vectors and strings, and a builder that writes
// them back to front, as the flatbuffers library does.

// errFlatbuffer says that a flatbuffer is malformed.
var errFlatbuffer = errors.New("malformed Arrow metadata")

// fbTable is a table in a flatbuffer.
type fbTable struct {
	buf []byte
	pos int
}

// fbRoot returns the root table of a flatbuffer.
func fbRoot(buf []byte) (fbTable, error) {
	if len(buf) < 4 {
		return fbTable{}, errFlatbuffer
	}
	t := fbTable{buf: buf, pos: int(binary.LittleEndian.Uint32(buf))}
	return t, t.check(t.pos, 4)
}

func (t fbTable) check(pos, n int) error {
	if pos < 0 || pos+n > len(t.buf) {
		return errFlatbuffer
	}
	return nil
}

// field returns the position of a field, or 0 if it is absent.
func (t fbTable) field(i int) int {
	if t.check(t.pos, 4) != nil {
		return 0
	}
	vt := t.pos - int(int32(binary.LittleEndian.Uint32(t.buf[t.pos:])))
	if t.check(vt, 4) != nil {
		return 0
	}
	size := int(binary.LittleEndian.Uint16(t.buf[vt:]))
	slot := 4 + 2*i
	if slot+2 > size || t.check(vt+slot, 2) != nil {
		return 0
	}
	off := int(binary.LittleEndian.Uint16(t.buf[vt+slot:]))
	if off == 0 {
		return 0
	}
	return t.pos + off
}

func (t fbTable) uint8(i int, def uint8) uint8 {
	p := t.field(i)
	if p == 0 || t.check(p, 1) != nil {
		return def
	}
	return t.buf[p]
}

func (t fbTable) bool(i int) bool { return t.uint8(i, 0) != 0 }

func (t fbTable) int16(i int, def int16) int16 {
	p := t.field(i)
	if p == 0 || t.check(p, 2) != nil {
		return def
	}
	return int16(binary.LittleEndian.Uint16(t.buf[p:]))
}

func (t fbTable) int32(i int, def int32) int32 {
	p := t.field(i)
	if p == 0 || t.check(p, 4) != nil {
		return def
	}
	return int32(binary.LittleEndian.Uint32(t.buf[p:]))
}

func (t fbTable) int64(i int, def int64) int64 {
	p := t.field(i)
	if p == 0 || t.check(p, 8) != nil {
		return def
	}
	return int64(binary.LittleEndian.Uint64(t.buf[p:]))
}

// indirect follows the offset at a position.
func (t fbTable) indirect(p int) int {
	if t.check(p, 4) != nil {
		return -1
	}
	return p + int(binary.LittleEndian.Uint32(t.buf[p:]))
}

// table returns a table that a field refers to.
func (t fbTable) table(i int) (fbTable, bool) {
	p := t.field(i)
	if p == 0 {
		return fbTable{}, false
	}
	q := t.indirect(p)
	return fbTable{buf: t.buf, pos: q}, t.check(q, 4) == nil
}

// vector returns the position of the first element of a vector that
// a field refers to, and its length.
func (t fbTable) vector(i int) (int, int) {
	p := t.field(i)
	if p == 0 {
		return 0, 0
	}
	q := t.indirect(p)
	if t.check(q, 4) != nil {
		return 0, 0
	}
	return q + 4, int(binary.LittleEndian.Uint32(t.buf[q:]))
}

// tables returns the tables of a vector of tables.
func (t fbTable) tables(i int) []fbTable {
	start, n := t.vector(i)
	out := make([]fbTable, 0, n)
	for k := range n {
		q := t.indirect(start + 4*k)
		if t.check(q, 4) != nil {
			return out
		}
		out = append(out, fbTable{buf: t.buf, pos: q})
	}
	return out
}

func (t fbTable) string(i int) string {
	start, n := t.vector(i)
	if n == 0 || t.check(start, n) != nil {
		return ""
	}
	return string(t.buf[start : start+n])
}

// fbBuilder writes a flatbuffer back to front: an object is written
// before what refers to it, and an offset is measured from the end.
type fbBuilder struct {
	buf      []byte // the bytes written so far, at the end of buf
	head     int    // where the written bytes start
	minAlign int
	vtable   []int // the offsets of the fields of the open table
	start    int   // the offset at which the open table started
}

func newFBBuilder() *fbBuilder {
	const initial = 256
	return &fbBuilder{buf: make([]byte, initial), head: initial, minAlign: 1}
}

// offset is the offset of the bytes written so far, from the end.
func (b *fbBuilder) offset() int { return len(b.buf) - b.head }

func (b *fbBuilder) grow(n int) {
	for b.head < n {
		size := len(b.buf)
		bigger := make([]byte, 2*size)
		copy(bigger[size:], b.buf)
		b.buf = bigger
		b.head += size
	}
}

// pad writes zero bytes.
func (b *fbBuilder) pad(n int) {
	b.grow(n)
	for range n {
		b.head--
		b.buf[b.head] = 0
	}
}

// prep aligns for a value of a given size, to be followed by
// additional bytes.
func (b *fbBuilder) prep(size, additional int) {
	b.minAlign = max(b.minAlign, size)
	padding := (-(b.offset() + additional)) & (size - 1)
	b.pad(padding)
}

func (b *fbBuilder) putBytes(p []byte) {
	b.grow(len(p))
	b.head -= len(p)
	copy(b.buf[b.head:], p)
}

func (b *fbBuilder) putUint8(v uint8) {
	b.prep(1, 0)
	b.putBytes([]byte{v})
}

func (b *fbBuilder) putUint16(v uint16) {
	b.prep(2, 0)
	b.putBytes(binary.LittleEndian.AppendUint16(nil, v))
}

func (b *fbBuilder) putUint32(v uint32) {
	b.prep(4, 0)
	b.putBytes(binary.LittleEndian.AppendUint32(nil, v))
}

func (b *fbBuilder) putUint64(v uint64) {
	b.prep(8, 0)
	b.putBytes(binary.LittleEndian.AppendUint64(nil, v))
}

// putOffset writes an offset to an object written earlier.
func (b *fbBuilder) putOffset(off int) {
	b.prep(4, 0)
	b.putBytes(binary.LittleEndian.AppendUint32(nil,
		uint32(b.offset()-off+4)))
}

// createString writes a string and returns its offset.
func (b *fbBuilder) createString(s string) int {
	b.prep(4, len(s)+1)
	b.putBytes(append([]byte(s), 0))
	b.putBytes(binary.LittleEndian.AppendUint32(nil,
		uint32(len(s))))
	return b.offset()
}

// createOffsets writes a vector of offsets and returns its offset.
func (b *fbBuilder) createOffsets(offs []int) int {
	b.prep(4, 4*len(offs))
	for _, off := range slices.Backward(offs) {
		b.putOffset(off)
	}
	b.putUint32(uint32(len(offs)))
	return b.offset()
}

// createPairs writes a vector of structs of two longs -- an Arrow
// FieldNode or Buffer -- and returns its offset.
func (b *fbBuilder) createPairs(pairs [][2]int64) int {
	const pairSize = 16
	b.prep(4, pairSize*len(pairs))
	b.prep(8, pairSize*len(pairs))
	for _, pair := range slices.Backward(pairs) {
		b.putUint64(uint64(pair[1]))
		b.putUint64(uint64(pair[0]))
	}
	b.putUint32(uint32(len(pairs)))
	return b.offset()
}

// startTable starts a table of a given number of fields.
func (b *fbBuilder) startTable(fields int) {
	b.vtable = make([]int, fields)
	b.start = b.offset()
}

// slot records that a field has just been written.
func (b *fbBuilder) slot(i int) { b.vtable[i] = b.offset() }

func (b *fbBuilder) addUint8(i int, v uint8) {
	b.putUint8(v)
	b.slot(i)
}

func (b *fbBuilder) addInt16(i int, v int16) {
	b.putUint16(uint16(v))
	b.slot(i)
}

func (b *fbBuilder) addInt32(i int, v int32) {
	b.putUint32(uint32(v))
	b.slot(i)
}

func (b *fbBuilder) addInt64(i int, v int64) {
	b.putUint64(uint64(v))
	b.slot(i)
}

func (b *fbBuilder) addOffset(i, off int) {
	b.putOffset(off)
	b.slot(i)
}

// endTable writes the table's vtable and returns the table's offset.
func (b *fbBuilder) endTable() int {
	b.putUint32(0) // the table's offset to its vtable, patched below
	table := b.offset()
	for _, field := range slices.Backward(b.vtable) {
		off := 0
		if field != 0 {
			off = table - field
		}
		b.putUint16(uint16(off))
	}
	const vtableHeader = 4
	b.putUint16(uint16(table - b.start))
	b.putUint16(uint16(2*len(b.vtable) + vtableHeader))
	vtable := b.offset()
	at := len(b.buf) - table
	binary.LittleEndian.PutUint32(b.buf[at:],
		uint32(int32(vtable-table)))
	b.vtable = nil
	return table
}

// finish writes the offset of the root table, and returns the bytes.
func (b *fbBuilder) finish(root int) []byte {
	b.prep(b.minAlign, 4)
	b.putOffset(root)
	return b.buf[b.head:]
}

// End flatbuffer.go
