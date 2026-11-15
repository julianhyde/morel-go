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
	"slices"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/types"
)

// addHashScan compiles the scan at steps[i] as a hash join, if it can
// be one, and returns how many steps it consumed: zero if it cannot.
//
// It can be one if it is an inner join, followed by the "where" that
// is its "on", or a left join with an "on"; if the condition has an
// equality between a key over the input row and a key over the
// element (see equiKeys); if the scan does not depend on the input
// row; and if neither the scan nor the condition reads the
// "ordinal". A right or full join is evaluated differently, and is
// not hashed. Otherwise the join remains a nested loop.
func (b *fromBuilder) addHashScan(steps []core.FromStep, i int,
) (int, error) {
	scan, isScan := steps[i].(*core.Scan)
	if !isScan ||
		len(b.rowPats) == 0 && !scan.Exists.FiltersLeft() {
		return 0, nil
	}
	var cond core.Exp
	consumed := 1
	left := false
	emit := eval.EmitPairs
	switch {
	case scan.Exists == core.SemiJoin:
		cond, emit = scan.On, eval.EmitSemi
	case scan.Exists == core.AntiJoin:
		cond, emit = scan.On, eval.EmitAnti
	case scan.Join == ast.LeftJoinOp:
		cond, left = scan.On, true
	case scan.Join == ast.RightJoinOp, scan.Join == ast.FullJoinOp:
		return 0, nil
	default:
		if i+1 < len(steps) {
			if w, isWhere := steps[i+1].(*core.Where); isWhere && w.On {
				cond, consumed = w.Exp, 2
			}
		}
	}
	if cond == nil {
		return 0, nil
	}
	var leftIDs []*core.IDPat
	for _, p := range b.rowPats {
		leftIDs = append(leftIDs, core.PatIDs(p)...)
	}
	rightIDs := core.PatIDs(scan.Pat)
	dependent := referencesAny(scan.Exp, leftIDs)
	if dependent && left {
		return 0, nil
	}
	noHash := dependent || containsOrdinal(scan.Exp) || containsOrdinal(cond) ||
		b.ordinal != nil &&
			(referencesAny(scan.Exp, []*core.IDPat{b.ordinal}) ||
				referencesAny(cond, []*core.IDPat{b.ordinal}))
	var leftKey, rightKey core.Exp
	if !noHash {
		leftKey, rightKey = equiKeys(b.c.sys, cond, rightIDs)
	}
	if leftKey == nil {
		if left {
			return 0, nil
		}
		// Not hashed; but an inner join's condition is still the
		// scan's, evaluated for each pair.
		return consumed, b.addScanWithCond(scan, cond, emit)
	}
	c := b.c
	name := c.scanName(scan.Pat)
	source, err := c.compileExp(scan.Exp)
	if err != nil {
		return 0, err
	}
	leftKeyCode, err := c.compileExp(leftKey)
	if err != nil {
		return 0, err
	}
	layout := c.layout
	c.layout = c.layout.push(rightIDs)
	pat, err := c.compilePat(scan.Pat)
	if err != nil {
		return 0, err
	}
	rightKeyCode, err := c.compileExp(rightKey)
	if err != nil {
		return 0, err
	}
	condCode, err := c.compileJoinCond(cond)
	if err != nil {
		return 0, err
	}
	patSlots := c.patSlots([]core.Pat{scan.Pat})
	b.stages = append(b.stages, &eval.HashScanStage{
		Source: source, Pat: pat, Name: name,
		LeftKey: leftKeyCode, RightKey: rightKeyCode, Cond: condCode,
		Left: left, PatSlots: patSlots, Emit: emit,
	})
	b.bind(scan.Pat, patSlots, emit, layout)
	return consumed, nil
}

// addScanWithCond compiles an inner join's scan and its condition,
// which it evaluates for each pair of a row and an element.
func (b *fromBuilder) addScanWithCond(scan *core.Scan, cond core.Exp,
	emit eval.Emit,
) error {
	c := b.c
	name := c.scanName(scan.Pat)
	source, err := c.compileExp(scan.Exp)
	if err != nil {
		return err
	}
	layout := c.layout
	c.layout = c.layout.push(core.PatIDs(scan.Pat))
	pat, err := c.compilePat(scan.Pat)
	if err != nil {
		return err
	}
	condCode, err := c.compileJoinCond(cond)
	if err != nil {
		return err
	}
	b.stages = append(b.stages, &eval.ScanStage{
		Source: source, Pat: pat, Name: name, Cond: condCode, Emit: emit,
	})
	b.bind(scan.Pat, c.patSlots([]core.Pat{scan.Pat}), emit, layout)
	return nil
}

// bind adds a join's scan pattern to the row; but a semi-join or an
// anti-join passes on its input rows, so its pattern, which was in
// scope only in its condition, leaves the stack layout as it found
// it.
func (b *fromBuilder) bind(pat core.Pat, slots []int, emit eval.Emit,
	layout *stackLayout,
) {
	if emit != eval.EmitPairs {
		b.c.layout = layout
		return
	}
	b.rowPats = append(b.rowPats, pat)
	b.allSlots = append(b.allSlots, slots...)
}

// compileJoinCond compiles a join's condition. Its "andalso"s, which
// resolve to cases, compile to the connective itself, as morel-java's
// join conditions do.
func (c *compiler) compileJoinCond(cond core.Exp) (eval.Code, error) {
	if a, b, f, ok := asBoolCase(cond); ok && isBoolLiteral(f, false) {
		aCode, err := c.compileJoinCond(a)
		if err != nil {
			return nil, err
		}
		bCode, err := c.compileJoinCond(b)
		if err != nil {
			return nil, err
		}
		return eval.Andalso(aCode, bCode), nil
	}
	return c.compileExp(cond)
}

// equiKeys returns the keys of a join condition that a hash join can
// use: a pair (leftKey, rightKey) such that the condition implies
// leftKey = rightKey, the right key reads only the element's
// variables (rightIDs), the left key reads none of them, both are
// simple (see isSimple), and their type's equality is that of its
// representation (see isHashable). If the condition has several such
// equalities, the keys are tuples. It returns nils if it has none.
func equiKeys(sys *types.System, cond core.Exp, rightIDs []*core.IDPat,
) (core.Exp, core.Exp) {
	var conjuncts []core.Exp
	decomposeConjuncts(cond, &conjuncts)
	var leftKeys, rightKeys []core.Exp
	for _, conjunct := range conjuncts {
		e0, e1, ok := equality(conjunct)
		if !ok || !isHashable(e0.Type()) ||
			!isSimple(e0) || !isSimple(e1) {
			continue
		}
		vars0, vars1 := expVars(e0), expVars(e1)
		left0, left1 := disjoint(vars0, rightIDs), disjoint(vars1, rightIDs)
		right0 := len(vars0) > 0 && subset(vars0, rightIDs)
		right1 := len(vars1) > 0 && subset(vars1, rightIDs)
		switch {
		case left0 && right1:
			leftKeys, rightKeys = append(leftKeys, e0), append(rightKeys, e1)
		case right0 && left1:
			leftKeys, rightKeys = append(leftKeys, e1), append(rightKeys, e0)
		}
	}
	switch len(leftKeys) {
	case 0:
		return nil, nil
	case 1:
		return leftKeys[0], rightKeys[0]
	default:
		return tupleOf(sys, leftKeys), tupleOf(sys, rightKeys)
	}
}

// equality returns the two sides of "e0 = e1".
func equality(e core.Exp) (core.Exp, core.Exp, bool) {
	apply, isApply := e.(*core.Apply)
	if !isApply {
		return nil, nil, false
	}
	id, isID := apply.Fn.(*core.ID)
	if !isID || id.Pat.Name != eqOpName {
		return nil, nil, false
	}
	tuple, isTuple := apply.Arg.(*core.Tuple)
	if !isTuple || len(tuple.Args) != 2 {
		return nil, nil, false
	}
	return tuple.Args[0], tuple.Args[1], true
}

// tupleOf builds a tuple of expressions.
func tupleOf(sys *types.System, exps []core.Exp) core.Exp {
	ts := make([]types.Type, len(exps))
	for i, e := range exps {
		ts[i] = e.Type()
	}
	return &core.Tuple{T: sys.Tuple(ts...), Args: exps}
}

// expVars returns the variables an expression reads.
func expVars(e core.Exp) []*core.IDPat {
	var pats []*core.IDPat
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		if id, isID := e.(*core.ID); isID {
			pats = append(pats, id.Pat)
		}
		return nil, false
	}
	r.rewriteExp(e)
	return pats
}

// disjoint reports whether none of pats is in set.
func disjoint(pats, set []*core.IDPat) bool {
	for _, p := range pats {
		if slices.Contains(set, p) {
			return false
		}
	}
	return true
}

// subset reports whether every one of pats is in set.
func subset(pats, set []*core.IDPat) bool {
	for _, p := range pats {
		if !slices.Contains(set, p) {
			return false
		}
	}
	return true
}

// isHashable reports whether values of a type can be hashed: whether
// two are equal, by Morel's "=", exactly when their representations
// are. Not "real", whose NaN and ~0.0 differ.
func isHashable(t types.Type) bool {
	// lint: sort until '^\t}' where '^\tcase '
	switch t := types.Unalias(t).(type) {
	case *types.List:
		return isHashable(t.Elem)
	case *types.Named:
		switch t.Name {
		case "date", decimalName:
			return true
		case "option":
			return len(t.Args) == 1 && isHashable(t.Args[0])
		default:
			return false
		}
	case *types.Primitive:
		switch t.String() {
		case boolName, charName, intName, stringName, unitName:
			return true
		default:
			return false
		}
	case *types.Record:
		for _, f := range t.Fields {
			if !isHashable(f.Type) {
				return false
			}
		}
		return true
	case *types.Tuple:
		for _, a := range t.Args {
			if !isHashable(a) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
