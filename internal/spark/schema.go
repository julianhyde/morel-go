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
	"strconv"
	"strings"

	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/types"
)

// The Spark schema of a Morel type, as the JSON that a
// LocalRelation carries.
//
// Spark to Morel loses information -- byte, short, int and long all
// arrive as int -- so Morel to Spark must choose, and it chooses the
// widest type that Morel can hold without loss: int becomes
// "integer" and real becomes "double".
//
// Nullability is the other difference. A Spark column is nullable
// or not; a Morel type says so by being an option. So int is a
// non-nullable integer and "int option" a nullable one, and a type
// that is an option of an option cannot be expressed.

// The names of the Morel types that a column can have.
const (
	boolType    = "bool"
	decimalType = "decimal"
	intType     = "int"
	optionType  = "option"
	realType    = "real"
	stringType  = "string"
)

const (
	// decimalPrecision is the precision of an uploaded decimal
	// column: Spark's own default, 38.
	decimalPrecision = 38
	// decimalScale is the scale of an uploaded decimal column:
	// Spark's own default, 18.
	decimalScale = 18
)

// unsupportedType is the error that a Morel type with no Spark
// equivalent raises.
func unsupportedType(message string) error {
	return eval.NewSparkException("UNSUPPORTED_TYPE", message)
}

// SchemaJSON returns the JSON schema of a row type, as
// LocalRelation's schema wants it: an object whose fields are the
// record's fields, in the order the record type holds them, which
// is sorted by name.
func SchemaJSON(rowType types.Type) (string, error) {
	record, isRecord := rowType.(*types.Record)
	if !isRecord {
		return "", unsupportedType("A relation's rows must be " +
			"records, and these are " + rowType.String())
	}
	var b strings.Builder
	err := structJSON(record, &b)
	return b.String(), err
}

func structJSON(record *types.Record, b *strings.Builder) error {
	b.WriteString(`{"fields":[`)
	for i, f := range record.Fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"metadata":{},"name":`)
		quoteJSON(f.Label, b)
		valueType, isNullable, err := nullable(f.Type)
		if err != nil {
			return err
		}
		b.WriteString(`,"nullable":` + strconv.FormatBool(isNullable) +
			`,"type":`)
		err = typeJSON(valueType, b)
		if err != nil {
			return err
		}
		b.WriteByte('}')
	}
	b.WriteString(`],"type":"struct"}`)
	return nil
}

func typeJSON(t types.Type, b *strings.Builder) error {
	switch t := t.(type) {
	case *types.Record:
		return structJSON(t, b)
	case *types.List:
		valueType, isNullable, err := nullable(t.Elem)
		if err != nil {
			return err
		}
		b.WriteString(`{"containsNull":` + strconv.FormatBool(isNullable) +
			`,"elementType":`)
		err = typeJSON(valueType, b)
		if err != nil {
			return err
		}
		b.WriteString(`,"type":"array"}`)
		return nil
	}
	s, err := primitiveName(t)
	if err != nil {
		return err
	}
	quoteJSON(s, b)
	return nil
}

// quoteJSON appends a JSON string literal.
func quoteJSON(s string, b *strings.Builder) {
	b.WriteByte('"')
	for i := range len(s) {
		c := s[i]
		if c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	b.WriteByte('"')
}

// primitiveName is the Spark name of a Morel type that is not a
// record or a list.
func primitiveName(t types.Type) (string, error) {
	// lint: sort until '^\t}' where '^\tcase '
	switch t.String() {
	case boolType:
		return "boolean", nil
	case decimalType:
		// Morel's decimal has no precision or scale of its own, so a
		// column of them has to be given one: the width Spark itself
		// gives a column it infers from a BigDecimal. A value too
		// wide for it is refused rather than rounded.
		return "decimal(" + strconv.Itoa(decimalPrecision) + "," +
			strconv.Itoa(decimalScale) + ")", nil
	case intType:
		return "integer", nil
	case realType:
		return "double", nil
	case stringType:
		return stringType, nil
	}
	return "", noEquivalent(t)
}

func noEquivalent(t types.Type) error {
	return unsupportedType("Morel type " + t.String() +
		" has no Spark equivalent")
}

// nullable returns the type of the values of a column whose Morel
// type is t, and whether the column is nullable: an option is a
// nullable column of the type it wraps. An option of an option has
// no Spark equivalent, since a column is nullable or not, and is
// rejected rather than flattened.
func nullable(t types.Type) (types.Type, bool, error) {
	named, isNamed := t.(*types.Named)
	if !isNamed || named.Name != optionType || len(named.Args) != 1 {
		return t, false, nil
	}
	_, isOption, err := nullable(named.Args[0])
	if err != nil {
		return nil, false, err
	}
	if isOption {
		return nil, false, unsupportedType("Morel type " + t.String() +
			" is an option of an option")
	}
	return named.Args[0], true, nil
}

// End schema.go
