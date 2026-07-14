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

// fromExpr parses "from scan, ... step ...": comma-separated
// scans, then where/yield/join steps in any order.
func (p *Parser) fromExpr() (ast.Expr, error) {
	start := p.tok.Span.Start
	err := p.next()
	if err != nil {
		return nil, err
	}
	steps, err := p.scanList()
	if err != nil {
		return nil, err
	}
	for {
		step, err := p.fromStep()
		if err != nil {
			return nil, err
		}
		if step == nil {
			break
		}
		steps = append(steps, step...)
	}
	last := steps[len(steps)-1]
	span := token.Span{Start: start, End: last.Span().End}
	return ast.NewFrom(span, steps), nil
}

// fromStep parses one pipeline step, or returns nil at the end
// of the query.
func (p *Parser) fromStep() ([]ast.FromStep, error) {
	// lint: sort until '^\t}' where '^\tcase '
	switch p.tok.Kind {
	case token.Join:
		err := p.next()
		if err != nil {
			return nil, err
		}
		return p.scanList()
	case token.Where:
		exp, err := p.keywordExpr(token.Where)
		if err != nil {
			return nil, err
		}
		return []ast.FromStep{ast.NewWhereStep(exp.Span(),
			exp)}, nil
	case token.Yield:
		exp, err := p.keywordExpr(token.Yield)
		if err != nil {
			return nil, err
		}
		return []ast.FromStep{ast.NewYieldStep(exp.Span(),
			exp)}, nil
	default:
		return nil, nil
	}
}

// scanList parses "scan [, scan ...]".
func (p *Parser) scanList() ([]ast.FromStep, error) {
	var steps []ast.FromStep
	for {
		scan, err := p.scan()
		if err != nil {
			return nil, err
		}
		steps = append(steps, scan)
		if p.tok.Kind != token.Comma {
			return steps, nil
		}
		err = p.next()
		if err != nil {
			return nil, err
		}
	}
}

// scan parses "pat [in exp | = exp] [on exp]".
func (p *Parser) scan() (*ast.Scan, error) {
	pat, err := p.pat()
	if err != nil {
		return nil, err
	}
	kind := ast.ScanUnbounded
	var exp ast.Expr
	switch p.tok.Kind {
	case token.Eq:
		kind = ast.ScanEq
	case token.In:
		kind = ast.ScanIn
	default:
	}
	end := pat.Span().End
	if kind != ast.ScanUnbounded {
		err = p.next()
		if err != nil {
			return nil, err
		}
		exp, err = p.expr()
		if err != nil {
			return nil, err
		}
		end = exp.Span().End
	}
	var on ast.Expr
	if p.tok.Kind == token.On {
		err = p.next()
		if err != nil {
			return nil, err
		}
		on, err = p.expr()
		if err != nil {
			return nil, err
		}
		end = on.Span().End
	}
	span := token.Span{Start: pat.Span().Start, End: end}
	return ast.NewScan(span, kind, pat, exp, on), nil
}
