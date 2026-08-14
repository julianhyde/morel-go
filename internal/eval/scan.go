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

package eval

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// A "scan" function reads a value from a prefix of a character
// stream and returns "SOME (value, rest)", or NONE if it cannot
// read one. The stream is whatever the reader understands; only
// the reader makes sense of it.
//
// charSource gives a scanner one character of lookahead over such
// a reader. It is a cursor: peek reads the character at the
// cursor without moving it, advance moves past it, and stream
// returns the stream positioned at the cursor, which is what a
// scanner returns alongside the value it read.

// noChar is what peek returns at the end of the stream. No
// character equals it, so a scanner can compare without asking
// whether the stream has ended.
const noChar rune = -1

type charSource struct {
	reader Val
	stream Val
	// c is the character at the cursor, or noChar at the end.
	c rune
	// next is the stream that follows c.
	next Val
	// err is the first error the reader returned, if any; a
	// scanner checks it before trusting what it read.
	err error
}

// newCharSource returns a cursor over reader, positioned at
// stream.
func newCharSource(reader, stream Val) *charSource {
	s := &charSource{reader: reader, stream: stream}
	s.read()
	return s
}

// read fills in the character at the cursor.
func (s *charSource) read() {
	option, err := ApplyVal(s.reader, s.stream)
	if err != nil {
		s.err, s.c = err, noChar
		return
	}
	pair, some := asOption(option)
	if !some {
		s.c = noChar
		return
	}
	first, rest := asPair(pair)
	s.c, s.next = asChar(first), rest
}

// peek returns the character at the cursor without moving it, or
// noChar at the end of the stream.
func (s *charSource) peek() rune { return s.c }

// advance moves the cursor past the character it is on.
func (s *charSource) advance() {
	s.stream = s.next
	s.read()
}

// mark returns the stream at the cursor, to return with a scanned
// value or to rewind to.
func (s *charSource) mark() Val { return s.stream }

// reset moves the cursor back to a stream that mark returned.
func (s *charSource) reset(stream Val) {
	s.stream = stream
	s.read()
}

// skipWhitespace moves the cursor past any whitespace.
func (s *charSource) skipWhitespace() {
	for s.c != noChar && unicode.IsSpace(s.c) {
		s.advance()
	}
}

// consume moves the cursor past word if the stream holds it, and
// reports whether it did; if not, the cursor does not move.
func (s *charSource) consume(word string) bool {
	start := s.mark()
	for _, want := range word {
		if s.c != want {
			s.reset(start)
			return false
		}
		s.advance()
	}
	return true
}

// stringReader returns a reader over the characters of s, for a
// "fromString" that is defined as its "scan" over the whole
// string. The stream is a position in s.
func stringReader(s string) Val {
	runes := []rune(s)
	return Fn(func(stream Val) (Val, error) {
		i := int(asInt(stream))
		if i >= len(runes) {
			return noneVal, nil
		}
		//nolint:gosec // a string's length fits in an int32.
		return someVal([]Val{runes[i], int32(i + 1)}), nil
	})
}

// scanString reads a value from a prefix of s with a two-argument
// scan function, and returns it without the stream that follows —
// the shape every "fromString" has.
func scanString(scan func(reader, stream Val) (Val, error),
	s string,
) (Val, error) {
	option, err := scan(stringReader(s), int32(0))
	if err != nil {
		return nil, err
	}
	return scanValue(option), nil
}

// scanValue is the value a scan returned, dropping the stream
// that follows it: "SOME (v, rest)" becomes "SOME v".
func scanValue(option Val) Val {
	pair, some := asOption(option)
	if !some {
		return noneVal
	}
	value, _ := asPair(pair)
	return someVal(value)
}

// boolScanFn is "Bool.scan rdr src": "true" or "false", after any
// leading whitespace.
func boolScanFn(reader, stream Val) (Val, error) {
	s := newCharSource(reader, stream)
	s.skipWhitespace()
	for _, word := range []struct {
		text  string
		value bool
	}{{"true", true}, {"false", false}} {
		if s.consume(word.text) {
			if s.err != nil {
				return nil, s.err
			}
			return someVal([]Val{word.value, s.mark()}), nil
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return noneVal, nil
}

// scanDigits reads the digits of a number in the given base,
// after an optional sign and radix prefix, and returns the digits
// and whether the number is negative. The digits are empty if
// there are none, which is NONE to the caller.
func scanDigits(s *charSource, base int, signed bool) (string, bool) {
	negative := false
	if signed {
		switch s.peek() {
		case '~', '-':
			negative = true
			s.advance()
		case '+':
			s.advance()
		}
	}
	if base == hexBase {
		skipHexPrefix(s, signed)
	}
	var digits []rune
	for digitVal(s.peek(), base) >= 0 {
		digits = append(digits, s.peek())
		s.advance()
	}
	return string(digits), negative
}

// skipHexPrefix moves the cursor past a "0x" prefix, or "0wx" for
// a word. If what follows is not a hexadecimal digit the "0" is a
// digit and the "x" is not ours, so the cursor does not move.
func skipHexPrefix(s *charSource, signed bool) {
	if s.peek() != '0' {
		return
	}
	start := s.mark()
	s.advance()
	if !signed && (s.peek() == 'w' || s.peek() == 'W') {
		s.advance()
	}
	if s.peek() == 'x' || s.peek() == 'X' {
		s.advance()
	}
	if digitVal(s.peek(), hexBase) < 0 {
		s.reset(start)
	}
}

// digitOffset is the value of the first letter digit, "a" or "A",
// in a base above ten.
const digitOffset = 10

// digitVal is the value of a digit in the given base, or -1 if it
// is not one.
func digitVal(c rune, base int) int {
	var v int
	switch {
	case c >= '0' && c <= '9':
		v = int(c - '0')
	case c >= 'a' && c <= 'z':
		v = int(c-'a') + digitOffset
	case c >= 'A' && c <= 'Z':
		v = int(c-'A') + digitOffset
	default:
		return -1
	}
	if v >= base {
		return -1
	}
	return v
}

// intFromStringFn is "Int.fromString s", which the Standard Basis
// defines as "StringCvt.scanString (scan DEC)".
func intFromStringFn(arg Val) (Val, error) {
	return scanString(func(reader, stream Val) (Val, error) {
		return intScan(decRadix, reader, stream)
	}, asString(arg))
}

// wordFromStringFn is "Word.fromString s", which the Standard
// Basis defines as "StringCvt.scanString (scan HEX)".
func wordFromStringFn(arg Val) (Val, error) {
	return scanString(func(reader, stream Val) (Val, error) {
		return wordScan(hexRadix, reader, stream)
	}, asString(arg))
}

// intScanFn is "Int.scan radix rdr src": an optionally signed
// number in the radix's base, after any leading whitespace.
func intScanFn(radix, reader, stream Val) (Val, error) {
	return intScan(radixBase(radix), reader, stream)
}

func intScan(base int, reader, stream Val) (Val, error) {
	s := newCharSource(reader, stream)
	s.skipWhitespace()
	digits, negative := scanDigits(s, base, true)
	if s.err != nil {
		return nil, s.err
	}
	if digits == "" {
		return noneVal, nil
	}
	if negative {
		digits = "-" + digits
	}
	i, err := strconv.ParseInt(digits, base, 32)
	if err != nil {
		return nil, &MorelError{Exn: ExnOverflow}
	}
	return someVal([]Val{int32(i), s.mark()}), nil
}

// wordScanFn is "Word.scan radix rdr src": an unsigned number in
// the radix's base, after any leading whitespace.
func wordScanFn(radix, reader, stream Val) (Val, error) {
	return wordScan(radixBase(radix), reader, stream)
}

func wordScan(base int, reader, stream Val) (Val, error) {
	s := newCharSource(reader, stream)
	s.skipWhitespace()
	digits, _ := scanDigits(s, base, false)
	if s.err != nil {
		return nil, s.err
	}
	if digits == "" {
		return noneVal, nil
	}
	u, err := strconv.ParseUint(digits, base, 64)
	if err != nil {
		return nil, &MorelError{Exn: ExnOverflow}
	}
	return someVal([]Val{u, s.mark()}), nil
}

// realScanFn is "Real.scan rdr src": a real number, after any
// leading whitespace. It accepts "inf", "infinity" and "nan" in
// any case, as well as the usual digits, point and exponent.
func realScanFn(reader, stream Val) (Val, error) {
	s := newCharSource(reader, stream)
	s.skipWhitespace()
	var b strings.Builder
	negative := false
	switch s.peek() {
	case '~', '-':
		negative = true
		b.WriteByte('-')
		s.advance()
	case '+':
		s.advance()
	}
	if v, ok := scanInfinityOrNan(s, negative); ok {
		if s.err != nil {
			return nil, s.err
		}
		return someVal([]Val{v, s.mark()}), nil
	}
	digits := scanRealDigits(s, &b)
	if s.err != nil {
		return nil, s.err
	}
	if digits == 0 {
		return noneVal, nil
	}
	f, err := strconv.ParseFloat(b.String(), 32)
	if err != nil {
		return noneVal, nil //nolint:nilerr // NONE, not an error
	}
	return someVal([]Val{float32(f), s.mark()}), nil
}

// scanInfinityOrNan reads "inf", "infinity" or "nan", in any
// case, and reports whether it did.
func scanInfinityOrNan(s *charSource, negative bool) (Val, bool) {
	for _, word := range []string{"infinity", infText, nanText} {
		if !s.consumeFold(word) {
			continue
		}
		if word == nanText {
			return float32(math.NaN()), true
		}
		sign := 1.0
		if negative {
			sign = -1.0
		}
		return float32(math.Inf(int(sign))), true
	}
	return nil, false
}

// scanRealDigits reads the digits, fraction and exponent of a
// real number into b, and returns how many digits it read before
// the exponent; zero means there was no number.
func scanRealDigits(s *charSource, b *strings.Builder) int {
	digits := scanDigitRun(s, b)
	if s.peek() == '.' {
		start := s.mark()
		s.advance()
		var frac strings.Builder
		frac.WriteByte('.')
		if n := scanDigitRun(s, &frac); n == 0 {
			// A point that no digit follows is not ours.
			s.reset(start)
		} else {
			b.WriteString(frac.String())
			digits += n
		}
	}
	if digits == 0 {
		return 0
	}
	if s.peek() == 'e' || s.peek() == 'E' {
		start := s.mark()
		s.advance()
		var exp strings.Builder
		exp.WriteByte('e')
		switch s.peek() {
		case '~', '-':
			exp.WriteByte('-')
			s.advance()
		case '+':
			s.advance()
		}
		if scanDigitRun(s, &exp) == 0 {
			// An "e" that no digit follows is not ours.
			s.reset(start)
		} else {
			b.WriteString(exp.String())
		}
	}
	return digits
}

// scanDigitRun reads a run of decimal digits into b, and returns
// how many there were.
func scanDigitRun(s *charSource, b *strings.Builder) int {
	n := 0
	for digitVal(s.peek(), decRadix) >= 0 {
		b.WriteRune(s.peek())
		s.advance()
		n++
	}
	return n
}

// consumeFold is consume, ignoring case.
func (s *charSource) consumeFold(word string) bool {
	start := s.mark()
	for _, want := range word {
		if s.c == noChar || unicode.ToLower(s.c) != want {
			s.reset(start)
			return false
		}
		s.advance()
	}
	return true
}

// realFromStringFn is "Real.fromString s", which the Standard
// Basis defines as "StringCvt.scanString scan".
func realFromStringFn(arg Val) (Val, error) {
	return scanString(realScanFn, asString(arg))
}
