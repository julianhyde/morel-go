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
	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/token"
)

func isDeclStart(kind token.Kind) bool {
	return kind == token.Val || kind == token.Fun
}

func (p *Parser) decl() (ast.Decl, error) {
	switch p.tok.Kind {
	case token.Val:
		return p.valDecl()
	case token.Fun:
		return p.funDecl()
	default:
		return nil, p.errorf("expected declaration, found " +
			p.tok.Kind.String())
	}
}

// valDecl parses "val [rec] pat = exp [and pat = exp ...]".
func (p *Parser) valDecl() (ast.Decl, error) {
	start := p.tok.Span.Start
	err := p.next()
	if err != nil {
		return nil, err
	}
	rec := false
	if p.tok.Kind == token.Rec {
		rec = true
		err = p.next()
		if err != nil {
			return nil, err
		}
	}
	var binds []*ast.ValBind
	for {
		var bind *ast.ValBind
		bind, err = p.valBind()
		if err != nil {
			return nil, err
		}
		binds = append(binds, bind)
		if p.tok.Kind != token.And {
			break
		}
		err = p.next()
		if err != nil {
			return nil, err
		}
	}
	last := binds[len(binds)-1]
	span := token.Span{Start: start, End: last.Span().End}
	return ast.NewValDecl(span, rec, binds), nil
}

func (p *Parser) valBind() (*ast.ValBind, error) {
	pat, err := p.pat()
	if err != nil {
		return nil, err
	}
	err = p.expect(token.Eq)
	if err != nil {
		return nil, err
	}
	err = p.next()
	if err != nil {
		return nil, err
	}
	exp, err := p.expr()
	if err != nil {
		return nil, err
	}
	span := token.Span{
		Start: pat.Span().Start,
		End:   exp.Span().End,
	}
	return ast.NewValBind(span, pat, exp), nil
}

// funDecl parses "fun bind [and bind ...]" where each bind is
// "clause [| clause ...]" and each clause is "name pat... = exp".
func (p *Parser) funDecl() (ast.Decl, error) {
	start := p.tok.Span.Start
	err := p.next()
	if err != nil {
		return nil, err
	}
	var binds []*ast.FunBind
	for {
		var bind *ast.FunBind
		bind, err = p.funBind()
		if err != nil {
			return nil, err
		}
		binds = append(binds, bind)
		if p.tok.Kind != token.And {
			break
		}
		err = p.next()
		if err != nil {
			return nil, err
		}
	}
	last := binds[len(binds)-1]
	span := token.Span{Start: start, End: last.Span().End}
	return ast.NewFunDecl(span, binds), nil
}

func (p *Parser) funBind() (*ast.FunBind, error) {
	var matches []*ast.FunMatch
	for {
		m, err := p.funMatch()
		if err != nil {
			return nil, err
		}
		matches = append(matches, m)
		if p.tok.Kind != token.Bar {
			break
		}
		err = p.next()
		if err != nil {
			return nil, err
		}
	}
	last := matches[len(matches)-1]
	span := token.Span{
		Start: matches[0].Span().Start,
		End:   last.Span().End,
	}
	return ast.NewFunBind(span, matches), nil
}

func (p *Parser) funMatch() (*ast.FunMatch, error) {
	err := p.expect(token.Ident)
	if err != nil {
		return nil, err
	}
	name := p.tok
	err = p.next()
	if err != nil {
		return nil, err
	}
	var pats []ast.Pat
	for p.tok.Kind != token.Eq {
		var pat ast.Pat
		pat, err = p.atomicPat()
		if err != nil {
			return nil, err
		}
		pats = append(pats, pat)
	}
	err = p.next()
	if err != nil {
		return nil, err
	}
	exp, err := p.expr()
	if err != nil {
		return nil, err
	}
	span := token.Span{
		Start: name.Span.Start,
		End:   exp.Span().End,
	}
	return ast.NewFunMatch(span, name.Text, pats, exp), nil
}

// letExpr parses "let decl ... in exp end"; declarations may be
// separated by ";".
func (p *Parser) letExpr() (ast.Expr, error) {
	start := p.tok.Span.Start
	err := p.next()
	if err != nil {
		return nil, err
	}
	var decls []ast.Decl
	for p.tok.Kind != token.In {
		if p.tok.Kind == token.Semi {
			err = p.next()
			if err != nil {
				return nil, err
			}
			continue
		}
		var d ast.Decl
		d, err = p.decl()
		if err != nil {
			return nil, err
		}
		decls = append(decls, d)
	}
	err = p.next()
	if err != nil {
		return nil, err
	}
	exp, err := p.expr()
	if err != nil {
		return nil, err
	}
	err = p.expect(token.End)
	if err != nil {
		return nil, err
	}
	span := token.Span{Start: start, End: p.tok.Span.End}
	err = p.next()
	if err != nil {
		return nil, err
	}
	return ast.NewLet(span, decls, exp), nil
}
