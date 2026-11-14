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

package shell_test

import (
	"strings"
	"testing"

	"github.com/hydromatic/morel-go/internal/shell"
)

func TestReplPrompts(t *testing.T) {
	// A one-line statement, then a statement split across two
	// lines: the continuation prompt "= " precedes the second
	// line. "--banner=false" keeps the output deterministic.
	in := "1 + 2;\nval x =\n5;\n"
	want := "- val it = 3 : int\n" +
		"- = val x = 5 : int\n" +
		"- \n"
	var out strings.Builder
	err := shell.ParseArgs([]string{"--banner=false"}).
		Repl(strings.NewReader(in), &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// TestReplLineCommentInBlockComment checks that inside a block
// comment "(*)" begins a line comment, as it does in the lexer, so a
// "*)" later on that line does not end the block. Comments that hold
// SQL, such as "count(*)", do this (hydromatic/morel#506). Once the
// comment closes, the buffer is blank, so the next prompt is "- ".
func TestReplLineCommentInBlockComment(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{
			"(* x 'select count(*)' y *)\n1;\n*)\n2;\n",
			"- = = - val it = 2 : int\n- \n",
		},
		{
			// A quote inside the comment does not open a literal.
			"(* x \"select count(*)\" y *)\n1;\n*)\n3;\n",
			"- = = - val it = 3 : int\n- \n",
		},
		{
			// The block continues on the next line, and ends there.
			"(* x count(*)\n *)\n4;\n",
			"- = - val it = 4 : int\n- \n",
		},
		{
			// A quote on a line that continues a comment does not
			// open a literal.
			"(* a\n * b \" c *)\n5;\n",
			"- = - val it = 5 : int\n- \n",
		},
	}
	for _, tt := range tests {
		var out strings.Builder
		err := shell.ParseArgs([]string{"--banner=false"}).
			Repl(strings.NewReader(tt.in), &out)
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != tt.want {
			t.Errorf("input %q: got %q, want %q", tt.in, out.String(),
				tt.want)
		}
	}
}

func TestReplBanner(t *testing.T) {
	var out strings.Builder
	err := shell.ParseArgs(nil).
		Repl(strings.NewReader(""), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "morel-go v") {
		t.Errorf("banner missing: %q", out.String())
	}
}

func TestWantsRepl(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{nil, true},
		{[]string{"--banner=false"}, true},
		{[]string{"-e", "1"}, false},
		{[]string{"--help"}, false},
		{[]string{"a.smli"}, false},
		{[]string{"--terminal=dumb"}, false},
	} {
		got := shell.ParseArgs(tc.argv).WantsRepl()
		if got != tc.want {
			t.Errorf("WantsRepl(%v) = %v, want %v",
				tc.argv, got, tc.want)
		}
	}
}
