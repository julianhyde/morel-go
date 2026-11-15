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
	"maps"

	"github.com/hydromatic/morel-go/internal/core"
)

// use classifies how a binding is used, which decides whether the
// inliner may substitute its expression at the use sites.
type use int

const (
	// multiUnsafe: may occur many times; inlining could duplicate
	// work. Also the class of every top-level binding, which must
	// survive for later statements.
	multiUnsafe use = iota

	// dead: never used; the binding can be discarded.
	dead

	// atomic: bound to a variable or literal; inlining any number
	// of occurrences duplicates neither code nor work.
	atomic

	// onceSafe: occurs exactly once; inlining is unconditionally
	// safe.
	onceSafe

	// onceUnsafe: occurs exactly once, but inside a lambda or a
	// query's per-row expression that its declaration is not in, so
	// inlining it would evaluate it each time that is evaluated.
	onceUnsafe

	// multiSafe: occurs at most once on each of several case
	// branches; inlining duplicates code but not work. Recognized
	// but not yet acted on.
	multiSafe
)

// useInfo accumulates the analysis of one binding.
type useInfo struct {
	declared bool // declaration site seen (else the use is global)
	top      bool // a top-level binding: never removed
	atomic   bool // bound expression is a variable or literal
	parallel bool // used on more than one branch of a case
	// value: the bound expression is a lambda, or is simple (see
	// isSimple); inlining it into a loop repeats no work worth
	// avoiding.
	value bool
	// repeated: a use is inside a lambda or a query's per-row
	// expression that the declaration is not.
	repeated bool
	// depth is the depth of the declaration (see analyzer.depth), or
	// -1 if it has not been seen.
	depth int
	count int // number of uses (max per branch across cases)
}

// fix converts the accumulated counts to a use class.
func (u *useInfo) fix() use {
	switch {
	case u.top:
		return multiUnsafe
	case u.count == 0:
		return dead
	case u.atomic:
		return atomic
	case u.count == 1:
		switch {
		case u.repeated && !u.value:
			// Inlining would evaluate it each time the lambda or the
			// query's row expression is evaluated.
			return onceUnsafe
		case u.parallel:
			return multiSafe
		default:
			return onceSafe
		}
	default:
		return multiUnsafe
	}
}

// analysis is the result of analyzing a declaration: the use
// class of every binding declared within it. A pattern absent
// from the map was declared outside the declaration (a global);
// looking one up yields the zero value, multiUnsafe, so an
// unknown binding is never inlined or discarded.
type analysis map[*core.IDPat]use

// analyzer walks a Core tree counting declarations and uses.
type analyzer struct {
	uses map[*core.IDPat]*useInfo
	// depth is how many lambdas and per-row expressions of a query
	// enclose the node being visited. A binding read at a greater
	// depth than it was declared may be read many times.
	depth int
	// outer holds the uses in the enclosing expression, if this
	// analyzer counts the uses in one branch of a case; nil
	// otherwise. A binding declared outside the branch has its
	// declaration's depth there.
	outer map[*core.IDPat]*useInfo
}

// deeper returns an analyzer for a node that may be evaluated many
// times, sharing this one's counts.
func (a *analyzer) deeper() *analyzer {
	return &analyzer{uses: a.uses, depth: a.depth + 1, outer: a.outer}
}

// analyze computes the use class of every binding declared in the
// declaration. The declaration's own pattern is marked so that it
// is never classified dead: it must survive for later statements.
func analyze(decl core.Decl) analysis {
	a := &analyzer{uses: map[*core.IDPat]*useInfo{}}
	switch d := decl.(type) {
	case *core.NonRecValDecl:
		for _, id := range core.PatIDs(d.Pat) {
			a.info(id).top = true
		}
	case *core.RecValDecl:
		for _, b := range d.Binds {
			for _, id := range core.PatIDs(b.Pat) {
				a.info(id).top = true
			}
		}
	}
	a.decl(decl)
	result := make(analysis, len(a.uses))
	for pat, info := range a.uses {
		if info.declared {
			result[pat] = info.fix()
		}
	}
	return result
}

// info returns the accumulator for a pattern, creating it at the
// pattern's declaration site.
func (a *analyzer) info(pat *core.IDPat) *useInfo {
	info := a.uses[pat]
	if info == nil {
		info = &useInfo{depth: -1}
		if outer := a.outer[pat]; outer != nil {
			info.depth = outer.depth
		}
		a.uses[pat] = info
	}
	return info
}

// declare records the declaration site of a variable.
func (a *analyzer) declare(pat *core.IDPat) {
	info := a.info(pat)
	info.declared = true
	if info.depth < 0 {
		info.depth = a.depth
	}
}

// use records a use of a variable.
func (a *analyzer) use(pat *core.IDPat) {
	info := a.info(pat)
	info.count++
	if info.depth >= 0 && a.depth > info.depth {
		info.repeated = true
	}
}

// declarePat records the declaration of every variable in a
// pattern, so an unused one is classified dead rather than global.
func (a *analyzer) declarePat(pat core.Pat) {
	for _, id := range core.PatIDs(pat) {
		a.declare(id)
	}
}

// decl analyzes a declaration's expressions.
func (a *analyzer) decl(d core.Decl) {
	switch d := d.(type) {
	case *core.NonRecValDecl:
		a.declarePat(d.Pat)
		a.exp(d.Exp)
		if id, ok := d.Pat.(*core.IDPat); ok {
			if isAtom(d.Exp) {
				a.info(id).atomic = true
			}
			if _, isFn := d.Exp.(*core.Fn); isFn || isSimple(d.Exp) {
				a.info(id).value = true
			}
		}
	case *core.RecValDecl:
		for _, b := range d.Binds {
			a.declarePat(b.Pat)
		}
		for _, b := range d.Binds {
			a.exp(b.Exp)
		}
	}
}

// isAtom reports whether an expression is a variable or literal,
// which is safe to substitute any number of times.
func isAtom(e core.Exp) bool {
	switch e.(type) {
	case *core.ID, *core.Literal:
		return true
	default:
		return false
	}
}

// isSimple reports whether an expression is simple: an atom, or a
// constructor, or a tuple or field selection of simple expressions,
// or a constructor applied to one. Evaluating it can neither raise
// nor have an effect, and repeats no work worth avoiding.
func isSimple(e core.Exp) bool {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		switch e.Fn.(type) {
		case *core.Con, *core.Selector:
			return isSimple(e.Arg)
		default:
			return false
		}
	case *core.Con, *core.ID, *core.Input, *core.Literal:
		return true
	case *core.Tuple:
		for _, arg := range e.Args {
			if !isSimple(arg) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// referencesAny reports whether the expression uses any of the
// patterns.
func referencesAny(e core.Exp, pats []*core.IDPat) bool {
	a := &analyzer{uses: map[*core.IDPat]*useInfo{}}
	a.exp(e)
	for _, p := range pats {
		if info := a.uses[p]; info != nil && info.count > 0 {
			return true
		}
	}
	return false
}

// exp analyzes an expression, counting each variable use.
func (a *analyzer) exp(e core.Exp) {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		a.exp(e.Fn)
		a.exp(e.Arg)
	case *core.Case:
		a.exp(e.Exp)
		if len(e.Matches) == 1 {
			a.declarePat(e.Matches[0].Pat)
			a.exp(e.Matches[0].Exp)
			return
		}
		a.caseBranches(e.Matches)
	case *core.Check:
		a.exp(e.Value)
		a.exp(e.Cond)
	case *core.Fn:
		// A lambda's body may be evaluated many times.
		a.declare(e.IDPat)
		a.deeper().exp(e.Exp)
	case *core.From:
		for i, s := range e.Steps {
			a.step(s, i == 0)
		}
	case *core.ID:
		a.use(e.Pat)
	case *core.Let:
		a.decl(e.Decl)
		a.exp(e.Exp)
	case *core.List:
		a.exps(e.Args)
	case *core.RangeList:
		for _, item := range e.Items {
			a.exp(item.Lo)
			if item.Hi != nil {
				a.exp(item.Hi)
			}
		}
	case *core.Tuple:
		a.exps(e.Args)
	case core.Rel:
		// A node declares before it reads: a join's binder is in
		// scope in its right input, and a group's key in the
		// aggregates beside it.
		//
		// A node's own expressions are evaluated for each row, and a
		// join's right input for each row of its left.
		row := a.deeper()
		for _, pat := range relBinders(e) {
			row.declare(pat)
		}
		for i, input := range e.Inputs() {
			if _, isJoin := e.(*core.Join); isJoin && i == 1 {
				row.exp(input)
				continue
			}
			a.exp(input)
		}
		row.exps(relExps(e))
	}
}

// caseBranches analyzes the branches of a case with more than one.
// Only one branch runs, so a binding's uses across branches cost only
// the worst branch: add the maximum per-branch count. A binding that
// occurs on several branches is marked parallel -- inlining it there
// duplicates code, though not work.
func (a *analyzer) caseBranches(matches []core.Match) {
	worst := map[*core.IDPat]int{}
	branches := map[*core.IDPat]int{}
	outer := a.uses
	if a.outer != nil {
		outer = merged(a.outer, a.uses)
	}
	for _, m := range matches {
		a.declarePat(m.Pat)
		branch := &analyzer{
			uses: map[*core.IDPat]*useInfo{}, depth: a.depth,
			outer: outer,
		}
		branch.exp(m.Exp)
		for pat, info := range branch.uses {
			if info.declared {
				// Declared in the branch: it is the branch's.
				*a.info(pat) = *info
				continue
			}
			if info.repeated {
				a.info(pat).repeated = true
			}
			if info.count == 0 {
				a.info(pat)
				continue
			}
			worst[pat] = max(worst[pat], info.count)
			branches[pat]++
		}
	}
	for pat, count := range worst {
		info := a.info(pat)
		info.count += count
		if branches[pat] > 1 {
			info.parallel = true
		}
	}
}

// merged returns a map with the entries of uses and, for the keys
// it lacks, of outer: for the uses of a case inside a branch of
// another case.
func merged(outer, uses map[*core.IDPat]*useInfo,
) map[*core.IDPat]*useInfo {
	m := make(map[*core.IDPat]*useInfo, len(outer)+len(uses))
	maps.Copy(m, outer)
	maps.Copy(m, uses)
	return m
}

// exps analyzes each expression of a slice.
func (a *analyzer) exps(exps []core.Exp) {
	for _, e := range exps {
		a.exp(e)
	}
}

// step analyzes the expressions and bindings of a query step. The
// first step's source is evaluated once; every other expression of
// a query is evaluated for each row, or, for an aggregate's
// function, for each group. "skip", "take", "into" and "through"
// are evaluated once.
func (a *analyzer) step(s core.FromStep, first bool) {
	row := a.deeper()
	// lint: sort until '^\t}' where '^\tcase '
	switch s := s.(type) {
	case *core.GroupStep:
		for _, k := range s.Keys {
			row.exp(k.Exp)
			a.declare(k.Pat)
		}
		for _, agg := range s.Aggs {
			row.exp(agg.Fn)
			if agg.Arg != nil {
				row.exp(agg.Arg)
			}
			a.declare(agg.Pat)
		}
	case *core.Into:
		a.exp(s.Fn)
	case *core.Order:
		row.exp(s.Exp)
	case *core.Scan:
		if first {
			a.exp(s.Exp)
		} else {
			row.exp(s.Exp)
		}
		a.declarePat(s.Pat)
	case *core.SetOp:
		a.exps(s.Args)
	case *core.SkipStep:
		a.exp(s.Exp)
	case *core.TakeStep:
		a.exp(s.Exp)
	case *core.Through:
		a.exp(s.Fn)
		a.declarePat(s.Pat)
	case *core.Where:
		row.exp(s.Exp)
	case *core.Yield:
		if s.Exp != nil {
			row.exp(s.Exp)
		}
		for _, f := range s.Fields {
			row.exp(f.Exp)
			a.declare(f.Pat)
		}
	}
}
