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

//nolint:testpackage // white-box: unparser is unexported
package compile

import (
	"testing"

	"github.com/hydromatic/morel-go/internal/core"
)

// conjunction returns "(x > 1 andalso y > 2) andalso z > 3".
func conjunction(f *fbbtFixture) core.Exp {
	c := func(pat *core.IDPat, n int32) core.Exp {
		return f.cmp(opGt, f.id(pat), f.i(n))
	}
	return f.and(f.and(c(f.x, 1), c(f.y, 2)), c(f.z, 3))
}

// unparseWidth renders an expression laid out to fit a width.
func unparseWidth(f *fbbtFixture, e core.Exp, width int) string {
	u := &unparser{
		sys: f.sys, seen: map[string][]*core.IDPat{}, width: width,
	}
	u.exp(e, 0, 0)
	return u.render()
}

// An unparser with no width prints on one line, and so does one
// whose width the text fits.
func TestUnparseFits(t *testing.T) {
	f := newFbbtFixture()
	e := conjunction(f)
	const flat = "x > 1 andalso y > 2 andalso z > 3"
	if got := f.text(e); got != flat {
		t.Errorf("no width: got %q, want %q", got, flat)
	}
	if got := unparseWidth(f, e, len(flat)); got != flat {
		t.Errorf("width %d: got %q, want %q", len(flat), got, flat)
	}
}

// An andalso chain that does not fit breaks before each andalso,
// which leads its line; the outer group breaks before the inner
// one, so what fits on a line stays on it.
func TestUnparseBreaks(t *testing.T) {
	f := newFbbtFixture()
	e := conjunction(f)
	tests := []struct {
		width int
		want  string
	}{
		{20, "x > 1 andalso y > 2\nandalso z > 3"},
		{10, "x > 1\nandalso y > 2\nandalso z > 3"},
	}
	for _, tt := range tests {
		if got := unparseWidth(f, e, tt.width); got != tt.want {
			t.Errorf("width %d: got %q, want %q", tt.width, got, tt.want)
		}
	}
}
