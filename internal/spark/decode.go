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
	"math"
	"math/big"
	"strconv"

	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/types"
)

// Decoding Arrow record batches, as Spark Connect sends them, into
// Morel values; morel-java's ArrowDecoder.
//
// Decoding is driven by the Morel type: an option type admits a
// null (as NONE), any other type does not, and a null there raises
// "Spark" with class NULL_VALUE. Integers of every width become int
// (raising if a bigint does not fit), floats, doubles and decimals
// become real, and so on.

// DecodeArrow decodes Arrow IPC streams, each with its schema, into
// rows of a Morel type: a record type, or any other type if the row
// has one field.
func DecodeArrow(streams [][]byte, rowType types.Type) ([]eval.Val,
	error,
) {
	var rows []eval.Val
	for _, stream := range streams {
		_, batches, err := readStream(stream)
		if err != nil {
			return nil, err
		}
		for _, batch := range batches {
			rows, err = decodeBatch(batch, rowType, rows)
			if err != nil {
				return nil, err
			}
		}
	}
	return rows, nil
}

func decodeBatch(batch *arrowBatch, rowType types.Type,
	rows []eval.Val,
) ([]eval.Val, error) {
	// Rows are numbered within the result, not within the batch:
	// Spark decides how many batches to send, so a batch-relative
	// number would name a different row depending on how the result
	// arrived.
	offset := len(rows)
	if !isRowRecord(rowType) {
		// A single unnamed column.
		if len(batch.columns) == 0 {
			return nil, eval.NewSparkException("CONNECTION",
				"Result has no columns")
		}
		c := batch.columns[0]
		for r := range batch.length {
			v, err := toValue(c, r, rowType, c.field.name, offset+r)
			if err != nil {
				return nil, err
			}
			rows = append(rows, v)
		}
		return rows, nil
	}
	// Columns are in Spark's order; a record's fields are in Morel's
	// order (sorted by label). Map by name.
	names, columnTypes := columns(rowType)
	byLabel := make([]*arrowColumn, len(names))
	for _, c := range batch.columns {
		for i, name := range names {
			if c.field.name == name {
				byLabel[i] = c
			}
		}
	}
	for i, c := range byLabel {
		if c == nil {
			return nil, eval.NewSparkException("CONNECTION",
				"Result has no column "+names[i])
		}
	}
	for r := range batch.length {
		fields := make([]eval.Val, len(names))
		for i, c := range byLabel {
			v, err := toValue(c, r, columnTypes[i], names[i], offset+r)
			if err != nil {
				return nil, err
			}
			fields[i] = v
		}
		rows = append(rows, fields)
	}
	return rows, nil
}

// toValue converts the value of a column at a row to a Morel value of
// the given type.
func toValue(c *arrowColumn, i int, t types.Type, column string,
	row int,
) (eval.Val, error) {
	valueType, isOption, err := nullable(t)
	if err != nil {
		return nil, err
	}
	if c.isNull(i) {
		if isOption {
			return eval.NoneVal, nil
		}
		return nil, eval.NewSparkException("NULL_VALUE",
			"Null in column "+column+" of row "+strconv.Itoa(row)+
				", whose type "+t.String()+" has no room for it")
	}
	if isOption {
		v, err := toValue(c, i, valueType, column, row)
		if err != nil {
			return nil, err
		}
		return eval.SomeVal(v), nil
	}
	switch t := t.(type) {
	case *types.Record:
		return structValue(c, i, t, column, row)
	case *types.List:
		return listValue(c, i, t, column, row)
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch t.String() {
	case "char":
		if s, ok := c.text(i); ok {
			if s == "" {
				return int32(0), nil
			}
			return []rune(s)[0], nil
		}
	case "unit":
		return core.Unit{}, nil
	case boolType:
		if c.field.typeID == arrowBool {
			return bit(c.buffers[0], i), nil
		}
	case decimalType:
		return decimalValue(c, i, column, row)
	case intType:
		if n, ok := c.integer(i); ok {
			if n < math.MinInt32 || n > math.MaxInt32 {
				return nil, eval.NewSparkException("CAST_OVERFLOW",
					"Value "+strconv.FormatInt(n, 10)+" in column "+column+
						" of row "+strconv.Itoa(row)+" does not fit in int")
			}
			return int32(n), nil
		}
	case realType:
		if f, ok := c.real(i); ok {
			return float32(f), nil
		}
	case stringType:
		if s, ok := c.text(i); ok {
			return s, nil
		}
	}
	return nil, unsupportedType("Cannot decode " + arrowTypeName(c.field) +
		" in column " + column + " as " + t.String())
}

func structValue(c *arrowColumn, i int, t *types.Record, column string,
	row int,
) (eval.Val, error) {
	if c.field.typeID != arrowStruct {
		return nil, unsupportedType("Cannot decode " +
			arrowTypeName(c.field) + " in column " + column + " as " +
			t.String())
	}
	values := make([]eval.Val, len(t.Fields))
	for k, f := range t.Fields {
		var child *arrowColumn
		for _, cc := range c.children {
			if cc.field.name == f.Label {
				child = cc
			}
		}
		name := column + "." + f.Label
		if child == nil {
			if _, isOption, _ := nullable(f.Type); isOption {
				values[k] = eval.NoneVal
				continue
			}
			return nil, eval.NewSparkException("NULL_VALUE",
				"Null in column "+name+" of row "+strconv.Itoa(row)+
					", whose type "+f.Type.String()+" has no room for it")
		}
		v, err := toValue(child, i, f.Type, name, row)
		if err != nil {
			return nil, err
		}
		values[k] = v
	}
	return values, nil
}

func listValue(c *arrowColumn, i int, t *types.List, column string,
	row int,
) (eval.Val, error) {
	//exhaustive:ignore
	switch c.field.typeID {
	case arrowBinary, arrowLargeBinary:
		// binary -> word list
		b, _ := c.binary(i)
		words := make([]eval.Val, len(b))
		for k, x := range b {
			words[k] = uint64(x)
		}
		return words, nil
	case arrowList, arrowLargeList:
		start, end, ok := c.span(i)
		if !ok || len(c.children) != 1 {
			break
		}
		values := make([]eval.Val, 0, end-start)
		for k := start; k < end; k++ {
			v, err := toValue(c.children[0], k, t.Elem, column, row)
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		return values, nil
	}
	return nil, unsupportedType("Cannot decode " + arrowTypeName(c.field) +
		" in column " + column + " as " + t.String())
}

// decimalValue converts a decimal, which has the precision its
// column declares, to a Morel decimal, which has 34 digits: rounded
// to those as half-even, and refused if it is too large for them.
func decimalValue(c *arrowColumn, i int, column string, row int) (
	eval.Val, error,
) {
	var unscaled *big.Int
	scale := 0
	//exhaustive:ignore
	switch c.field.typeID {
	case arrowDecimal:
		if c.field.bitWidth != decimalWidth {
			return nil, unsupportedType("Cannot decode " +
				arrowTypeName(c.field) + " in column " + column +
				" as decimal")
		}
		b := c.buffers[0]
		if decimalBytes*(i+1) > len(b) {
			return nil, errArrow(errFlatbuffer)
		}
		be := make([]byte, decimalBytes)
		for k := range decimalBytes {
			be[k] = b[decimalBytes*i+decimalBytes-1-k]
		}
		unscaled = new(big.Int).SetBytes(be)
		if be[0]&0x80 != 0 {
			unscaled.Sub(unscaled,
				new(big.Int).Lsh(big.NewInt(1), decimalWidth))
		}
		scale = int(c.field.scale)
	default:
		n, ok := c.integer(i)
		if !ok {
			return nil, unsupportedType("Cannot decode " +
				arrowTypeName(c.field) + " in column " + column +
				" as decimal")
		}
		unscaled = big.NewInt(n)
	}
	d, ok := eval.DecimalOf(unscaled, -scale)
	if !ok {
		return nil, eval.NewSparkException("CAST_OVERFLOW",
			"Value "+unscaled.String()+"E-"+strconv.Itoa(scale)+
				" in column "+column+" of row "+strconv.Itoa(row)+
				" is too large for a Morel decimal")
	}
	return d, nil
}

func bit(b []byte, i int) bool {
	return i/8 < len(b) && b[i/8]&(1<<(i%8)) != 0
}

// integer returns an integer value of any width.
func (c *arrowColumn) integer(i int) (int64, bool) {
	if c.field.typeID != arrowInt || len(c.buffers) == 0 {
		return 0, false
	}
	b := c.buffers[0]
	w := int(c.field.bitWidth) / 8
	if w*(i+1) > len(b) {
		return 0, false
	}
	p := b[w*i:]
	// lint: sort until '^\t}' where '^\tcase '
	switch w {
	case 1:
		return int64(int8(p[0])), true
	case 2:
		return int64(int16(binary.LittleEndian.Uint16(p))), true
	case 4:
		return int64(int32(binary.LittleEndian.Uint32(p))), true
	case 8:
		return int64(binary.LittleEndian.Uint64(p)), true
	}
	return 0, false
}

// real returns a floating-point value, or a decimal one as a double.
func (c *arrowColumn) real(i int) (float64, bool) {
	//exhaustive:ignore
	// lint: sort until '^\t}' where '^\tcase '
	switch c.field.typeID {
	case arrowDecimal:
		v, err := decimalValue(c, i, "", 0)
		d, isDecimal := v.(eval.Decimal)
		if err != nil || !isDecimal {
			return 0, false
		}
		f, err := strconv.ParseFloat(d.ToString('-'), 64)
		return f, err == nil
	case arrowFloatingPoint:
		b := c.buffers[0]
		if c.field.precision == precisionSingle {
			if 4*(i+1) > len(b) {
				return 0, false
			}
			return float64(math.Float32frombits(
				binary.LittleEndian.Uint32(b[4*i:]))), true
		}
		if alignment*(i+1) > len(b) {
			return 0, false
		}
		return math.Float64frombits(
			binary.LittleEndian.Uint64(b[alignment*i:])), true
	case arrowInt:
		n, ok := c.integer(i)
		return float64(n), ok
	}
	return 0, false
}

// span returns the range of a variable-width value: of its bytes, or
// of its elements in the child.
func (c *arrowColumn) span(i int) (int, int, bool) {
	if len(c.buffers) == 0 {
		return 0, 0, false
	}
	offsets := c.buffers[0]
	//exhaustive:ignore
	switch c.field.typeID {
	case arrowLargeBinary, arrowLargeUtf8, arrowLargeList:
		if alignment*(i+2) > len(offsets) {
			return 0, 0, false
		}
		start := binary.LittleEndian.Uint64(offsets[alignment*i:])
		end := binary.LittleEndian.Uint64(offsets[alignment*(i+1):])
		return int(start), int(end), start <= end
	}
	if 4*(i+2) > len(offsets) {
		return 0, 0, false
	}
	start := binary.LittleEndian.Uint32(offsets[4*i:])
	end := binary.LittleEndian.Uint32(offsets[4*(i+1):])
	return int(start), int(end), start <= end
}

// binary returns the bytes of a binary or string value.
func (c *arrowColumn) binary(i int) ([]byte, bool) {
	start, end, ok := c.span(i)
	if !ok || len(c.buffers) < 2 || end > len(c.buffers[1]) {
		return nil, false
	}
	return c.buffers[1][start:end], true
}

// text returns a string value.
func (c *arrowColumn) text(i int) (string, bool) {
	//exhaustive:ignore
	switch c.field.typeID {
	case arrowUtf8, arrowLargeUtf8:
		b, ok := c.binary(i)
		return string(b), ok
	}
	return "", false
}

// arrowTypeName names an Arrow type, for messages.
func arrowTypeName(f *arrowField) string {
	//exhaustive:ignore
	// lint: sort until '^\t}' where '^\tcase '
	switch f.typeID {
	case arrowBinary, arrowLargeBinary:
		return "binary"
	case arrowBool:
		return "bool"
	case arrowDate:
		return "date"
	case arrowDecimal:
		return "decimal"
	case arrowFloatingPoint:
		return "float"
	case arrowInt:
		return "int" + strconv.Itoa(int(f.bitWidth))
	case arrowList, arrowLargeList:
		return "list"
	case arrowNull:
		return "null"
	case arrowStruct:
		return "struct"
	case arrowTimestamp:
		return "timestamp"
	case arrowUtf8, arrowLargeUtf8:
		return "utf8"
	}
	return "type " + strconv.Itoa(int(f.typeID))
}

// End decode.go
