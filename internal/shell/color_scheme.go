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

package shell

import "strings"

// The built-in syntax-highlighting color schemes. A style is zero
// or more attributes (bold, italic, underline, faint) followed by a
// color: an ANSI color name, an index 0-255, or #rrggbb. An empty
// style leaves the category in the terminal's default style.
//
// morel-java loads these from .properties resources; the values
// here are the same, so that the three implementations color
// identically.

// The scheme names. "none" is the scheme that styles nothing, and
// is what a shell with no color uses.
const (
	SchemeDark  = "dark"
	SchemeLight = "light"
	SchemeNone  = "none"
)

// Styles used by more than one scheme.
const (
	styleMagenta = "magenta"
	styleRed     = "underline red"
	styleGreen   = "green"
)

// ColorScheme is a style for each token category, by name.
type ColorScheme struct {
	Name   string
	Styles map[Category]string
}

// Style is the style for a category, or "" if the category is left
// in the terminal's default style.
func (s ColorScheme) Style(c Category) string {
	return s.Styles[c]
}

// colorSchemes are the built-in schemes, in the order
// Sys.colorSchemes reports them.
var colorSchemes = []ColorScheme{
	{
		Name: SchemeDark,
		Styles: map[Category]string{
			CatComment:  "italic 245",
			CatConstant: "yellow",
			CatError:    styleRed,
			CatKeyword:  "bold cyan",
			CatNumeric:  "yellow",
			CatString:   styleGreen,
			CatSymbol:   "cyan",
			CatTypeVar:  styleMagenta,
		},
	},
	{
		Name: SchemeLight,
		Styles: map[Category]string{
			CatComment:  "italic bright-black",
			CatConstant: styleMagenta,
			CatError:    styleRed,
			CatKeyword:  "bold blue",
			CatNumeric:  styleMagenta,
			CatString:   styleGreen,
			CatSymbol:   "blue",
			CatTypeVar:  styleMagenta,
		},
	},
	{Name: SchemeNone, Styles: map[Category]string{}},
}

// schemeFields are the categories that Sys.colorSchemes reports, in
// the order the record's fields are sorted: a Morel record's fields
// are in label order, and "name" falls between "keyword" and
// "numeric".
var schemeFields = []struct {
	label    string
	category Category
}{
	// lint: sort until '^}' where '^\t{"'
	{"comment", CatComment},
	{"constant", CatConstant},
	{"error", CatError},
	{"identifier", CatIdentifier},
	{"keyword", CatKeyword},
	{"name", CatNone}, // the scheme's name, not a category
	{"numeric", CatNumeric},
	{"string", CatString},
	{"symbol", CatSymbol},
	{"typeVar", CatTypeVar},
}

// FindColorScheme returns the built-in scheme of that name.
func FindColorScheme(name string) (ColorScheme, bool) {
	for _, s := range colorSchemes {
		if s.Name == name {
			return s, true
		}
	}
	return ColorScheme{}, false
}

// DeduceColorScheme is the scheme in effect: the "colorScheme"
// property if it names a built-in scheme, otherwise the scheme the
// terminal's background implies, and "none" if that is unknown —
// which is the case in a script, where there is no terminal.
func (k *Kernel) DeduceColorScheme() ColorScheme {
	if s, ok := FindColorScheme(k.config.props["colorScheme"]); ok {
		return s
	}
	if s, ok := FindColorScheme(
		schemeForBackground(k.config.props["terminalBackground"]),
	); ok {
		return s
	}
	s, _ := FindColorScheme(SchemeNone)
	return s
}

// schemeForBackground names the scheme a terminal background calls
// for: "light" for a light background and "dark" for a dark one. An
// empty or unparsable background gives "", so that the caller falls
// back to "none".
func schemeForBackground(background string) string {
	luma, ok := backgroundLuma(background)
	if !ok {
		return ""
	}
	// Rec. 601 luma, and morel-java's 0.5 threshold.
	const midpoint = 0.5
	if luma > midpoint {
		return SchemeLight
	}
	return SchemeDark
}

// backgroundLuma is the perceived brightness, in [0, 1], of a
// background reported as "rgb:RRRR/GGGG/BBBB" — the form an xterm
// answers an OSC 11 query with, and the form the
// "terminalBackground" property holds.
func backgroundLuma(background string) (float64, bool) {
	rest, found := strings.CutPrefix(background, "rgb:")
	if !found {
		return 0, false
	}
	parts := strings.Split(rest, "/")
	const channels = 3
	if len(parts) != channels {
		return 0, false
	}
	var v [channels]float64
	for i, p := range parts {
		f, ok := hexFraction(p)
		if !ok {
			return 0, false
		}
		v[i] = f
	}
	// Rec. 601, as morel-java and morel-rust.
	const (
		redWeight   = 0.299
		greenWeight = 0.587
		blueWeight  = 0.114
	)
	return redWeight*v[0] + greenWeight*v[1] +
		blueWeight*v[2], true
}

// hexFraction reads a hex channel of any width — "f", "ff", "ffff"
// are all full — as a fraction in [0, 1].
func hexFraction(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	var n, full uint64
	const hexBase = 16
	for i := range len(s) {
		d := strings.IndexByte("0123456789abcdef",
			lowerASCII(s[i]))
		if d < 0 {
			return 0, false
		}
		n = n*hexBase + uint64(d)
		full = full*hexBase + hexBase - 1
	}
	return float64(n) / float64(full), true
}

// lowerASCII lowercases an ASCII letter.
func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b - 'A' + 'a'
	}
	return b
}
