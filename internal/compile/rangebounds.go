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

package compile

import (
	"math/big"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/types"
)

// listBounds is the pair of comparisons a scan over a list of
// numeric literals implies for its variable: it is at least the
// least of them and at most the greatest. An element that is not
// a literal tells us nothing, so the whole list is dropped.
func listBounds(sys *types.System, scan *core.Scan) []core.Exp {
	pat, ok := scan.Pat.(*core.IDPat)
	if !ok || (pat.T != sys.Int && pat.T != sys.Real) {
		return nil
	}
	list, ok := scan.Exp.(*core.List)
	if !ok || len(list.Args) == 0 {
		return nil
	}
	var lo, hi *big.Rat
	for _, arg := range list.Args {
		v, isNum := literalRat(arg)
		if !isNum {
			return nil
		}
		if lo == nil || ratCmp(v, lo) < 0 {
			lo = v
		}
		if hi == nil || ratCmp(v, hi) > 0 {
			hi = v
		}
	}
	return []core.Exp{
		boundConjunct(sys, pat, lo, false, true),
		boundConjunct(sys, pat, hi, false, false),
	}
}

// impliedRangeBound is the comparison a one-sided range-list
// scan implies for its variable.
func impliedRangeBound(sys *types.System, scan *core.Scan,
) (*core.IDPat, core.Exp) {
	pat, ok := scan.Pat.(*core.IDPat)
	if !ok {
		return nil, nil
	}
	rl, ok := scan.Exp.(*core.RangeList)
	if !ok || len(rl.Items) != 1 {
		return nil, nil
	}
	item := rl.Items[0]
	var op string
	var v core.Exp
	// lint: sort until '^\t}' where '^\tcase '
	switch item.Kind {
	case ast.RangeAtLeast:
		op, v = opGe, item.Lo
	case ast.RangeAtMost:
		op, v = opLe, item.Hi
	case ast.RangeGreaterThan:
		op, v = opGt, item.Lo
	case ast.RangeLessThan:
		op, v = opLt, item.Hi
	default:
		return nil, nil
	}
	pairT := sys.Tuple(pat.T, pat.T)
	return pat, &core.Apply{
		T: sys.Bool,
		Fn: &core.ID{Pat: &core.IDPat{
			T:    sys.Fn(pairT, sys.Bool),
			Name: op,
		}},
		Arg: &core.Tuple{T: pairT, Args: []core.Exp{
			&core.ID{Pat: pat}, v,
		}},
	}
}

// pushBound finds the tightest literal bound on the variable in
// the following filter steps and folds it into a one-sided range
// item, returning the closed item and the conjunct consumed.
func pushBound(sys *types.System, pat *core.IDPat,
	item core.RangeItem, wantUpper bool, rest []core.FromStep,
) (core.RangeItem, core.Exp) {
	var best *bound
	var source core.Exp
	for _, step := range rest {
		where, ok := step.(*core.Where)
		if !ok {
			continue
		}
		var conjuncts []core.Exp
		decomposeConjuncts(where.Exp, &conjuncts)
		for _, c := range conjuncts {
			lo, hi := conjunctBounds(sys, c, pat)
			b := hi
			if !wantUpper {
				b = lo
			}
			if b == nil {
				continue
			}
			lit, ok := b.value.(*core.Literal)
			if !ok {
				continue
			}
			if best != nil && !tighter(lit, b, best, wantUpper) {
				continue
			}
			best, source = b, c
		}
	}
	if best == nil {
		return item, nil
	}
	if crossesEmpty(item, best, wantUpper) {
		return item, nil
	}
	if wantUpper {
		return core.RangeItem{
			Kind: closeUpper(item.Kind, best.strict),
			Lo:   item.Lo,
			Hi:   best.value,
		}, source
	}
	return core.RangeItem{
		Kind: closeLower(item.Kind, best.strict),
		Lo:   best.value,
		Hi:   item.Hi,
	}, source
}

// tighter reports whether a new literal bound is stricter than
// the best so far.
func tighter(lit *core.Literal, b, best *bound,
	wantUpper bool,
) bool {
	bestLit, ok := best.value.(*core.Literal)
	if !ok {
		return true
	}
	c := compareLiterals(lit, bestLit)
	if wantUpper {
		return c < 0 || (c == 0 && b.strict && !best.strict)
	}
	return c > 0 || (c == 0 && b.strict && !best.strict)
}

// crossesEmpty reports whether folding the bound into the item
// would produce an obviously empty range (both endpoints
// literal, in the wrong order).
func crossesEmpty(item core.RangeItem, b *bound,
	wantUpper bool,
) bool {
	fixed := item.Lo
	if !wantUpper {
		fixed = item.Hi
	}
	fixedLit, ok := fixed.(*core.Literal)
	if !ok {
		return false
	}
	lit, ok := b.value.(*core.Literal)
	if !ok {
		return false
	}
	if wantUpper {
		return compareLiterals(lit, fixedLit) < 0
	}
	return compareLiterals(lit, fixedLit) > 0
}

// compareLiterals orders two int or char literals (both are
// int32-valued).
func compareLiterals(a, b *core.Literal) int {
	av, aOK := a.Value.(int32)
	bv, bOK := b.Value.(int32)
	switch {
	case !aOK || !bOK:
		return 0
	case av < bv:
		return -1
	case av > bv:
		return 1
	default:
		return 0
	}
}

// closeUpper is the bounded range kind after adding an upper
// bound to a lower-only item.
func closeUpper(kind ast.RangeKind, strict bool) ast.RangeKind {
	open := kind == ast.RangeGreaterThan
	switch {
	case open && strict:
		return ast.RangeOpen
	case open:
		return ast.RangeOpenClosed
	case strict:
		return ast.RangeClosedOpen
	default:
		return ast.RangeClosed
	}
}

// closeLower is the bounded range kind after adding a lower bound
// to an upper-only item.
func closeLower(kind ast.RangeKind, strict bool) ast.RangeKind {
	open := kind == ast.RangeLessThan
	switch {
	case open && strict:
		return ast.RangeOpen
	case open:
		return ast.RangeClosedOpen
	case strict:
		return ast.RangeOpenClosed
	default:
		return ast.RangeClosed
	}
}

// distinctScan wraps a collection in a subquery that takes its
// distinct values in order, "from v in exp distinct order v".
// The subquery scans into the generator's own variable where it
// has one, as morel-java's plan does -- "join e in (from e in
// ... group e order e)".
//
// An unbounded variable stands for the values that its predicate
// allows, which is a set; and a set that the query reads back has
// to be read in some order, of which the values' own is the only
// one that does not depend on which generator the predicate
// happened to yield. The subquery scans the collection into one
// variable rather than the generator's pattern, so that a pattern
// with parts that bind nothing -- a literal field, say -- still
// yields elements of the collection's own type, which the scan
// outside then destructures.
func distinctScan(sys *types.System, pat core.Pat,
	exp core.Exp,
) core.Exp {
	elem, ok := pat.(*core.IDPat)
	if !ok {
		elem = &core.IDPat{T: pat.Type(), Name: "$elem"}
	}
	return &core.From{
		T: sys.List(pat.Type()),
		Steps: []core.FromStep{
			&core.Scan{Pat: elem, Exp: exp},
			&core.Distinct{},
			&core.Order{Exp: &core.ID{Pat: elem}},
		},
		Kind: ast.FromOp,
	}
}

// substituteFresh rewrites a condition onto a scan's fresh
// pattern copies.
func substituteFresh(e core.Exp,
	fresh map[*core.IDPat]*core.IDPat,
) core.Exp {
	binds := map[*core.IDPat]core.Exp{}
	for orig, copy := range fresh {
		binds[orig] = &core.ID{Pat: copy}
	}
	return substituteExp(e, binds)
}

// eqExp builds an equality between two expressions.
func eqExp(sys *types.System, a, b core.Exp) core.Exp {
	argType := sys.Tuple(a.Type(), b.Type())
	return &core.Apply{
		T: sys.Bool,
		Fn: &core.ID{Pat: &core.IDPat{
			T:    sys.Fn(argType, sys.Bool),
			Name: eqOpName,
		}},
		Arg: &core.Tuple{T: argType, Args: []core.Exp{a, b}},
	}
}
