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
	"github.com/hydromatic/morel-go/internal/types"
)

// Lowers a relational tree (core.Rel) into the form that
// executes: a core.From whose steps bind names, which the
// compiler turns into a pipeline. It is morel#449's RelLowerer.
//
// This is the reverse of the translation: the step list survives
// as an unprinted lowering artifact. Where the translation
// eliminates variables, the lowering reintroduces them.
//
// It linearizes. The tree is left-deep after translation, so one
// step list carries the whole left spine rather than each node
// nesting a query of its own. What makes that work is carrying a
// node's element as an *expression* over the step list's bindings
// instead of materializing it: a projection then changes the
// expression, not the steps, and "from e in emps, d in depts
// where p" lowers back to the three steps it began as.
//
// The element is materialized, by a yield, only where something
// needs the row itself: before a sort, which reads it, and at the
// end.

// lowerer is one lowering, of one tree.
type lowerer struct {
	sys *types.System
	// nextName numbers the binders the lowering invents. A tree
	// has no names, so it has to.
	nextName int
	// declined says what the lowering could not express, and is
	// empty where it succeeded.
	declined string
	// names is what to call the scan of a collection, by
	// identity; see LowerRelNamed.
	names map[core.Exp]core.Pat
}

// LowerRel lowers a tree into an expression that executes, or
// returns nil and a reason where it cannot.
func LowerRel(sys *types.System, exp core.Exp) (core.Exp, string) {
	return LowerRelNamed(sys, exp, nil)
}

// LowerRelNamed lowers a tree, naming a scan after the leaf it
// scans where the caller knows the name.
//
// A tree has no names, so the lowering invents them, and
// "from e in emps" comes back as "from w$0 in emps". Where a
// caller knows the name the user wrote -- grounding does, having
// been handed the query's patterns -- saying so keeps it in the
// plan, which is what the reader of a plan wants to see.
func LowerRelNamed(sys *types.System, exp core.Exp,
	names map[core.Exp]core.Pat,
) (core.Exp, string) {
	l := &lowerer{sys: sys, names: names}
	out := l.all(l.rel(exp))
	if l.declined != "" {
		return nil, l.declined
	}
	return out, ""
}

// all lowers every tree left inside an expression.
//
// A query may appear wherever an expression may -- the right
// input of a dependent join, the argument of "nonEmpty", a field
// of a record -- and the lowering of the tree that holds one does
// not reach inside its expressions. What is left there is a node,
// and the compiler knows steps, not nodes.
func (l *lowerer) all(exp core.Exp) core.Exp {
	if exp == nil {
		return nil
	}
	r := &rewriter{sys: l.sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		rel, isRel := e.(core.Rel)
		if !isRel {
			return nil, false
		}
		return l.all(l.rel(rel)), true
	}
	return r.rewriteExp(exp)
}

func (l *lowerer) decline(reason string) {
	if l.declined == "" {
		l.declined = reason
	}
}

// rel lowers a node into an expression of collection type.
func (l *lowerer) rel(exp core.Exp) core.Exp {
	if _, isRel := exp.(core.Rel); !isRel {
		// A leaf is already an expression.
		return exp
	}
	b := &lowerBuilder{sys: l.sys}
	element := l.into(b, exp)
	if element == nil {
		return nil
	}
	b.finish(element)
	return b.build(exp.Type())
}

// into appends the steps for a node, and returns an expression,
// over the step list's bindings, that denotes the node's element.
func (l *lowerer) into(b *lowerBuilder, exp core.Exp) core.Exp {
	rel, isRel := exp.(core.Rel)
	if !isRel {
		return b.scan(l, exp)
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch r := rel.(type) {
	case *core.Filter:
		element := l.into(b, r.Input)
		if element == nil {
			return nil
		}
		b.where(subst1(l.sys, r.Condition, 0, element))
		return element
	case *core.Group:
		return l.group(b, r)
	case *core.Join:
		return l.join(b, r)
	case *core.Project:
		// A projection changes the element, not the steps; nothing
		// is emitted unless a later step needs the row.
		element := l.into(b, r.Input)
		if element == nil {
			return nil
		}
		out := subst1(l.sys, r.Exp, 0, element)
		if containsOrdinal(out) {
			// Except for an ordinal, which counts rows: only a
			// step evaluates its expression exactly once per row,
			// so deferring one would change what it counts.
			return b.materialize(l, out)
		}
		return out
	case *core.SetRel:
		return l.setRel(b, r)
	case *core.Skip:
		element := l.into(b, r.Input)
		if element == nil {
			return nil
		}
		b.add(&core.SkipStep{Exp: r.Count})
		return element
	case *core.Sort:
		// A sort reads the element, so a projection above it must
		// be paid for here rather than deferred: deferring it
		// would sort a wider row, and evaluate the projection
		// twice for every expression the sort key shares with it.
		element := b.materialize(l, l.into(b, r.Input))
		if element == nil {
			return nil
		}
		b.add(&core.Order{
			Exp: subst1(l.sys, r.Exp, 0, element), Span: r.Span,
		})
		return element
	case *core.Take:
		element := l.into(b, r.Input)
		if element == nil {
			return nil
		}
		b.add(&core.TakeStep{Exp: r.Count})
		return element
	case *core.Unorder:
		element := l.into(b, r.Input)
		if element == nil {
			return nil
		}
		// The step list has no "unorder": a query that forgets its
		// ordering says so in its own type.
		b.ordered = false
		return element
	default:
		l.decline("lower " + rel.OpName())
		return nil
	}
}

// join lowers a join: the right input becomes a scan beside the
// left's steps, which is what makes the whole left spine one step
// list.
func (l *lowerer) join(b *lowerBuilder, j *core.Join) core.Exp {
	left := l.into(b, j.Left)
	if left == nil {
		return nil
	}
	right := j.Right
	if j.Binder != nil {
		// A dependent join reads the left element through its
		// binder; in a step list the collection is an expression
		// beside the bindings, so the binder becomes the element.
		right = substituteIDs(l.sys, right,
			oneAccess(j.Binder, left))
	}
	right = l.rel(right)
	if right == nil {
		return nil
	}
	rightElem := types.ElemOf(right.Type())
	if rightElem == nil {
		l.decline("join over a non-collection")
		return nil
	}
	// The name is looked up on the join's own right input, not on
	// what the lowering made of it: a dependent join substitutes
	// its binder in first, and the result is a collection nothing
	// named.
	pat := l.scanPat(j.Right, rightElem, "$1")
	scan := &core.Scan{
		Pat: pat, Exp: right, Join: joinOpOf(j.Kind),
	}
	rightElement := patElement(pat, rightElem)
	if j.Kind != core.InnerJoin {
		// An outer join's condition is evaluated on candidate
		// pairs, where both elements are present, so it reads them
		// as they are.
		scan.On = subst2(l.sys, j.Condition, left, rightElement)
	}
	b.add(scan)
	b.row = append(b.row, core.PatIDs(pat)...)
	b.ordered = b.ordered && types.IsOrdered(right.Type())
	if j.Kind == core.InnerJoin &&
		!isBoolLiteral(j.Condition, true) {
		b.where(subst2(l.sys, j.Condition, left, rightElement))
	}
	return joinElement(l.sys, j, left, rightElement)
}

// joinElement is the tuple of a join's components, which is its
// element.
func joinElement(sys *types.System, j *core.Join,
	left, right core.Exp,
) core.Exp {
	args := make([]core.Exp, 0,
		core.ComponentCount(j.Left)+core.ComponentCount(j.Right))
	args = append(args, componentsOf(sys, j.Left, left)...)
	args = append(args, componentsOf(sys, j.Right, right)...)
	if len(args) == 1 {
		return args[0]
	}
	argTypes := make([]types.Type, len(args))
	for i, a := range args {
		argTypes[i] = a.Type()
	}
	return &core.Tuple{T: sys.Tuple(argTypes...), Args: args}
}

// componentsOf reads a node's components out of an expression for
// its element.
func componentsOf(sys *types.System, node core.Exp,
	exp core.Exp,
) []core.Exp {
	n := core.ComponentCount(node)
	if n == 1 {
		return []core.Exp{exp}
	}
	out := make([]core.Exp, n)
	for i := range n {
		out[i] = fieldAt(sys, exp, i)
	}
	return out
}

// joinOpOf is the step-list flavour for a join kind.
func joinOpOf(kind core.JoinType) ast.Op {
	// lint: sort until '^\t}' where '^\tcase '
	switch kind {
	case core.FullJoin:
		return ast.FullJoinOp
	case core.LeftJoin:
		return ast.LeftJoinOp
	case core.RightJoin:
		return ast.RightJoinOp
	default:
		return ast.ScanOp
	}
}

// group lowers a group: its keys and aggregates become the step's,
// and its element is the record they build.
func (l *lowerer) group(b *lowerBuilder, g *core.Group) core.Exp {
	element := l.into(b, g.Input)
	if element == nil {
		return nil
	}
	keys := make([]core.GroupKey, len(g.Keys))
	pats := make([]*core.IDPat, 0, len(g.Keys)+len(g.Aggs))
	for i, k := range g.Keys {
		// The key's own pattern, where the tree carries one, so
		// that an aggregate of this group that names the key still
		// refers to something the steps bind.
		pat := k.Pat
		if pat == nil {
			pat = &core.IDPat{T: k.Exp.Type(), Name: k.Label}
		}
		keys[i] = core.GroupKey{
			Pat: pat, Exp: subst1(l.sys, k.Exp, 0, element),
		}
		pats = append(pats, pat)
	}
	aggs := make([]core.GroupAgg, len(g.Aggs))
	for i, a := range g.Aggs {
		pat := &core.IDPat{T: a.T, Name: a.Label}
		var arg core.Exp
		if a.Arg != nil {
			arg = subst1(l.sys, a.Arg, 0, element)
		}
		// The function may read the element too: "fn vs => k +
		// List.length vs" names a key the query bound, which the
		// translation turned into a path over "$0".
		aggs[i] = core.GroupAgg{
			Pat: pat, Fn: subst1(l.sys, a.Fn, 0, element),
			Arg: arg, Span: a.Span,
		}
		pats = append(pats, pat)
	}
	b.add(&core.GroupStep{Keys: keys, Aggs: aggs})
	b.row = pats
	// A record whether there is one label or many, because that is
	// what the tree's group builds. The step list would atomize a
	// single binder to its bare value, and then a projection above
	// -- "#i $0", which is how a query that wants the value says so
	// -- would read a field of an int.
	return b.recordOf(pats)
}

// setRel lowers a set operator, whose inputs are collections
// rather than steps, so the row is needed first.
func (l *lowerer) setRel(b *lowerBuilder, s *core.SetRel) core.Exp {
	element := b.materialize(l, l.into(b, s.Args[0]))
	if element == nil {
		return nil
	}
	args := make([]core.Exp, 0, len(s.Args)-1)
	for _, arg := range s.Args[1:] {
		args = append(args, l.rel(arg))
	}
	if l.declined != "" {
		return nil
	}
	b.add(&core.SetOp{
		Kind: setOpOf(s.Kind), Args: args, Distinct: s.Distinct,
	})
	return element
}

// setOpOf is the step-list operator for a set kind.
func setOpOf(kind core.SetKind) ast.Op {
	// lint: sort until '^\t}' where '^\tcase '
	switch kind {
	case core.ExceptSet:
		return ast.ExceptOp
	case core.IntersectSet:
		return ast.IntersectOp
	default:
		return ast.UnionOp
	}
}

// scanPat is what to call a scan's binder: the pattern the caller
// gave the collection, where it gave one and the type agrees, and
// otherwise input, the name the tree gives that element: "$0", or
// "$1" for a join's right input. Binders are told apart by
// identity, so many may share that name; a plan numbers them.
//
// A pattern rather than a name, because one generator may bound
// several leaves -- "(x, y) elem pairs" bounds both -- and the
// collection that replaces them yields the pair. Scanning it
// under one name and reading the components back out would lose
// the names the query wrote, where a pattern of them keeps both.
func (l *lowerer) scanPat(collection core.Exp,
	elem types.Type, input string,
) core.Pat {
	if pat, ok := l.names[collection]; ok && pat.Type() == elem {
		return pat
	}
	return &core.IDPat{T: elem, Name: input}
}

// freshPat creates a binder the lowering needs and the query did
// not write. Its prefix is the lowering's own, so that a tree's
// "v$" and a lowering's "w$" do not interleave when a plan
// renumbers them.
func (l *lowerer) freshPat(t types.Type) *core.IDPat {
	name := "w$" + itoa(l.nextName)
	l.nextName++
	return &core.IDPat{T: t, Name: name}
}

// oneAccess is a one-entry substitution map.
func oneAccess(pat *core.IDPat, exp core.Exp) *relAccess {
	a := &relAccess{}
	a.put(pat, exp)
	return a
}

// subst2 replaces "$0" and "$1" with two expressions at once, so
// that neither substitution can see the other's result. Like
// subst1, it stops at a nested node.
func subst2(sys *types.System, exp core.Exp,
	zero, one core.Exp,
) core.Exp {
	r := &rewriter{sys: sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch e := e.(type) {
		case *core.Apply:
			sel, isSel := e.Fn.(*core.Selector)
			if !isSel {
				return nil, false
			}
			in, isIn := e.Arg.(*core.Input)
			if !isIn {
				return nil, false
			}
			if in.Ordinal == 0 {
				return fieldAt(sys, zero, sel.Index), true
			}
			return fieldAt(sys, one, sel.Index), true
		case *core.Input:
			if e.Ordinal == 0 {
				return zero, true
			}
			return one, true
		case core.Rel:
			return e, true
		default:
			return nil, false
		}
	}
	return r.rewriteExp(exp)
}

// lowerBuilder accumulates a step list and the binders its steps
// leave.
type lowerBuilder struct {
	sys   *types.System
	steps []core.FromStep
	row   []*core.IDPat
	// ordered is whether the steps so far leave a list; a scan of
	// a bag, or an unorder, makes it false.
	ordered bool
	started bool
}

func (b *lowerBuilder) add(step core.FromStep) {
	b.steps = append(b.steps, step)
}

// where filters the rows, and emits no step for a condition that
// is the literal "true".
//
// The tree keeps such a filter -- "from where true yield 1" has
// one, and the conformance suite pins it -- but a step list does
// not: the step list's own engine drops a true conjunct when it
// rebuilds a query, so a lowering that kept the step would give a
// plan the other engine had cleaned up. A range pushdown is where
// one arises: it takes "c < #\"e\"" into the scan and leaves the
// filter behind.
func (b *lowerBuilder) where(exp core.Exp) {
	if isBoolLiteral(exp, true) {
		return
	}
	b.add(&core.Where{Exp: exp})
}

// scan adds a scan of a collection, binding its element to a
// fresh name, and returns the expression that denotes it.
func (b *lowerBuilder) scan(l *lowerer, collection core.Exp,
) core.Exp {
	if !b.started {
		b.started, b.ordered = true, true
	}
	if isUnitCollection(collection) {
		// The inverse of the translation's unit collection: a
		// query with no scan iterates over one row, which is unit,
		// and the tree says so with a leaf holding that one row. A
		// step list says it by having no scan.
		return &core.Literal{
			T: b.sys.Unit, Kind: ast.UnitLiteralOp,
			Value: core.Unit{},
		}
	}
	lowered := l.rel(collection)
	if lowered == nil {
		return nil
	}
	elem := types.ElemOf(lowered.Type())
	if elem == nil {
		l.decline("scan over a non-collection")
		return nil
	}
	pat := l.scanPat(collection, elem, "$0")
	if steps := splicedSteps(lowered, pat); steps != nil &&
		len(b.steps) == 0 {
		// The collection is a query that scans what we were about
		// to scan, under the very binder we were about to bind,
		// and nothing after rebinds it. Its steps are this
		// query's first steps, and a scan of it would be a scan
		// of a scan: "from d in (from d in depts group d order
		// d)" is "from d in depts group d order d".
		//
		// Only as the first steps. Further along, a group would
		// span every variable bound so far and not this one, and
		// the step list's own engine nests there too.
		for _, step := range steps {
			b.add(step)
		}
		b.row = append(b.row, core.PatIDs(pat)...)
		b.ordered = b.ordered && types.IsOrdered(lowered.Type())
		return patElement(pat, elem)
	}
	b.add(&core.Scan{Pat: pat, Exp: lowered, Join: ast.ScanOp})
	b.row = append(b.row, core.PatIDs(pat)...)
	b.ordered = b.ordered && types.IsOrdered(lowered.Type())
	return patElement(pat, elem)
}

// splicedSteps are a collection's own steps, where scanning it
// under a pattern is the same as running them, and nil otherwise.
//
// The one case, and it is the one grounding builds: a query whose
// first step scans under this very pattern, and whose later steps
// neither rebind nor add a binder. "distinct" and "order" are
// such steps; a yield or a group is not, and a second scan adds a
// binder the outer query did not ask for.
//
// Narrow on purpose. morel-java inlines a scan over a subquery in
// general, in "FromBuilder.scan", and the general rule has to
// rename what it splices and decide what a yield leaves. This one
// renames nothing, because the pattern is already the subquery's
// own: grounding names the leaf, and the collection it builds for
// that leaf is scanned under the same name.
func splicedSteps(collection core.Exp, pat core.Pat) []core.FromStep {
	from, isFrom := collection.(*core.From)
	if !isFrom || from.Kind != ast.FromOp || len(from.Steps) == 0 {
		return nil
	}
	scan, isScan := from.Steps[0].(*core.Scan)
	if !isScan || scan.Pat != pat || scan.On != nil {
		return nil
	}
	// An outer join's scan keeps rows its condition rejects, and
	// wraps what the other side binds in "option"; splicing one
	// as a first step would drop both. A plain scan is either
	// flavour unset or "ScanOp", because not every builder says
	// so: "distinctScan" leaves the field zero.
	if scan.Join == ast.LeftJoinOp || scan.Join == ast.RightJoinOp ||
		scan.Join == ast.FullJoinOp {
		return nil
	}
	for _, step := range from.Steps[1:] {
		switch step.(type) {
		case *core.Distinct, *core.Order:
		default:
			return nil
		}
	}
	return from.Steps
}

// patElement is what a scan's pattern denotes: the binder's value
// where the pattern is one binder, and otherwise the tuple of
// them, in the pattern's order -- which is the order of the
// components of the element it matched.
func patElement(pat core.Pat, elem types.Type) core.Exp {
	if id, isID := pat.(*core.IDPat); isID {
		return &core.ID{Pat: id}
	}
	ids := core.PatIDs(pat)
	args := make([]core.Exp, len(ids))
	for i, p := range ids {
		args[i] = &core.ID{Pat: p}
	}
	return &core.Tuple{T: elem, Args: args}
}

// finish makes the element the query's result. A trailing yield
// says "this is the value", where a mid-query one rebinds; and
// where the element is already what the steps leave, neither is
// needed.
func (b *lowerBuilder) finish(element core.Exp) {
	if element == nil || b.isRow(element) {
		return
	}
	b.add(&core.Yield{Exp: element})
}

// materialize makes the element the step list's row, where it is
// not that already, and returns the expression that denotes it
// afterwards.
func (b *lowerBuilder) materialize(l *lowerer,
	element core.Exp,
) core.Exp {
	if element == nil || b.isRow(element) {
		return element
	}
	pat := l.freshPat(element.Type())
	b.add(&core.Yield{
		Fields: []core.YieldField{{Pat: pat, Exp: element}},
	})
	b.row = []*core.IDPat{pat}
	return &core.ID{Pat: pat}
}

// isRow reports whether an expression is already what the steps
// leave: the one binder's value where there is one binder, and
// otherwise the record of them all, in the order a row's fields
// go.
//
// The second case is what stops a query ending in a projection
// from also ending in a yield: "from x in a, y in b yield {x, y}"
// leaves exactly that record, so saying it again buys nothing and
// reads as a step the query did not have.
func (b *lowerBuilder) isRow(element core.Exp) bool {
	if len(b.row) == 1 {
		id, isID := element.(*core.ID)
		return isID && id.Pat == b.row[0]
	}
	tuple, isTuple := element.(*core.Tuple)
	if !isTuple || len(tuple.Args) != len(b.row) {
		return false
	}
	rec, isRec := tuple.T.(*types.Record)
	if !isRec {
		return false
	}
	sorted := slices.Clone(b.row)
	slices.SortStableFunc(sorted, func(x, y *core.IDPat) int {
		switch {
		case types.LabelLess(x.Name, y.Name):
			return -1
		case types.LabelLess(y.Name, x.Name):
			return 1
		default:
			return 0
		}
	})
	for i, arg := range tuple.Args {
		id, isID := arg.(*core.ID)
		if !isID || id.Pat != sorted[i] ||
			rec.Fields[i].Label != sorted[i].Name {
			return false
		}
	}
	return true
}

// recordOf is the record of some binders' values, whether there
// is one of them or many.
//
// In label order, because a record type keeps its fields that way
// whatever order it was given them in, and the values have to
// line up with the fields they are the values of. Handing them
// over unsorted is not a type error and not a crash: the labels
// and the values simply cross, and a query answers with the right
// fields holding each other's values.
func (b *lowerBuilder) recordOf(pats []*core.IDPat) core.Exp {
	sorted := slices.Clone(pats)
	slices.SortStableFunc(sorted, func(x, y *core.IDPat) int {
		switch {
		case types.LabelLess(x.Name, y.Name):
			return -1
		case types.LabelLess(y.Name, x.Name):
			return 1
		default:
			return 0
		}
	})
	fields := make([]types.Field, len(sorted))
	args := make([]core.Exp, len(sorted))
	for i, p := range sorted {
		fields[i] = types.Field{Label: p.Name, Type: p.T}
		args[i] = &core.ID{Pat: p}
	}
	return &core.Tuple{T: b.sys.Record(fields), Args: args}
}

// build is the query the steps make, at the type the tree had.
func (b *lowerBuilder) build(want types.Type) core.Exp {
	return &core.From{T: want, Steps: b.steps, Kind: ast.FromOp}
}

// containsOrdinal reports whether an expression reads the row
// counter. It does not descend into a nested query, which counts
// its own rows.
func containsOrdinal(exp core.Exp) bool {
	found := false
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		switch e.(type) {
		case *core.Ordinal:
			found = true
			return e, true
		case *core.From:
			return e, true
		default:
			return nil, false
		}
	}
	r.rewriteExp(exp)
	return found
}

// isUnitCollection reports whether an expression is the one-unit
// list a scanless query iterates over.
func isUnitCollection(exp core.Exp) bool {
	list, isList := exp.(*core.List)
	if !isList || len(list.Args) != 1 {
		return false
	}
	lit, isLit := list.Args[0].(*core.Literal)
	return isLit && lit.Kind == ast.UnitLiteralOp
}
