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
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/big"
	"strconv"

	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/types"
)

// The Arrow IPC stream format, as much of it as Spark Connect uses:
// a schema message, record batches, and an end-of-stream marker.
// Each message is a flatbuffer, "Message.fbs", followed by a body of
// buffers. This is morel-java's ArrowEncoder and ArrowDecoder, which
// use the Arrow library; this port does the format by hand.

// Message header types, of the union MessageHeader.
const (
	headerSchema      = 1
	headerRecordBatch = 3
)

// Type ids, of the union Type in Schema.fbs.
const (
	arrowNull          = 1
	arrowInt           = 2
	arrowFloatingPoint = 3
	arrowBinary        = 4
	arrowUtf8          = 5
	arrowBool          = 6
	arrowDecimal       = 7
	arrowDate          = 8
	arrowTimestamp     = 10
	arrowList          = 12
	arrowStruct        = 13
	arrowLargeBinary   = 19
	arrowLargeUtf8     = 20
	arrowLargeList     = 21
)

const (
	// metadataV5 is the version of the metadata this writes.
	metadataV5 = 4
	// continuation marks the start of a message.
	continuation = 0xFFFFFFFF
	// precisionDouble is FloatingPoint's DOUBLE.
	precisionDouble = 2
	// precisionSingle is FloatingPoint's SINGLE.
	precisionSingle = 1
	// alignment is the alignment of a message and of each buffer.
	alignment = 8
	// decimalBytes is the width of a decimal128 value.
	decimalBytes = 16
	// int32Bits is the width of Morel's int.
	int32Bits = 32
	// decimalWidth is the default bit width of a decimal.
	decimalWidth = 128
)

// errArrow is the error of Arrow data that cannot be read.
func errArrow(err error) error {
	return eval.NewSparkException("CONNECTION",
		"Error reading Arrow data: "+err.Error())
}

// arrowField is a field of an Arrow schema.
type arrowField struct {
	name     string
	nullable bool
	typeID   uint8
	// bitWidth and signed describe an Int; precision a FloatingPoint
	// (or a Decimal, with scale); unit a Date or Timestamp.
	bitWidth  int32
	signed    bool
	precision int32
	scale     int32
	unit      int16
	children  []*arrowField
}

// writeMessage writes one encapsulated message: the continuation
// marker, the metadata's length, the metadata padded to a multiple of
// 8, and the body.
func writeMessage(w *bytes.Buffer, metadata, body []byte) {
	const prefix = 8
	padded := (len(metadata) + prefix + alignment - 1) / alignment *
		alignment
	_ = binary.Write(w, binary.LittleEndian, uint32(continuation))
	_ = binary.Write(w, binary.LittleEndian,
		uint32(padded-prefix))
	w.Write(metadata)
	w.Write(make([]byte, padded-prefix-len(metadata)))
	w.Write(body)
}

// message builds the flatbuffer of a Message whose header has been
// written to the builder.
func message(b *fbBuilder, headerType uint8, header int,
	bodyLength int64,
) []byte {
	b.startTable(5)
	b.addInt64(3, bodyLength)
	b.addOffset(2, header)
	b.addInt16(0, metadataV5)
	b.addUint8(1, headerType)
	return b.finish(b.endTable())
}

// EncodeArrow encodes rows as an Arrow IPC stream, schema and all,
// which is how Spark Connect carries the rows of a local relation.
//
// A row is a list of its fields in Morel's order (sorted by label),
// or a value if the row type is not a record; an option is the only
// type that admits a null; and the Morel type decides what Spark
// type a column has, as SchemaJSON decides it for the schema beside
// the batch.
func EncodeArrow(rows []eval.Val, rowType types.Type) ([]byte, error) {
	names, columnTypes := columns(rowType)
	valueTypes := make([]types.Type, len(columnTypes))
	nullables := make([]bool, len(columnTypes))
	for i, t := range columnTypes {
		v, isNullable, err := nullable(t)
		if err != nil {
			return nil, err
		}
		_, err = arrowTypeID(v)
		if err != nil {
			return nil, err
		}
		valueTypes[i], nullables[i] = v, isNullable
	}
	var out bytes.Buffer
	writeMessage(&out, schemaMessage(names, valueTypes, nullables), nil)
	var body bytes.Buffer
	var nodes, buffers [][2]int64
	addBuffer := func(p []byte) {
		buffers = append(buffers,
			[2]int64{int64(body.Len()), int64(len(p))})
		body.Write(p)
		if pad := (-body.Len()) & (alignment - 1); pad > 0 {
			body.Write(make([]byte, pad))
		}
	}
	hasColumns := isRowRecord(rowType)
	for c, t := range valueTypes {
		values := make([]eval.Val, len(rows))
		for r, row := range rows {
			v := row
			if hasColumns {
				fields, _ := row.([]eval.Val)
				v = fields[c]
			}
			values[r] = v
		}
		validity, nulls, data, err := encodeColumn(values, t,
			nullables[c])
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, [2]int64{int64(len(rows)), int64(nulls)})
		addBuffer(validity)
		for _, d := range data {
			addBuffer(d)
		}
	}
	b := newFBBuilder()
	nodesOff := b.createPairs(nodes)
	buffersOff := b.createPairs(buffers)
	b.startTable(5)
	b.addInt64(0, int64(len(rows)))
	b.addOffset(1, nodesOff)
	b.addOffset(2, buffersOff)
	batch := b.endTable()
	writeMessage(&out, message(b, headerRecordBatch, batch,
		int64(body.Len())), body.Bytes())
	_ = binary.Write(&out, binary.LittleEndian, uint32(continuation))
	_ = binary.Write(&out, binary.LittleEndian, uint32(0))
	return out.Bytes(), nil
}

// isRowRecord reports whether a row type has columns of its own: a
// record or a tuple. A collection of ints is one column, not none.
func isRowRecord(rowType types.Type) bool {
	switch rowType.(type) {
	case *types.Record, *types.Tuple:
		return true
	}
	return false
}

// columns returns the names and Morel types of the columns of a row
// type. A collection of values rather than records has one column,
// which Spark names, as it names the one column of a scalar result.
func columns(rowType types.Type) ([]string, []types.Type) {
	switch t := rowType.(type) {
	case *types.Record:
		names := make([]string, len(t.Fields))
		ts := make([]types.Type, len(t.Fields))
		for i, f := range t.Fields {
			names[i], ts[i] = f.Label, f.Type
		}
		return names, ts
	case *types.Tuple:
		return labels(t), t.Args
	}
	return []string{"it"}, []types.Type{rowType}
}

// arrowTypeID returns the Arrow type of a Morel type that a column's
// values have.
func arrowTypeID(t types.Type) (uint8, error) {
	// lint: sort until '^\t}' where '^\tcase '
	switch t.String() {
	case boolType:
		return arrowBool, nil
	case decimalType:
		return arrowDecimal, nil
	case intType:
		return arrowInt, nil
	case realType:
		return arrowFloatingPoint, nil
	case stringType:
		return arrowUtf8, nil
	}
	return 0, noEquivalent(t)
}

// schemaMessage returns the metadata of the schema of a batch.
func schemaMessage(names []string, valueTypes []types.Type,
	nullables []bool,
) []byte {
	b := newFBBuilder()
	fields := make([]int, len(names))
	for i, name := range names {
		id, _ := arrowTypeID(valueTypes[i])
		typ := typeTable(b, id)
		nameOff := b.createString(name)
		children := b.createOffsets(nil)
		b.startTable(7)
		b.addOffset(0, nameOff)
		b.addOffset(3, typ)
		b.addOffset(5, children)
		if nullables[i] {
			b.addUint8(1, 1)
		}
		b.addUint8(2, id)
		fields[i] = b.endTable()
	}
	fieldsOff := b.createOffsets(fields)
	b.startTable(4)
	b.addOffset(1, fieldsOff)
	schema := b.endTable()
	return message(b, headerSchema, schema, 0)
}

// typeTable writes the table of an Arrow type.
func typeTable(b *fbBuilder, id uint8) int {
	//exhaustive:ignore
	// lint: sort until '^\t}' where '^\tcase '
	switch id {
	case arrowDecimal:
		b.startTable(3)
		b.addInt32(0, decimalPrecision)
		b.addInt32(1, decimalScale)
		b.addInt32(2, decimalWidth)
	case arrowFloatingPoint:
		b.startTable(1)
		b.addInt16(0, precisionDouble)
	case arrowInt:
		b.startTable(2)
		b.addInt32(0, int32Bits)
		b.addUint8(1, 1)
	default:
		b.startTable(0)
	}
	return b.endTable()
}

// encodeColumn encodes the values of one column: its validity
// bitmap, its count of nulls, and its other buffers.
func encodeColumn(values []eval.Val, t types.Type, isNullable bool,
) ([]byte, int, [][]byte, error) {
	n := len(values)
	validity := make([]byte, (n+7)/8)
	nulls := 0
	present := make([]eval.Val, n)
	for i, v := range values {
		if isNullable {
			// An option: NONE is the null, and SOME v is v.
			con, _ := v.(eval.Con)
			if con.Arg == nil {
				nulls++
				continue
			}
			v = con.Arg
		}
		validity[i/8] |= 1 << (i % 8)
		present[i] = v
	}
	if nulls == 0 {
		validity = nil
	}
	var data [][]byte
	// lint: sort until '^\t}' where '^\tcase '
	switch t.String() {
	case boolType:
		bits := make([]byte, (n+7)/8)
		for i, v := range present {
			if b, _ := v.(bool); b {
				bits[i/8] |= 1 << (i % 8)
			}
		}
		data = [][]byte{bits}
	case decimalType:
		buf := make([]byte, decimalBytes*n)
		for i, v := range present {
			if v == nil {
				continue
			}
			d, _ := v.(eval.Decimal)
			err := putDecimal(buf[decimalBytes*i:], d)
			if err != nil {
				return nil, 0, nil, err
			}
		}
		data = [][]byte{buf}
	case intType:
		buf := make([]byte, 0, 4*n)
		for _, v := range present {
			i, _ := v.(int32)
			buf = binary.LittleEndian.AppendUint32(buf,
				uint32(i))
		}
		data = [][]byte{buf}
	case realType:
		buf := make([]byte, 0, alignment*n)
		for _, v := range present {
			f, _ := v.(float32)
			buf = binary.LittleEndian.AppendUint64(buf,
				math.Float64bits(float64(f)))
		}
		data = [][]byte{buf}
	case stringType:
		offsets := make([]byte, 0, 4*(n+1))
		var chars []byte
		offsets = binary.LittleEndian.AppendUint32(offsets, 0)
		for _, v := range present {
			s, _ := v.(string)
			chars = append(chars, s...)
			offsets = binary.LittleEndian.AppendUint32(offsets,
				uint32(len(chars)))
		}
		data = [][]byte{offsets, chars}
	}
	return validity, nulls, data, nil
}

// putDecimal writes a decimal at the scale an uploaded column has.
//
// A Morel decimal carries its own scale, and every value of a column
// has to carry the column's, so each is restated at that scale. A
// value that cannot be -- more fractional digits than the scale, or
// more digits than the precision -- is refused, because the
// alternative is to round it and send something the program did not
// say.
func putDecimal(buf []byte, d eval.Decimal) error {
	width := "(" + strconv.Itoa(decimalPrecision) + ", " +
		strconv.Itoa(decimalScale) + ")"
	unscaled, ok := d.Unscaled(decimalScale)
	if !ok {
		return unsupportedType("Decimal " + d.String() +
			" has more digits after the point than Spark's decimal" +
			width + " can hold")
	}
	if len(new(big.Int).Abs(unscaled).String()) > decimalPrecision {
		return unsupportedType("Decimal " + d.String() +
			" has more digits than Spark's decimal" + width + " can hold")
	}
	// Two's complement, little-endian, in 16 bytes.
	twos := new(big.Int).Set(unscaled)
	if twos.Sign() < 0 {
		twos.Add(twos, new(big.Int).Lsh(big.NewInt(1), decimalWidth))
	}
	be := twos.FillBytes(make([]byte, decimalBytes))
	for i := range decimalBytes {
		buf[i] = be[decimalBytes-1-i]
	}
	return nil
}

// arrowBatch is a record batch: its row count, and a column per
// field of the schema.
type arrowBatch struct {
	length  int
	columns []*arrowColumn
}

// arrowColumn is one column of a batch, or one child of a column.
type arrowColumn struct {
	field    *arrowField
	length   int
	validity []byte
	buffers  [][]byte
	children []*arrowColumn
}

// readStream reads an Arrow IPC stream: its schema, and its batches.
func readStream(data []byte) ([]*arrowField, []*arrowBatch, error) {
	r := bytes.NewReader(data)
	var schema []*arrowField
	var batches []*arrowBatch
	for {
		var length uint32
		err := binary.Read(r, binary.LittleEndian, &length)
		if errors.Is(err, io.EOF) {
			return schema, batches, nil
		}
		if err != nil {
			return nil, nil, errArrow(err)
		}
		if length == continuation {
			err = binary.Read(r, binary.LittleEndian, &length)
			if err != nil {
				return nil, nil, errArrow(err)
			}
		}
		if length == 0 {
			return schema, batches, nil
		}
		metadata := make([]byte, length)
		_, err = io.ReadFull(r, metadata)
		if err != nil {
			return nil, nil, errArrow(err)
		}
		msg, err := fbRoot(metadata)
		if err != nil {
			return nil, nil, errArrow(err)
		}
		body := make([]byte, msg.int64(3, 0))
		_, err = io.ReadFull(r, body)
		if err != nil {
			return nil, nil, errArrow(err)
		}
		header, ok := msg.table(2)
		if !ok {
			return nil, nil, errArrow(errFlatbuffer)
		}
		switch msg.uint8(1, 0) {
		case headerSchema:
			schema = readFields(header.tables(1))
		case headerRecordBatch:
			if schema == nil {
				return nil, nil, errArrow(errors.New("batch before schema"))
			}
			batch, err := readBatch(header, body, schema)
			if err != nil {
				return nil, nil, errArrow(err)
			}
			batches = append(batches, batch)
		default:
			return nil, nil, errArrow(errors.New("unsupported message"))
		}
	}
}

func readFields(tables []fbTable) []*arrowField {
	fields := make([]*arrowField, len(tables))
	for i, t := range tables {
		f := &arrowField{
			name:     t.string(0),
			nullable: t.bool(1),
			typeID:   t.uint8(2, 0),
			children: readFields(t.tables(5)),
		}
		if typ, ok := t.table(3); ok {
			//exhaustive:ignore
			// lint: sort until '^\t\t}' where '^\t\tcase '
			switch f.typeID {
			case arrowDate, arrowTimestamp:
				f.unit = typ.int16(0, 1)
			case arrowDecimal:
				f.precision = typ.int32(0, 0)
				f.scale = typ.int32(1, 0)
				f.bitWidth = typ.int32(2, decimalWidth)
			case arrowFloatingPoint:
				f.precision = int32(typ.int16(0, 0))
			case arrowInt:
				f.bitWidth = typ.int32(0, 0)
				f.signed = typ.bool(1)
			}
		}
		fields[i] = f
	}
	return fields
}

// batchReader takes the nodes and buffers of a batch in order, as
// the fields of the schema consume them.
type batchReader struct {
	body    []byte
	nodes   [][2]int64
	buffers [][2]int64
}

func (r *batchReader) node() ([2]int64, error) {
	if len(r.nodes) == 0 {
		return [2]int64{}, errFlatbuffer
	}
	n := r.nodes[0]
	r.nodes = r.nodes[1:]
	return n, nil
}

func (r *batchReader) buffer() ([]byte, error) {
	if len(r.buffers) == 0 {
		return nil, errFlatbuffer
	}
	b := r.buffers[0]
	r.buffers = r.buffers[1:]
	if b[0] < 0 || b[1] < 0 || b[0]+b[1] > int64(len(r.body)) {
		return nil, errFlatbuffer
	}
	return r.body[b[0] : b[0]+b[1]], nil
}

func readPairs(t fbTable, i int) [][2]int64 {
	start, n := t.vector(i)
	const pairSize = 16
	if n == 0 || t.check(start, pairSize*n) != nil {
		return nil
	}
	pairs := make([][2]int64, n)
	for k := range n {
		p := start + pairSize*k
		pairs[k] = [2]int64{
			int64(binary.LittleEndian.Uint64(t.buf[p:])),
			int64(binary.LittleEndian.Uint64(t.buf[p+8:])),
		}
	}
	return pairs
}

func readBatch(header fbTable, body []byte, schema []*arrowField) (
	*arrowBatch, error,
) {
	if _, compressed := header.table(3); compressed {
		return nil, errors.New("compressed batches are not supported")
	}
	r := &batchReader{
		body:    body,
		nodes:   readPairs(header, 1),
		buffers: readPairs(header, 2),
	}
	batch := &arrowBatch{length: int(header.int64(0, 0))}
	for _, f := range schema {
		c, err := r.column(f)
		if err != nil {
			return nil, err
		}
		batch.columns = append(batch.columns, c)
	}
	return batch, nil
}

// bufferCount is how many buffers a field of a type has, besides its
// validity bitmap.
func bufferCount(typeID uint8) int {
	//exhaustive:ignore
	// lint: sort until '^\t}' where '^\tcase '
	switch typeID {
	case arrowBinary, arrowLargeBinary, arrowLargeUtf8, arrowUtf8:
		return 2
	case arrowList, arrowLargeList:
		return 1
	case arrowStruct:
		return 0
	}
	return 1
}

func (r *batchReader) column(f *arrowField) (*arrowColumn, error) {
	n, err := r.node()
	if err != nil {
		return nil, err
	}
	c := &arrowColumn{field: f, length: int(n[0])}
	if f.typeID != arrowNull {
		c.validity, err = r.buffer()
		if err != nil {
			return nil, err
		}
		if n[1] == 0 {
			// No nulls, whatever the bitmap says.
			c.validity = nil
		}
		for range bufferCount(f.typeID) {
			b, err := r.buffer()
			if err != nil {
				return nil, err
			}
			c.buffers = append(c.buffers, b)
		}
	}
	for _, child := range f.children {
		cc, err := r.column(child)
		if err != nil {
			return nil, err
		}
		c.children = append(c.children, cc)
	}
	return c, nil
}

// isNull reports whether a column's value at a row is null.
func (c *arrowColumn) isNull(i int) bool {
	if c.field.typeID == arrowNull {
		return true
	}
	if c.validity == nil {
		return false
	}
	if i/8 >= len(c.validity) {
		return true
	}
	return c.validity[i/8]&(1<<(i%8)) == 0
}

// End arrow.go
