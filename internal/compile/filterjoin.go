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
		return pushFilter(sys, filter), true
	}
	return r.rewriteDecl(decl)
}

// pushFilter applies the rules to a filter: an "exists" in its
// condition becomes a semi-join, and its conjuncts move into the join
// below it.
func pushFilter(sys *types.System, filter *core.Filter) core.Exp {
	e := filterExistsIntoJoin(sys, filter)
	if e == nil {
		return filterIntoJoin(sys, filter)
	}
	if f, isFilter := e.(*core.Filter); isFilter {
		return pushFilter(sys, f)
	}
	return e
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
	if join.Kind.FiltersLeft() {
		return filterIntoSemiJoin(sys, filter, join)
	}
	element, padded := joinElementOver(sys, join)
	m := sortConjuncts(sys, filter, join, element, padded)
	if len(m.toLeft) == 0 && len(m.toRight) == 0 &&
		len(m.toCondition) == 0 {
		return exp
	}
	left, right := join.Left, join.Right
	if len(m.toLeft) > 0 {
		left = filterOver(sys, left, join.LeftName,
			composeConjuncts(sys, m.toLeft))
	}
	if len(m.toRight) > 0 {
		// The conjuncts read the right element as "$1"; over the
		// right input, it is "$0".
		cond := subst2(sys, composeConjuncts(sys, m.toRight), nil,
			core.NewInput(types.ElemOf(join.Right.Type()), 0))
		right = filterOver(sys, right, join.RightName, cond)
	}
	var conditions []core.Exp
	if !isBoolLiteral(join.Condition, true) {
		conditions = append(conditions, join.Condition)
	}
	conditions = append(conditions, m.toCondition...)
	join2 := core.NewJoin(sys, join.Kind, join.Binder, left, right,
		composeConjuncts(sys, conditions))
	join2.LeftName, join2.RightName = join.LeftName, join.RightName
	return filterAbove(sys, join2, filter.Name, m.remaining)
}

// filterIntoSemiJoin moves the safe conjuncts of a filter over a semi-
// or anti-join into its left input: the join's element is its left
// element.
func filterIntoSemiJoin(sys *types.System, filter *core.Filter,
	join *core.Join,
) core.Exp {
	var toLeft, remaining []core.Exp
	for _, conjunct := range relConjuncts(filter.Condition) {
		if isSafe(conjunct) {
			toLeft = append(toLeft, conjunct)
		} else {
			remaining = append(remaining, conjunct)
		}
	}
	if len(toLeft) == 0 {
		return filter
	}
	join2 := core.NewJoin(sys, join.Kind, join.Binder,
		filterOver(sys, join.Left, filter.Name,
			composeConjuncts(sys, toLeft)),
		join.Right, join.Condition)
	join2.LeftName, join2.RightName = join.LeftName, join.RightName
	return filterAbove(sys, join2, filter.Name, remaining)
}

// filterAbove is a filter of the conjuncts that stay over input, or
// input itself if none do.
func filterAbove(sys *types.System, input core.Exp, name string,
	remaining []core.Exp,
) core.Exp {
	if len(remaining) == 0 {
		return input
	}
	f := core.NewFilter(input, composeConjuncts(sys, remaining))
	f.Name = name
	return f
}

// joinElementOver is a join's element over its own inputs, "$0" and
// "$1". The components of a side that an outer join pads with NONE are
// options, which the inputs' are not, so they are placeholders, which
// it also returns; a conjunct that reads one stays.
func joinElementOver(sys *types.System, join *core.Join,
) (core.Exp, []*core.IDPat) {
	leftElem := core.NewInput(types.ElemOf(join.Left.Type()), 0)
	rightElem := core.NewInput(types.ElemOf(join.Right.Type()), 1)
	var components []core.Exp
	var padded []*core.IDPat
	add := func(side []core.Exp, pad bool) {
		for _, c := range side {
			if pad {
				p := &core.IDPat{
					T: sys.Named("option", c.Type()), Name: "$pad",
				}
				padded = append(padded, p)
				c = &core.ID{Pat: p}
			}
			components = append(components, c)
		}
	}
	add(componentsOf(sys, join.Left, leftElem), join.Kind.LeftIsOption())
	add(componentsOf(sys, join.Right, rightElem),
		join.Kind.RightIsOption())
	return tupleOf(sys, components), padded
}

// moves are where the conjuncts of a filter over a join go.
type moves struct {
	toLeft, toRight, toCondition, remaining []core.Exp
}

// sortConjuncts decides where each conjunct of a filter over a join
// goes; see filterIntoJoin.
func sortConjuncts(sys *types.System, filter *core.Filter,
	join *core.Join, element core.Exp, padded []*core.IDPat,
) moves {
	var m moves
	inner := join.Kind == core.InnerJoin
	for _, conjunct := range relConjuncts(filter.Condition) {
		if !isSafe(conjunct) {
			m.remaining = append(m.remaining, conjunct)
			continue
		}
		e := subst2(sys, conjunct, element, nil)
		readsLeft, readsRight := readsInput(e, 0), readsInput(e, 1)
		switch {
		case referencesAny(e, padded):
			m.remaining = append(m.remaining, conjunct)
		case !inner && readsLeft && !readsRight:
			// Over an outer join, a conjunct may filter the side
			// whose every element is in the output, but may not join
			// the condition: a pair that fails it would be padded
			// with NONE, not removed.
			m.toLeft = append(m.toLeft, e)
		case !inner && readsRight && !readsLeft:
			m.toRight = append(m.toRight, e)
		case !inner:
			m.remaining = append(m.remaining, conjunct)
		case readsLeft && readsRight:
			m.toCondition = append(m.toCondition, e)
		case readsLeft:
			m.toLeft = append(m.toLeft, e)
		case readsRight && join.Binder == nil:
			m.toRight = append(m.toRight, e)
		case readsRight:
			m.toCondition = append(m.toCondition, e)
		default:
			m.remaining = append(m.remaining, conjunct)
		}
	}
	return m
}

// filterOver is a filter over input, named after the id it replaces,
// so that the plan reads as the query; and pushed into input, if that
// is a join.
func filterOver(sys *types.System, input core.Exp, name string,
	cond core.Exp,
) core.Exp {
	f := core.NewFilter(input, cond)
	f.Name = name
	return pushFilter(sys, f)
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

// filterExistsIntoJoin converts a conjunct "exists q" of a filter
// into a semi-join of the filter's input with q, and "not (exists q)"
// into an anti-join, when q reads the row only in equalities: "from c
// in customers where (exists s in sales where s.cust = c.id)" becomes
// a semi-join of customers with sales on "s.cust = c.id", which
// evaluates sales once rather than once per customer, and can be a
// hash join. It is morel-java's rule FILTER_EXISTS_INTO_JOIN
// (hydromatic/morel#518).
//
// A query inside a filter's condition reads the row by the name that
// the condition's "let val v = $0" gives it. The equalities may be in
// any filter of q that is under only filters and inner joins; each
// comes out of its filter and into the join's condition, re-expressed
// over q's element. If q reads the row anywhere else, the conjunct
// stays. The other conjuncts go into a filter below the join, so that
// they can move into the joins below it; a filter that reads its
// ordinal is not changed.
func filterExistsIntoJoin(sys *types.System, filter *core.Filter,
) core.Exp {
	if containsOrdinal(filter.Condition) {
		return nil
	}
	conjuncts := relConjuncts(filter.Condition)
	for i, conjunct := range conjuncts {
		rowName, q, anti, ok := existsConjunct(conjunct)
		if !ok {
			continue
		}
		d := decorrelate(sys, q, []*core.IDPat{rowName}, nil)
		if d == nil || len(d.conditions) == 0 {
			continue
		}
		conditions := semiConditions(sys, filter, d, rowName)
		if conditions == nil {
			continue
		}
		// The conjuncts before the "exists" are evaluated before it,
		// so they may go below the join; so may a conjunct after it
		// that can neither raise nor have an effect. A conjunct after
		// it that might raise stays above, so that it still sees only
		// the rows that pass the "exists".
		others := append([]core.Exp{}, conjuncts[:i]...)
		var above []core.Exp
		for _, c := range conjuncts[i+1:] {
			if isSafe(c) {
				others = append(others, c)
			} else {
				above = append(above, c)
			}
		}
		left := filter.Input
		if len(others) > 0 {
			left = filterOver(sys, left, filter.Name,
				composeConjuncts(sys, others))
		}
		kind := core.SemiJoin
		if anti {
			kind = core.AntiJoin
		}
		join := core.NewJoin(sys, kind, nil, left, d.rel,
			composeConjuncts(sys, conditions))
		join.LeftName, join.RightName = filter.Name, d.name
		if join.LeftName == "" {
			join.LeftName = core.ElementName(left)
		}
		return filterAbove(sys, join, filter.Name, above)
	}
	return nil
}

// existsConjunct decodes a conjunct "let val v = $0 in nonEmpty q end",
// or "empty q", or "not" of either (outside the "let" or in it),
// returning the row's name, v, the query, and whether the conjunct is
// true when the query is empty.
func existsConjunct(conjunct core.Exp,
) (*core.IDPat, core.Exp, bool, bool) {
	anti := false
	if arg, ok := callTo(conjunct, notName); ok {
		anti, conjunct = true, arg
	}
	let, isLet := conjunct.(*core.Let)
	if !isLet {
		return nil, nil, false, false
	}
	decl, isVal := let.Decl.(*core.NonRecValDecl)
	if !isVal {
		return nil, nil, false, false
	}
	rowName, isID := decl.Pat.(*core.IDPat)
	in, isIn := decl.Exp.(*core.Input)
	if !isID || !isIn || in.Ordinal != 0 {
		return nil, nil, false, false
	}
	e := let.Exp
	if arg, ok := callTo(e, notName); ok {
		anti, e = !anti, arg
	}
	q, ok := callTo(e, nonEmptyName)
	if !ok {
		if q, ok = callTo(e, emptyName); !ok {
			return nil, nil, false, false
		}
		anti = !anti
	}
	// Whether the query has a row does not depend on what its
	// elements are, or their order; so a projection or a sort on top
	// of it can go, provided that evaluating it could neither raise
	// nor have an effect.
	return rowName, stripSafe(q), anti, true
}

// semiConditions are a semi-join's conditions: the equalities taken
// from its query, over the join's "$0" and "$1". It returns nil if one
// reads the row inside a query nested in it, which "$0" would not
// reach.
func semiConditions(sys *types.System, filter *core.Filter,
	d *decorrelated, rowName *core.IDPat,
) []core.Exp {
	leftElem := core.NewInput(types.ElemOf(filter.Input.Type()), 0)
	rightElem := core.NewInput(types.ElemOf(d.rel.Type()), 1)
	var conditions []core.Exp
	for _, condition := range d.conditions {
		c := replaceIDs(sys, condition(rightElem),
			map[*core.IDPat]core.Exp{rowName: leftElem})
		if c == nil {
			return nil
		}
		conditions = append(conditions, c)
	}
	return conditions
}

// callTo returns the argument of a call to a built-in, by its name or
// its name in "Relational" or "Bool".
func callTo(e core.Exp, name string) (core.Exp, bool) {
	apply, isApply := e.(*core.Apply)
	if !isApply {
		return nil, false
	}
	switch builtinName(apply.Fn) {
	case name, "Relational." + name, "Bool." + name:
		return apply.Arg, true
	default:
		return nil, false
	}
}

// stripSafe removes the projections and sorts on top of a query whose
// expressions can neither raise nor have an effect.
func stripSafe(q core.Exp) core.Exp {
	for {
		switch r := q.(type) {
		case *core.Project:
			if !isSafe(r.Exp) || containsOrdinal(r.Exp) {
				return q
			}
			q = r.Input
		case *core.Sort:
			if !isSafe(r.Exp) || containsOrdinal(r.Exp) {
				return q
			}
			q = r.Input
		default:
			return q
		}
	}
}

// decorrelated is a query from which the equalities that read the
// outer row have been taken: the query without them, and each
// equality as a function of an expression for the query's element.
type decorrelated struct {
	rel        core.Exp
	conditions []func(element core.Exp) core.Exp
	// name is a name for the query's element, after the id of the
	// node it came from.
	name string
}

// decorrelate takes from a query the equalities that read the outer
// row, under any of rowNames; it returns nil if the query reads it in
// any other way. locals are the binders that the query binds above
// this node, which the outer side of an equality must not read.
func decorrelate(sys *types.System, node core.Exp,
	rowNames, locals []*core.IDPat,
) *decorrelated {
	if !referencesAny(node, rowNames) {
		return &decorrelated{rel: node, name: elementName(node)}
	}
	switch n := node.(type) {
	case *core.Filter:
		return decorrelateFilter(sys, n, rowNames, locals)
	case *core.Join:
		return decorrelateJoin(sys, n, rowNames, locals)
	default:
		return nil
	}
}

// decorrelateFilter is decorrelate of a filter.
func decorrelateFilter(sys *types.System, n *core.Filter,
	rowNames, locals []*core.IDPat,
) *decorrelated {
	if containsOrdinal(n.Condition) {
		return nil
	}
	input := decorrelate(sys, n.Input, rowNames, locals)
	if input == nil {
		return nil
	}
	conditions := append([]func(core.Exp) core.Exp{},
		input.conditions...)
	var remaining, conjuncts []core.Exp
	decomposeConjuncts(n.Condition, &conjuncts)
	for _, conjunct := range conjuncts {
		if !referencesAny(conjunct, rowNames) {
			// Once the equalities are taken out, the query is
			// evaluated once, over all of its elements, rather than
			// for each outer row; a conjunct that the equalities used
			// to guard would then be evaluated on more of them. Only
			// a conjunct that can neither raise nor have an effect
			// may stay.
			if !isSafe(conjunct) {
				return nil
			}
			remaining = append(remaining, conjunct)
			continue
		}
		e0, e1, isEq := equality(conjunct)
		if !isEq || !isOuterKey(e0, rowNames, locals) &&
			!isOuterKey(e1, rowNames, locals) {
			return nil
		}
		// The equality, given an expression for the element; the
		// filter's element is its input's.
		conditions = append(conditions, func(element core.Exp) core.Exp {
			return subst2(sys, conjunct, element, nil)
		})
	}
	return &decorrelated{
		rel:        filterAbove(sys, input.rel, n.Name, remaining),
		conditions: conditions,
		name:       n.Name,
	}
}

// decorrelateJoin is decorrelate of a join.
func decorrelateJoin(sys *types.System, n *core.Join,
	rowNames, locals []*core.IDPat,
) *decorrelated {
	if n.Kind != core.InnerJoin || containsOrdinal(n.Condition) ||
		referencesAny(n.Condition, rowNames) {
		return nil
	}
	left := decorrelate(sys, n.Left, rowNames, locals)
	rightLocals := locals
	if n.Binder != nil {
		rightLocals = append(append([]*core.IDPat{}, locals...), n.Binder)
	}
	right := decorrelate(sys, n.Right, rowNames, rightLocals)
	if left == nil || right == nil {
		return nil
	}
	// Once equalities are taken out of an input, the join sees elements
	// that they used to exclude. So its condition must be one that can
	// neither raise nor have an effect, and if equalities came out of
	// the left input, the right input may not depend on the left.
	if len(left.conditions) > 0 || len(right.conditions) > 0 {
		if !isSafe(n.Condition) ||
			len(left.conditions) > 0 && n.Binder != nil {
			return nil
		}
	}
	join2 := core.NewJoin(sys, n.Kind, n.Binder, left.rel, right.rel,
		n.Condition)
	join2.LeftName, join2.RightName = n.LeftName, n.RightName
	leftCount := core.ComponentCount(join2.Left)
	rightCount := core.ComponentCount(join2.Right)
	var conditions []func(core.Exp) core.Exp
	for _, c := range left.conditions {
		conditions = append(conditions, func(element core.Exp) core.Exp {
			return c(slice(sys, join2, element, 0, leftCount))
		})
	}
	// A condition from the right input may also read the left input's
	// element (the right input of a dependent join may), by the join's
	// binder; that, too, becomes a component of the join's element.
	for _, c := range right.conditions {
		conditions = append(conditions, func(element core.Exp) core.Exp {
			e := c(slice(sys, join2, element, leftCount,
				leftCount+rightCount))
			if n.Binder == nil {
				return e
			}
			return replaceIDs(sys, e, map[*core.IDPat]core.Exp{
				n.Binder: slice(sys, join2, element, 0, leftCount),
			})
		})
	}
	return &decorrelated{
		rel: join2, conditions: conditions, name: left.name,
	}
}

// isOuterKey reports whether one side of an equality is a key over the
// outer row: it reads the row, and neither the query's own element nor
// its binders.
func isOuterKey(exp core.Exp, rowNames, locals []*core.IDPat) bool {
	return referencesAny(exp, rowNames) && !referencesAny(exp, locals) &&
		!readsInput(exp, 0) && !readsInput(exp, 1)
}

// slice is the expression for the element of an input of a join, given
// an expression for the join's element: its components start to end,
// as a tuple if there are several.
func slice(sys *types.System, join *core.Join, element core.Exp,
	start, end int,
) core.Exp {
	components := componentsOf(sys, join, element)[start:end]
	if len(components) == 1 {
		return components[0]
	}
	return tupleOf(sys, components)
}

// elementName is a name for the element of a query, after its id, if
// it has one.
func elementName(node core.Exp) string {
	if join, isJoin := node.(*core.Join); isJoin {
		return elementName(join.Left)
	}
	if name := core.ElementName(node); name != "" {
		return name
	}
	switch r := node.(type) {
	case *core.Project:
		if r.Name != "" {
			return r.Name
		}
	case *core.Group:
		if r.Name != "" {
			return r.Name
		}
	}
	return "e"
}

// replaceIDs replaces each use of the given variables, outside any
// relation nested in the expression; it returns nil if a nested
// relation reads one of them.
func replaceIDs(sys *types.System, exp core.Exp,
	with map[*core.IDPat]core.Exp,
) core.Exp {
	pats := make([]*core.IDPat, 0, len(with))
	for p := range with {
		pats = append(pats, p)
	}
	nested := false
	r := &rewriter{sys: sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		switch e := e.(type) {
		case *core.ID:
			if x, ok := with[e.Pat]; ok {
				return x, true
			}
		case core.Rel:
			if referencesAny(e, pats) {
				nested = true
			}
			return e, true
		}
		return nil, false
	}
	result := r.rewriteExp(exp)
	if nested {
		return nil
	}
	return result
}
