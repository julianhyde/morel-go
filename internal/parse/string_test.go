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

package parse_test

import (
	"testing"

	"github.com/hydromatic/morel-go/internal/parse"
)

// TestShouldWriteRaw checks which strings a raw literal may hold: one
// with enough newlines, and nothing invisible.
func TestShouldWriteRaw(t *testing.T) {
	tests := []struct {
		s           string
		minNewlines int
		want        bool
	}{
		{"a\nb", 1, true},
		{"a\nb", 2, false},
		{"abc", 0, true},
		{"a\nb", -1, false},
		// A space or tab before a newline; a character that is not
		// printable ASCII.
		{"a \nb", 1, false},
		{"a\t\nb", 1, false},
		{"aé\nb", 1, false},
	}
	for _, test := range tests {
		got := parse.ShouldWriteRaw(test.s, test.minNewlines)
		if got != test.want {
			t.Errorf("ShouldWriteRaw(%q, %d) = %v, want %v",
				test.s, test.minNewlines, got, test.want)
		}
	}
}

// TestRawLiteral checks the fences a raw literal takes.
func TestRawLiteral(t *testing.T) {
	tests := []struct{ content, want string }{
		{"a\nb", "{|a\nb|}"},
		// The second line starts with a space, so the content starts
		// on the line after the fence and the tag starts with "_".
		{"a\n  b", "{_|\na\n  b|_}"},
		// A trailing newline leaves the closing fence on its own line.
		{"a\nb\n", "{|a\nb\n|}"},
		// The content contains "|}", so the fences carry a tag.
		{"a|}\nb", "{a|a|}\nb|a}"},
	}
	for _, test := range tests {
		if got := parse.RawLiteral(test.content); got != test.want {
			t.Errorf("RawLiteral(%q) = %q, want %q", test.content, got,
				test.want)
		}
	}
}
