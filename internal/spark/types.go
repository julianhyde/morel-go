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
	"slices"
	"strconv"
	"strings"

	"github.com/hydromatic/morel-go/internal/spark/connect"
	"github.com/hydromatic/morel-go/internal/types"
)

// Spark types as Morel types; morel-java's SparkTypes.
//
// A nullable type maps to option, except at the top level of a row,
// where Spark's flag is true of every column of a stored table and
// therefore says nothing; there, a column is an option only if
// something declared it so.

// RowType returns the Morel type of a row of the given struct type,
// where the columns named in nullable are declared nullable and
// therefore map to option; "*" declares every column.
func RowType(sys *types.System, schema *connect.DataType,
	nullable map[string]bool,
) (types.Type, error) {
	s := schema.GetStruct()
	if s == nil {
		return nil, unsupportedType("Expected a struct, got " +
			DescribeType(schema))
	}
	return structType(sys, s, false, nullable)
}

func structType(sys *types.System, s *connect.DataType_Struct,
	strict bool, nullable map[string]bool,
) (types.Type, error) {
	fields := make([]types.Field, 0, len(s.GetFields()))
	seen := map[string]bool{}
	for _, f := range s.GetFields() {
		name := f.GetName()
		if seen[name] {
			return nil, unsupportedType("Duplicate field name " + name)
		}
		seen[name] = true
		// One level down -- inside a struct, an array or a map --
		// Spark's flag is recorded rather than inferred, so believe
		// it; at the top level of a row it is true of everything, so
		// believe the declaration instead.
		isNullable := f.GetNullable()
		if !strict {
			isNullable = nullable["*"] || nullable[strings.ToLower(name)]
		}
		t, err := toType(sys, f.GetDataType(), isNullable)
		if err != nil {
			return nil, err
		}
		fields = append(fields, types.Field{Label: name, Type: t})
	}
	slices.SortFunc(fields, func(a, b types.Field) int {
		switch {
		case types.LabelLess(a.Label, b.Label):
			return -1
		case types.LabelLess(b.Label, a.Label):
			return 1
		}
		return 0
	})
	return sys.Record(fields), nil
}

// toType returns the Morel type of a Spark type, an option if the
// value may be null.
func toType(sys *types.System, t *connect.DataType, isNullable bool,
) (types.Type, error) {
	m, err := kindType(sys, t)
	if err != nil {
		return nil, err
	}
	if isNullable {
		return sys.Named(optionType, m), nil
	}
	return m, nil
}

func kindType(sys *types.System, t *connect.DataType) (types.Type,
	error,
) {
	// lint: sort until '^\t}' where '^\tcase '
	switch k := t.GetKind().(type) {
	case *connect.DataType_Array_:
		elem, err := toType(sys, k.Array.GetElementType(),
			k.Array.GetContainsNull())
		if err != nil {
			return nil, err
		}
		return sys.List(elem), nil
	case *connect.DataType_Binary_:
		return sys.List(sys.Word), nil
	case *connect.DataType_Boolean_:
		return sys.Bool, nil
	case *connect.DataType_Byte_, *connect.DataType_Short_,
		*connect.DataType_Integer_, *connect.DataType_Long_:
		return sys.Int, nil
	case *connect.DataType_Date_, *connect.DataType_TimestampNtz:
		return sys.Named("date"), nil
	case *connect.DataType_Decimal_:
		return sys.Named(decimalType), nil
	case *connect.DataType_Float_, *connect.DataType_Double_:
		return sys.Real, nil
	case *connect.DataType_String_, *connect.DataType_Char_,
		*connect.DataType_VarChar_:
		return sys.String, nil
	case *connect.DataType_Struct_:
		return structType(sys, k.Struct, true, nil)
	case *connect.DataType_Timestamp_:
		return sys.Named("time"), nil
	}
	return nil, unsupportedType("Spark type " + DescribeType(t) +
		" has no Morel equivalent")
}

// DescribeType describes a Spark type, for error messages, as
// "map<string,integer>".
func DescribeType(t *connect.DataType) string {
	// lint: sort until '^\t}' where '^\tcase '
	switch k := t.GetKind().(type) {
	case *connect.DataType_Array_:
		return "array<" + DescribeType(k.Array.GetElementType()) + ">"
	case *connect.DataType_Decimal_:
		return "decimal(" + strconv.Itoa(int(k.Decimal.GetPrecision())) +
			"," + strconv.Itoa(int(k.Decimal.GetScale())) + ")"
	case *connect.DataType_Map_:
		return "map<" + DescribeType(k.Map.GetKeyType()) + "," +
			DescribeType(k.Map.GetValueType()) + ">"
	case *connect.DataType_Struct_:
		parts := make([]string, len(k.Struct.GetFields()))
		for i, f := range k.Struct.GetFields() {
			parts[i] = f.GetName() + ":" + DescribeType(f.GetDataType())
		}
		return "struct<" + strings.Join(parts, ",") + ">"
	}
	fd := t.ProtoReflect().WhichOneof(
		t.ProtoReflect().Descriptor().Oneofs().ByName("kind"))
	if fd == nil {
		return "kind_not_set"
	}
	return string(fd.Name())
}

// End types.go
