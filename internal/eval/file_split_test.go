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

//nolint:testpackage // white-box: the field splitter is unexported
package eval

import (
	"slices"
	"testing"
)

// TestSplitFields checks how a line of a data file splits into
// fields: a quoted field may hold a comma, a doubled quote in it is
// one quote, and an empty field is a field.
func TestSplitFields(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{"'hello, world',7", []string{"'hello, world'", "7"}},
		{"'it''s',x", []string{"'it's'", "x"}},
		{"1,,NULL,", []string{"1", "", "NULL", ""}},
		{"''", []string{"''"}},
	}
	for _, tt := range tests {
		if got := splitFields(tt.line); !slices.Equal(got, tt.want) {
			t.Errorf("splitFields(%q) = %q, want %q", tt.line, got,
				tt.want)
		}
	}
}
