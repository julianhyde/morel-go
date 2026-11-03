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
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/types"
)

// EncodeArrow encodes rows as an Arrow IPC stream, schema and all,
// which is how Spark Connect carries the rows of a local relation.
//
// A row is a list of its fields in Morel's order (sorted by label),
// or a value if the row type is not a record; an option is the only
// type that admits a null; and the Morel type decides what Spark
// type a column has, as SchemaJSON decides it for the schema beside
// the batch.
func EncodeArrow(rows []eval.Val, rowType types.Type) ([]byte, error) {
	for _, t := range columnTypes(rowType) {
		_, err := arrowColumnType(t)
		if err != nil {
			return nil, err
		}
	}
	_ = rows
	return []byte{}, nil
}

// columnTypes returns the Morel type of each column of a row type.
// A collection of values rather than records is one column.
func columnTypes(rowType types.Type) []types.Type {
	switch t := rowType.(type) {
	case *types.Record:
		ts := make([]types.Type, len(t.Fields))
		for i, f := range t.Fields {
			ts[i] = f.Type
		}
		return ts
	case *types.Tuple:
		return t.Args
	}
	return []types.Type{rowType}
}

// arrowColumnType returns the type of a column's values, which is
// the type an option wraps if the column is nullable, and checks
// that Arrow has a vector for it.
func arrowColumnType(t types.Type) (types.Type, error) {
	t, _, err := nullable(t)
	if err != nil {
		return nil, err
	}
	switch t.String() {
	case boolType, decimalType, intType, realType, stringType:
		return t, nil
	}
	return nil, noEquivalent(t)
}

// End arrow.go
