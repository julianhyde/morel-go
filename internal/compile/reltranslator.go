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
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/types"
)

// Translates a core.From -- a list of steps, each executing in an
// environment of bindings -- into a relational tree of core.Rel
// nodes, whose expressions name their input element "$0" and bind
// nothing. It is morel#449's RelTranslator.
//
// This is variable elimination: a binder becomes an expression
// over "$0" -- the element itself where the element is that
// binder's value, otherwise a field of it -- and the binding list
// disappears, because a node's element type says everything the
// bindings said.
//
// morel-java reads each step's environment off the step, which
// carries one. morel-go's steps do not, so the translation
// maintains the row itself: rowPats, which is the binders in
// scope, and the element type they describe.
//
// A construct the translation cannot express makes it return nil
// rather than guess. That is not a failure: the tree is a shadow
// until the resolver builds it, and a query it declines is one
// the step list still answers.

// relAccess is the expression, over the current tree's element,
// that reads each binder in scope.
//
// Between steps the map is uniform: the element either is the only
// binder's value ("$0") or is a record with one field per binder
// ("#name $0"). It is a slice rather than a map because the order
// is the record's field order, and a map would not keep it.
type relAccess struct {
	pats []*core.IDPat
	exps []core.Exp
}

func (a *relAccess) get(pat *core.IDPat) core.Exp {
	for i, p := range a.pats {
		if p == pat {
			return a.exps[i]
		}
	}
	return nil
}

func (a *relAccess) put(pat *core.IDPat, exp core.Exp) {
	for i, p := range a.pats {
		if p == pat {
			a.exps[i] = exp
			return
		}
	}
	a.pats = append(a.pats, pat)
	a.exps = append(a.exps, exp)
}

func (a *relAccess) len() int { return len(a.pats) }

func (a *relAccess) clear() { a.pats, a.exps = nil, nil }

func (a *relAccess) clone() *relAccess {
	return &relAccess{
		pats: slices.Clone(a.pats), exps: slices.Clone(a.exps),
	}
}

// relTranslator is one translation, of one query.
type relTranslator struct {
	sys *types.System
	// access reads each binder in scope off the tree's element.
	access *relAccess
	// rowPats are the binders in scope, which morel-java reads off
	// the step's environment.
	rowPats []*core.IDPat
	// rowIsRecord is whether the element is a record even where
	// there is one binder: a group's element is a record of its keys
	// and aggregates, one field or many, and the query's bare value
	// is a projection at its end. A scan's or a yield's lone binder
	// is the element itself.
	rowIsRecord bool
	// exp is the tree built so far, nil before the first step.
	exp core.Exp
	// nextName numbers the binders this translation generates.
	// Per tree, so that a query's plan text does not depend on how
	// many names were generated before it. A "$" cannot occur in
	// an identifier, so these cannot capture a name the query
	// wrote.
	nextName int
	// deferred is whether the element is a join's components
	// rather than the record the bindings describe.
	//
	// A join's element is its inputs' components, which is not
	// what the query's bindings describe, so normalizing after one
	// would project -- and a projection between two joins is what
	// stops them nesting, which is the whole point of
	// concatenating. So the projection waits until something needs
	// the row: a step that computes its own element, or the root.
	// Until then the access map carries paths into the components,
	// which is all any expression needs.
	deferred bool
	// patternAccess is whether the access map came from
	// destructuring a scan's pattern rather than from an element a
	// step computed. Only such a map can read the fields in an
	// order the bindings do not describe.
	patternAccess bool
	// done is whether a trailing yield has made the element the
	// query's own, so that nothing more is normalized.
	done bool
	// declined says what the translation could not express, and is
	// empty where it succeeded. It is what the shadow counts, and
	// what says where the translation has to grow next.
	declined string
}

// decline records why the translation cannot go on, and returns
// false so that a caller can "return t.decline(...)".
func (t *relTranslator) decline(reason string) bool {
	if t.declined == "" {
		t.declined = reason
	}
	return false
}

// TranslateFrom translates a query into a relational tree, or
// returns nil and a reason where the query contains something the
// translation does not express. The result is an expression, not
// necessarily a core.Rel: the tree for "from e in emps" is the
// leaf "emps".
func TranslateFrom(sys *types.System, from *core.From) (core.Exp,
	string,
) {
	t := &relTranslator{sys: sys, access: &relAccess{}}
	exp := t.from(from)
	if exp == nil {
		if os.Getenv("MOREL_REL_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "translation declined: %s <= %s\n",
				t.declined, UnparseDecl(sys, &core.NonRecValDecl{
					Pat: &core.IDPat{T: from.T, Name: "q"}, Exp: from,
				}))
		}
		return nil, t.declined
	}
	return translateNested(sys, exp), ""
}

// translateNested replaces every query inside a tree's
// expressions with a tree of its own.
//
// A query may appear wherever an expression may, including inside
// the expressions of another tree -- the right input of a
// dependent join is the common case -- and a tree that left one
// as a step list would print half in one notation and half in the
// other. A query that declines is left as it is, which is the
// same answer the top level gives.
func translateNested(sys *types.System, exp core.Exp) core.Exp {
	r := &rewriter{sys: sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		from, isFrom := e.(*core.From)
		if !isFrom {
			return nil, false
		}
		if from.Ordinal != nil {
			// The query counts its rows in a hidden variable that
			// the tree has nowhere to put. Left as a step list it
			// still counts them.
			return nil, false
		}
		return nestedTree(sys, from)
	}
	return r.rewriteExp(exp)
}

// nestedNext numbers every "v$" binder that translation, grounding
// or resolution generates. The printer renumbers generated names
// by first occurrence, so the count need only be unique.
var nestedNext int

// nestedTree translates a query nested in a tree's expression
// into a tree of its own, and reports whether it could.
//
// A tree's "$0" is its own input's element, so a nested query
// that reads the enclosing element cannot read it as "$0". The
// element is bound to a name instead, "let val v$0 = $0 in ...
// end", and the nested tree reads the name -- which is what
// morel-java's resolver does where a nested tree wants the row.
//
// A query that tests for existence is a boolean, not a
// collection, so its tree stands under "Relational.nonEmpty",
// which is how morel-java's resolver writes "exists".
func nestedTree(sys *types.System, from *core.From) (core.Exp, bool) {
	binds := map[int]*core.IDPat{}
	var order []int
	query := from
	if mentionsInput(from) {
		rw := &rewriter{sys: sys}
		rw.exp = func(e core.Exp) (core.Exp, bool) {
			// lint: sort until '^\t\t}' where '^\t\tcase '
			switch e := e.(type) {
			case *core.Input:
				pat := binds[e.Ordinal]
				if pat == nil {
					pat = &core.IDPat{
						T: e.T, Name: "v$" + itoa(nestedNext),
					}
					nestedNext++
					binds[e.Ordinal] = pat
					order = append(order, e.Ordinal)
				}
				return &core.ID{Pat: pat}, true
			case core.Rel:
				// A tree nested in this query rebinds "$0"; what
				// is inside it is not about the enclosing element.
				return e, true
			default:
				return nil, false
			}
		}
		rewritten, isFrom := rw.rewriteExp(from).(*core.From)
		if !isFrom {
			return nil, false
		}
		query = rewritten
	}
	negated := false
	if from.Kind == ast.ForallOp {
		// A universal quantifier is "not (exists ... where not
		// ...)" in morel-java's Core: the rows its "require" step
		// rejects are the witnesses against it.
		query = existsAgainst(sys, query)
		if query == nil {
			return nil, false
		}
		negated = true
	}
	inner, _ := TranslateFrom(sys, query)
	if inner == nil {
		return nil, false
	}
	switch from.Kind {
	case ast.ExistsOp, ast.ForallOp:
		inner = nonEmptyOf(sys, inner)
		if negated {
			inner = notOf(sys, inner)
		}
	default:
		if inner.Type() != from.Type() {
			return nil, false
		}
	}
	for _, ordinal := range slices.Backward(order) {
		pat := binds[ordinal]
		inner = &core.Let{
			Decl: &core.NonRecValDecl{
				Pat: pat, Exp: core.NewInput(pat.T, ordinal),
			},
			Exp: inner,
		}
	}
	return inner, true
}

// nonEmptyOf is "Relational.nonEmpty coll".
// existsAgainst rewrites a "forall" as the "exists" of the rows
// that fail its "require" step, or nil where the last step is not
// one.
func existsAgainst(sys *types.System, from *core.From) *core.From {
	n := len(from.Steps)
	if n == 0 {
		return nil
	}
	require, isYield := from.Steps[n-1].(*core.Yield)
	if !isYield || require.Exp == nil || require.Exp.Type() != sys.Bool {
		return nil
	}
	steps := make([]core.FromStep, 0, n)
	steps = append(steps, from.Steps[:n-1]...)
	steps = append(steps, &core.Where{Exp: notOf(sys, require.Exp)})
	return &core.From{T: from.T, Steps: steps, Kind: ast.ExistsOp}
}

// notOf negates a boolean expression, "not e".
func notOf(sys *types.System, e core.Exp) core.Exp {
	return &core.Apply{
		T: sys.Bool,
		Fn: &core.ID{Pat: &core.IDPat{
			T: sys.Fn(sys.Bool, sys.Bool), Name: notName,
		}},
		Arg: e,
	}
}

func nonEmptyOf(sys *types.System, coll core.Exp) core.Exp {
	return &core.Apply{
		T: sys.Bool,
		Fn: &core.ID{Pat: &core.IDPat{
			T: sys.Fn(coll.Type(), sys.Bool), Name: relNonEmptyName,
		}},
		Arg: coll,
	}
}

func (t *relTranslator) from(from *core.From) core.Exp {
	if len(from.Steps) == 0 {
		// Bare "from" iterates over a single element, which is
		// unit.
		return t.unitCollection()
	}
	for _, step := range from.Steps {
		if !t.step(step) {
			return nil
		}
		if !t.deferred && !t.done {
			t.normalize()
		}
	}
	if t.deferred && !t.done {
		// The root's element is the query's, so the row is needed
		// here even though no step needed it.
		t.normalize()
	}
	t.closeRow(from)
	return t.matchKind(from)
}

// closeRow makes the tree's element the query's where they differ
// only in whether a lone value is wrapped in a record: a group's
// element is a record of one field where the query, "group i",
// answers the bare value, and the projection that unwraps it is the
// query's last node.
func (t *relTranslator) closeRow(from *core.From) {
	if t.exp == nil || from.Kind == ast.ExistsOp ||
		from.Kind == ast.ForallOp {
		return
	}
	if wanted := types.ElemOf(from.T); wanted != nil {
		if aligned := t.align(t.exp, wanted); aligned != nil {
			t.exp = aligned
		}
	}
}

// matchKind makes the tree's kind the query's.
//
// morel-go erases "unorder": it is not a step, and a query that
// forgets its ordering says so only in its own type. The tree says
// it in a node, because a kind is part of a node's type, so the
// node the step list does not have is added here.
func (t *relTranslator) matchKind(from *core.From) core.Exp {
	if t.exp == nil || from.Kind == ast.ExistsOp ||
		from.Kind == ast.ForallOp {
		// A quantifier's result is a boolean, not a collection,
		// so the query's type says nothing about the tree's kind.
		return t.exp
	}
	want, got := from.T, t.exp.Type()
	if types.IsOrdered(want) == types.IsOrdered(got) {
		return t.exp
	}
	if types.IsOrdered(want) {
		// Nothing makes a bag a list but a sort, and there is no
		// key to sort on. This is an unbounded scan, whose leaf
		// is the placeholder that grounding replaces, and whose
		// kind is therefore not yet the query's.
		t.decline("unground leaf: " + got.String() + " want " +
			want.String())
		return nil
	}
	return core.NewUnorder(t.sys, t.exp)
}

// setOp translates a set operator. Morel aligns the branches of a
// union, so an input whose element is a one-field record where the
// row describes a bare value (or the other way about) is converted
// with a projection.
func (t *relTranslator) setOp(s *core.SetOp) bool {
	wanted := t.elementType()
	inputs := make([]core.Exp, 0, len(s.Args)+1)
	inputs = append(inputs, t.exp)
	for _, arg := range s.Args {
		inputs = append(inputs, t.rewrite(arg))
	}
	for i, input := range inputs {
		aligned := t.align(input, wanted)
		if aligned == nil {
			return t.decline("set operator branch alignment")
		}
		inputs[i] = aligned
	}
	var kind core.SetKind
	// lint: sort until '^	}' where '^	case '
	switch s.Kind {
	case ast.ExceptOp:
		kind = core.ExceptSet
	case ast.IntersectOp:
		kind = core.IntersectSet
	default:
		kind = core.UnionSet
	}
	t.exp = core.NewSetRel(t.sys, kind, s.Distinct, inputs)
	return true
}

// through passes the query so far through a function and scans
// what comes back: "from ... through p in f" is "from p in f (from
// ...)", as morel-java's resolver writes it.
func (t *relTranslator) through(s *core.Through) bool {
	collection := t.applied(s.Fn)
	if collection == nil {
		return t.decline("through with a non-function")
	}
	t.exp = nil
	t.rowPats = nil
	t.access.clear()
	t.rowIsRecord = false
	return t.scan(&core.Scan{Pat: s.Pat, Exp: collection})
}

// into reduces the query so far to what a function makes of it:
// "from ... into f" is "f (from ...)".
func (t *relTranslator) into(s *core.Into) bool {
	value := t.applied(s.Fn)
	if value == nil {
		return t.decline("into with a non-function")
	}
	t.exp = value
	t.done = true
	return true
}

// applied is the query so far, its element the row, passed to a
// function; nil where the function's type is not a function type.
func (t *relTranslator) applied(fn core.Exp) core.Exp {
	fnT, isFn := types.Unalias(fn.Type()).(*types.Fn)
	if !isFn {
		return nil
	}
	t.normalize()
	return &core.Apply{T: fnT.Result, Fn: fn, Arg: t.exp}
}

// step translates one step, returning false where it cannot.
func (t *relTranslator) step(step core.FromStep) bool {
	t.patternAccess = false
	if t.exp == nil {
		if _, isScan := step.(*core.Scan); !isScan {
			// A "from" with no scan -- "from where p",
			// "from yield e" -- iterates over a single element,
			// which is unit.
			t.exp = t.unitCollection()
		}
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch s := step.(type) {
	case *core.Distinct:
		return t.distinct()
	case *core.GroupStep:
		t.deferred = false
		return t.group(s)
	case *core.Into:
		return t.into(s)
	case *core.Order:
		t.exp = core.NewSort(t.sys, t.exp, t.rewrite(s.Exp),
			s.Span)
		return true
	case *core.Scan:
		return t.scan(s)
	case *core.SetOp:
		// A set operator compares rows, so it needs the row.
		if t.deferred {
			t.normalize()
		}
		return t.setOp(s)
	case *core.SkipStep:
		t.exp = core.NewSkip(t.exp, t.rewrite(s.Exp))
		return true
	case *core.TakeStep:
		t.exp = core.NewTake(t.exp, t.rewrite(s.Exp))
		return true
	case *core.Through:
		return t.through(s)
	case *core.Where:
		t.exp = core.NewFilter(t.exp, t.rewrite(s.Exp))
		return true
	case *core.Yield:
		return t.yield(s)
	default:
		// "into", "through" and "require" are not nodes.
		return t.decline("step " + step.Op().String())
	}
}

// mentionsInput reports whether an expression reads "$0" or "$1".
func mentionsInput(exp core.Exp) bool {
	found := false
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		if _, isInput := e.(*core.Input); isInput {
			found = true
		}
		return nil, false
	}
	r.rewriteExp(exp)
	return found
}

// unitCollection is the collection a scanless "from" iterates
// over: a list containing one unit element.
func (t *relTranslator) unitCollection() core.Exp {
	unit := &core.Literal{
		T: t.sys.Unit, Kind: ast.UnitLiteralOp, Value: core.Unit{},
	}
	return &core.List{
		T: t.sys.List(t.sys.Unit), Args: []core.Exp{unit},
	}
}

// yield translates a yield. A trailing yield -- one that computes
// the query's result -- ends the translation's interest in the
// row; a mid-query yield rebinds, and its fields become the row.
func (t *relTranslator) yield(s *core.Yield) bool {
	if s.Fields == nil {
		t.exp = t.project(t.rewrite(s.Exp))
		t.deferred = false
		t.done = true
		return true
	}
	pats := make([]*core.IDPat, len(s.Fields))
	exps := make([]core.Exp, len(s.Fields))
	for i, f := range s.Fields {
		pats[i] = f.Pat
		exps[i] = t.rewrite(f.Exp)
	}
	t.exp = t.project(t.buildRow(pats, exps))
	t.rowPats = pats
	t.rowIsRecord = false
	t.deferred = false
	return true
}

// project maps the element through an expression, except where
// the expression *is* the element: a projection that yields what
// it was given cannot be observed, and a node nothing can observe
// is a node a reader has to read past.
func (t *relTranslator) project(exp core.Exp) core.Exp {
	if isInput0(exp) {
		return t.exp
	}
	return core.NewProject(t.sys, t.exp, exp)
}

// distinct is a group whose key is the whole element and which has
// no aggregates.
//
// Where the row is one binder, the key is that binder: its value
// is the element, and the lowering names the group's key after
// the pattern the tree carries. Without one it would name the key
// after the label, and the label of a key that is the whole
// element is "$0" -- a name no query wrote and no step list
// should hold, which is what "from x in [1, 2, 3] group x order
// x" came back from a round trip as.
func (t *relTranslator) distinct() bool {
	if len(t.rowPats) <= 1 && !t.rowIsRecord {
		var pat *core.IDPat
		if len(t.rowPats) == 1 {
			pat = t.rowPats[0]
		}
		t.exp = relDistinct(t.sys, t.exp, pat)
		return true
	}
	// One row of each distinct value, by grouping on every binder,
	// as morel-java's resolver does; the row is the record of the
	// binders first, and the group's element is that record again.
	t.normalize()
	sorted := t.sortedRow()
	keys := make([]core.RelGroupKey, len(sorted))
	for i, p := range sorted {
		keys[i] = core.RelGroupKey{
			Label: p.Name, Exp: t.access.get(p), Pat: p,
		}
	}
	t.exp = core.NewGroup(t.sys, t.exp, keys, nil)
	t.rowIsRecord = true
	return true
}

// relDistinct takes a collection's elements distinct: a group
// whose key is the whole element and which has no aggregates, and
// the projection that takes the value back out of the one-field
// record such a group builds.
//
// Pat is the binder the key is known by. Without one the lowering
// names the key after the label, and the label of a key that is
// the whole element is "$0" -- a name no query wrote.
func relDistinct(sys *types.System, input core.Exp,
	pat *core.IDPat,
) core.Exp {
	elem := types.ElemOf(input.Type())
	// The key is named after the binder where there is one, "group
	// [x = $0]", as the query would have written it; "$0" is a name
	// no query wrote, and is what is left when the row has no name.
	label := "$0"
	if pat != nil {
		label = pat.Name
	}
	group := core.NewGroup(sys, input, []core.RelGroupKey{{
		Label: label, Exp: core.NewInput(elem, 0), Pat: pat,
	}}, nil)
	return core.NewProject(sys, group,
		fieldAt(sys, core.NewInput(types.ElemOf(group.Type()), 0), 0))
}

// group translates a group step: its keys and aggregates become
// the node's, and its output binders become the row.
func (t *relTranslator) group(g *core.GroupStep) bool {
	keys := make([]core.RelGroupKey, len(g.Keys))
	pats := make([]*core.IDPat, 0, len(g.Keys)+len(g.Aggs))
	for i, k := range g.Keys {
		keys[i] = core.RelGroupKey{
			Label: k.Pat.Name, Exp: t.rewrite(k.Exp),
			Pat: k.Pat,
		}
		pats = append(pats, k.Pat)
	}
	aggs := make([]core.RelGroupAgg, len(g.Aggs))
	for i, a := range g.Aggs {
		var arg core.Exp
		if a.Arg != nil {
			arg = t.rewrite(a.Arg)
		}
		aggs[i] = core.RelGroupAgg{
			Label: a.Pat.Name, Fn: t.converted(t.rewrite(a.Fn), arg),
			Arg: arg, T: a.Pat.T, Span: a.Span,
		}
		pats = append(pats, a.Pat)
	}
	t.exp = core.NewGroup(t.sys, t.exp, keys, aggs)
	t.rowPats = pats
	t.rowIsRecord = true
	return true
}

// converted composes an aggregate function that takes a bag with a
// converter, where the input is a list: "fn col$0 => count
// (#fromList Bag col$0)". The parameter takes a generated name that
// does not start with "$", because a "$" name is a node's pattern.
// This is what morel-java's resolver does, and the plan text shows
// it; a function that takes a list is left alone.
func (t *relTranslator) converted(fn, arg core.Exp) core.Exp {
	if !types.IsOrdered(t.exp.Type()) || !takesBag(fn) {
		return fn
	}
	fnT, isFn := types.Unalias(fn.Type()).(*types.Fn)
	if !isFn {
		return fn
	}
	elem := t.sys.Unit
	if arg != nil {
		elem = arg.Type()
	}
	listT, bagT := t.sys.List(elem), t.sys.Bag(elem)
	resultT := fnT.Result
	param := &core.IDPat{T: listT, Name: "col$" + itoa(t.nextName)}
	t.nextName++
	converter := &core.ID{Pat: &core.IDPat{
		T: t.sys.Fn(listT, bagT), Name: bagFromListName,
	}}
	composedT, _ := t.sys.Fn(listT, resultT).(*types.Fn)
	return &core.Fn{
		T:     composedT,
		IDPat: param,
		Exp: &core.Apply{
			T:  resultT,
			Fn: fn,
			Arg: &core.Apply{
				T: bagT, Fn: converter, Arg: &core.ID{Pat: param},
			},
		},
	}
}

// takesBag reports whether an aggregate function is declared over a
// bag, and so needs a converter where the input is a list.
//
// The collection aggregates -- count, sum, max, min, empty, nonEmpty,
// only -- are declared over "'a bag" and adapt to a list, so their
// instance here is typed by the input; morel-java's are declared over
// a bag and converted, and its plan text shows the converter. Any
// other function is what its type says.
func takesBag(fn core.Exp) bool {
	name := strings.TrimPrefix(builtinName(fn), "Relational.")
	if IsCollectionAggregate(name) {
		return true
	}
	if fnT, isFn := types.Unalias(fn.Type()).(*types.Fn); isFn {
		return types.IsCollection(fnT.Param) && !types.IsOrdered(fnT.Param)
	}
	return false
}

// scan translates a scan. The first scan is a leaf; a later scan
// whose collection does not depend on the bindings so far is an
// ordinary join; one that does is a join carrying a binder that
// its right input reads.
func (t *relTranslator) scan(s *core.Scan) bool {
	kind := joinKindOf(s.Join)
	rightElem := types.ElemOf(s.Exp.Type())
	if rightElem == nil {
		return t.decline("scan over a non-collection")
	}
	if t.exp == nil {
		if kind != core.InnerJoin {
			// An outer join cannot be the first step; there is
			// nothing to be outer to.
			return t.decline("outer join with no left input")
		}
		return t.firstScan(s, rightElem)
	}
	return t.joinScan(s, rightElem, kind)
}

// joinKindOf is the tree's join kind for a scan's flavour. A scan
// names its flavour positively -- left, right or full -- and
// anything else, including the zero value a synthesized scan
// carries, is an inner scan.
func joinKindOf(join ast.Op) core.JoinType {
	// lint: sort until '^\t}' where '^\tcase '
	switch join {
	case ast.FullJoinOp:
		return core.FullJoin
	case ast.LeftJoinOp:
		return core.LeftJoin
	case ast.RightJoinOp:
		return core.RightJoin
	default:
		return core.InnerJoin
	}
}

// firstScan makes the query's first scan a leaf, the pattern's
// binders being paths into its element.
func (t *relTranslator) firstScan(s *core.Scan,
	rightElem types.Type,
) bool {
	t.exp = s.Exp
	t.patternAccess = true
	element := core.NewInput(rightElem, 0)
	if !t.scanPattern(s.Pat, element, t.access) {
		return false
	}
	t.rowPats = append(t.rowPats, core.PatIDs(s.Pat)...)
	t.rowIsRecord = false
	if s.On != nil {
		t.exp = core.NewFilter(t.exp, t.rewrite(s.On))
	}
	// The element is the collection's, which is not the record the
	// query's bindings describe where the pattern destructures. The
	// projection that reconciles them waits, as it does after a
	// join, until something needs the row.
	t.deferred = true
	return true
}

// scanPattern binds a scan's pattern and, where the pattern can
// fail to match, filters by it. The two halves are separate nodes:
// a filter for the condition, and -- where the bindings do not
// describe the element -- the projection that normalize adds.
func (t *relTranslator) scanPattern(pat core.Pat, element core.Exp,
	a *relAccess,
) bool {
	if !relTestable(pat) {
		return t.decline("pattern " + pat.Op().String())
	}
	if !t.destructure(pat, element, a) {
		return false
	}
	if test := relTest(t.sys, pat, element); test != nil {
		t.exp = core.NewFilter(t.exp, test)
	}
	return true
}

// joinScan makes a later scan a join: the left element is "$0",
// the right "$1". Where the collection reads the left element it
// reads it through a binder, because the right input is a tree of
// its own and its "$0" is its own.
func (t *relTranslator) joinScan(s *core.Scan,
	rightElem types.Type, kind core.JoinType,
) bool {
	left := t.exp
	dependent := t.dependsOnBindings(s.Exp)
	if dependent &&
		(kind == core.RightJoin || kind == core.FullJoin) {
		// Every element of a correlated collection comes from some
		// left element, so there is nothing for the other side to
		// be outer to.
		return t.decline("correlated right or full join")
	}
	var binder *core.IDPat
	right := s.Exp
	if dependent {
		binder = t.param(types.ElemOf(left.Type()))
		right = substituteIDs(t.sys, s.Exp, t.over(binder))
	}

	rightAccess := &relAccess{}
	if !t.destructure(s.Pat, core.NewInput(rightElem, 1),
		rightAccess) {
		return false
	}
	// The condition sees both elements as they are, because it is
	// evaluated on candidate pairs.
	cond := core.Exp(&core.Literal{
		T: t.sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
	})
	if s.On != nil {
		cond = substituteIDs(t.sys, s.On,
			both(t.access, rightAccess))
	}
	join := core.NewJoin(t.sys, kind, binder, left, right, cond)

	// The element is the inputs' components in order, so each
	// binder's access is rebased onto that: left components keep
	// their positions and right components follow them.
	k := core.ComponentCount(left)
	access2 := &relAccess{}
	if !t.rebase(access2, t.access, left, 0, 0,
		kind.LeftIsOption(), join) ||
		!t.rebase(access2, rightAccess, right, 1, k,
			kind.RightIsOption(), join) {
		return false
	}
	t.access = access2
	t.rowPats = append(t.rowPats, core.PatIDs(s.Pat)...)
	t.rowIsRecord = false
	t.patternAccess = true
	t.deferred = true
	t.exp = join
	return true
}

// rebase re-expresses one input's access map over the join's
// element.
//
// An input with one component is the whole of a position, so its
// bare reference becomes that field; an input that is itself a
// join has several, and its "#j" becomes "#(offset + j)". A binder
// is never the whole element of a multi-component input, which is
// why the second case never has to name a range of fields.
func (t *relTranslator) rebase(target, source *relAccess,
	input core.Exp, inputOrdinal, offset int, option bool,
	join *core.Join,
) bool {
	element := core.NewInput(types.ElemOf(join.Type()), 0)
	n := core.ComponentCount(input)
	rawRef := core.NewInput(types.ElemOf(input.Type()), inputOrdinal)
	for i, pat := range source.pats {
		a := source.exps[i]
		switch {
		case n == 1:
			component := fieldAt(t.sys, element, offset)
			if option {
				target.put(pat, t.optionize(a, rawRef, component,
					t.sys.Named("option", a.Type())))
			} else {
				target.put(pat,
					subst1(t.sys, a, inputOrdinal, component))
			}
		case option && !isComponentRef(a, inputOrdinal):
			// Each component of an absent side is option-typed in
			// its own right, so a binder that *is* a component
			// reads it directly. One that is a path within a
			// component would have to map through the option, and
			// is not expressed yet.
			return t.decline("outer join over a path in a component")
		default:
			target.put(pat, t.shift(a, inputOrdinal, offset, element))
		}
	}
	return true
}

// isComponentRef reports whether an access is exactly one
// component of an input.
func isComponentRef(exp core.Exp, inputOrdinal int) bool {
	apply, isApply := exp.(*core.Apply)
	if !isApply {
		return false
	}
	if _, isSel := apply.Fn.(*core.Selector); !isSel {
		return false
	}
	in, isIn := apply.Arg.(*core.Input)
	return isIn && in.Ordinal == inputOrdinal
}

// optionize re-expresses an access into an element as an access
// into an option of that element, for a side an outer join can
// leave absent.
//
// Where the binder is the whole element, the option-typed
// reference is the access. Otherwise the access is mapped through
// the option, because Morel makes each binder of the absent side
// an option, not the side as a whole: "left join (j, k) in pairs"
// binds "j : int option" and "k : int option", not
// "(int * int) option".
func (t *relTranslator) optionize(access core.Exp,
	rawRef *core.Input, optionRef core.Exp, optionType types.Type,
) core.Exp {
	if _, isInput := access.(*core.Input); isInput {
		return optionRef
	}
	param := t.freshPat(rawRef.Type())
	body := subst1(t.sys, access, rawRef.Ordinal,
		&core.ID{Pat: param})
	fnT, isFn := t.sys.Fn(rawRef.Type(), body.Type()).(*types.Fn)
	if !isFn {
		return access
	}
	mappedT := t.sys.Fn(optionRef.Type(), optionType)
	return &core.Apply{
		T: optionType,
		Fn: &core.Apply{
			T: mappedT,
			Fn: &core.ID{Pat: &core.IDPat{
				T: t.sys.Fn(fnT, mappedT), Name: "Option.map",
			}},
			Arg: &core.Fn{T: fnT, IDPat: param, Exp: body},
		},
		Arg: optionRef,
	}
}

// shift rewrites "#j $i" to "#(offset + j) $0", for an input that
// is itself a join and so occupies several of the element's
// positions. An outer path survives: a binder inside a component's
// record reads that component and then its own field.
func (t *relTranslator) shift(exp core.Exp, inputOrdinal,
	offset int, element *core.Input,
) core.Exp {
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		apply, isApply := e.(*core.Apply)
		if !isApply {
			return nil, false
		}
		sel, isSel := apply.Fn.(*core.Selector)
		if !isSel {
			return nil, false
		}
		in, isIn := apply.Arg.(*core.Input)
		if !isIn || in.Ordinal != inputOrdinal {
			return nil, false
		}
		return fieldAt(t.sys, element, offset+sel.Index), true
	}
	return r.rewriteExp(exp)
}

// normalize makes the tree's element agree with the element the
// row describes, adding a projection where it does not, and leaves
// the access map uniform.
func (t *relTranslator) normalize() {
	t.deferred = false
	if len(t.rowPats) == 0 {
		return
	}
	wanted := t.elementType()
	elem := types.ElemOf(t.exp.Type())
	switch {
	case elem != wanted:
		// A step that computes its own element -- a yield, a group
		// -- differs from what the bindings describe only in
		// whether a lone value is wrapped in a record; a scan
		// differs in more, and its access map says how.
		if aligned := t.align(t.exp, wanted); aligned != nil {
			t.exp = aligned
		} else {
			t.exp = core.NewProject(t.sys, t.exp, t.element(wanted))
		}
	case t.patternAccess && !t.isUniform(wanted):
		// The element has the right type but the binders read the
		// wrong fields of it, which a pattern that permutes them
		// does: "from {b = a, a = b} in rs" binds a to field b.
		t.exp = core.NewProject(t.sys, t.exp, t.element(wanted))
	}
	t.setUniformAccess()
}

// align converts a collection so that its element has the wanted
// type, returning the input unchanged where it already does, or
// nil where the conversion is not one of the two that branch
// alignment allows.
func (t *relTranslator) align(input core.Exp,
	wanted types.Type,
) core.Exp {
	elem := types.ElemOf(input.Type())
	if elem == wanted {
		return input
	}
	if rec, isRec := wanted.(*types.Record); isRec &&
		len(rec.Fields) == 1 && rec.Fields[0].Type == elem {
		// Wrap a bare value in the one-field record the row
		// describes.
		return core.NewProject(t.sys, input, &core.Tuple{
			T: wanted, Args: []core.Exp{core.NewInput(elem, 0)},
		})
	}
	if rec, isRec := elem.(*types.Record); isRec &&
		len(rec.Fields) == 1 && rec.Fields[0].Type == wanted {
		// Unwrap a one-field record to the bare value the row
		// describes.
		return core.NewProject(t.sys, input,
			fieldAt(t.sys, core.NewInput(elem, 0), 0))
	}
	return nil
}

// elementType is the element type the row describes: the bare type
// of the only binder where the row atomizes, otherwise a record
// with one field per binder.
//
// A binder's type here is the type of what *reads* it, not the
// type its pattern declares. morel-java reads the types off the
// step's environment, and a step has its own: a binder on the
// absent side of an outer join is an option in the steps after
// the join and the bare type in the scan that bound it.
// morel-go's steps share one pattern, which carries the later
// type, so asking the pattern would make the scan's own element
// an option before anything had wrapped it.
func (t *relTranslator) elementType() types.Type {
	sorted := t.sortedRow()
	if len(sorted) == 1 && !t.rowIsRecord {
		return t.binderType(sorted[0])
	}
	fields := make([]types.Field, len(sorted))
	for i, p := range sorted {
		fields[i] = types.Field{
			Label: p.Name, Type: t.binderType(p),
		}
	}
	return t.sys.Record(fields)
}

// binderType is the type of what reads a binder here.
func (t *relTranslator) binderType(pat *core.IDPat) types.Type {
	if a := t.access.get(pat); a != nil {
		return a.Type()
	}
	return pat.T
}

// sortedRow is the row's binders in field order.
func (t *relTranslator) sortedRow() []*core.IDPat {
	sorted := slices.Clone(t.rowPats)
	sort.SliceStable(sorted, func(i, j int) bool {
		return types.LabelLess(sorted[i].Name, sorted[j].Name)
	})
	return sorted
}

// element builds an element of the wanted type from the binders'
// access expressions: the only binder's value where the element
// atomizes, otherwise a record.
func (t *relTranslator) element(wanted types.Type) core.Exp {
	sorted := t.sortedRow()
	if len(sorted) == 1 && !t.rowIsRecord {
		if a := t.access.get(sorted[0]); a != nil &&
			a.Type() == wanted {
			return a
		}
	}
	pats := make([]*core.IDPat, len(sorted))
	exps := make([]core.Exp, len(sorted))
	for i, p := range sorted {
		pats[i] = p
		exps[i] = t.access.get(p)
		if exps[i] == nil {
			return nil
		}
	}
	return t.buildRow(pats, exps)
}

// buildRow is the record of the given binders' values, in field
// order, or the bare value where there is one binder and the row
// does not wrap it.
func (t *relTranslator) buildRow(pats []*core.IDPat,
	exps []core.Exp,
) core.Exp {
	order := make([]int, len(pats))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return types.LabelLess(pats[order[a]].Name,
			pats[order[b]].Name)
	})
	fields := make([]types.Field, len(order))
	args := make([]core.Exp, len(order))
	for i, j := range order {
		fields[i] = types.Field{
			Label: pats[j].Name, Type: exps[j].Type(),
		}
		args[i] = exps[j]
	}
	return &core.Tuple{T: t.sys.Record(fields), Args: args}
}

// setUniformAccess points each binder at the element the row
// describes: the element itself where the row atomizes, otherwise
// the field of the same name.
func (t *relTranslator) setUniformAccess() {
	t.patternAccess = false
	t.access.clear()
	sorted := t.sortedRow()
	if len(sorted) == 0 {
		return
	}
	elem := t.elementType()
	element := core.NewInput(elem, 0)
	if len(sorted) == 1 && !t.rowIsRecord {
		t.access.put(sorted[0], element)
		return
	}
	for i, p := range sorted {
		t.access.put(p, fieldAt(t.sys, element, i))
	}
}

// isUniform reports whether each binder already reads the element
// the way the uniform map would.
func (t *relTranslator) isUniform(wanted types.Type) bool {
	sorted := t.sortedRow()
	if len(sorted) != t.access.len() {
		return false
	}
	for i, p := range sorted {
		a := t.access.get(p)
		if a == nil {
			return false
		}
		if len(sorted) == 1 {
			if !isInput0(a) {
				return false
			}
			continue
		}
		apply, isApply := a.(*core.Apply)
		if !isApply {
			return false
		}
		sel, isSel := apply.Fn.(*core.Selector)
		if !isSel || !isInput0(apply.Arg) || sel.Index != i {
			return false
		}
	}
	_ = wanted
	return true
}

func isInput0(e core.Exp) bool {
	in, isIn := e.(*core.Input)
	return isIn && in.Ordinal == 0
}

// destructure adds an access expression for each binder of a
// pattern, returning false where the pattern is not one this
// translation expresses.
func (t *relTranslator) destructure(pat core.Pat, element core.Exp,
	a *relAccess,
) bool {
	// lint: sort until '^\t}' where '^\tcase '
	switch p := pat.(type) {
	case *core.AsPat:
		// The name matches what the pattern it wraps matches, so
		// it reads the whole element.
		a.put(p.Pat, element)
		return t.destructure(p.Body, element, a)
	case *core.ConsPat:
		// "h :: t" binds the head and the tail, which "hd" and
		// "tl" reach.
		return t.destructure(p.Head, listHd(t.sys, element), a) &&
			t.destructure(p.Tail, listTl(t.sys, element), a)
	case *core.IDPat:
		a.put(p, element)
		return true
	case *core.ListPat:
		for i, item := range p.Args {
			if !t.destructure(item, listNth(t.sys, element, i), a) {
				return false
			}
		}
		return true
	case *core.LiteralPat:
		// A literal binds nothing; it filters, and relTest says
		// how.
		return true
	case *core.TuplePat:
		for i, arg := range p.Args {
			if !t.destructure(arg, fieldAt(t.sys, element, i), a) {
				return false
			}
		}
		return true
	case *core.WildcardPat:
		return true
	default:
		// A user datatype's constructor filters too, but
		// extracting what it binds has no total expression.
		return t.decline("pattern " + pat.Op().String())
	}
}

// fieldAt selects a field of a record- or tuple-typed expression
// by position.
//
// Where the expression is a construction, the field is read off
// it rather than projected out of it: "#1 (x, y)" is "x". Leaving
// it unreduced hands whatever reads it a projection of a
// construction, which the lowering then has to build a row for
// and take apart again.
func fieldAt(sys *types.System, exp core.Exp, i int) core.Exp {
	if tuple, isTuple := exp.(*core.Tuple); isTuple &&
		i < len(tuple.Args) {
		return tuple.Args[i]
	}
	var label string
	var fieldT types.Type
	switch t := types.Unalias(exp.Type()).(type) {
	case *types.Record:
		label, fieldT = t.Fields[i].Label, t.Fields[i].Type
	case *types.Tuple:
		label, fieldT = tupleLabel(i), t.Args[i]
	default:
		return exp
	}
	return &core.Apply{
		T: fieldT,
		Fn: &core.Selector{
			T: sys.Fn(exp.Type(), fieldT), Name: label, Index: i,
		},
		Arg: exp,
	}
}

// rewrite replaces each binder in scope with its access
// expression over "$0", and binds the element where a query
// inside the expression needs it.
//
// Where the query inside is a *tree*, the binding cannot wait.
// A node rebinds "$0" to its own input, so an access expression
// written there would read the inner element, and once written
// there is nothing to tell it from the node's own "$0" -- both
// are the same text. So the binder is minted first and the
// substitution writes it: "v$0" where it crosses into a node,
// "$0" everywhere else.
func (t *relTranslator) rewrite(exp core.Exp) core.Exp {
	if v, inner := t.crossing(exp); v != nil {
		r := &rewriter{sys: t.sys}
		r.exp = func(e core.Exp) (core.Exp, bool) {
			if _, isRel := e.(core.Rel); isRel {
				// Stop here: inside is the node's scope, where
				// the element is reached by name.
				return substituteIDs(t.sys, e, inner), true
			}
			if id, isID := e.(*core.ID); isID {
				if sub := t.access.get(id.Pat); sub != nil {
					return sub, true
				}
			}
			return nil, false
		}
		return &core.Let{
			Decl: &core.NonRecValDecl{
				Pat: v, Exp: core.NewInput(v.T, 0),
			},
			Exp: r.rewriteExp(exp),
		}
	}
	return t.bindRow(substituteIDs(t.sys, exp, t.access))
}

// crossing is the binder a nested tree reads the element by, and
// the access map written over it, or nil where no tree is nested
// in the expression.
//
// Only a tree. A nested *step list* has no "$0" of its own, so an
// access expression written into one still means the element, and
// bindRow can put the binding in afterwards.
func (t *relTranslator) crossing(exp core.Exp) (
	*core.IDPat, *relAccess,
) {
	if t.exp == nil || t.access.len() == 0 {
		return nil, nil
	}
	elem := types.ElemOf(t.exp.Type())
	if elem == nil || !nestedReadsAccess(exp, t.access) {
		return nil, nil
	}
	v := t.freshPat(elem)
	return v, t.over(v)
}

// nestedReadsAccess reports whether a node inside an expression
// reads a binder the enclosing query binds.
//
// A node that reads none is independent of the element -- a set
// operator's branch is one, and it is an *input* rather than an
// expression over "$0", so there is nothing there for a binder to
// stand for. Binding one anyway leaves "let val v$0 = $0 in
// <branch> end" where a collection belongs, and the "$0" reaches
// code generation with no node above it to mean anything by.
func nestedReadsAccess(exp core.Exp, a *relAccess) bool {
	found := false
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		if _, isRel := e.(core.Rel); !isRel {
			return nil, false
		}
		inner := &rewriter{}
		inner.exp = func(x core.Exp) (core.Exp, bool) {
			if id, isID := x.(*core.ID); isID &&
				a.get(id.Pat) != nil {
				found = true
			}
			return nil, false
		}
		inner.rewriteExp(e)
		return e, true
	}
	r.rewriteExp(exp)
	return found
}

// bindRow binds the element to a name where a query inside an
// expression reads it.
//
// A nested tree rebinds "$0" to its own input, so it cannot say
// "$0" and mean the element of the node that holds it. A bound
// name crosses that boundary by ordinary lexical scoping, which
// is the same device a dependent join's binder is, written with
// "let" because the node's argument is not a function:
//
//	let val v$0 = $0 in ... nonEmpty (<tree mentioning v$0>) end
//
// Only what crosses in is rewritten. The rest of the expression
// still says "$0", because there it still means what it says.
func (t *relTranslator) bindRow(exp core.Exp) core.Exp {
	if t.exp == nil || !nestedReadsRow(exp) {
		return exp
	}
	elem := types.ElemOf(t.exp.Type())
	if elem == nil {
		return exp
	}
	v := t.freshPat(elem)
	body := substNestedInput(t.sys, exp, v)
	return &core.Let{
		Decl: &core.NonRecValDecl{
			Pat: v, Exp: core.NewInput(elem, 0),
		},
		Exp: body,
	}
}

// nestedReadsRow reports whether an expression holds a query that
// reads the element of the node the expression belongs to.
func nestedReadsRow(exp core.Exp) bool {
	found := false
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		from, isFrom := e.(*core.From)
		if isFrom && types.IsCollection(from.Type()) &&
			mentionsInput(from) {
			found = true
			return e, true
		}
		return nil, false
	}
	r.rewriteExp(exp)
	return found
}

// substNestedInput replaces "$0" with a name inside every query
// the expression holds, and leaves the rest alone.
func substNestedInput(sys *types.System, exp core.Exp,
	v *core.IDPat,
) core.Exp {
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		if _, isFrom := e.(*core.From); !isFrom {
			return nil, false
		}
		return subst1(sys, e, 0, &core.ID{Pat: v}), true
	}
	return r.rewriteExp(exp)
}

// substituteIDs replaces each binder the map names with its
// expression.
func substituteIDs(sys *types.System, exp core.Exp,
	a *relAccess,
) core.Exp {
	r := &rewriter{sys: sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		id, isID := e.(*core.ID)
		if !isID {
			return nil, false
		}
		if sub := a.get(id.Pat); sub != nil {
			return sub, true
		}
		return nil, false
	}
	return r.rewriteExp(exp)
}

// subst1 replaces "$i" in an expression with another expression.
//
// It stops at a nested node, which rebinds "$0" to its own input:
// an occurrence there is that node's, not this one's.
func subst1(sys *types.System, exp core.Exp, i int,
	e core.Exp,
) core.Exp {
	r := &rewriter{sys: sys}
	r.exp = func(x core.Exp) (core.Exp, bool) {
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch x := x.(type) {
		case *core.Apply:
			// "#j $i" reads a field of what replaces "$i", and
			// where that is a construction the field is read off
			// it: "#1 (x, y)" is "x". Leaving it unreduced hands
			// whatever reads it a projection of a construction.
			sel, isSel := x.Fn.(*core.Selector)
			if !isSel {
				return nil, false
			}
			in, isIn := x.Arg.(*core.Input)
			if !isIn || in.Ordinal != i {
				return nil, false
			}
			return fieldAt(sys, e, sel.Index), true
		case *core.Input:
			if x.Ordinal == i {
				return e, true
			}
			return x, true
		case core.Rel:
			return x, true
		default:
			return nil, false
		}
	}
	return r.rewriteExp(exp)
}

// over re-expresses the access map over a binder rather than over
// "$0", for the right input of a dependent join.
func (t *relTranslator) over(binder *core.IDPat) *relAccess {
	out := &relAccess{}
	paramID := &core.ID{Pat: binder}
	for i, p := range t.access.pats {
		out.put(p, subst1(t.sys, t.access.exps[i], 0, paramID))
	}
	return out
}

// both is the two maps, the right one winning.
func both(left, right *relAccess) *relAccess {
	out := left.clone()
	for i, p := range right.pats {
		out.put(p, right.exps[i])
	}
	return out
}

// dependsOnBindings reports whether an expression names any binder
// in scope.
func (t *relTranslator) dependsOnBindings(exp core.Exp) bool {
	found := false
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		if id, isID := e.(*core.ID); isID &&
			t.access.get(id.Pat) != nil {
			found = true
		}
		return nil, false
	}
	r.rewriteExp(exp)
	return found
}

// param creates a dependent join's binder, which is always a
// generated one.
//
// Reusing the query's own binder where the element is that
// binder's value would read better -- "from d in depts, e in
// d.emps" would say "join [d]" -- and morel-java's RelTranslator
// does exactly that. But its resolver, which is what builds the
// tree that Sys.planOf prints, always generates; so the contract
// says "join [v$0]", and a name the query wrote must not appear
// there.
func (t *relTranslator) param(elemType types.Type) *core.IDPat {
	return t.freshPat(elemType)
}

// freshPat creates a binder the translation needs and the query
// did not write.
//
// The name comes from the counter every generated "v$" binder
// draws on, so that no two binders in a statement share a name:
// a plan renumbers them by first appearance, but the compiler
// resolves a binder by name.
func (t *relTranslator) freshPat(typ types.Type) *core.IDPat {
	name := "v$" + itoa(nestedNext)
	nestedNext++
	return &core.IDPat{T: typ, Name: name}
}
