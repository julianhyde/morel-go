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
	"sort"
	"strconv"
	"strings"

	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/types"
)

// Plan text for a relational tree: morel#449 spec.md §6, which is
// the cross-implementation contract. Sys.planEx and Sys.planOf
// print it; Sys.plan prints the step list that executes, which is
// a different thing said in a different notation.
//
// Core expressions have one formatter, and it lays a relational
// node out in one of two ways. *Inline* mode prints a relation as
// an ordinary expression, nested like any other application, and
// is how Core reads when a plan is not what you are looking at.
// *Tree* mode is what a plan prints, and in it a relation is
// always broken out, whatever the width: layout is not a function
// of the width here, and the width governs only how a node's own
// line wraps.
//
// The invariant that makes the text parseable is that a relational
// operator is the first non-whitespace on its line. One node per
// line; a node's inputs are the lines below it, indented two;
// where a node's own line does not fit it wraps, and the
// continuation is indented four from the node, so that a reader --
// and a parser -- tells a continuation from a grandchild by
// position.

// A relation that §6.2 forbids printing in place -- inside a
// "let", a "case", the argument of "nonEmpty", a field of a record
// -- is replaced by a reference, "r$0", "r$1", ..., numbered from
// zero in the order the references were handed out. Each is then
// printed as a block of its own, after the tree and before the
// legend.
//
// A fragment declares what it reads from outside itself: the names
// in brackets after "r$N" are the variables it uses that something
// outside it binds, and the same text stands at the reference and
// at the definition. The list is *transitive*, and that is the
// point of repeating it at the reference: a fragment can be free
// in a variable it never mentions, reaching it only through a
// fragment nested inside it. It comes out transitive without
// trying, because a fragment nested inside this one is part of its
// subtree, however far away it is printed.

// relRef registers a relation that cannot print where it stands,
// and returns the reference that prints instead.
func (u *unparser) relRef(rel core.Rel) string {
	for i, r := range u.relDefs {
		if r == rel {
			return u.relHeader(i)
		}
	}
	u.relDefs = append(u.relDefs, rel)
	return u.relHeader(len(u.relDefs) - 1)
}

// relHeader is the reference for a broken-out relation, with the
// variables it reads from outside itself: "r$0[v$0, v$1]".
//
// Renaming here is safe, and not merely convenient: a parameter is
// bound by a "let" that encloses the reference, so it has been
// printed -- and therefore numbered -- before this runs.
func (u *unparser) relHeader(i int) string {
	params := u.relParams(u.relDefs[i])
	ref := "r$" + strconv.Itoa(i)
	if len(params) == 0 {
		return ref
	}
	names := make([]string, len(params))
	for j, p := range params {
		names[j] = u.printedName(p)
	}
	sort.Strings(names)
	return ref + "[" + strings.Join(names, ", ") + "]"
}

// printedName is the name a binder prints as, which for a
// generated one is its position in its prefix's sequence.
func (u *unparser) printedName(pat *core.IDPat) string {
	if prefix, isGen := genPrefix(pat.Name); isGen {
		return u.genName(pat, prefix)
	}
	return pat.Name
}

// relParams is what a relation reads from outside itself.
//
// Asked when the relation is first referred to, which is the only
// moment it needs to be known and the only moment the writer knows
// the relation is being broken out at all.
func (u *unparser) relParams(rel core.Rel) []*core.IDPat {
	if u.relParamCache == nil {
		u.relParamCache = map[core.Rel][]*core.IDPat{}
	}
	if params, ok := u.relParamCache[rel]; ok {
		return params
	}
	var params []*core.IDPat
	for _, pat := range relFreePats(rel) {
		if u.boundInPlan[pat] {
			params = append(params, pat)
		}
	}
	u.relParamCache[rel] = params
	return params
}

// relFreePats is the variables an expression reads that nothing
// inside it binds, in order of first use.
func relFreePats(exp core.Exp) []*core.IDPat {
	uses, bound := relNames(exp)
	free := uses[:0]
	for _, pat := range uses {
		if !bound[pat] {
			free = append(free, pat)
		}
	}
	return free
}

// relBoundPats is every variable an expression binds.
func relBoundPats(exp core.Exp) map[*core.IDPat]bool {
	_, bound := relNames(exp)
	return bound
}

// relNames walks an expression, collecting the variables it reads
// (in order of first use) and the variables it binds.
func relNames(exp core.Exp) ([]*core.IDPat, map[*core.IDPat]bool) {
	var uses []*core.IDPat
	seen := map[*core.IDPat]bool{}
	bound := map[*core.IDPat]bool{}
	bind := func(pat core.Pat) {
		for _, p := range core.PatIDs(pat) {
			bound[p] = true
		}
	}
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		// lint: sort until '^		}' where '^		case '
		switch e := e.(type) {
		case *core.Case:
			for _, m := range e.Matches {
				bind(m.Pat)
			}
		case *core.Fn:
			bound[e.IDPat] = true
		case *core.ID:
			if !seen[e.Pat] {
				seen[e.Pat] = true
				uses = append(uses, e.Pat)
			}
		case *core.Join:
			if e.Binder != nil {
				bound[e.Binder] = true
			}
		case *core.Let:
			bindDecl(e.Decl, bind)
		}
		return nil, false
	}
	r.rewriteExp(exp)
	return uses, bound
}

// bindDecl records what a declaration binds.
func bindDecl(d core.Decl, bind func(core.Pat)) {
	switch d := d.(type) {
	case *core.NonRecValDecl:
		bind(d.Pat)
	case *core.RecValDecl:
		for _, b := range d.Binds {
			bind(b.Pat)
		}
	}
}

// relIndent is how far a node's inputs are indented from it.
const relIndent = 2

// relContinue is how far a wrapped node line is indented from the
// node. Two deeper than an input, so a continuation cannot be
// read as a child.
const relContinue = 4

// relMaxTypeLength is the longest type moniker a plan prints in
// full. A longer one is replaced by a reference and printed once
// in a legend.
//
// A character count rather than a rule about the type's shape,
// because three implementations must agree on which types are
// abbreviated and they already agree on the moniker's text; a rule
// phrased over records and tuples would abbreviate "(int * int)
// list", which is short and reads better in full. The threshold
// sits above "int option list" (15) and "{a:int, b:int} list"
// (19), and below a three-field record (41).
const relMaxTypeLength = 24

// RelPlan renders a relational tree as plan text, laid out within
// a width, with the collection type of every node where withTypes.
//
// The whole text is written by one writer. Two of its three
// numbering sequences -- the generated binders, and the type
// legend -- are properties of the *text* rather than of any node,
// so a tree nested in another tree's expressions shares the
// enclosing text's numbering and one legend covers the whole plan.
// An implementation that built the text by concatenating strings
// its children returned would get both wrong, and get them wrong
// silently.
func RelPlan(sys *types.System, exp core.Exp, width int,
	withTypes bool,
) string {
	u := &unparser{
		sys:         sys,
		seen:        map[string][]*core.IDPat{},
		width:       width,
		treeMode:    true,
		boundInPlan: relBoundPats(exp),
	}
	u.relTree(exp, 0, withTypes)
	// The list grows while it is walked, because a block may
	// itself hold a relation that has to be broken out. Index, do
	// not iterate, and re-read the length each time round.
	for i := 0; ; i++ {
		if i >= len(u.relDefs) {
			break
		}
		rel := u.relDefs[i]
		u.hardBreak()
		u.put(u.relHeader(i) + " =")
		u.hardBreak()
		u.relTree(rel, relIndent, withTypes)
	}
	// Every line ends in a newline, the last one included, and the
	// legend follows after a blank line. A plan is a block of
	// lines, not a value with a last line, and morel-java's text
	// -- which is the contract -- is written that way.
	text := u.render()
	if legend := u.typeLegend(); legend != "" {
		text += "\n" + legend
	}
	return text
}

// RelPlanDecl renders a declaration as Sys.planEx prints it: the
// ordinary one-line form where its value is not a query, and
// "val it =" followed by the tree where it is.
//
// A relational operator is the first non-whitespace on its line
// (§6.2), so a tree cannot follow an "=" on the same line.
// Otherwise every plan of a query would begin "val it = r$0" and
// its one interesting line would be an indirection.
func RelPlanDecl(sys *types.System, decl core.Decl,
	width int,
) string {
	d, isVal := decl.(*core.NonRecValDecl)
	if !isVal {
		return UnparseDecl(sys, decl)
	}
	tree := d.Exp
	if _, isRel := tree.(core.Rel); !isRel {
		return UnparseDecl(sys, decl)
	}
	u := &unparser{
		sys:         sys,
		seen:        map[string][]*core.IDPat{},
		width:       width,
		treeMode:    true,
		boundInPlan: relBoundPats(tree),
	}
	u.put("val ")
	u.pat(d.Pat)
	u.put(" =")
	u.hardBreak()
	u.relTree(tree, relIndent, true)
	for i := 0; ; i++ {
		if i >= len(u.relDefs) {
			break
		}
		rel := u.relDefs[i]
		u.hardBreak()
		u.put(u.relHeader(i) + " =")
		u.hardBreak()
		u.relTree(rel, relIndent, true)
	}
	text := u.render()
	if legend := u.typeLegend(); legend != "" {
		text += "\n" + legend
	}
	return strings.TrimSuffix(text, "\n")
}

// relTree writes a node and the lines below it: a node prints as a
// node, and any other expression as a leaf line.
func (u *unparser) relTree(exp core.Exp, indent int,
	withTypes bool,
) {
	rel, isRel := exp.(core.Rel)
	if !isRel {
		// A leaf is a node line too, and wraps the same way.
		u.relLine(indent, withTypes, exp.Type(), func() {
			u.exp(exp, 0, 0)
		})
		return
	}
	u.relLine(indent, withTypes, rel.Type(), func() {
		u.put(rel.OpName())
		u.relArgs(rel)
	})
	for _, input := range rel.Inputs() {
		u.relTree(input, indent+relIndent, withTypes)
	}
}

// relLine writes one line of a plan: the indent, then whatever the
// body writes, then the type, then the break to the next line.
//
// The body is a group indented four from the node, so that a line
// that does not fit wraps there. Continuations follow the node
// line immediately, so the run of lines at four belongs to the
// node above them; a line at four *after* a line at two is a
// grandchild.
func (u *unparser) relLine(indent int, withTypes bool,
	t types.Type, body func(),
) {
	u.put(strings.Repeat(" ", indent))
	u.startGroup(indent + relContinue)
	body()
	if withTypes {
		// A continuation line carries no type; the type belongs to
		// the node, and the node is the line it starts on.
		u.put(" : " + u.typeRef(t))
	}
	u.endGroup()
	u.hardBreak()
}

// relArgs writes a node's arguments, each in brackets, in the
// order spec §3 lists them. An argument that carries no
// information -- an inner join's kind, a condition that is true, a
// projection that is "$0" -- is omitted.
func (u *unparser) relArgs(rel core.Rel) {
	// lint: sort until '^\t}' where '^\tcase '
	switch r := rel.(type) {
	case *core.Filter:
		u.relArg(func() { u.exp(r.Condition, 0, 0) })
	case *core.Group:
		u.relGroupArgs(r)
	case *core.Join:
		if name := r.Kind.OpName(); name != "" {
			u.relArg(func() { u.put(name) })
		}
		if r.Binder != nil {
			u.relArg(func() { u.name(r.Binder) })
		}
		if !isBoolLiteral(r.Condition, true) {
			u.relArg(func() { u.exp(r.Condition, 0, 0) })
		}
	case *core.Project:
		if isInput0(r.Exp) {
			return
		}
		u.relArg(func() { u.exp(r.Exp, 0, 0) })
	case *core.SetRel:
		// A set operator that removes duplicates says nothing, and
		// one that keeps them says "all", which is how Morel
		// writes it: "union" and "union all".
		if !r.Distinct {
			u.relArg(func() { u.put("all") })
		}
	case *core.Skip:
		u.relArg(func() { u.exp(r.Count, 0, 0) })
	case *core.Sort:
		u.relArg(func() { u.exp(r.Exp, 0, 0) })
	case *core.Take:
		u.relArg(func() { u.exp(r.Count, 0, 0) })
	default:
		// unorder has no arguments.
	}
}

// relGroupArgs writes a group's keys and its aggregates, each
// list in brackets of its own, and omits a list that is empty.
func (u *unparser) relGroupArgs(r *core.Group) {
	if len(r.Keys) > 0 {
		u.relArg(func() {
			for i, k := range r.Keys {
				if i > 0 {
					u.put(", ")
				}
				u.put(k.Label + " = ")
				u.exp(k.Exp, 0, 0)
			}
		})
	}
	if len(r.Aggs) > 0 {
		u.relArg(func() {
			for i, a := range r.Aggs {
				if i > 0 {
					u.put(", ")
				}
				u.put(a.Label + " = ")
				u.relAgg(a)
			}
		})
	}
}

// relAgg writes one aggregate: "count over ()", "sum over #sal $0".
func (u *unparser) relAgg(a core.RelGroupAgg) {
	u.exp(a.Fn, 0, 0)
	u.put(" over ")
	if a.Arg == nil {
		u.put("()")
		return
	}
	u.exp(a.Arg, 0, 0)
}

// relArg writes one argument in brackets.
func (u *unparser) relArg(body func()) {
	u.put(" [")
	body()
	u.put("]")
}

// typeRef is how a plan writes a type: in full where its moniker
// is short, and otherwise a reference that the legend explains.
//
// Two types that print the same moniker share a reference, because
// the moniker is all the plan says about them.
func (u *unparser) typeRef(t types.Type) string {
	moniker := t.String()
	if len(moniker) <= relMaxTypeLength {
		return moniker
	}
	for i, name := range u.typeNames {
		if name == moniker {
			return "t$" + strconv.Itoa(i)
		}
	}
	u.typeNames = append(u.typeNames, moniker)
	return "t$" + strconv.Itoa(len(u.typeNames)-1)
}

// typeLegend is one line per reference typeRef handed out, in the
// order they were handed out, or empty where there were none.
func (u *unparser) typeLegend() string {
	var b strings.Builder
	for i, name := range u.typeNames {
		b.WriteString("t$" + strconv.Itoa(i) + " " + name + "\n")
	}
	return b.String()
}

// isInput0 reports whether an expression is "$0" itself, which a
// projection of the element leaves out of its arguments.
func isInput0(e core.Exp) bool {
	in, isIn := e.(*core.Input)
	return isIn && in.Ordinal == 0
}
