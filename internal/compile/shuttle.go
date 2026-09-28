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
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/types"
)

// rewriter rebuilds a Core tree bottom-up. A node whose children
// are all unchanged is returned as the same pointer, so a caller
// can detect "nothing changed" — and hence a fixpoint — by
// comparing the root before and after.
//
// The exp hook, if set, intercepts every expression before the
// structural walk; returning (e2, true) replaces the expression
// wholesale (the hook is responsible for rewriting e2's children,
// typically by calling rewriteExp itself), while (nil, false)
// falls through to the structural rebuild below.
type rewriter struct {
	exp func(e core.Exp) (core.Exp, bool)
	// sys is needed to rebuild a relational node, whose type is
	// derived from its parts. A rewriter that never meets one may
	// leave it nil.
	sys *types.System
}

// rewriteExp rewrites an expression, preserving pointer identity
// when nothing beneath it changes.
func (r *rewriter) rewriteExp(e core.Exp) core.Exp {
	if r.exp != nil {
		if e2, ok := r.exp(e); ok {
			return e2
		}
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		fn, arg := r.rewriteExp(e.Fn), r.rewriteExp(e.Arg)
		if fn == e.Fn && arg == e.Arg {
			return e
		}
		return &core.Apply{T: e.T, Fn: fn, Arg: arg, Span: e.Span}
	case *core.Case:
		return r.rewriteCase(e)
	case *core.Check:
		cond, value := r.rewriteExp(e.Cond), r.rewriteExp(e.Value)
		if cond == e.Cond && value == e.Value {
			return e
		}
		check := *e
		check.Cond, check.Value = cond, value
		return &check
	case *core.Fn:
		body := r.rewriteExp(e.Exp)
		if body == e.Exp {
			return e
		}
		return &core.Fn{T: e.T, IDPat: e.IDPat, Exp: body}
	case *core.From:
		steps := make([]core.FromStep, len(e.Steps))
		changed := false
		for i, s := range e.Steps {
			steps[i] = r.rewriteStep(s)
			if steps[i] != s {
				changed = true
			}
		}
		if !changed {
			return e
		}
		return &core.From{
			T:       e.T,
			Steps:   steps,
			Kind:    e.Kind,
			Ordinal: e.Ordinal,
		}
	case *core.Let:
		decl := r.rewriteDecl(e.Decl)
		body := r.rewriteExp(e.Exp)
		if decl == e.Decl && body == e.Exp {
			return e
		}
		return &core.Let{Decl: decl, Exp: body}
	case *core.List:
		if args, changed := r.rewriteExps(e.Args); changed {
			return &core.List{T: e.T, Args: args}
		}
		return e
	case *core.RangeList:
		return r.rewriteRangeList(e)
	case *core.Tuple:
		if args, changed := r.rewriteExps(e.Args); changed {
			return &core.Tuple{T: e.T, Args: args}
		}
		return e
	case core.Rel:
		return r.rewriteRel(e)
	default:
		// Leaves: Literal, ID, Con, Selector, Ordinal, Unit.
		return e
	}
}

// rewriteRel rewrites a relational node: its inputs, and the
// expressions over its element.
//
// A node's type is derived from its parts, so a rewrite rebuilds
// through the constructors rather than copying the type across: a
// rewrite that changes an element's type and keeps the old one is
// exactly what the validator exists to catch, and rebuilding
// cannot produce it.
func (r *rewriter) rewriteRel(rel core.Rel) core.Exp {
	sys := r.sys
	// lint: sort until '^\t}' where '^\tcase '
	switch e := rel.(type) {
	case *core.Filter:
		input, cond := r.rewriteExp(e.Input), r.rewriteExp(e.Condition)
		if input == e.Input && cond == e.Condition {
			return e
		}
		return core.NewFilter(input, cond)
	case *core.Group:
		return r.rewriteGroupRel(e)
	case *core.Join:
		left, right := r.rewriteExp(e.Left), r.rewriteExp(e.Right)
		cond := r.rewriteExp(e.Condition)
		if left == e.Left && right == e.Right &&
			cond == e.Condition {
			return e
		}
		return core.NewJoin(sys, e.Kind, e.Binder, left, right, cond)
	case *core.Project:
		input, exp := r.rewriteExp(e.Input), r.rewriteExp(e.Exp)
		if input == e.Input && exp == e.Exp {
			return e
		}
		return core.NewProject(sys, input, exp)
	case *core.SetRel:
		if args, changed := r.rewriteExps(e.Args); changed {
			return core.NewSetRel(sys, e.Kind, e.Distinct, args)
		}
		return e
	case *core.Skip:
		input, count := r.rewriteExp(e.Input), r.rewriteExp(e.Count)
		if input == e.Input && count == e.Count {
			return e
		}
		return core.NewSkip(input, count)
	case *core.Sort:
		input, exp := r.rewriteExp(e.Input), r.rewriteExp(e.Exp)
		if input == e.Input && exp == e.Exp {
			return e
		}
		return core.NewSort(sys, input, exp, e.Span)
	case *core.Take:
		input, count := r.rewriteExp(e.Input), r.rewriteExp(e.Count)
		if input == e.Input && count == e.Count {
			return e
		}
		return core.NewTake(input, count)
	case *core.Unorder:
		if input := r.rewriteExp(e.Input); input != e.Input {
			return core.NewUnorder(sys, input)
		}
		return e
	default:
		return rel
	}
}

// relExps are the expressions a node evaluates -- the ones that
// are not its inputs, and that read its element as "$0".
//
// Inputs come from the node's own "Inputs", which the datatype
// provides; this is the other half, so that a pass which only
// needs to *walk* a node can have both without a switch of its
// own. A pass that rebuilds still needs one, because rebuilding
// goes through the constructors.
func relExps(rel core.Rel) []core.Exp {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := rel.(type) {
	case *core.Filter:
		return []core.Exp{e.Condition}
	case *core.Group:
		out := make([]core.Exp, 0, len(e.Keys)+2*len(e.Aggs))
		for _, k := range e.Keys {
			out = append(out, k.Exp)
		}
		for _, a := range e.Aggs {
			out = append(out, a.Fn)
			if a.Arg != nil {
				out = append(out, a.Arg)
			}
		}
		return out
	case *core.Join:
		return []core.Exp{e.Condition}
	case *core.Project:
		return []core.Exp{e.Exp}
	case *core.Skip:
		return []core.Exp{e.Count}
	case *core.Sort:
		return []core.Exp{e.Exp}
	case *core.Take:
		return []core.Exp{e.Count}
	default:
		// A set operator and an unorder evaluate nothing of their
		// own; their inputs are all there is.
		return nil
	}
}

// relBinders are the patterns a node declares: a join's binder,
// which names its left element inside its right input, and a
// group's keys, which an aggregate of the same group may name.
//
// A pass that counts uses has to know these are declarations and
// not references, or it counts a binding as a use of something
// outside the node.
func relBinders(rel core.Rel) []*core.IDPat {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := rel.(type) {
	case *core.Group:
		var out []*core.IDPat
		for _, k := range e.Keys {
			if k.Pat != nil {
				out = append(out, k.Pat)
			}
		}
		return out
	case *core.Join:
		if e.Binder == nil {
			return nil
		}
		return []*core.IDPat{e.Binder}
	default:
		return nil
	}
}

// rewriteGroupRel rewrites a group's keys and aggregates.
func (r *rewriter) rewriteGroupRel(e *core.Group) core.Exp {
	input := r.rewriteExp(e.Input)
	changed := input != e.Input
	keys := make([]core.RelGroupKey, len(e.Keys))
	for i, k := range e.Keys {
		// The key's pattern is carried across: it is what an
		// aggregate of this group names, and what the lowering
		// calls the key, and a rewrite that dropped it would
		// rename the key after its label -- which for a distinct
		// is "$0".
		keys[i] = core.RelGroupKey{
			Label: k.Label, Exp: r.rewriteExp(k.Exp), Pat: k.Pat,
		}
		changed = changed || keys[i].Exp != k.Exp
	}
	aggs := make([]core.RelGroupAgg, len(e.Aggs))
	for i, a := range e.Aggs {
		aggs[i] = a
		aggs[i].Fn = r.rewriteExp(a.Fn)
		if a.Arg != nil {
			aggs[i].Arg = r.rewriteExp(a.Arg)
		}
		changed = changed || aggs[i].Fn != a.Fn ||
			aggs[i].Arg != a.Arg
	}
	if !changed {
		return e
	}
	return core.NewGroup(r.sys, input, keys, aggs)
}

// rewriteRangeList rewrites the bounds of a range list.
func (r *rewriter) rewriteRangeList(e *core.RangeList) core.Exp {
	items := make([]core.RangeItem, len(e.Items))
	changed := false
	for i, item := range e.Items {
		items[i] = item
		items[i].Lo = r.rewriteExp(item.Lo)
		if items[i].Lo != item.Lo {
			changed = true
		}
		if item.Hi != nil {
			items[i].Hi = r.rewriteExp(item.Hi)
			if items[i].Hi != item.Hi {
				changed = true
			}
		}
	}
	if !changed {
		return e
	}
	return &core.RangeList{T: e.T, Items: items, Span: e.Span}
}

// rewriteCase rewrites a case's scrutinee and arm bodies.
func (r *rewriter) rewriteCase(e *core.Case) core.Exp {
	exp := r.rewriteExp(e.Exp)
	matches := make([]core.Match, len(e.Matches))
	changed := exp != e.Exp
	for i, m := range e.Matches {
		matches[i] = m
		matches[i].Exp = r.rewriteExp(m.Exp)
		if matches[i].Exp != m.Exp {
			changed = true
		}
	}
	if !changed {
		return e
	}
	return &core.Case{T: e.T, Exp: exp, Matches: matches, Span: e.Span}
}

// rewriteExps rewrites a slice of expressions, reporting whether
// any element changed; if none did, the returned slice is the
// argument itself.
func (r *rewriter) rewriteExps(exps []core.Exp) ([]core.Exp, bool) {
	out := exps
	changed := false
	for i, e := range exps {
		e2 := r.rewriteExp(e)
		if e2 == e {
			continue
		}
		if !changed {
			out = make([]core.Exp, len(exps))
			copy(out, exps)
			changed = true
		}
		out[i] = e2
	}
	return out, changed
}

// rewriteDecl rewrites the expressions inside a declaration.
func (r *rewriter) rewriteDecl(d core.Decl) core.Decl {
	switch d := d.(type) {
	case *core.NonRecValDecl:
		exp := r.rewriteExp(d.Exp)
		if exp == d.Exp {
			return d
		}
		return &core.NonRecValDecl{Pat: d.Pat, Exp: exp, Span: d.Span}
	case *core.RecValDecl:
		binds := make([]*core.NonRecValDecl, len(d.Binds))
		changed := false
		for i, b := range d.Binds {
			exp := r.rewriteExp(b.Exp)
			if exp == b.Exp {
				binds[i] = b
				continue
			}
			binds[i] = &core.NonRecValDecl{
				Pat: b.Pat, Exp: exp, Span: b.Span,
			}
			changed = true
		}
		if !changed {
			return d
		}
		return &core.RecValDecl{Binds: binds}
	default:
		return d
	}
}

// rewriteStep rewrites the expressions inside a query step.
func (r *rewriter) rewriteStep(s core.FromStep) core.FromStep {
	// lint: sort until '^\t}' where '^\tcase '
	switch s := s.(type) {
	case *core.GroupStep:
		return r.rewriteGroup(s)
	case *core.Into:
		fn := r.rewriteExp(s.Fn)
		if fn == s.Fn {
			return s
		}
		return &core.Into{Fn: fn}
	case *core.Order:
		exp := r.rewriteExp(s.Exp)
		if exp == s.Exp {
			return s
		}
		return &core.Order{Exp: exp, Span: s.Span}
	case *core.Scan:
		exp := r.rewriteExp(s.Exp)
		if exp == s.Exp {
			return s
		}
		return &core.Scan{Pat: s.Pat, Exp: exp}
	case *core.SetOp:
		if args, changed := r.rewriteExps(s.Args); changed {
			return &core.SetOp{
				Kind: s.Kind, Args: args, Distinct: s.Distinct,
			}
		}
		return s
	case *core.SkipStep:
		exp := r.rewriteExp(s.Exp)
		if exp == s.Exp {
			return s
		}
		return &core.SkipStep{Exp: exp}
	case *core.TakeStep:
		exp := r.rewriteExp(s.Exp)
		if exp == s.Exp {
			return s
		}
		return &core.TakeStep{Exp: exp}
	case *core.Through:
		fn := r.rewriteExp(s.Fn)
		if fn == s.Fn {
			return s
		}
		return &core.Through{Pat: s.Pat, Fn: fn}
	case *core.Where:
		exp := r.rewriteExp(s.Exp)
		if exp == s.Exp {
			return s
		}
		return &core.Where{Exp: exp}
	case *core.Yield:
		return r.rewriteYield(s)
	default:
		// Distinct has no expressions.
		return s
	}
}

// rewriteYield rewrites a yield step's expressions.
func (r *rewriter) rewriteYield(s *core.Yield) core.FromStep {
	exp := s.Exp
	if exp != nil {
		exp = r.rewriteExp(exp)
	}
	// A nil Fields slice means a final yield, so nil-ness must
	// survive the rebuild.
	var fields []core.YieldField
	changed := exp != s.Exp
	if s.Fields != nil {
		fields = make([]core.YieldField, len(s.Fields))
		for i, f := range s.Fields {
			fields[i] = f
			fields[i].Exp = r.rewriteExp(f.Exp)
			if fields[i].Exp != f.Exp {
				changed = true
			}
		}
	}
	if !changed {
		return s
	}
	return &core.Yield{Exp: exp, Fields: fields}
}

// rewriteGroup rewrites a group step's key and aggregate
// expressions.
func (r *rewriter) rewriteGroup(s *core.GroupStep) core.FromStep {
	keys := make([]core.GroupKey, len(s.Keys))
	aggs := make([]core.GroupAgg, len(s.Aggs))
	changed := false
	for i, k := range s.Keys {
		keys[i] = k
		keys[i].Exp = r.rewriteExp(k.Exp)
		if keys[i].Exp != k.Exp {
			changed = true
		}
	}
	for i, a := range s.Aggs {
		aggs[i] = a
		aggs[i].Fn = r.rewriteExp(a.Fn)
		if aggs[i].Fn != a.Fn {
			changed = true
		}
		if a.Arg != nil {
			aggs[i].Arg = r.rewriteExp(a.Arg)
			if aggs[i].Arg != a.Arg {
				changed = true
			}
		}
	}
	if !changed {
		return s
	}
	return &core.GroupStep{Keys: keys, Aggs: aggs}
}
