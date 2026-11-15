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
	"strings"

	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/types"
)

// PushFilters applies filterIntoJoin to every filter in a
// declaration's trees, as morel-java's rules do once grounding has
// run.
func PushFilters(sys *types.System, decl core.Decl) core.Decl {
	r := &rewriter{sys: sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		filter, isFilter := e.(*core.Filter)
		if !isFilter {
			return nil, false
		}
		// Bottom-up: the filter's input first, then the filter.
		input := r.rewriteExp(filter.Input)
		cond := r.rewriteExp(filter.Condition)
		if input != filter.Input || cond != filter.Condition {
			f := core.NewFilter(input, cond)
			f.Name = filter.Name
			filter = f
		}
		return filterIntoJoin(sys, filter), true
	}
	return r.rewriteDecl(decl)
}

// filterIntoJoin moves the conjuncts of a filter over a join into
// the join, as far down as each can go: into the left input, if it
// reads only the left element; into the right input, if it reads only
// the right element and the right input does not read the left
// element; into the join's condition, if it reads both. It is
// morel-java's rule FILTER_INTO_JOIN (hydromatic/morel#505).
//
// So "from x in xs, y in ys where x.b = y.c andalso y.d > 0", whose
// join is a cross product, becomes a join on "x.b = y.c" of xs with
// ys filtered by "y.d > 0", and the join can be a hash join. A filter
// it puts over a join is pushed in turn, so a conjunct reaches the
// lowest join that reads both its sides.
//
// Over a left join, only a conjunct that reads only the left element
// moves, into the left input; over a right join, only one that reads
// only the right element, into the right input. A conjunct that reads
// the side padded with NONE stays, and nothing moves over a full
// join. A conjunct moves only if it is safe (see isSafe), and neither
// a filter nor a join that reads its ordinal is changed, because
// moving a conjunct changes what is counted.
func filterIntoJoin(sys *types.System, exp core.Exp) core.Exp {
	filter, isFilter := exp.(*core.Filter)
	if !isFilter || containsOrdinal(filter.Condition) {
		return exp
	}
	join, isJoin := filter.Input.(*core.Join)
	if !isJoin || join.Kind == core.FullJoin ||
		containsOrdinal(join.Condition) {
		return exp
	}
	// The element, over the join's own inputs, "$0" and "$1". The
	// components of a side that an outer join pads with NONE are
	// options, which the inputs' are not, so they are placeholders;
	// a conjunct that reads one stays.
	leftElem := core.NewInput(types.ElemOf(join.Left.Type()), 0)
	rightElem := core.NewInput(types.ElemOf(join.Right.Type()), 1)
	var components []core.Exp
	var padded []*core.IDPat
	add := func(side []core.Exp, pad bool) {
		for _, c := range side {
			if pad {
				p := &core.IDPat{T: sys.Named("option", c.Type()), Name: "$pad"}
				padded = append(padded, p)
				c = &core.ID{Pat: p}
			}
			components = append(components, c)
		}
	}
	add(componentsOf(sys, join.Left, leftElem), join.Kind.LeftIsOption())
	add(componentsOf(sys, join.Right, rightElem),
		join.Kind.RightIsOption())
	element := tupleOf(sys, components)
	var toLeft, toRight, toCondition, remaining []core.Exp
	for _, conjunct := range relConjuncts(filter.Condition) {
		if !isSafe(conjunct) {
			remaining = append(remaining, conjunct)
			continue
		}
		e := subst2(sys, conjunct, element, nil)
		readsLeft, readsRight := readsInput(e, 0), readsInput(e, 1)
		inner := join.Kind == core.InnerJoin
		switch {
		case referencesAny(e, padded):
			remaining = append(remaining, conjunct)
		case !inner && readsLeft && !readsRight:
			// Over an outer join, a conjunct may filter the side
			// whose every element is in the output, but may not join
			// the condition: a pair that fails it would be padded
			// with NONE, not removed.
			toLeft = append(toLeft, e)
		case !inner && readsRight && !readsLeft:
			toRight = append(toRight, e)
		case !inner:
			remaining = append(remaining, conjunct)
		case readsLeft && readsRight:
			toCondition = append(toCondition, e)
		case readsLeft:
			toLeft = append(toLeft, e)
		case readsRight && join.Binder == nil:
			toRight = append(toRight, e)
		case readsRight:
			toCondition = append(toCondition, e)
		default:
			remaining = append(remaining, conjunct)
		}
	}
	if len(toLeft) == 0 && len(toRight) == 0 && len(toCondition) == 0 {
		return exp
	}
	left, right := join.Left, join.Right
	if len(toLeft) > 0 {
		left = filterOver(sys, left, join.LeftName,
			composeConjuncts(sys, toLeft))
	}
	if len(toRight) > 0 {
		// The conjuncts read the right element as "$1"; over the
		// right input, it is "$0".
		cond := subst2(sys, composeConjuncts(sys, toRight), nil,
			core.NewInput(rightElem.T, 0))
		right = filterOver(sys, right, join.RightName, cond)
	}
	var conditions []core.Exp
	if !isBoolLiteral(join.Condition, true) {
		conditions = append(conditions, join.Condition)
	}
	conditions = append(conditions, toCondition...)
	join2 := core.NewJoin(sys, join.Kind, join.Binder, left, right,
		composeConjuncts(sys, conditions))
	join2.LeftName, join2.RightName = join.LeftName, join.RightName
	if len(remaining) == 0 {
		return join2
	}
	f := core.NewFilter(join2, composeConjuncts(sys, remaining))
	f.Name = filter.Name
	return f
}

// filterOver is a filter over input, named after the id it replaces,
// so that the plan reads as the query; and pushed into input, if that
// is a join.
func filterOver(sys *types.System, input core.Exp, name string,
	cond core.Exp,
) core.Exp {
	f := core.NewFilter(input, cond)
	f.Name = name
	return filterIntoJoin(sys, f)
}

// relConjuncts returns the conjuncts of a filter's condition.
//
// When a conjunct refers to the row from inside a query, as in "where
// x.a = y.b andalso (exists z in zs where z.c = x.a)", the condition
// names the row, and becomes "let val v = $0 in ... end". Such a
// condition is split too: each conjunct of its body that does not
// mention v is a conjunct on its own, and each that does is wrapped
// in the let.
func relConjuncts(cond core.Exp) []core.Exp {
	var top, out []core.Exp
	decomposeConjuncts(cond, &top)
	for _, e := range top {
		let, isLet := e.(*core.Let)
		if !isLet {
			out = append(out, e)
			continue
		}
		decl, isVal := let.Decl.(*core.NonRecValDecl)
		pat, isID := decl.Pat.(*core.IDPat)
		if !isVal || !isID || !isAtom(decl.Exp) && !isInput(decl.Exp) {
			out = append(out, e)
			continue
		}
		for _, c := range relConjuncts(let.Exp) {
			if referencesAny(c, []*core.IDPat{pat}) {
				c = &core.Let{Decl: decl, Exp: c}
			}
			out = append(out, c)
		}
	}
	return out
}

// isInput reports whether an expression is "$0" or "$1".
func isInput(e core.Exp) bool {
	_, ok := e.(*core.Input)
	return ok
}

// readsInput reports whether an expression reads the input element
// of the given ordinal, outside any relation nested in it.
func readsInput(exp core.Exp, ordinal int) bool {
	found := false
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		switch e := e.(type) {
		case *core.Input:
			if e.Ordinal == ordinal {
				found = true
			}
		case core.Rel:
			return e, true
		}
		return nil, false
	}
	r.rewriteExp(exp)
	return found
}

// isSafe reports whether an expression is safe: simple (see
// isSimple), or a comparison, "elem", "notelem", "andalso", "orelse"
// or "not" of safe expressions, or a tuple, field selection or
// constructor application of safe expressions. Evaluating one can
// neither raise nor have an effect, so a condition that is safe may be
// evaluated earlier, or on fewer rows, than it was written to be.
func isSafe(e core.Exp) bool {
	if isSimple(e) {
		return true
	}
	if a, b, c, ok := asBoolCase(e); ok {
		// "andalso" and "orelse".
		return isSafe(a) && isSafe(b) && isSafe(c)
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		switch fn := e.Fn.(type) {
		case *core.Con, *core.Selector:
			return isSafe(e.Arg)
		case *core.ID:
			return isSafeFunction(fn.Pat.Name) && isSafe(e.Arg)
		default:
			return false
		}
	case *core.Tuple:
		for _, arg := range e.Args {
			if !isSafe(arg) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// isSafeFunction reports whether a built-in can neither raise nor
// have an effect: a comparison, such as "op <" or "Decimal.<",
// "elem", "notelem", or "not".
func isSafeFunction(name string) bool {
	switch name {
	case notName, opElem, opNotElem, "Bool.not":
		return true
	}
	op := strings.TrimPrefix(name, "op ")
	if i := strings.LastIndexByte(op, '.'); i >= 0 && op == name {
		op = op[i+1:]
	}
	switch op {
	case "<", "<=", "<>", "=", ">", ">=":
		return true
	default:
		return false
	}
}
