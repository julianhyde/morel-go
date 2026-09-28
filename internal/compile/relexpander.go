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
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/token"
	"github.com/hydromatic/morel-go/internal/types"
)

// Grounding, on the tree: morel-java's RelExpander.
//
// The engine that inverts predicates is the generator machinery,
// unchanged. It keys on a variable, and on field accesses into
// that variable, which is exactly the shape a tree gives it once
// the element of a leaf has a name. This is the front end that
// gives it one.
//
// What grounds a leaf that is an infinite extent is the filters
// above it. Their conditions are expressions over "$0", so naming
// the element and substituting that name for "$0" turns them into
// the constraints the engine expects. A join tree is grounded as
// a whole, because a constraint can tie two of its leaves to each
// other, and is then rebuilt: a leaf whose generator reads another
// leaf's element makes the join dependent; a generator that binds
// every leaf replaces the join; a right side the left reads comes
// first, and a projection puts the components back.
//
// A tree has erased the patterns its query was written with, so
// the caller hands them back: it translated the query, and the
// translation keeps the scans in order, so a leaf is named what
// the user named it and the plan reads as it was written.

// relExpander is one grounding of one tree.
type relExpander struct {
	sys    *types.System
	recFns map[string]*core.Fn
	// rowsUsed is whether the rows of the query are used, rather
	// than only counted or tested for existence. When they are
	// not, a leaf that no constraint mentions can be dropped
	// rather than grounded.
	rowsUsed bool
	nextName int
	// leafPats are the patterns to name the leaves with, in the
	// order a walk reaches them, and how many have been used.
	leafPats    []core.Pat
	nextLeafPat int
	// destructure is whether to name a leaf whose element is a
	// tuple by one variable per component. The pattern the user
	// wrote says which; a tree has erased it, so this tries one
	// and then the other.
	destructure bool
	// dedupObservable is whether removing a generator's duplicate
	// values would change the query's answer.
	dedupObservable bool
	// dropped is how many components a dropped leaf took off the
	// front of the element, and how many are left, or nil.
	dropped []int
	// leafNames are the names the query's own leaves bind.
	leafNames map[*core.IDPat]bool
	// subsumed are the conditions a sealed generator enforces, by
	// identity, which the filter they came from can drop.
	subsumed map[core.Exp]bool
	// simplified is what a generator makes of a condition it does
	// not entirely enforce, by identity.
	simplified map[core.Exp]core.Exp
	// tightened is what a leaf that is an infinite range was
	// tightened to, by identity.
	tightened map[core.Exp]core.Exp
	// names is the pattern each collection that replaced a leaf
	// is scanned under, for the lowering.
	names map[core.Exp]core.Pat
	// failed says a leaf could not be grounded, and reason why.
	// There is no exception to throw; the attempt is abandoned as
	// soon as it is seen.
	failed bool
	reason string
}

// fail records why grounding is abandoning the attempt.
func (x *relExpander) fail(reason string) {
	if !x.failed {
		x.reason = reason
	}
	x.failed = true
}

// RelGroundDecline is why the last tree grounding declined, for a
// diagnostic and for the tests that count what is handed back.
var RelGroundDecline string

// RelLeafPats are the names a tree's leaves had, read back from
// the projection at its root, or from the names its extents carry,
// or nil where the tree does not say.
//
// A tree has no names -- a leaf is a bare expression -- but a
// query with several binders ends in a projection that names its
// element's components after them: "project [{deptno = #2 $0,
// loc = #1 $0, name = #3 $0}]". A join concatenates its inputs'
// components, so component k is leaf k, and the projection is the
// map from leaf to name. Where there is no such projection, an
// extent carries the names its scan bound.
//
// It matters beyond plan text: grounding names what it builds
// after the leaf it bounds, a group's key record sorts its fields
// by label, and a generated label sorts differently from the one
// the user wrote -- which puts a query's rows in a different order.
func RelLeafPats(tree core.Exp) []*core.IDPat {
	if pats := relProjectedPats(tree); pats != nil {
		return pats
	}
	return relExtentPats(tree)
}

// relProjectedPats reads the leaf patterns back out of the
// projection at a tree's root.
func relProjectedPats(tree core.Exp) []*core.IDPat {
	project, isProject := tree.(*core.Project)
	if !isProject {
		return nil
	}
	tuple, isTuple := project.Exp.(*core.Tuple)
	if !isTuple {
		return nil
	}
	rec, isRec := types.Unalias(tuple.T).(*types.Record)
	if !isRec || len(rec.Fields) != len(tuple.Args) {
		return nil
	}
	// Which component each output field reads, and what it calls
	// it.
	names := map[int]string{}
	for i, arg := range tuple.Args {
		apply, isApply := arg.(*core.Apply)
		if !isApply {
			return nil
		}
		sel, isSel := apply.Fn.(*core.Selector)
		if !isSel {
			return nil
		}
		in, isInput := apply.Arg.(*core.Input)
		if !isInput || in.Ordinal != 0 {
			return nil
		}
		names[sel.Index] = rec.Fields[i].Label
	}
	var leaves []core.Exp
	if !relCollectLeaves(project.Input, &leaves) ||
		len(leaves) != len(names) {
		return nil
	}
	pats := make([]*core.IDPat, len(leaves))
	for i, leaf := range leaves {
		name, named := names[i]
		elem := types.ElemOf(leaf.Type())
		if !named || elem == nil {
			return nil
		}
		pats[i] = &core.IDPat{T: elem, Name: name}
	}
	return pats
}

// relExtentPats reads the leaf patterns from the names the
// extents carry, for a tree with no projection to read them from:
// a query with one binder. Nil unless every leaf is an extent that
// names its one element.
func relExtentPats(tree core.Exp) []*core.IDPat {
	var leaves []core.Exp
	relAllLeaves(tree, &leaves)
	pats := make([]*core.IDPat, 0, len(leaves))
	for _, leaf := range leaves {
		extent := extentOf(leaf)
		if extent == nil || len(extent.Names) != 1 {
			return nil
		}
		pats = append(pats, &core.IDPat{
			T: types.ElemOf(leaf.Type()), Name: extent.Names[0],
		})
	}
	return pats
}

// relCollectLeaves gathers a tree's leaves left to right, and
// reports false where it meets a node that is not a filter or a
// join -- above one of those the element is no longer the leaves'
// components, so the projection does not name them.
func relCollectLeaves(node core.Exp, leaves *[]core.Exp) bool {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := node.(type) {
	case *core.Filter:
		return relCollectLeaves(e.Input, leaves)
	case *core.Join:
		return relCollectLeaves(e.Left, leaves) &&
			relCollectLeaves(e.Right, leaves)
	case core.Rel:
		return false
	default:
		*leaves = append(*leaves, node)
		return true
	}
}

// RelGroundRewrite grounds a tree: each unbounded leaf is
// replaced by the collection a generator bounds it to, and the
// join tree around it rebuilt to scan what the generators need
// first. It returns nil where a leaf has no generator, which is
// the case the caller must report as "not grounded".
func RelGroundRewrite(sys *types.System,
	recFns map[string]*core.Fn, exp core.Exp, rowsUsed bool,
	leafPats []*core.IDPat,
) (core.Exp, map[core.Exp]core.Pat) {
	x := newRelExpander(sys, recFns, rowsUsed, leafPats)
	x.dedupObservable = rowsUsed || relHasTakeOrSkip(exp)
	out := x.expand(exp, nil)
	if x.failed || out == nil {
		RelGroundDecline = x.reason
		return nil, nil
	}
	RelGroundDecline = ""
	return out, x.names
}

func newRelExpander(sys *types.System, recFns map[string]*core.Fn,
	rowsUsed bool, leafPats []*core.IDPat,
) *relExpander {
	pats := make([]core.Pat, len(leafPats))
	for i, p := range leafPats {
		pats[i] = p
	}
	return &relExpander{
		sys:             sys,
		recFns:          recFns,
		rowsUsed:        rowsUsed,
		leafPats:        pats,
		dedupObservable: true,
		leafNames:       map[*core.IDPat]bool{},
		subsumed:        map[core.Exp]bool{},
		simplified:      map[core.Exp]core.Exp{},
		tightened:       map[core.Exp]core.Exp{},
		names:           map[core.Exp]core.Pat{},
	}
}

// relHasTakeOrSkip reports whether a tree takes or skips rows
// anywhere, where a duplicate changes which rows come through
// even when the rows are only counted.
func relHasTakeOrSkip(tree core.Exp) bool {
	rel, isRel := tree.(core.Rel)
	if !isRel {
		return false
	}
	switch rel.(type) {
	case *core.Skip, *core.Take:
		return true
	}
	return slices.ContainsFunc(rel.Inputs(), relHasTakeOrSkip)
}

// filterOf builds a filter as morel-java's relational builder does
// for the grounder: a filter over a filter is one filter, their
// conditions conjoined, the lower one's first.
func (x *relExpander) filterOf(input, condition core.Exp) core.Exp {
	if filter, isFilter := input.(*core.Filter); isFilter {
		return core.NewFilter(filter.Input,
			composeConjuncts(x.sys, []core.Exp{
				filter.Condition, condition,
			}))
	}
	return core.NewFilter(input, condition)
}

// projectOf builds a projection as morel-java's relational builder
// does for the grounder: a projection of the element itself is
// nothing, and a projection over a projection is one projection,
// the upper expression said over the lower one's.
func (x *relExpander) projectOf(input, exp core.Exp) core.Exp {
	if in, isInput := exp.(*core.Input); isInput && in.Ordinal == 0 {
		return input
	}
	if project, isProject := input.(*core.Project); isProject {
		return core.NewProject(x.sys, project.Input,
			x.subst(exp, project.Exp, nil))
	}
	return core.NewProject(x.sys, input, exp)
}

// trueLiteral is the boolean literal true.
func trueLiteral(sys *types.System) core.Exp {
	return &core.Literal{T: sys.Bool, Kind: ast.BoolLiteralOp, Value: true}
}

// expand rewrites a node, carrying the conditions of the filters
// passed on the way down, and replacing each infinite-extent leaf
// with what grounds it.
func (x *relExpander) expand(exp core.Exp,
	conditions []core.Exp,
) core.Exp {
	if x.failed {
		return nil
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch e := exp.(type) {
	case *core.Filter:
		return x.expandFilter(e, conditions)
	case *core.Group:
		// A group builds its element, so a condition above it
		// says nothing about the element below; but what is below
		// still has to be expanded, or an unbounded leaf survives
		// under it.
		input := x.expand(e.Input, nil)
		if input == nil {
			return nil
		}
		return core.NewGroup(x.sys, input, e.Keys, e.Aggs)
	case *core.Join:
		return x.expandJoinTree(e, conditions)
	case *core.Project:
		return x.expandProject(e, conditions)
	case *core.SetRel:
		inputs := make([]core.Exp, len(e.Args))
		for i, arg := range e.Args {
			inputs[i] = x.expand(arg, nil)
			if inputs[i] == nil {
				return nil
			}
		}
		return core.NewSetRel(x.sys, e.Kind, e.Distinct, inputs)
	case *core.Skip:
		// A node that neither changes the element nor drops rows
		// by position passes the conditions down.
		input := x.expand(e.Input, conditions)
		if input == nil {
			return nil
		}
		return core.NewSkip(input, e.Count)
	case *core.Sort:
		input := x.expand(e.Input, conditions)
		if input == nil {
			return nil
		}
		return core.NewSort(x.sys, input, e.Exp, e.Span)
	case *core.Take:
		input := x.expand(e.Input, conditions)
		if input == nil {
			return nil
		}
		return core.NewTake(input, e.Count)
	case *core.Unorder:
		input := x.expand(e.Input, conditions)
		if input == nil {
			return nil
		}
		return core.NewUnorder(x.sys, input)
	case core.Rel:
		// A node whose element does not come from a leaf below it
		// in a way this pass understands: leave its inputs alone.
		return exp
	default:
		if extentOf(exp) != nil {
			return x.bound(exp, conditions)
		}
		// An infinite range is unbounded too, and what bounds it
		// is a literal bound in a filter above rather than a
		// generator: "[1..]" under "where x < 5" is "[1..^5]".
		if finite, consumed := x.tightenLeaf(exp, conditions); finite != nil {
			x.subsumed[consumed] = true
			return finite
		}
		return exp
	}
}

// expandFilter expands a filter: its conjuncts join the conditions
// carried down, and what a sealed generator came to enforce comes
// out of the filter.
func (x *relExpander) expandFilter(e *core.Filter,
	conditions []core.Exp,
) core.Exp {
	var conjuncts []core.Exp
	decomposeConjuncts(e.Condition, &conjuncts)
	// This filter's conjuncts go before the ones already carried,
	// which are from filters above it and so were written later.
	// Order counts: where two constraints could each generate a
	// name, the engine keeps the first, so the query's own order
	// is the one to present them in.
	input := x.expand(e.Input, slices.Concat(conjuncts, conditions))
	if input == nil {
		return nil
	}
	// A conjunct that a sealed generator subsumes is now enforced
	// by the collection that replaced the leaf, so the filter need
	// not test it again; if that was all it tested, the filter
	// goes.
	remaining := x.remaining(conjuncts, true)
	if len(remaining) == 0 {
		return input
	}
	return x.filterOf(input, composeConjuncts(x.sys, remaining))
}

// remaining is the conjuncts of a filter that still have to be
// tested once the leaves below it are grounded: those no sealed
// generator subsumed, each as simplified, and shifted to the
// grounded element's fields where asked.
func (x *relExpander) remaining(conjuncts []core.Exp,
	shift bool,
) []core.Exp {
	var remaining []core.Exp
	for _, conjunct := range conjuncts {
		if x.subsumed[conjunct] {
			continue
		}
		exp2 := conjunct
		if s, ok := x.simplified[conjunct]; ok {
			exp2 = s
		}
		if isBoolLiteral(exp2, true) {
			continue
		}
		if shift {
			exp2 = x.shiftFields(exp2)
		}
		remaining = append(remaining, exp2)
	}
	return remaining
}

// expandProject expands a projection. A projection changes what
// $0 means, and substituting the projection into a condition says
// the same thing about the element below it.
func (x *relExpander) expandProject(e *core.Project,
	conditions []core.Exp,
) core.Exp {
	pushed := make([]core.Exp, len(conditions))
	for i, condition := range conditions {
		pushed[i] = x.subst(condition, e.Exp, nil)
	}
	input := x.expand(e.Input, pushed)
	if input == nil {
		return nil
	}
	if !x.rowsUsed {
		// Nothing reads the rows, so a projection is unobservable:
		// it maps each row to a value and changes how many there
		// are not at all.
		return input
	}
	return x.projectOf(input, e.Exp)
}

// expandJoinTree grounds every extent leaf of a join tree at once,
// naming the leaves as the query did and, where that fails,
// again with each tuple-typed leaf named by one variable per
// component.
func (x *relExpander) expandJoinTree(join *core.Join,
	conditions []core.Exp,
) core.Exp {
	mark, leafMark := x.nextName, x.nextLeafPat
	if out := x.expandJoinTree2(join, conditions, false); out != nil &&
		!x.failed {
		return out
	}
	// The first attempt got far enough to record what it thought
	// a generator subsumed, or simplified, or which leaf it
	// dropped. None of that survives the attempt.
	reason := x.reason
	x.reset(mark, leafMark)
	if out := x.expandJoinTree2(join, conditions, true); out != nil &&
		!x.failed {
		return out
	}
	// Both attempts failed. Report the first one's reason: it was
	// made with the names the query wrote.
	x.failed = true
	x.reason = reason
	return nil
}

// reset forgets what an abandoned attempt recorded.
func (x *relExpander) reset(mark, leafMark int) {
	x.subsumed = map[core.Exp]bool{}
	x.simplified = map[core.Exp]core.Exp{}
	x.tightened = map[core.Exp]core.Exp{}
	x.dropped = nil
	x.nextName = mark
	x.nextLeafPat = leafMark
	x.failed = false
	x.reason = ""
}

// relFrame is what a join tree looks like from above: the
// expression that denotes its element in terms of a name per leaf,
// the pattern that names each leaf, and the conditions that its
// own joins impose.
type relFrame struct {
	element core.Exp
	// leaves is the pattern each leaf is named by, by identity,
	// and leafOrder the leaves left to right.
	leaves    map[core.Exp]core.Pat
	leafOrder []core.Exp
	// constraints are the conditions the tree's own nodes impose,
	// written in the leaves' names.
	constraints []core.Exp
	// originals is the conjunct a constraint came from, by
	// identity, so that the filter can drop what a sealed
	// generator has taken over.
	originals map[core.Exp]core.Exp
	// elements is the element expression of every node of the
	// tree, in terms of the names of the leaves below it.
	elements map[core.Exp]core.Exp
	// order is the extents to register and constraints to apply,
	// in the order a walk of the tree reaches them. The engine
	// improves its generators after every constraint, so which
	// extents are registered by then decides what it settles on.
	order []relGroundStep
}

// relGroundStep is one step of grounding: an extent to register,
// or a constraint to apply.
type relGroundStep struct {
	pat core.Pat
	exp core.Exp
}

func (x *relExpander) expandJoinTree2(join *core.Join,
	conditions []core.Exp, destructure bool,
) core.Exp {
	x.destructure = destructure
	// Whatever a join further up dropped is not this join's
	// business.
	outerDropped := x.dropped
	x.dropped = nil
	frame := x.collect(join)
	outerLeafNames := x.leafNames
	x.leafNames = map[*core.IDPat]bool{}
	for _, pat := range frame.leaves {
		for _, id := range core.PatIDs(pat) {
			x.leafNames[id] = true
		}
	}
	constraints := slices.Clone(frame.constraints)
	originals := maps.Clone(frame.originals)
	for _, condition := range conditions {
		constraint := x.subst(condition, frame.element, nil)
		originals[constraint] = condition
		constraints = append(constraints, constraint)
		// Above the tree, so after everything in it -- a "where"
		// that follows every scan.
		frame.order = append(frame.order, relGroundStep{exp: constraint})
	}
	// A leaf that is a list of numbers bounds its name, and FBBT
	// can carry that to another name. The bound is for FBBT to
	// read and not for the engine to apply -- the leaf enforces
	// it -- so it is written into the constraints and never into
	// the order.
	for _, leaf := range frame.leafOrder {
		constraints = append(constraints,
			x.listBounds(leaf, frame.leaves[leaf])...)
	}
	x.tightenRanges(frame, constraints)
	var extents []relGroundStep
	x.extentsInOrder(join, frame, &extents)
	if len(extents) == 0 && len(x.tightened) == 0 {
		left := x.expand(join.Left, nil)
		right := x.expand(join.Right, nil)
		if left == nil || right == nil {
			return nil
		}
		x.leafNames = outerLeafNames
		return core.NewJoin(x.sys, join.Kind, join.Binder, left, right,
			join.Condition)
	}
	ungrounded := map[*core.IDPat]bool{}
	for _, extent := range extents {
		for _, id := range core.PatIDs(extent.pat) {
			ungrounded[id] = true
		}
	}
	x.correlatedPats(join, frame, map[*core.IDPat]core.Exp{}, ungrounded)
	// Interleaved, in the order the walk reached them; the
	// conjuncts that strengthening added are not in that order,
	// having no place in the tree, so they go at the end.
	written := map[core.Exp]bool{}
	for _, c := range constraints {
		written[c] = true
	}
	order := slices.Clone(frame.order)
	for _, constraint := range x.strengthen(constraints, extents) {
		if !written[constraint] {
			order = append(order, relGroundStep{exp: constraint})
		}
	}
	cache := x.runEngine(order, ungrounded)
	for _, leaf := range frame.leafOrder {
		if extentOf(leaf) != nil {
			x.recordSubsumed(frame.leaves[leaf], cache, originals)
		}
	}
	result := x.rebuild(join, frame, cache, map[*core.IDPat]core.Exp{})
	if x.dropped == nil {
		x.dropped = outerDropped
	}
	x.leafNames = outerLeafNames
	return result
}

// extentsInOrder collects the extent leaves under a node, left
// to right.
func (x *relExpander) extentsInOrder(node core.Exp, frame *relFrame,
	extents *[]relGroundStep,
) {
	if join, isJoin := node.(*core.Join); isJoin {
		x.extentsInOrder(join.Left, frame, extents)
		x.extentsInOrder(join.Right, frame, extents)
		return
	}
	if filter, isFilter := node.(*core.Filter); isFilter {
		x.extentsInOrder(filter.Input, frame, extents)
		return
	}
	pat, named := frame.leaves[node]
	if named && extentOf(node) != nil {
		*extents = append(*extents, relGroundStep{pat: pat, exp: node})
	}
}

// relCache is the generators the engine settled on, one per name.
type relCache struct {
	gens map[*core.IDPat]*generator
	// infinite is the names an infinite extent was registered for.
	// A name whose extent is finite has its generator already, and
	// the engine does not look for a better one; the rest are asked
	// again after every constraint.
	infinite map[*core.IDPat]bool
}

// best returns the generator for a name, or nil.
func (c *relCache) best(pat *core.IDPat) *generator {
	return c.gens[pat]
}

// runEngine registers the extents and applies the constraints in
// order, as morel-java's Expander.ground does: after every
// constraint, each extent name is asked again, and the last
// generator found for a name is the one it keeps.
func (x *relExpander) runEngine(order []relGroundStep,
	ungrounded map[*core.IDPat]bool,
) *relCache {
	cache := &relCache{
		gens:     map[*core.IDPat]*generator{},
		infinite: map[*core.IDPat]bool{},
	}
	extents := map[*core.IDPat]bool{}
	for _, step := range order {
		if step.pat != nil {
			for _, id := range core.PatIDs(step.pat) {
				extents[id] = true
			}
		}
	}
	ctx := &genContext{
		sys: x.sys, extents: extents, recFns: x.recFns,
		ungrounded: ungrounded, leaves: x.leafNames,
	}
	var registered []*core.IDPat
	var constraints []core.Exp
	for _, step := range order {
		if step.pat != nil {
			registered = append(registered, core.PatIDs(step.pat)...)
			x.registerExtent(cache, step.pat, step.exp)
			continue
		}
		constraints = append(constraints, step.exp)
		relDebugf("constraint: %s", relDebugExp(x.sys, step.exp))
		// Every extent name is asked again after every constraint,
		// and the generator found last is the one kept: a later
		// constraint can improve on an earlier one, as a constant
		// bound that strengthening derived improves on a bound
		// that reads a name still looking for a generator.
		for _, pat := range registered {
			if !cache.infinite[pat] {
				continue
			}
			g := maybeGenerator(ctx, pat, constraints)
			if g == nil || g.card == infinite {
				continue
			}
			relDebugf("  generator for %s: %s", pat.Name,
				relDebugExp(x.sys, g.exp))
			for _, p := range core.PatIDs(g.pat) {
				cache.gens[p] = g
			}
		}
	}
	return cache
}

// registerExtent gives each name an extent scans the extent as
// its first generator: finite by itself when the type is finite.
// A tuple or record pattern over an unbounded extent also gets one
// per component, so that a component whose type is finite -- a
// "bool" beside an "int" -- is grounded by its own extent while the
// other waits for a constraint.
func (x *relExpander) registerExtent(cache *relCache, pat core.Pat,
	leaf core.Exp,
) {
	g := &generator{
		exp: leaf, pat: pat, card: finite, unique: true, sealed: true,
	}
	if isInfiniteExtent(leaf) {
		g.card = infinite
	}
	for _, p := range core.PatIDs(pat) {
		cache.gens[p] = g
		if g.card == infinite {
			cache.infinite[p] = true
		}
	}
	if !isInfiniteExtent(leaf) || relContainsAsPat(pat) {
		return
	}
	x.registerComponents(cache, pat)
}

// registerComponents registers an extent generator for each
// component of a tuple or record pattern, recursively.
func (x *relExpander) registerComponents(cache *relCache, pat core.Pat) {
	tuple, isTuple := pat.(*core.TuplePat)
	if !isTuple {
		return
	}
	for _, arg := range tuple.Args {
		names := extentNames(arg)
		extent := extentScanExp(x.sys, arg.Type(), token.Span{}, names...)
		x.registerExtent(cache, arg, extent)
		x.registerComponents(cache, arg)
	}
}

// relContainsAsPat reports whether a pattern contains an "as"
// pattern.
func relContainsAsPat(pat core.Pat) bool {
	// lint: sort until '^	}' where '^	case '
	switch p := pat.(type) {
	case *core.AsPat:
		return true
	case *core.TuplePat:
		return slices.ContainsFunc(p.Args, relContainsAsPat)
	}
	return false
}

// rebuild rebuilds a join tree with each extent leaf replaced by
// the collection that bounds it. A leaf whose generator reads
// another leaf's element makes the join dependent, its binder
// naming what the right side reads.
func (x *relExpander) rebuild(node core.Exp, frame *relFrame,
	cache *relCache, bound map[*core.IDPat]core.Exp,
) core.Exp {
	if x.failed {
		return nil
	}
	if filter, isFilter := node.(*core.Filter); isFilter {
		return x.rebuildFilter(filter, frame, cache, bound)
	}
	join, isJoin := node.(*core.Join)
	if !isJoin {
		return x.rebuildLeaf(node, frame, cache, bound)
	}
	if !x.rowsUsed {
		// Nothing looks at the rows, so a side that nothing
		// constrains cannot affect the answer, and need not be
		// enumerated.
		if x.droppable(join.Right, frame, cache) {
			return x.rebuild(join.Left, frame, cache, bound)
		}
		if x.droppable(join.Left, frame, cache) {
			survivor := x.rebuild(join.Right, frame, cache, bound)
			x.dropped = []int{
				relComponentCount(join.Left), relComponentCount(join.Right),
			}
			return survivor
		}
	}
	if len(bound) == 0 {
		if chained := x.scheduled(join, frame, cache); chained != nil {
			return chained
		}
		if x.failed {
			return nil
		}
	}
	if common := x.commonGenerator(join, frame, cache, bound); common != nil {
		return x.fromCommon(join, frame, common, bound)
	}
	right := join.Right
	var rightGenerator *generator
	if extentOf(right) != nil {
		rightGenerator = x.generator(frame.leaves[right], cache)
	}
	if rightGenerator != nil && len(x.free(rightGenerator, bound)) > 0 {
		return x.rebuildDependent(join, frame, cache, bound, rightGenerator)
	}
	if (rightGenerator != nil ||
		relBoundedLeaf(right, frame) && join.Binder == nil) &&
		isBoolLiteral(join.Condition, true) &&
		x.reads(join.Left, frame, cache, bound, frame.leaves[right]) {
		// A right side that already reads the left through the
		// join's binder cannot come first; the sides would each
		// read the other.
		return x.rebuildSwapped(join, frame, cache, bound, rightGenerator)
	}
	left := x.rebuild(join.Left, frame, cache, bound)
	right2 := x.rebuild(right, frame, cache, bound)
	if left == nil || right2 == nil {
		return nil
	}
	return core.NewJoin(x.sys, join.Kind, join.Binder, left, right2,
		join.Condition)
}

// rebuildFilter rebuilds a filter. Its conjuncts were grounded
// with the rest of the tree's constraints, so what a sealed
// generator now enforces comes out, and the filter goes if that
// was all of it.
func (x *relExpander) rebuildFilter(filter *core.Filter,
	frame *relFrame, cache *relCache, bound map[*core.IDPat]core.Exp,
) core.Exp {
	input := x.rebuild(filter.Input, frame, cache, bound)
	if input == nil {
		return nil
	}
	var conjuncts []core.Exp
	decomposeConjuncts(filter.Condition, &conjuncts)
	remaining := x.remaining(conjuncts, false)
	if len(remaining) == 0 {
		return input
	}
	return x.filterOf(input, composeConjuncts(x.sys, remaining))
}

// rebuildLeaf rebuilds a node under a join that is not a join: an
// extent leaf becomes what bounds it, a range leaf what tightened
// it, and a tree is expanded on its own.
func (x *relExpander) rebuildLeaf(node core.Exp, frame *relFrame,
	cache *relCache, bound map[*core.IDPat]core.Exp,
) core.Exp {
	if extentOf(node) != nil {
		// A leaf that collect reached, so leaves has a pattern
		// for it.
		return x.bounded(frame.leaves[node], cache, bound)
	}
	if finite := x.tightened[node]; finite != nil {
		return finite
	}
	if _, isRel := node.(core.Rel); isRel {
		return x.expand(node, nil)
	}
	return node
}

// rebuildDependent rebuilds a join whose right side is correlated:
// it reads names that the left side binds. The join becomes
// dependent, its binder naming the left element, and each name
// the generator reads becomes the path that reads it out of that
// element.
func (x *relExpander) rebuildDependent(join *core.Join,
	frame *relFrame, cache *relCache, bound map[*core.IDPat]core.Exp,
	rightGenerator *generator,
) core.Exp {
	leftElement := frame.elements[join.Left]
	param := x.groundPat(types.ElemOf(join.Left.Type()))
	paths := map[*core.IDPat]core.Exp{}
	for _, name := range x.free(rightGenerator, bound) {
		var path core.Exp
		if leftElement != nil {
			path = x.pathTo(leftElement, &core.ID{Pat: param}, name)
		}
		if path == nil {
			// What the generator reads is not bound on the left --
			// it is a leaf further away, which would need the join
			// reordered.
			x.fail("generator reads a name the left side does not bind: " +
				name.Name)
			return nil
		}
		paths[name] = path
	}
	if !isBoolLiteral(join.Condition, true) {
		x.fail("dependent join would need a condition")
		return nil
	}
	maps.Copy(paths, bound)
	collection := x.replace(rightGenerator.exp, paths)
	x.names[collection] = frame.leaves[join.Right]
	left := x.rebuild(join.Left, frame, cache, bound)
	if left == nil {
		return nil
	}
	return core.NewJoin(x.sys, join.Kind, param, left, collection,
		join.Condition)
}

// rebuildSwapped rebuilds a join the other way round: the right
// side grounds on its own and the left side reads it, so the
// right comes first.
func (x *relExpander) rebuildSwapped(join *core.Join,
	frame *relFrame, cache *relCache, bound map[*core.IDPat]core.Exp,
	rightGenerator *generator,
) core.Exp {
	right := join.Right
	rightPat := frame.leaves[right]
	param := x.groundPat(types.ElemOf(right.Type()))
	bound2 := maps.Clone(bound)
	for _, name := range core.PatIDs(rightPat) {
		bound2[name] = x.path(rightPat, &core.ID{Pat: param}, name)
	}
	left := x.rebuild(join.Left, frame, cache, bound2)
	if left == nil {
		return nil
	}
	boundedRight := right
	if rightGenerator != nil {
		boundedRight = x.bounded(rightPat, cache, bound)
		if boundedRight == nil {
			return nil
		}
	}
	// The sides swap, so the yield commutes with them.
	swapped := core.NewJoin(x.sys, join.Kind, param, boundedRight,
		left, join.Condition)
	// Swapping the inputs moves the components, and with no yield
	// to absorb the swap a projection puts them back where the
	// tree above expects them.
	return x.permute(swapped, relComponentCount(boundedRight),
		relComponentCount(left))
}

// scheduled grounds a join tree whose leaves share a generator, by
// scanning each generator once and joining on the names already
// bound.
//
// A generator may bind several names: "(x, y) elem pairs" grounds
// both. Replacing each leaf on its own enumerates the collection
// once per leaf and pairs every value with every other, which is
// not what the constraint said. So each generator is scanned once,
// a later one joins on whichever name is already bound, and the
// tree scans what the chain yields.
//
// Returns nil where this does not apply -- nothing is shared, a
// leaf is not an extent, a node between the leaves is not a join,
// a join carries a condition, or no order satisfies the
// generators' dependencies -- and the ordinary leaf-by-leaf path
// runs instead.
func (x *relExpander) scheduled(join *core.Join, frame *relFrame,
	cache *relCache,
) core.Exp {
	if !relJoinsAndExtents(join, frame) {
		return nil
	}
	gens, names := x.sharedGenerators(join, frame, cache)
	if gens == nil {
		return nil
	}
	order := scheduleOrder(gens, names)
	if order == nil {
		return nil
	}
	c := &relChain{x: x, bindings: map[*core.IDPat]chainBinding{}}
	projectsAway := false
	anyDuplicates := false
	for _, i := range order {
		g := gens[i]
		anyDuplicates = anyDuplicates || !g.unique
		for _, p := range core.PatIDs(g.pat) {
			if _, bound := c.bindings[p]; !bound &&
				!slices.Contains(names, p) {
				projectsAway = true
			}
		}
		c.add(g)
	}
	collection := x.projectOf(c.exp, c.yield(names))
	if x.dedupObservable && (anyDuplicates || projectsAway) {
		collection = x.dedupRow(collection, x.recordOrAtomPat(names), names)
	}
	// The tree above wants the join's element, which is written in
	// terms of the leaves' names; read each out of the row the
	// chain yields.
	out := core.NewInput(types.ElemOf(collection.Type()), 0)
	return x.projectOf(collection,
		x.rename(frame.elements[join], out, x.recordOrAtomPat(names)))
}

// sharedGenerators is the generators of the names the leaves under
// a join bind, one per generator, and the names; nil where a name
// has no finite generator, or where nothing is shared -- each name
// has its own generator, or one generator binds every name, which
// the ordinary paths say more directly.
func (x *relExpander) sharedGenerators(join *core.Join,
	frame *relFrame, cache *relCache,
) ([]*generator, []*core.IDPat) {
	var names []*core.IDPat
	for _, leaf := range frame.leafOrder {
		if relContains(join, leaf) {
			names = append(names, core.PatIDs(frame.leaves[leaf])...)
		}
	}
	var gens []*generator
	for _, name := range names {
		g := cache.best(name)
		if g == nil || g.card == infinite {
			return nil, nil
		}
		if !slices.Contains(gens, g) {
			gens = append(gens, g)
		}
	}
	if len(gens) == len(names) || len(gens) == 1 {
		return nil, nil
	}
	return gens, names
}

// scheduleOrder orders generators so that each is scanned once
// every name it reads that one of the leaves binds has been
// scanned; nil where no order does.
func scheduleOrder(gens []*generator, names []*core.IDPat) []int {
	var order []int
	scheduled := map[*core.IDPat]bool{}
	for len(order) < len(gens) {
		next := -1
		for i, g := range gens {
			if slices.Contains(order, i) {
				continue
			}
			waits := func(free *core.IDPat) bool {
				return slices.Contains(names, free) && !scheduled[free]
			}
			if !slices.ContainsFunc(g.freePats, waits) {
				next = i
				break
			}
		}
		if next < 0 {
			return nil
		}
		order = append(order, next)
		for _, p := range core.PatIDs(gens[next].pat) {
			scheduled[p] = true
		}
	}
	return order
}

// chainBinding says where a chain binds a name: in which
// generator's element, and by what pattern.
type chainBinding struct {
	k   int
	pat core.Pat
}

// relChain is a chain of generator scans being built: a name the
// chain has already bound is tested against what bound it, and a
// collection that reads a bound name reads it through the join's
// binder. A join's element is its inputs' components concatenated,
// so the k-th generator's element is component k of the chain's,
// and the only component while the chain is one scan.
type relChain struct {
	x        *relExpander
	bindings map[*core.IDPat]chainBinding
	exp      core.Exp
	leaves   int
}

// pathOf reads a name the chain has bound out of the chain's
// element.
func (c *relChain) pathOf(p *core.IDPat, base core.Exp) core.Exp {
	b := c.bindings[p]
	if c.leaves > 1 {
		base = fieldAt(c.x.sys, base, b.k)
	}
	return c.x.path(b.pat, base, p)
}

// add scans one more generator at the end of the chain.
func (c *relChain) add(g *generator) {
	x := c.x
	if c.exp == nil {
		c.exp = x.scanned(g.exp, g)
		x.names[g.exp] = g.pat
		c.leaves = 1
		for _, p := range core.PatIDs(g.pat) {
			c.bindings[p] = chainBinding{k: 0, pat: g.pat}
		}
		return
	}
	// The names the collection reads are bound on the left, and
	// read through the binder.
	var binder *core.IDPat
	reads := map[*core.IDPat]core.Exp{}
	for _, free := range g.freePats {
		if _, bound := c.bindings[free]; bound {
			if binder == nil {
				binder = x.groundPat(types.ElemOf(c.exp.Type()))
			}
			reads[free] = c.pathOf(free, &core.ID{Pat: binder})
		}
	}
	right := x.scanned(x.replace(g.exp, reads), g)
	x.names[g.exp] = g.pat
	rightElem := types.ElemOf(right.Type())
	leftElem := types.ElemOf(c.exp.Type())
	// A name both sides bind is tested for agreement.
	var conditions []core.Exp
	for _, p := range core.PatIDs(g.pat) {
		if _, bound := c.bindings[p]; bound {
			conditions = append(conditions, eqExp(x.sys,
				x.path(g.pat, core.NewInput(rightElem, 1), p),
				c.pathOf(p, core.NewInput(leftElem, 0))))
		}
	}
	condition := trueLiteral(x.sys)
	if len(conditions) > 0 {
		condition = composeConjuncts(x.sys, conditions)
	}
	c.exp = core.NewJoin(x.sys, core.InnerJoin, binder, c.exp, right,
		condition)
	for _, p := range core.PatIDs(g.pat) {
		if _, bound := c.bindings[p]; !bound {
			c.bindings[p] = chainBinding{k: c.leaves, pat: g.pat}
		}
	}
	c.leaves++
}

// yield is what the chain yields: the record of the names, or the
// one name.
func (c *relChain) yield(names []*core.IDPat) core.Exp {
	row := core.NewInput(types.ElemOf(c.exp.Type()), 0)
	if len(names) == 1 {
		return c.pathOf(names[0], row)
	}
	sorted := slices.Clone(names)
	slices.SortFunc(sorted, func(a, b *core.IDPat) int {
		return strings.Compare(a.Name, b.Name)
	})
	fields := make([]types.Field, len(sorted))
	args := make([]core.Exp, len(sorted))
	for i, p := range sorted {
		fields[i] = types.Field{Label: p.Name, Type: p.T}
		args[i] = c.pathOf(p, row)
	}
	return &core.Tuple{T: c.x.sys.Record(fields), Args: args}
}

// relJoinsAndExtents reports whether every node under a join is a
// join or an extent leaf, and every join is unconditional -- the
// shape the chain can stand in for.
func relJoinsAndExtents(node core.Exp, frame *relFrame) bool {
	if join, isJoin := node.(*core.Join); isJoin {
		return join.Kind == core.InnerJoin && join.Binder == nil &&
			isBoolLiteral(join.Condition, true) &&
			relJoinsAndExtents(join.Left, frame) &&
			relJoinsAndExtents(join.Right, frame)
	}
	_, named := frame.leaves[node]
	return named && extentOf(node) != nil
}

// fromCommon grounds a join tree that one generator binds every
// leaf of: the leaves and the join between them become one scan
// of that generator, read through the paths its pattern gives
// each name.
func (x *relExpander) fromCommon(join *core.Join, frame *relFrame,
	common *generator, bound map[*core.IDPat]core.Exp,
) core.Exp {
	collection := x.replace(common.exp, bound)
	x.names[collection] = common.pat
	element := frame.elements[join]
	if x.dedupObservable && !common.unique {
		// A generator may repeat a value where an unbounded scan
		// yields each assignment once, so it is scanned under its
		// own pattern, deduplicated, and ordered by the record of
		// its variables. That order is the query's answer, not a
		// detail.
		vars := core.PatIDs(common.pat)
		deduped := x.dedupRow(x.scanned(collection, common), common.pat,
			vars)
		return x.projectOf(deduped,
			x.rename(element, core.NewInput(types.ElemOf(deduped.Type()), 0),
				x.recordOrAtomPat(vars)))
	}
	row := core.NewInput(types.ElemOf(collection.Type()), 0)
	if !relDestructurable(common.pat) || len(common.conds) > 0 {
		// The pattern can fail, and a projection reads every row
		// where the pattern matches only some. Scanning it
		// filters, and the element is already written in terms of
		// the names it binds.
		return x.projectOf(x.scanned(collection, common),
			x.rename(element, row, common.pat))
	}
	return x.projectOf(collection,
		x.rename(element, row, common.pat))
}

// scanned is a generator's collection scanned under its pattern:
// the rows the pattern matches, where it may fail, that also
// satisfy the conditions the generator's scan must apply at once.
func (x *relExpander) scanned(collection core.Exp, g *generator) core.Exp {
	row := core.NewInput(types.ElemOf(collection.Type()), 0)
	var tests []core.Exp
	if test := relTest(x.sys, g.pat, row); test != nil {
		tests = append(tests, test)
	}
	for _, c := range g.conds {
		tests = append(tests, x.rename(c, row, g.pat))
	}
	if len(tests) == 0 {
		return collection
	}
	return x.filterOf(collection, composeConjuncts(x.sys, tests))
}

// dedupRow takes a collection's rows distinct by the variables a
// pattern binds, and orders them by the record of those
// variables: "group [x = ..., y = ...]" and then "sort [$0]".
func (x *relExpander) dedupRow(collection core.Exp, pat core.Pat,
	vars []*core.IDPat,
) core.Exp {
	row := core.NewInput(types.ElemOf(collection.Type()), 0)
	sorted := slices.Clone(vars)
	slices.SortFunc(sorted, func(a, b *core.IDPat) int {
		return strings.Compare(a.Name, b.Name)
	})
	keys := make([]core.RelGroupKey, len(sorted))
	for i, v := range sorted {
		keys[i] = core.RelGroupKey{
			Label: v.Name, Exp: x.path(pat, row, v), Pat: v,
		}
	}
	group := core.NewGroup(x.sys, collection, keys, nil)
	var out core.Exp = group
	if len(sorted) == 1 {
		out = x.projectOf(group,
			fieldAt(x.sys, core.NewInput(types.ElemOf(group.Type()), 0), 0))
	}
	return core.NewSort(x.sys, out,
		core.NewInput(types.ElemOf(out.Type()), 0), token.Span{})
}

// dedupNamed takes a collection's values distinct, in their own
// order, where a generator may produce one more than once and an
// unbounded scan yields each assignment once: "sort [$0]" over
// "project [#x $0]" over "group [x = $0]".
func (x *relExpander) dedupNamed(collection core.Exp,
	pat *core.IDPat,
) core.Exp {
	elem := types.ElemOf(collection.Type())
	group := core.NewGroup(x.sys, collection, []core.RelGroupKey{{
		Label: pat.Name, Exp: core.NewInput(elem, 0), Pat: pat,
	}}, nil)
	project := x.projectOf(group,
		fieldAt(x.sys, core.NewInput(types.ElemOf(group.Type()), 0), 0))
	return core.NewSort(x.sys, project, core.NewInput(elem, 0), token.Span{})
}

// permute projects a join whose inputs were swapped back into the
// component order the tree above expects: the last k first, then
// the first m.
func (x *relExpander) permute(join core.Exp, m, k int) core.Exp {
	element := core.NewInput(types.ElemOf(join.Type()), 0)
	exps := make([]core.Exp, 0, m+k)
	for i := range k {
		exps = append(exps, fieldAt(x.sys, element, m+i))
	}
	for i := range m {
		exps = append(exps, fieldAt(x.sys, element, i))
	}
	return x.projectOf(join, x.tupleOf(exps))
}

// tupleOf is a tuple of expressions.
func (x *relExpander) tupleOf(exps []core.Exp) core.Exp {
	ts := make([]types.Type, len(exps))
	for i, e := range exps {
		ts[i] = e.Type()
	}
	return &core.Tuple{T: x.sys.Tuple(ts...), Args: exps}
}

// shiftFields moves a condition onto the element a dropped leaf
// left behind: "#2 $0" reads the second component, and with the
// first gone it is the first, or the whole element where only
// one is left.
func (x *relExpander) shiftFields(exp core.Exp) core.Exp {
	drop := x.dropped
	if drop == nil {
		return exp
	}
	r := &rewriter{sys: x.sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		apply, isApply := e.(*core.Apply)
		if !isApply {
			return nil, false
		}
		sel, isSel := apply.Fn.(*core.Selector)
		in, isIn := apply.Arg.(*core.Input)
		if !isSel || !isIn || in.Ordinal != 0 || sel.Index < drop[0] {
			return nil, false
		}
		if drop[1] == 1 {
			return core.NewInput(apply.T, 0), true
		}
		return fieldAt(x.sys, core.NewInput(apply.Arg.Type(), 0),
			sel.Index-drop[0]), true
	}
	return r.rewriteExp(exp)
}

// relBoundedLeaf reports whether a node is a leaf that needs no
// generator.
func relBoundedLeaf(node core.Exp, frame *relFrame) bool {
	_, named := frame.leaves[node]
	return extentOf(node) == nil && named
}

// reads reports whether any extent leaf under a node has a
// generator that reads a name the given pattern binds.
func (x *relExpander) reads(node core.Exp, frame *relFrame,
	cache *relCache, bound map[*core.IDPat]core.Exp, pat core.Pat,
) bool {
	if pat == nil {
		return false
	}
	names := map[*core.IDPat]bool{}
	for _, id := range core.PatIDs(pat) {
		names[id] = true
	}
	for _, leaf := range frame.leafOrder {
		if !relContains(node, leaf) || extentOf(leaf) == nil {
			continue
		}
		for _, name := range core.PatIDs(frame.leaves[leaf]) {
			g := cache.best(name)
			if g == nil {
				continue
			}
			for _, free := range x.free(g, bound) {
				if names[free] {
					return true
				}
			}
		}
	}
	return false
}

// commonGenerator returns the generator that binds the names of
// every leaf under a node, if one does: nil if the leaves have
// different generators, if any is not an extent, or if a
// condition of the joins between them is not one the generator
// enforces.
func (x *relExpander) commonGenerator(node core.Exp, frame *relFrame,
	cache *relCache, bound map[*core.IDPat]core.Exp,
) *generator {
	var common *generator
	for _, leaf := range frame.leafOrder {
		if !relContains(node, leaf) {
			continue
		}
		if extentOf(leaf) == nil {
			return nil
		}
		for _, name := range core.PatIDs(frame.leaves[leaf]) {
			g := cache.best(name)
			if g == nil || g.card == infinite || len(x.free(g, bound)) > 0 {
				return nil
			}
			if common == nil {
				common = g
			} else if common != g {
				return nil
			}
		}
	}
	if common == nil {
		return nil
	}
	if _, isID := common.pat.(*core.IDPat); isID {
		// A generator that binds one name grounds one leaf, which
		// the ordinary path handles.
		return nil
	}
	if !relConditionsEnforced(node) {
		return nil
	}
	return common
}

// relContains reports whether a node contains another, by
// identity, through filters and joins.
func relContains(node, target core.Exp) bool {
	if node == target {
		return true
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch e := node.(type) {
	case *core.Filter:
		return relContains(e.Input, target)
	case *core.Join:
		return relContains(e.Left, target) || relContains(e.Right, target)
	default:
		return false
	}
}

// relConditionsEnforced reports whether every join under a node
// has a trivial condition. A filter under the node says no:
// collapsing the leaves under it into one scan would discard the
// filter with the join it replaces.
func relConditionsEnforced(node core.Exp) bool {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := node.(type) {
	case *core.Filter:
		return false
	case *core.Join:
		return isBoolLiteral(e.Condition, true) &&
			relConditionsEnforced(e.Left) &&
			relConditionsEnforced(e.Right)
	default:
		return true
	}
}

// rename replaces each name of a pattern with the path that reads
// it out of an element.
func (x *relExpander) rename(exp, element core.Exp, pat core.Pat) core.Exp {
	binds := map[*core.IDPat]core.Exp{}
	for _, id := range core.PatIDs(pat) {
		if path := x.path(pat, element, id); path != nil {
			binds[id] = path
		}
	}
	return substituteExp(exp, binds)
}

// pathTo returns the expression that reads a name out of an
// element, given the expression that says what the element is
// made of, or nil if the element does not contain the name.
func (x *relExpander) pathTo(element, accessor core.Exp,
	name *core.IDPat,
) core.Exp {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := element.(type) {
	case *core.ID:
		if e.Pat == name {
			return accessor
		}
		return nil
	case *core.Tuple:
		for i, arg := range e.Args {
			exp := x.pathTo(arg, fieldAt(x.sys, accessor, i), name)
			if exp != nil {
				return exp
			}
		}
		return nil
	default:
		return nil
	}
}

// replace replaces each name of a map with the expression it maps
// to.
func (x *relExpander) replace(exp core.Exp,
	replacements map[*core.IDPat]core.Exp,
) core.Exp {
	if len(replacements) == 0 {
		return exp
	}
	return substituteExp(exp, replacements)
}

// droppable reports whether a side of a join is an infinite
// extent that no constraint mentions, and can therefore be
// dropped when the rows are not used.
func (x *relExpander) droppable(node core.Exp, frame *relFrame,
	cache *relCache,
) bool {
	pat, named := frame.leaves[node]
	if !named || !isInfiniteExtent(node) {
		return false
	}
	for _, name := range core.PatIDs(pat) {
		if best := cache.best(name); best != nil && best.card != infinite {
			return false
		}
		for _, constraint := range frame.constraints {
			if containsRef(constraint, name) {
				return false
			}
		}
	}
	return true
}

// generator returns the generator of a leaf named by a single
// variable, or nil.
func (x *relExpander) generator(pat core.Pat, cache *relCache) *generator {
	if id, isID := pat.(*core.IDPat); isID {
		return cache.best(id)
	}
	return nil
}

// bounded returns the collection that bounds a leaf. A leaf named
// by a tuple of variables has a generator per component, each
// bounded by a different constraint, so what bounds the leaf is
// their product.
func (x *relExpander) bounded(pat core.Pat, cache *relCache,
	bound map[*core.IDPat]core.Exp,
) core.Exp {
	names := core.PatIDs(pat)
	var product core.Exp
	for _, name := range names {
		g := cache.best(name)
		if g == nil {
			x.fail("no generator for " + name.Name)
			return nil
		}
		if free := x.free(g, bound); len(free) > 0 {
			// What bounds this name reads something that is not
			// bound yet.
			x.fail("generator for " + name.Name + " reads unbound " +
				free[0].Name)
			return nil
		}
		component := x.project(g, name, bound)
		if component == nil {
			return nil
		}
		if product == nil {
			product = component
			continue
		}
		product = core.NewJoin(x.sys, core.InnerJoin, nil, product, component,
			trueLiteral(x.sys))
	}
	return product
}

// listBounds returns, for a leaf that is a list of numeric
// literals, the least and the greatest of them as bounds on its
// name.
func (x *relExpander) listBounds(leaf core.Exp, pat core.Pat) []core.Exp {
	id, isID := pat.(*core.IDPat)
	if !isID {
		return nil
	}
	return listBounds(x.sys, &core.Scan{Pat: id, Exp: leaf})
}

// tightenLeaf replaces a leaf that is an infinite range with the
// finite range the conditions over "$0" make of it, returning the
// new leaf and the conjunct consumed, or nil.
func (x *relExpander) tightenLeaf(leaf core.Exp,
	conditions []core.Exp,
) (core.Exp, core.Exp) {
	rl, isRange := leaf.(*core.RangeList)
	if !isRange || len(rl.Items) != 1 || len(conditions) == 0 {
		return nil, nil
	}
	pat := x.groundPat(types.ElemOf(leaf.Type()))
	named := make([]core.Exp, len(conditions))
	for i, c := range conditions {
		named[i] = x.subst(c, &core.ID{Pat: pat}, nil)
	}
	return x.tightenRange(rl, pat, named, conditions)
}

// tightenRange folds the tightest literal bound on a name into
// a one-sided range item. The conjuncts are given in the name's
// terms, and originals is what each was written as, which is what
// the caller records as consumed.
func (x *relExpander) tightenRange(rl *core.RangeList, pat *core.IDPat,
	conjuncts, originals []core.Exp,
) (core.Exp, core.Exp) {
	item := rl.Items[0]
	var wantUpper bool
	// lint: sort until '^\t}' where '^\tcase '
	switch item.Kind {
	case ast.RangeAtLeast, ast.RangeGreaterThan:
		wantUpper = true
	case ast.RangeAtMost, ast.RangeLessThan:
		wantUpper = false
	default:
		return nil, nil
	}
	rest := []core.FromStep{
		&core.Where{Exp: composeConjuncts(x.sys, conjuncts)},
	}
	item2, consumed := pushBound(x.sys, pat, item, wantUpper, rest)
	if consumed == nil {
		return nil, nil
	}
	original := consumed
	for i, c := range conjuncts {
		if c == consumed && i < len(originals) {
			original = originals[i]
		}
	}
	// The bound was read in the name's terms; the leaf is written
	// in none, so a bound that names the leaf itself is not one.
	if containsRef(item2.Lo, pat) || containsRef(item2.Hi, pat) {
		return nil, nil
	}
	return &core.RangeList{
		T: rl.T, Items: []core.RangeItem{item2}, Span: rl.Span,
	}, original
}

// tightenRanges replaces each leaf that is an infinite range
// with the finite range that the constraints make of it: the
// range's own bound is written as a constraint, or FBBT has
// nothing to propagate from; and the bound that comes back names
// the leaf, because a join's condition speaks of its components
// and not of "$0".
func (x *relExpander) tightenRanges(frame *relFrame,
	constraints []core.Exp,
) {
	var ranges []core.Exp
	var pats []*core.IDPat
	for _, leaf := range frame.leafOrder {
		id, isID := frame.leaves[leaf].(*core.IDPat)
		rl, isRange := leaf.(*core.RangeList)
		if isID && isRange && len(rl.Items) == 1 &&
			unboundedCollection(leaf) {
			ranges = append(ranges, leaf)
			pats = append(pats, id)
		}
	}
	if len(ranges) == 0 {
		return
	}
	augmented := slices.Clone(constraints)
	for i, leaf := range ranges {
		_, implied := impliedRangeBound(x.sys,
			&core.Scan{Pat: pats[i], Exp: leaf})
		if implied != nil {
			augmented = append(augmented, implied)
		}
	}
	var strengthened []core.Exp
	decomposeConjuncts(
		fbbtStrengthen(x.sys, pats, composeConjuncts(x.sys, augmented)),
		&strengthened)
	for i, leaf := range ranges {
		rl, _ := leaf.(*core.RangeList)
		finite, _ := x.tightenRange(rl, pats[i], strengthened, strengthened)
		if finite != nil {
			x.tightened[leaf] = finite
		}
	}
}

// strengthen deduces tighter bounds for the leaves, which
// grounding needs done first: a pair of comparisons only becomes
// a range once FBBT has turned them into one.
func (x *relExpander) strengthen(constraints []core.Exp,
	extents []relGroundStep,
) []core.Exp {
	if len(constraints) == 0 {
		return constraints
	}
	var unbounded []*core.IDPat
	for _, extent := range extents {
		unbounded = append(unbounded, core.PatIDs(extent.pat)...)
	}
	var out []core.Exp
	decomposeConjuncts(
		fbbtStrengthen(x.sys, unbounded, composeConjuncts(x.sys, constraints)),
		&out)
	return out
}

// project projects a generator's collection down to one leaf's
// element.
func (x *relExpander) project(g *generator, pat *core.IDPat,
	bound map[*core.IDPat]core.Exp,
) core.Exp {
	if g.card == infinite {
		x.fail("infinite generator for " + pat.Name)
		return nil
	}
	exp := x.replace(g.exp, bound)
	if id, isID := g.pat.(*core.IDPat); isID {
		x.names[exp] = id
		return x.dedup(g, id, exp)
	}
	row := core.NewInput(types.ElemOf(exp.Type()), 0)
	element := x.path(g.pat, row, pat)
	if element == nil {
		x.fail("generator pattern does not bind " + pat.Name)
		return nil
	}
	x.names[exp] = g.pat
	// What else the generator's pattern binds, and where each
	// stands. A name the query already bound has to be tested, or
	// the rows that disagree with it come through. A name that
	// nothing bound is the generator's own, from an "exists"
	// inside the constraint, and is projected away -- which can
	// leave the same value twice, so it is deduplicated.
	var tested []core.Exp
	projectsAway := false
	weakened := false
	for _, p := range core.PatIDs(g.pat) {
		if p == pat {
			continue
		}
		value, isBound := bound[p]
		switch {
		case isBound:
			tested = append(tested, eqExp(x.sys, x.path(g.pat, row, p), value))
		case x.leafNames[p]:
			// Another leaf of this query binds it. Reading it
			// here would need a dependent join, so the name is
			// projected away and the generator produces more rows
			// than the constraint allows -- which is no matter
			// while the filter above still tests it, and every
			// matter once a sealed generator has taken it off.
			projectsAway = true
			weakened = true
		case g.readsFree(p):
			// A name the constraint read that no leaf of this
			// query binds is bound in an enclosing scope, and a
			// row that disagrees with it is not a match.
			tested = append(tested, eqExp(x.sys, x.path(g.pat, row, p),
				&core.ID{Pat: p}))
		default:
			projectsAway = true
		}
	}
	if weakened {
		// What this generator was thought to enforce, it does not.
		x.subsumed = map[core.Exp]bool{}
		x.simplified = map[core.Exp]core.Exp{}
	}
	if len(tested) == 0 && !projectsAway && relDestructurable(g.pat) &&
		len(g.conds) == 0 {
		// Nothing to test and nothing to drop, and the pattern
		// cannot fail, so reading the name out of each row says
		// it all.
		return x.projectOf(exp, element)
	}
	// Otherwise scan the pattern, which also filters where it can
	// fail, and read the name out.
	scanned := x.scanned(exp, g)
	if len(tested) > 0 {
		scanned = x.filterOf(scanned, composeConjuncts(x.sys, tested))
	}
	out := x.projectOf(scanned, element)
	if x.dedupObservable && (projectsAway || !g.unique) {
		return x.dedupNamed(out, pat)
	}
	return out
}

// dedup deduplicates a generator's collection where its
// duplicates would be observable.
func (x *relExpander) dedup(g *generator, pat *core.IDPat,
	collection core.Exp,
) core.Exp {
	if g.unique || !x.dedupObservable {
		return collection
	}
	return x.dedupNamed(collection, pat)
}

// bound returns the collection that bounds a leaf that stands
// alone rather than under a join.
func (x *relExpander) bound(leaf core.Exp, conditions []core.Exp) core.Exp {
	mark, leafMark := x.nextName, x.nextLeafPat
	if out := x.bound2(leaf, conditions, false); out != nil && !x.failed {
		return out
	}
	reason := x.reason
	x.reset(mark, leafMark)
	if out := x.bound2(leaf, conditions, true); out != nil && !x.failed {
		return out
	}
	x.failed = true
	x.reason = reason
	return nil
}

func (x *relExpander) bound2(leaf core.Exp, conditions []core.Exp,
	destructure bool,
) core.Exp {
	x.destructure = destructure
	pat := x.elementPat(leaf)
	element := x.patExp(pat)
	originals := map[core.Exp]core.Exp{}
	constraints := make([]core.Exp, 0, len(conditions))
	for _, condition := range conditions {
		constraint := x.subst(condition, element, nil)
		originals[constraint] = condition
		constraints = append(constraints, constraint)
	}
	extents := []relGroundStep{{pat: pat, exp: leaf}}
	ungrounded := map[*core.IDPat]bool{}
	for _, id := range core.PatIDs(pat) {
		ungrounded[id] = true
	}
	strengthened := x.strengthen(constraints, extents)
	order := make([]relGroundStep, 0, len(strengthened)+1)
	order = append(order, relGroundStep{pat: pat, exp: leaf})
	for _, c := range strengthened {
		order = append(order, relGroundStep{exp: c})
	}
	cache := x.runEngine(order, ungrounded)
	x.recordSubsumed(pat, cache, originals)
	x.leafNames = map[*core.IDPat]bool{}
	for _, id := range core.PatIDs(pat) {
		x.leafNames[id] = true
	}
	if !isInfiniteExtent(leaf) {
		// A finite extent needs no generator; it is a finite
		// collection like any other. It keeps its name for the
		// lowering.
		x.names[leaf] = pat
		return leaf
	}
	return x.bounded(pat, cache, map[*core.IDPat]core.Exp{})
}

// correlatedPats adds to pats the names of every leaf that reads
// one of them, directly or through the binder of a dependent
// join, and so cannot run until that name has a generator.
func (x *relExpander) correlatedPats(node core.Exp, frame *relFrame,
	binders map[*core.IDPat]core.Exp, pats map[*core.IDPat]bool,
) {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := node.(type) {
	case *core.Filter:
		x.correlatedPats(e.Input, frame, binders, pats)
	case *core.Join:
		x.correlatedPats(e.Left, frame, binders, pats)
		binders2 := maps.Clone(binders)
		if binders2 == nil {
			binders2 = map[*core.IDPat]core.Exp{}
		}
		if e.Binder != nil {
			binders2[e.Binder] = frame.elements[e.Left]
		}
		x.correlatedPats(e.Right, frame, binders2, pats)
	default:
		pat, named := frame.leaves[node]
		if !named || extentOf(node) != nil {
			return
		}
		for _, read := range relFreePats(node) {
			correlated := false
			if element, isBound := binders[read]; isBound {
				for _, p := range relFreePats(element) {
					if pats[p] {
						correlated = true
					}
				}
			} else {
				correlated = pats[read]
			}
			if correlated {
				for _, id := range core.PatIDs(pat) {
					pats[id] = true
				}
				return
			}
		}
	}
}

// recordSubsumed remembers the conditions that a sealed generator
// enforces, so that the filter they came from can drop them, and
// what each generator makes of the conditions it was given.
func (x *relExpander) recordSubsumed(pat core.Pat, cache *relCache,
	originals map[core.Exp]core.Exp,
) {
	for _, name := range core.PatIDs(pat) {
		g := cache.best(name)
		if g == nil {
			continue
		}
		if g.sealed {
			for constraint := range g.provenance {
				if original, ok := originals[constraint]; ok {
					x.subsumed[original] = true
				}
			}
		}
		for constraint, original := range originals {
			was := original
			if s, ok := x.simplified[original]; ok {
				was = s
			}
			now := x.simplifyBy(g, constraint)
			if now != constraint {
				x.simplified[original] = now
			} else if was != original {
				x.simplified[original] = was
			}
		}
	}
}

// simplifyBy is what a generator makes of a condition: true where
// the generator enforces it entirely, and the condition itself
// otherwise.
func (x *relExpander) simplifyBy(g *generator, constraint core.Exp) core.Exp {
	if g.sealed && g.provenance[constraint] {
		return trueLiteral(x.sys)
	}
	return constraint
}

// path returns the expression that reads a pattern's binder out
// of an element, or nil if the pattern does not bind it.
func (x *relExpander) path(pat core.Pat, element core.Exp,
	target *core.IDPat,
) core.Exp {
	// lint: sort until '^\t}' where '^\tcase '
	switch p := pat.(type) {
	case *core.AsPat:
		if p.Pat == target {
			return element
		}
		return x.path(p.Body, element, target)
	case *core.IDPat:
		if p == target {
			return element
		}
		return nil
	case *core.TuplePat:
		for i, arg := range p.Args {
			exp := x.path(arg, fieldAt(x.sys, element, i), target)
			if exp != nil {
				return exp
			}
		}
		return nil
	default:
		return nil
	}
}

// elementPat creates the pattern that the engine keys on for a
// leaf's element: the one the query wrote, where the caller handed
// it over, and otherwise a fresh one.
func (x *relExpander) elementPat(leaf core.Exp) core.Pat {
	elem := types.ElemOf(leaf.Type())
	if !x.destructure && x.nextLeafPat < len(x.leafPats) {
		pat := x.leafPats[x.nextLeafPat]
		x.nextLeafPat++
		if pat.Type() == elem {
			return pat
		}
		// The walk and the scans disagree about which leaf is
		// which, so the rest of the names are not to be trusted
		// either.
		x.leafPats = nil
	}
	return x.pat(elem)
}

// pat creates a pattern for a type: a tuple of variables where a
// tuple-typed leaf is to be destructured, otherwise one variable.
func (x *relExpander) pat(t types.Type) core.Pat {
	if tuple, isTuple := types.Unalias(t).(*types.Tuple); isTuple &&
		x.destructure {
		args := make([]core.Pat, len(tuple.Args))
		for i, argT := range tuple.Args {
			args[i] = x.pat(argT)
		}
		return &core.TuplePat{T: t, Args: args}
	}
	return x.groundPat(t)
}

// groundPat creates a binder for a leaf's element.
//
// The name comes from the counter the nested trees' binders use,
// so that no two binders in a statement share a name: what a plan
// prints is renumbered by first appearance, but the compiler
// resolves a binder by name.
func (x *relExpander) groundPat(t types.Type) *core.IDPat {
	x.nextName++
	nestedNext++
	return &core.IDPat{T: t, Name: "v$" + itoa(nestedNext-1)}
}

// collect names the leaves of a join tree and works out what its
// element is in terms of those names.
func (x *relExpander) collect(node core.Exp) *relFrame {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := node.(type) {
	case *core.Filter:
		// A filter between two joins constrains the leaves below
		// it just as a "where" between two scans constrains the
		// patterns before it.
		input := x.collect(e.Input)
		var conjuncts []core.Exp
		decomposeConjuncts(e.Condition, &conjuncts)
		for _, conjunct := range conjuncts {
			constraint := x.subst(conjunct, input.element, nil)
			input.originals[constraint] = conjunct
			input.constraints = append(input.constraints, constraint)
			input.order = append(input.order, relGroundStep{exp: constraint})
		}
		input.elements[node] = input.element
		return input
	case *core.Join:
		left := x.collect(e.Left)
		right := x.collect(e.Right)
		frame := &relFrame{
			leaves:    map[core.Exp]core.Pat{},
			originals: map[core.Exp]core.Exp{},
			elements:  map[core.Exp]core.Exp{},
		}
		maps.Copy(frame.leaves, left.leaves)
		maps.Copy(frame.leaves, right.leaves)
		frame.leafOrder = slices.Concat(left.leafOrder, right.leafOrder)
		frame.constraints = slices.Concat(left.constraints, right.constraints)
		if !isBoolLiteral(e.Condition, true) {
			frame.constraints = append(frame.constraints,
				x.subst(e.Condition, left.element, right.element))
		}
		components := slices.Concat(
			x.components(e.Left, left.element),
			x.components(e.Right, right.element))
		frame.element = x.tupleOf(components)
		maps.Copy(frame.elements, left.elements)
		maps.Copy(frame.elements, right.elements)
		frame.elements[node] = frame.element
		maps.Copy(frame.originals, left.originals)
		maps.Copy(frame.originals, right.originals)
		frame.order = slices.Concat(left.order, right.order)
		if !isBoolLiteral(e.Condition, true) {
			frame.order = append(frame.order, relGroundStep{
				exp: frame.constraints[len(frame.constraints)-1],
			})
		}
		return frame
	default:
		pat := x.elementPat(node)
		frame := &relFrame{
			element:   x.patExp(pat),
			leaves:    map[core.Exp]core.Pat{node: pat},
			leafOrder: []core.Exp{node},
			originals: map[core.Exp]core.Exp{},
			elements:  map[core.Exp]core.Exp{},
		}
		frame.elements[node] = frame.element
		if extentOf(node) != nil {
			frame.order = append(frame.order,
				relGroundStep{pat: pat, exp: node})
		}
		return frame
	}
}

// components are the expressions a node's element is made of,
// given the expression that denotes the element: a join's are its
// two inputs' components, concatenated, and anything else's is
// the element itself. A node that preserves its input's element
// -- a filter, a sort -- has its input's.
func (x *relExpander) components(node, element core.Exp) []core.Exp {
	if input := relElementPreserved(node); input != nil {
		return x.components(input, element)
	}
	join, isJoin := node.(*core.Join)
	if !isJoin {
		return []core.Exp{element}
	}
	n := relComponentCount(join)
	if tuple, isTuple := element.(*core.Tuple); isTuple &&
		len(tuple.Args) == n {
		return tuple.Args
	}
	out := make([]core.Exp, n)
	for i := range out {
		out[i] = fieldAt(x.sys, element, i)
	}
	return out
}

// relElementPreserved is the input whose element a node passes
// through unchanged, or nil.
func relElementPreserved(node core.Exp) core.Exp {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := node.(type) {
	case *core.Filter:
		return e.Input
	case *core.Skip:
		return e.Input
	case *core.Sort:
		return e.Input
	case *core.Take:
		return e.Input
	case *core.Unorder:
		return e.Input
	default:
		return nil
	}
}

// relComponentCount is how many components a node's element has:
// a join's inputs' counts added, and one for anything else.
func relComponentCount(node core.Exp) int {
	if input := relElementPreserved(node); input != nil {
		return relComponentCount(input)
	}
	if join, isJoin := node.(*core.Join); isJoin {
		return relComponentCount(join.Left) + relComponentCount(join.Right)
	}
	return 1
}

// patExp returns the expression that a pattern's variables
// denote.
func (x *relExpander) patExp(pat core.Pat) core.Exp {
	if tuple, isTuple := pat.(*core.TuplePat); isTuple {
		args := make([]core.Exp, len(tuple.Args))
		for i, arg := range tuple.Args {
			args[i] = x.patExp(arg)
		}
		return &core.Tuple{T: tuple.T, Args: args}
	}
	id, _ := pat.(*core.IDPat)
	return &core.ID{Pat: id}
}

// free returns the names a generator reads that something else
// must bind: the names of this query's leaves, less those already
// bound and those its own scan binds. Everything else a generator
// mentions -- a global, a function, a constructor -- the
// environment binds already.
func (x *relExpander) free(g *generator,
	bound map[*core.IDPat]core.Exp,
) []*core.IDPat {
	var free []*core.IDPat
	for _, pat := range g.freePats {
		if _, isBound := bound[pat]; isBound || !x.leafNames[pat] ||
			slices.Contains(g.freshPats, pat) {
			continue
		}
		free = append(free, pat)
	}
	return free
}

// recordOrAtomPat is the pattern that a record of variables, or
// the one variable, is read under.
func (x *relExpander) recordOrAtomPat(vars []*core.IDPat) core.Pat {
	if len(vars) == 1 {
		return vars[0]
	}
	sorted := slices.Clone(vars)
	slices.SortFunc(sorted, func(a, b *core.IDPat) int {
		return strings.Compare(a.Name, b.Name)
	})
	fields := make([]types.Field, len(sorted))
	args := make([]core.Pat, len(sorted))
	for i, v := range sorted {
		fields[i] = types.Field{Label: v.Name, Type: v.T}
		args[i] = v
	}
	return &core.TuplePat{T: x.sys.Record(fields), Args: args}
}

// relDestructurable reports whether a pattern cannot fail to
// match: a variable, a wildcard, or a tuple of those.
func relDestructurable(pat core.Pat) bool {
	// lint: sort until '^\t}' where '^\tcase '
	switch p := pat.(type) {
	case *core.AsPat:
		return relDestructurable(p.Body)
	case *core.IDPat:
		return true
	case *core.TuplePat:
		for _, arg := range p.Args {
			if !relDestructurable(arg) {
				return false
			}
		}
		return true
	case *core.WildcardPat:
		return true
	default:
		return false
	}
}

// subst replaces "$0" and "$1", the elements of a node's inputs,
// with expressions, and folds the selectors that then read a
// component off a tuple that holds it.
//
// The walk stops at a nested node, whose "$0" is its own input's
// element. What the enclosing node's element is called inside a
// nested tree is a binder the resolver made for it, and once the
// element is an ordinary name that binder has nothing left to
// protect: the binding is dropped, so that the engine reads
// "Relational.nonEmpty (...)" where it would otherwise read a
// "let".
func (x *relExpander) subst(exp, e0, e1 core.Exp) core.Exp {
	r := &rewriter{sys: x.sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch e := e.(type) {
		case *core.Input:
			if e.Ordinal == 0 {
				return e0, true
			}
			if e.Ordinal == 1 && e1 != nil {
				return e1, true
			}
			return e, true
		case core.Rel:
			return e, true
		default:
			return nil, false
		}
	}
	// Unbinding a row binder puts a tuple under the selectors that
	// read it, so the fold comes after.
	return relFoldSelectors(x.sys, relUnbindRow(x.sys, r.rewriteExp(exp)))
}

// relFoldSelectors reads a component off the tuple that holds it:
// "#1 (a, b)" is "a".
func relFoldSelectors(sys *types.System, exp core.Exp) core.Exp {
	r := &rewriter{sys: sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		apply, isApply := e.(*core.Apply)
		if !isApply {
			return nil, false
		}
		sel, isSel := apply.Fn.(*core.Selector)
		tuple, isTuple := apply.Arg.(*core.Tuple)
		if !isSel || !isTuple || sel.Index >= len(tuple.Args) {
			return nil, false
		}
		return r.rewriteExp(tuple.Args[sel.Index]), true
	}
	return r.rewriteExp(exp)
}

// relUnbindRow replaces each binder that held a node's element for
// a nested tree with the value it is bound to, and drops the
// binding, where the value no longer reads an input. A binding
// whose value still holds a "$0" belongs to a tree nested in this
// one's expressions, and is left alone.
func relUnbindRow(sys *types.System, exp core.Exp) core.Exp {
	r := &rewriter{sys: sys}
	values := map[*core.IDPat]core.Exp{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch e := e.(type) {
		case *core.ID:
			if value, ok := values[e.Pat]; ok {
				return value, true
			}
			return nil, false
		case *core.Let:
			decl, isVal := e.Decl.(*core.NonRecValDecl)
			if !isVal {
				return nil, false
			}
			pat, isID := decl.Pat.(*core.IDPat)
			if !isID || !strings.HasPrefix(pat.Name, "v$") ||
				mentionsInput(decl.Exp) {
				return nil, false
			}
			values[pat] = r.rewriteExp(decl.Exp)
			return r.rewriteExp(e.Exp), true
		default:
			return nil, false
		}
	}
	return r.rewriteExp(exp)
}

// relDebugf prints a line of the grounder's working, where the
// environment asks for it.
func relDebugf(format string, args ...any) {
	if os.Getenv("MOREL_REL_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

// relDebugExp renders an expression for relDebugf.
func relDebugExp(sys *types.System, e core.Exp) string {
	u := &unparser{sys: sys, seen: map[string][]*core.IDPat{}}
	u.exp(e, 0, 0)
	return u.render()
}
