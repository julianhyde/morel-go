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

package parse

import (
	"slices"
	"strings"
)

// Unquote returns the value of a string or char literal token:
// the text without its quotes (and "#" for a char), with escape
// sequences decoded. The text must have been produced by the
// lexer; malformed input panics.
func Unquote(text string) string {
	s := strings.TrimPrefix(text, "#")
	s = s[1 : len(s)-1]
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		if r[i] != '\\' {
			b.WriteRune(r[i])
			continue
		}
		i++
		i += decodeEscape(&b, r[i:])
	}
	return b.String()
}

var simpleEscapes = map[rune]rune{
	'a': '\a', 'b': '\b', 't': '\t', 'n': '\n', 'v': '\v',
	'f': '\f', 'r': '\r', '"': '"', '\\': '\\',
}

// decodeEscape writes the value of one escape sequence starting
// at r (just after the backslash) and returns how many extra
// runes beyond the first it consumed.
func decodeEscape(b *strings.Builder, r []rune) int {
	if c, ok := simpleEscapes[r[0]]; ok {
		b.WriteRune(c)
		return 0
	}
	if r[0] == '^' {
		b.WriteRune(r[1] - '@')
		return 1
	}
	if r[0] == '\r' || r[0] == '\n' {
		// A line continuation: the newline, and the spaces and tabs
		// that begin the next line, contribute nothing.
		n := 1
		if r[0] == '\r' && n < len(r) && r[n] == '\n' {
			n++
		}
		for n < len(r) && (r[n] == ' ' || r[n] == '\t') {
			n++
		}
		return n - 1
	}
	const digits, base = 3, 10
	n := rune(0)
	for k := range digits {
		n = n*base + (r[k] - '0')
	}
	// A morel string is byte-indexed: a "\ddd" escape is one
	// byte (0..255), not a UTF-8-encoded rune.
	b.WriteByte(byte(n))
	return digits - 1
}

// unquoteIdent returns the name of a backtick-quoted identifier
// token: the text without its backticks, with doubled backticks
// undoubled.
func unquoteIdent(text string) string {
	s := text[1 : len(text)-1]
	return strings.ReplaceAll(s, "``", "`")
}

// QuoteIdent renders a name -- a binding's or a record label's --
// as it appears in printed output: back-tick-quoted (doubling any
// internal backtick) when the name is a reserved word or contains
// a backtick or a space, and unchanged otherwise. So a binding
// named "val" prints as "val `val` = ...", and a structure with a
// member named "exists" as "{`exists`=fn, ...}"; each is quoted
// exactly as it must be written to be read back.
func QuoteIdent(id string) string {
	if strings.Contains(id, "`") {
		return "`" + strings.ReplaceAll(id, "`", "``") + "`"
	}
	if reservedWords[id] || strings.Contains(id, " ") {
		return "`" + id + "`"
	}
	return id
}

// reservedWords are morel's keywords, which may be used as an
// identifier only when back-tick quoted.
var reservedWords = map[string]bool{
	// lint: sort until '^}' where '^\t"'
	"and":       true,
	"andalso":   true,
	"as":        true,
	"asOpt":     true,
	"case":      true,
	"check":     true,
	"compute":   true,
	"current":   true,
	"datatype":  true,
	"distinct":  true,
	"div":       true,
	"elem":      true,
	"elements":  true,
	"else":      true,
	"end":       true,
	"eqtype":    true,
	"except":    true,
	"exception": true,
	"exists":    true,
	"extend":    true,
	"fn":        true,
	"forall":    true,
	"from":      true,
	"full":      true,
	"fun":       true,
	"group":     true,
	"if":        true,
	"implies":   true,
	"in":        true,
	"inst":      true,
	"intersect": true,
	"into":      true,
	"join":      true,
	"left":      true,
	"let":       true,
	"mod":       true,
	"notelem":   true,
	"o":         true,
	"of":        true,
	"on":        true,
	"op":        true,
	"order":     true,
	"ordinal":   true,
	"orelse":    true,
	"over":      true,
	"raise":     true,
	"rec":       true,
	"remove":    true,
	"rename":    true,
	"replace":   true,
	"require":   true,
	"right":     true,
	"sig":       true,
	"signature": true,
	"skip":      true,
	"take":      true,
	"then":      true,
	"through":   true,
	"type":      true,
	"typeof":    true,
	"union":     true,
	"unorder":   true,
	"val":       true,
	"where":     true,
	"yield":     true,
	"yieldAll":  true,
}

// ShouldWriteRaw reports whether a string should be written as a
// raw literal, "{|...|}".
//
// It should if it contains at least minNewlines newlines, and is
// safe to write raw: every character is a newline or printable
// ASCII (no tab, carriage return, control character or non-ASCII
// character, which would be invisible or fragile), and no newline
// follows a space (which would be invisible, and easily lost by
// editors). A space at the very end is fine, because the closing
// fence follows it.
//
// If minNewlines is 0, a string with no newlines may be written
// raw; if it is negative, no string is.
func ShouldWriteRaw(s string, minNewlines int) bool {
	if minNewlines < 0 {
		return false
	}
	newlines := 0
	for i := range len(s) {
		c := s[i]
		if c == '\n' {
			if i > 0 && s[i-1] == ' ' {
				return false
			}
			newlines++
		} else if c < ' ' || c > '~' {
			return false
		}
	}
	return newlines >= minNewlines
}

// RawLiteral writes a string as a raw literal whose fences do not
// occur in it.
//
// If the content's second line starts with a space, the content
// starts on the line after the opening fence, so that its lines
// line up; the tag then starts with "_", which tells the reader to
// discard the newline after the fence: the literal reads "{_|", a
// newline, the content, "|_}". Otherwise the content starts right
// after the opening fence, "{|", and every newline in the literal
// is content.
func RawLiteral(content string) string {
	nextLine := startsOnNextLine(content)
	prefix := ""
	if nextLine {
		prefix = "_"
	}
	tag := prefix
	for i := 1; strings.Contains(content, "|"+tag+"}"); i++ {
		tag = prefix + rawTag(i)
	}
	open := "{" + tag + "|"
	if nextLine {
		open += "\n"
	}
	return open + content + "|" + tag + "}"
}

// startsOnNextLine reports whether a raw literal's content starts
// on the line after the opening fence: when its second line starts
// with a space, so that the lines line up.
func startsOnNextLine(content string) bool {
	i := strings.IndexByte(content, '\n')
	return i > 0 && i+1 < len(content) && content[i+1] == ' '
}

// rawTag is the i-th raw literal tag in the sequence a, b, ..., z,
// aa, ab, ...
func rawTag(i int) string {
	const letters = 26
	var b []byte
	for ; i > 0; i = (i - 1) / letters {
		b = append(b, byte('a'+(i-1)%letters))
	}
	slices.Reverse(b)
	return string(b)
}
