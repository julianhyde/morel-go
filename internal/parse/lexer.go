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

// Package parse holds the Morel lexer and parser.
package parse

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/hydromatic/morel-go/internal/token"
)

// Error is a lexical or syntax error at a source position.
type Error struct {
	Name string
	Msg  string
	Span token.Span
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s:%s: %s", e.Name, e.Span, e.Msg)
}

// Lexer splits source text into tokens.
type Lexer struct {
	name string
	src  []rune
	i    int
	pos  token.Pos
}

// NewLexer returns a lexer over src; name (e.g. "stdIn" or a file
// name) is used in error messages.
func NewLexer(name, src string) *Lexer {
	return &Lexer{
		name: name,
		src:  []rune(src),
		pos:  token.Pos{Line: 1, Col: 1},
	}
}

// Next returns the next token, skipping whitespace and comments.
// At end of input it returns an EOF token.
func (l *Lexer) Next() (token.Token, error) {
	err := l.skipTrivia()
	if err != nil {
		return token.Token{}, err
	}
	start := l.pos
	r := l.peek(0)
	switch {
	case r < 0:
		return l.token(token.EOF, start), nil
	case isLetter(r):
		return l.scanIdent(start), nil
	case isDigit(r):
		return l.scanNumber(start), nil
	case r == '"':
		return l.scanString(start, token.StringLit)
	case r == '#':
		return l.scanHash(start)
	default:
		return l.scanSymbol(start)
	}
}

func (l *Lexer) errorAt(span token.Span, msg string) error {
	return &Error{Name: l.name, Span: span, Msg: msg}
}

// peek returns the rune at offset k from the current position, or
// -1 at end of input.
func (l *Lexer) peek(k int) rune {
	if l.i+k >= len(l.src) {
		return -1
	}
	return l.src[l.i+k]
}

func (l *Lexer) advance() {
	r := l.src[l.i]
	l.i++
	if r == '\n' {
		l.pos.Line++
		l.pos.Col = 1
	} else {
		l.pos.Col++
	}
}

func (l *Lexer) skipN(n int) {
	for range n {
		l.advance()
	}
}

// has reports whether the source at the current position starts
// with s.
func (l *Lexer) has(s string) bool {
	for k, r := range []rune(s) {
		if l.peek(k) != r {
			return false
		}
	}
	return true
}

// token builds a token whose text is the source from start to the
// current position.
func (l *Lexer) token(k token.Kind, start token.Pos) token.Token {
	// Tokens never contain newlines, so the start index can be
	// recovered from the column difference.
	n := l.i - (l.pos.Col - start.Col)
	return token.Token{
		Kind: k,
		Text: string(l.src[n:l.i]),
		Span: token.Span{Start: start, End: l.pos},
	}
}

func (l *Lexer) skipTrivia() error {
	for {
		switch {
		case isSpace(l.peek(0)):
			l.advance()
		case l.has("(*)"):
			l.skipLineComment()
		case l.has("(*"):
			err := l.skipBlockComment()
			if err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func (l *Lexer) skipLineComment() {
	for l.peek(0) >= 0 && l.peek(0) != '\n' {
		l.advance()
	}
}

// skipBlockComment consumes a "(* ... *)" comment. Comments nest;
// a "(*)" inside a block comment starts a line comment within
// which "*)" does not close the block.
func (l *Lexer) skipBlockComment() error {
	start := l.pos
	l.skipN(len("(*"))
	depth := 1
	for depth > 0 {
		switch {
		case l.peek(0) < 0:
			span := token.Span{Start: start, End: l.pos}
			return l.errorAt(span, "unclosed comment")
		case l.has("(*)"):
			l.skipLineComment()
		case l.has("(*"):
			l.skipN(len("(*"))
			depth++
		case l.has("*)"):
			l.skipN(len("*)"))
			depth--
		default:
			l.advance()
		}
	}
	return nil
}

func (l *Lexer) scanIdent(start token.Pos) token.Token {
	for isIdentPart(l.peek(0)) {
		l.advance()
	}
	t := l.token(token.Ident, start)
	t.Kind = token.Lookup(t.Text)
	return t
}

func (l *Lexer) scanNumber(start token.Pos) token.Token {
	for isDigit(l.peek(0)) {
		l.advance()
	}
	if l.peek(0) != '.' || !isDigit(l.peek(1)) {
		return l.token(token.IntLit, start)
	}
	l.advance()
	for isDigit(l.peek(0)) {
		l.advance()
	}
	return l.token(token.RealLit, start)
}

// scanString consumes a quoted string, validating escapes:
// \a \b \t \n \v \f \r \" \\, a control escape \^C for C in
// "@"–"_", or a three-digit decimal escape \ddd.
func (l *Lexer) scanString(start token.Pos, kind token.Kind) (
	token.Token, error,
) {
	l.advance()
	for {
		switch r := l.peek(0); {
		case r < 0 || r == '\n':
			span := token.Span{Start: start, End: l.pos}
			return token.Token{},
				l.errorAt(span, "unclosed string")
		case r == '"':
			l.advance()
			return l.token(kind, start), nil
		case r == '\\':
			err := l.scanEscape()
			if err != nil {
				return token.Token{}, err
			}
		default:
			l.advance()
		}
	}
}

const (
	controlEscapeLen = len(`^C`)
	decimalEscapeLen = len(`ddd`)
)

func (l *Lexer) scanEscape() error {
	start := l.pos
	l.advance()
	r := l.peek(0)
	switch {
	case r >= 0 && strings.ContainsRune(`abtnvfr"\`, r):
		l.advance()
		return nil
	case r == '^' && l.peek(1) >= '@' && l.peek(1) <= '_':
		l.skipN(controlEscapeLen)
		return nil
	case l.hasDigits(decimalEscapeLen):
		l.skipN(decimalEscapeLen)
		return nil
	default:
		span := token.Span{Start: start, End: l.pos}
		return l.errorAt(span, "illegal escape")
	}
}

// hasDigits reports whether the next n runes are all digits.
func (l *Lexer) hasDigits(n int) bool {
	for k := range n {
		if !isDigit(l.peek(k)) {
			return false
		}
	}
	return true
}

// scanHash consumes a char literal #"c" or a record label #x.
func (l *Lexer) scanHash(start token.Pos) (token.Token, error) {
	if l.peek(1) == '"' {
		l.advance()
		return l.scanString(start, token.CharLit)
	}
	if !isIdentPart(l.peek(1)) {
		l.advance()
		span := token.Span{Start: start, End: l.pos}
		return token.Token{}, l.errorAt(span, "illegal character")
	}
	l.advance()
	for isIdentPart(l.peek(0)) {
		l.advance()
	}
	return l.token(token.Label, start), nil
}

var symbols = []struct {
	text string
	kind token.Kind
}{
	{"...", token.Ellipsis},
	{"::", token.Cons},
	{"<=", token.Le},
	{">=", token.Ge},
	{"<>", token.Ne},
	{"=>", token.RArrow},
	{"->", token.RThinArrow},
	{"(", token.LParen},
	{")", token.RParen},
	{"{", token.LBrace},
	{"}", token.RBrace},
	{"[", token.LBracket},
	{"]", token.RBracket},
	{";", token.Semi},
	{"|", token.Bar},
	{".", token.Dot},
	{",", token.Comma},
	{"=", token.Eq},
	{">", token.Gt},
	{"<", token.Lt},
	{":", token.Colon},
	{"+", token.Plus},
	{"-", token.Minus},
	{"^", token.Caret},
	{"*", token.Star},
	{"/", token.Slash},
	{"~", token.Tilde},
	{"@", token.At},
}

func (l *Lexer) scanSymbol(start token.Pos) (token.Token, error) {
	for _, s := range symbols {
		if l.has(s.text) {
			l.skipN(len(s.text))
			return l.token(s.kind, start), nil
		}
	}
	l.advance()
	span := token.Span{Start: start, End: l.pos}
	return token.Token{}, l.errorAt(span, "illegal character")
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' ||
		r == '\f'
}

func isLetter(r rune) bool {
	return unicode.IsLetter(r)
}

func isDigit(r rune) bool {
	return r >= 0 && unicode.IsDigit(r)
}

func isIdentPart(r rune) bool {
	return isLetter(r) || isDigit(r) || r == '_' || r == '\''
}
