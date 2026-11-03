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
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The protobuf text format, as protobuf-java's
// "TextFormat.printer()" writes it.
//
// Go's own printer, prototext, deliberately varies its whitespace
// from run to run, so that nobody depends on it; a plan's text is
// compared byte for byte with what morel-java prints, so it is
// written here instead. A field is printed if it is set -- a
// singular scalar without presence only if it is not its default --
// in field-number order, two spaces per level of nesting, a string
// or bytes value escaped as Java escapes it: C escapes, and octal
// for every byte that is not printable ASCII.

// textIndent is the indentation of one level of nesting.
const textIndent = "  "

// The widths of Java's float and double, which format alike.
const (
	float32Bits = 32
	float64Bits = 64
)

// Text returns a message in the protobuf text format.
func Text(m proto.Message) string {
	var b strings.Builder
	printMessage(&b, m.ProtoReflect(), 0)
	return b.String()
}

func printMessage(b *strings.Builder, m protoreflect.Message,
	depth int,
) {
	fields := m.Descriptor().Fields()
	sorted := make([]protoreflect.FieldDescriptor, fields.Len())
	for i := range fields.Len() {
		sorted[i] = fields.Get(i)
	}
	slices.SortFunc(sorted, func(a, b protoreflect.FieldDescriptor) int {
		return int(a.Number()) - int(b.Number())
	})
	for _, fd := range sorted {
		switch {
		case fd.IsList():
			list := m.Get(fd).List()
			for i := range list.Len() {
				printField(b, fd, list.Get(i), depth)
			}
		case fd.IsMap():
			printMap(b, fd, m.Get(fd).Map(), depth)
		case m.Has(fd):
			printField(b, fd, m.Get(fd), depth)
		}
	}
}

// printMap prints a map as Java does, as a repeated message of key
// and value, here in key order.
func printMap(b *strings.Builder, fd protoreflect.FieldDescriptor,
	m protoreflect.Map, depth int,
) {
	var keys []protoreflect.MapKey
	m.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
		keys = append(keys, k)
		return true
	})
	slices.SortFunc(keys, func(a, b protoreflect.MapKey) int {
		return strings.Compare(a.String(), b.String())
	})
	indent := strings.Repeat(textIndent, depth)
	for _, k := range keys {
		b.WriteString(indent + string(fd.Name()) + " {\n")
		printField(b, fd.MapKey(), k.Value(), depth+1)
		printField(b, fd.MapValue(), m.Get(k), depth+1)
		b.WriteString(indent + "}\n")
	}
}

func printField(b *strings.Builder, fd protoreflect.FieldDescriptor,
	v protoreflect.Value, depth int,
) {
	indent := strings.Repeat(textIndent, depth)
	b.WriteString(indent)
	name := string(fd.Name())
	if fd.Kind() == protoreflect.GroupKind {
		name = string(fd.Message().Name())
	}
	b.WriteString(name)
	if fd.Kind() == protoreflect.MessageKind ||
		fd.Kind() == protoreflect.GroupKind {
		b.WriteString(" {\n")
		printMessage(b, v.Message(), depth+1)
		b.WriteString(indent + "}\n")
		return
	}
	b.WriteString(": ")
	b.WriteString(scalarText(fd, v))
	b.WriteString("\n")
}

func scalarText(fd protoreflect.FieldDescriptor,
	v protoreflect.Value,
) string {
	//exhaustive:ignore
	// lint: sort until '^\t}' where '^\tcase '
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return strconv.FormatBool(v.Bool())
	case protoreflect.BytesKind:
		return `"` + escapeBytes(v.Bytes()) + `"`
	case protoreflect.DoubleKind:
		return javaReal(v.Float(), float64Bits)
	case protoreflect.EnumKind:
		n := v.Enum()
		if e := fd.Enum().Values().ByNumber(n); e != nil {
			return string(e.Name())
		}
		return strconv.Itoa(int(n))
	case protoreflect.FloatKind:
		return javaReal(v.Float(), float32Bits)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind,
		protoreflect.Sfixed32Kind, protoreflect.Int64Kind,
		protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(v.Int(), 10)
	case protoreflect.StringKind:
		return `"` + escapeBytes([]byte(v.String())) + `"`
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(v.Uint(), 10)
	default:
		return v.String()
	}
}

// escapeBytes escapes bytes as protobuf-java's TextFormat does.
func escapeBytes(bs []byte) string {
	var b strings.Builder
	for _, c := range bs {
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\'':
			b.WriteString(`\'`)
		case '\\':
			b.WriteString(`\\`)
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\v':
			b.WriteString(`\v`)
		default:
			if c >= ' ' && c <= '~' {
				b.WriteByte(c)
			} else {
				fmt.Fprintf(&b, "\\%03o", c)
			}
		}
	}
	return b.String()
}

// javaReal formats a float or double as Java's "Float.toString" and
// "Double.toString" do: the shortest digits that identify the value,
// in plain notation from 10^-3 up to 10^7 and in computerized
// scientific notation ("1.0E10") outside it, always with a digit
// after the point.
func javaReal(f float64, bits int) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	sign := ""
	if math.Signbit(f) {
		sign = "-"
		f = -f
	}
	if f == 0 {
		return sign + "0.0"
	}
	// "d.ddde±x": the digits, and the exponent of the first.
	s := strconv.FormatFloat(f, 'e', -1, bits)
	mantissa, exponent, _ := strings.Cut(s, "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	exp, _ := strconv.Atoi(exponent)
	const plainMin, plainMax = -3, 7
	if exp < plainMin || exp >= plainMax {
		frac := digits[1:]
		if frac == "" {
			frac = "0"
		}
		return sign + digits[:1] + "." + frac + "E" + strconv.Itoa(exp)
	}
	if exp < 0 {
		return sign + "0." + strings.Repeat("0", -exp-1) + digits
	}
	if len(digits) <= exp+1 {
		return sign + digits + strings.Repeat("0", exp+1-len(digits)) +
			".0"
	}
	return sign + digits[:exp+1] + "." + digits[exp+1:]
}

// End textformat.go
