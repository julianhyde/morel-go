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

//nolint:testpackage // white-box: the record-label encoding is unexported
package compile

import (
	"slices"
	"testing"
)

// TestRecordLabel checks that a record type's labels survive being
// encoded in its term operator and read back, including a label that
// contains the separator or the quote (hydromatic/morel#511).
func TestRecordLabel(t *testing.T) {
	tests := []struct {
		labels []string
		want   string
	}{
		{[]string{"a", "b"}, "record:a:b"},
		{[]string{"a:b", "c"}, "record:`a:b`:c"},
		{[]string{"a`b", "c"}, "record:`a``b`:c"},
		{[]string{"it`s:x", "``"}, "record:`it``s:x`:``````"},
	}
	for _, tt := range tests {
		got := recordLabel(tt.labels)
		if got != tt.want {
			t.Errorf("recordLabel(%q) = %q, want %q", tt.labels, got,
				tt.want)
		}
		back := splitQuoted(got)[1:]
		if !slices.Equal(back, tt.labels) {
			t.Errorf("splitQuoted(%q) = %q, want %q", got, back,
				tt.labels)
		}
	}
}
