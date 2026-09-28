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
	"strconv"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/token"
	"github.com/hydromatic/morel-go/internal/types"
)

// Ground rewrites every query containing an unbounded variable —
// a scan over an infinite extent — by inverting the predicates
// that constrain the variable into a finite generator of its
// values. A used variable that no predicate grounds is an error.
// Queries inside recursive function bodies are left alone: they
// are the function's logic, and the queries that call the
// function handle them. The declaration is returned unchanged
// (the same pointer) when nothing needed rewriting.
func Ground(decl core.Decl, sys *types.System,
	recFns map[string]*core.Fn,
) (core.Decl, error) {
	if _, rec := decl.(*core.RecValDecl); rec {
		return decl, nil
	}
	g := &grounder{recFns: recFns}
	g.sys = sys
	if g.recFns == nil {
		g.recFns = map[string]*core.Fn{}
	}
	g.exp = g.visit
	decl2 := g.rewriteDecl(decl)
	if g.err != nil {
		return nil, g.err
	}
	return decl2, nil
}

// Replan re-runs the optimization pipeline over a statement's
// resolved declaration up to a numbered pass, for Sys.planEx. A
// phase that is not an integer returns the declaration as
// resolved; "0" the limited inlining pass; "2" onward the state
// after each changing inlining pass; anything past the last
// changing pass — "-1" conventionally — the final form, grounded.
func Replan(decl core.Decl, env *InlineEnv, sys *types.System,
	recFns map[string]*core.Fn, passCount int, phase string,
) core.Decl {
	target, err := strconv.Atoi(phase)
	if err != nil {
		// Not a number -- "~1", as the corpus writes it -- so the
		// declaration is returned as it was resolved, before any
		// pass. Its built-in references are still identifiers.
		return decl
	}
	if target == 0 || passCount <= 0 {
		return resolveBuiltins(sys,
			newPass(sys, decl, env, true).rewriteDecl(decl))
	}
	cur := decl
	for i := range passCount {
		next := newPass(sys, cur, env, false).rewriteDecl(cur)
		if next == cur {
			break
		}
		cur = next
		if i+2 == target {
			return resolveBuiltins(sys, cur)
		}
	}
	// The inlining passes converged before the phase asked for,
	// so what remains is the final form: grounded, as morel-java's
	// replan grounds once the passes are done, with no phase of
	// its own.
	for i := range passCount {
		if i > 0 && !ContainsUnbounded(cur) {
			break
		}
		next, gerr := Ground(cur, sys, recFns)
		if gerr != nil || next == cur {
			break
		}
		cur = next
	}
	return resolveBuiltins(sys, cur)
}

// visitLet walks a "let". The functions it binds are recorded, so
// that grounding can see through a call to one of them; a
// recursive binding's body is the function's own logic and is not
// expanded, the let's body is.
func (g *grounder) visitLet(e *core.Let) (core.Exp, bool) {
	var binds []*core.NonRecValDecl
	skipDecl := false
	switch d := e.Decl.(type) {
	case *core.NonRecValDecl:
		binds = []*core.NonRecValDecl{d}
	case *core.RecValDecl:
		// The recursive bindings' bodies are the functions'
		// own logic, not expanded; the let's body is.
		binds = d.Binds
		skipDecl = true
	}
	saved := g.recFns
	extended := false
	for _, b := range binds {
		pat, okPat := b.Pat.(*core.IDPat)
		fn, okFn := b.Exp.(*core.Fn)
		if !okPat || !okFn {
			continue
		}
		if !extended {
			g.recFns = maps.Clone(saved)
			if g.recFns == nil {
				g.recFns = map[string]*core.Fn{}
			}
			extended = true
		}
		g.recFns[pat.Name] = fn
	}
	if !extended && !skipDecl {
		return nil, false
	}
	decl := e.Decl
	if !skipDecl {
		decl = g.rewriteDecl(e.Decl)
	}
	body := g.rewriteExp(e.Exp)
	g.recFns = saved
	if decl == e.Decl && body == e.Exp {
		return e, true
	}
	return &core.Let{Decl: decl, Exp: body}, true
}

// groundQuery grounds a query as a tree: translated, its leaves
// bounded, and left a tree for the compiler to lower. A query the
// translation declines is grounded as the step list it is.
func (g *grounder) groundQuery(e *core.From) (core.Exp, bool) {
	if e.Ordinal != nil {
		return g.groundCounting(e)
	}
	if e.Kind != ast.FromOp {
		// A quantifier's value is a boolean: its tree stands under
		// "Relational.nonEmpty", negated for "forall", and is
		// grounded with its rows counted only.
		if tree, ok := nestedTree(g.sys, e); ok {
			out := g.rewriteExp(tree)
			if g.err != nil {
				g.declined(e)
			}
			return out, true
		}
		return g.groundStepList(e)
	}
	tree, _ := TranslateFrom(g.sys, e)
	if tree == nil {
		return g.groundStepList(e)
	}
	if rel, isRel := tree.(core.Rel); isRel {
		out, done := g.groundRel(rel)
		if g.err != nil {
			g.declined(e)
		}
		return out, done
	}
	if containsRel(tree) {
		// Not a tree but holding one -- "f (from ...)", which an
		// "into" or a "through" makes -- whose trees are grounded
		// each as a root of its own.
		return g.rewriteExp(tree), true
	}
	// A query that is a leaf alone has nothing to bound but the
	// leaf; the query stays, because what it scans may be a foreign
	// relation, which is a query's to read and prints as one.
	if !unboundedCollection(tree) {
		return nil, false
	}
	return g.groundStepList(e)
}

// declined reports, under MOREL_REL_DEBUG, that the tree's grounder
// could not ground a query, and why; the error stands.
func (g *grounder) declined(e *core.From) {
	if os.Getenv("MOREL_REL_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "tree grounding declined: %s <= %s\n",
			RelGroundDecline, relDebugQuery(g.sys, e))
	}
}

// relDebugWidth is the line width debug output is laid out to.
const relDebugWidth = 79

// relDebugQuery is a query as source, for a debug message.
func relDebugQuery(sys *types.System, e *core.From) string {
	return UnparseDecl(sys, &core.NonRecValDecl{
		Pat: &core.IDPat{T: e.T, Name: "q"}, Exp: e,
	})
}

// groundCounting grounds a query that counts its rows. The tree
// has nowhere to hold the counter, so the query is grounded on its
// tree and lowered back to the step list it was, which counts its
// rows as before; a query with nothing unbounded stays as it is.
func (g *grounder) groundCounting(e *core.From) (core.Exp, bool) {
	tree, _ := TranslateFrom(g.sys, e)
	rel, isRel := tree.(core.Rel)
	if !isRel || !relContainsUnbounded(rel) {
		return g.groundStepList(e)
	}
	out, _ := RelGroundRewrite(g.sys, g.recFns, rel, !g.countedOnly,
		RelLeafPats(rel))
	if out == nil {
		g.err = relNotGrounded(rel)
		g.declined(e)
		return e, true
	}
	lowered, _ := LowerRel(g.sys, out)
	from, isFrom := lowered.(*core.From)
	if !isFrom {
		return g.groundStepList(e)
	}
	from.Ordinal = e.Ordinal
	return from, true
}

// groundStepList is the fate of a query that has no tree to ground
// on: one whose translation declined, or one that counts its rows.
// Nothing grounds it, so it is an error where it has an unbounded
// scan, and stays as it is otherwise.
func (g *grounder) groundStepList(e *core.From) (core.Exp, bool) {
	err := stepListNotGrounded(e)
	if err != nil {
		g.err = err
		return e, true
	}
	return nil, false
}

// stepListNotGrounded is the error for a query with a scan that
// nothing bounds, or nil where every scan is bounded. Where the
// query is a single step binding a single variable it has
// simplified to that variable's extent alone, and there is no
// pattern left to name, so the message names the type instead --
// as morel-java's does. Any other query still has a scan whose
// pattern can be named.
func stepListNotGrounded(from *core.From) error {
	vars := 0
	var unbounded *core.Scan
	for _, step := range from.Steps {
		scan, isScan := step.(*core.Scan)
		if !isScan {
			continue
		}
		vars += len(core.PatIDs(scan.Pat))
		if unbounded == nil && unboundedCollection(scan.Exp) {
			unbounded = scan
		}
	}
	if unbounded == nil {
		return nil
	}
	pats := core.PatIDs(unbounded.Pat)
	span := token.Span{}
	if apply, isApply := unbounded.Exp.(*core.Apply); isApply {
		span = apply.Span
	}
	if len(pats) == 0 {
		return &Error{Span: span, Msg: "query is not grounded"}
	}
	msg := fmt.Sprintf("pattern '%s' is not grounded", pats[0].Name)
	if len(from.Steps) == 1 && vars == 1 {
		msg = fmt.Sprintf(
			"cannot enumerate all values of type '%s'", pats[0].T)
	}
	return &Error{Span: span, Msg: msg}
}

// groundRel grounds a tree in place, and leaves it a tree.
//
// That is the whole of the difference from the step list's path,
// and it is what a plan shows: morel-go's grounding lowers, and
// then wraps a bare collection in a "from" so the compiler has a
// query, where morel-java's tree survives to a compiler that
// lowers at its own boundary -- so `from i where i > 0 andalso
// i < 10` prints as `from i in #flatten Range (...)` here and as
// the range alone there. Grounding that leaves the tree alone
// does not add the wrapper.
//
// A tree the rewrite declines is an error, where a step list
// would have fallen back to the other engine. There is no other
// engine to fall back to once the resolver returns a tree, so a
// leaf no generator bounds is "not grounded" -- which is what the
// step list's engine says about the same query.
func (g *grounder) groundRel(rel core.Rel) (core.Exp, bool) {
	if !relContainsUnbounded(rel) {
		// Nothing to ground; the trees nested in its expressions
		// are roots of their own.
		if g.done == nil {
			g.done = map[core.Exp]bool{}
		}
		markRels(rel, g.done)
		return g.rewriteExp(rel), true
	}
	out, _ := RelGroundRewrite(g.sys, g.recFns, rel, !g.countedOnly,
		RelLeafPats(rel))
	if out == nil {
		g.err = relNotGrounded(rel)
		return rel, true
	}
	if os.Getenv("MOREL_REL_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "grounded tree:\n%s\n",
			RelPlanDecl(g.sys, &core.NonRecValDecl{
				Pat: &core.IDPat{T: out.Type(), Name: "q"}, Exp: out,
			}, relDebugWidth, false))
	}
	// The tree is grounded; what remains is the trees nested in
	// its expressions, each a root of its own, which the walk
	// below reaches once the nodes of this one are marked as done.
	if g.done == nil {
		g.done = map[core.Exp]bool{}
	}
	markRels(out, g.done)
	return g.rewriteExp(out), true
}

// isCountingCall reports whether an application only counts the
// rows of its argument: "Relational.nonEmpty" or "Relational.empty".
func isCountingCall(e *core.Apply) bool {
	id, isID := e.Fn.(*core.ID)
	if !isID {
		return false
	}
	switch id.Pat.Name {
	case relEmptyName, relNonEmptyName, emptyName, nonEmptyName:
		return true
	default:
		return false
	}
}

// markRels records every node of a tree, through its inputs, as
// grounded.
func markRels(exp core.Exp, done map[core.Exp]bool) {
	rel, isRel := exp.(core.Rel)
	if !isRel {
		return
	}
	done[rel] = true
	for _, input := range rel.Inputs() {
		markRels(input, done)
	}
}

// relNotGrounded is the error for a tree with a leaf that nothing
// bounds: morel-java's "RelExpander.ungrounded", plus the leaf's
// position, which morel-go's leaves carry and java's throw away.
//
// The name is RelLeafPats read back, so a query with one binder
// has no projection and so no name -- and the message then says
// what it can. The step list's engine names it, because a step
// list still has the scan; this is what a tree costs a
// diagnostic, and morel-java pays it too.
func relNotGrounded(rel core.Rel) error {
	pats := RelLeafPats(rel)
	var leaves []core.Exp
	relAllLeaves(rel, &leaves)
	for i, leaf := range leaves {
		if !unboundedCollection(leaf) {
			continue
		}
		span := token.Span{}
		if apply, isApply := leaf.(*core.Apply); isApply {
			span = apply.Span
		}
		if i < len(pats) {
			return &Error{
				Span: span,
				Msg: "pattern '" + pats[i].Name +
					"' is not grounded",
			}
		}
		return &Error{
			Span: span,
			Msg: "cannot enumerate all values of type '" +
				types.ElemOf(leaf.Type()).String() + "'",
		}
	}
	return &Error{Msg: "query is not grounded"}
}

// relContainsUnbounded reports whether a tree has a leaf that
// grounding must bound: an infinite extent, or a range with an
// open end.
func relContainsUnbounded(tree core.Exp) bool {
	var leaves []core.Exp
	relAllLeaves(tree, &leaves)
	return slices.ContainsFunc(leaves, unboundedCollection)
}

// relAllLeaves collects a tree's leaves, left to right, through
// every node.
func relAllLeaves(exp core.Exp, leaves *[]core.Exp) {
	rel, isRel := exp.(core.Rel)
	if !isRel {
		*leaves = append(*leaves, exp)
		return
	}
	for _, in := range rel.Inputs() {
		relAllLeaves(in, leaves)
	}
}

// grounder walks a declaration expanding each query, outermost
// first; an expanded query's subqueries are then visited within
// the rewritten form.
type grounder struct {
	// The rewriter's own "sys" is the one to set: it rebuilds a
	// relational node through the constructors, which derive the
	// node's type. A field here would shadow it, and the rewriter
	// would dereference nil the first time a tree reached it.
	rewriter

	recFns map[string]*core.Fn
	err    error
	// done are the nodes of the trees grounded so far, so that a
	// walk into a grounded tree's expressions does not ground it
	// again.
	done map[core.Exp]bool
	// countedOnly is set while walking under "Relational.nonEmpty"
	// or "Relational.empty", where a query's rows are only counted:
	// a duplicate row changes nothing, and grounding need not take
	// the rows distinct.
	countedOnly bool
}

// visit intercepts queries (to expand them) and recursive
// declarations (to leave their bodies alone).
func (g *grounder) visit(e core.Exp) (core.Exp, bool) {
	if g.err != nil {
		return e, true
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		if g.countedOnly || !isCountingCall(e) {
			return nil, false
		}
		g.countedOnly = true
		arg := g.rewriteExp(e.Arg)
		g.countedOnly = false
		if arg == e.Arg {
			return e, true
		}
		return &core.Apply{T: e.T, Fn: e.Fn, Arg: arg}, true
	case *core.From:
		return g.groundQuery(e)
	case *core.Let:
		return g.visitLet(e)
	default:
		return nil, false
	case core.Rel:
		if g.done[e] {
			// Grounded already, as part of the tree above it; walk
			// into its expressions, where a nested tree is a root
			// of its own.
			return nil, false
		}
		return g.groundRel(e)
	}
}
