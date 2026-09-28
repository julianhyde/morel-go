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
	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/types"
)

// A pattern that can fail to match filters as well as binds, and
// in a relational tree the two halves separate: relTest is the
// filter, and the translation's destructure is the binding. A scan
// is then a leaf with a filter above it and, where the bindings do
// not describe the element, a projection above that -- three
// ordinary nodes, each of which a rule can see through, rather
// than one node holding a "case" that yields a collection.
//
// These are morel#449's RelBuilder.testable and RelBuilder.test.

// relTestable reports whether relTest can express a pattern's
// condition, and the translation's destructure its bindings, as
// expressions over the element.
func relTestable(pat core.Pat) bool {
	// lint: sort until '^\t}' where '^\tcase '
	switch p := pat.(type) {
	case *core.AsPat:
		// The name matches whatever the pattern it wraps matches.
		return relTestable(p.Body)
	case *core.ConsPat:
		// "::" is a constructor, but the list datatype has total
		// accessors -- null, hd, tl -- where a user datatype has
		// none.
		return relTestable(p.Head) && relTestable(p.Tail)
	case *core.IDPat, *core.WildcardPat:
		return true
	case *core.ListPat:
		return allTestable(p.Args)
	case *core.LiteralPat:
		return true
	case *core.TuplePat:
		return allTestable(p.Args)
	default:
		// A user datatype's constructor filters too, but
		// extracting what it binds has no total expression.
		return false
	}
}

func allTestable(pats []core.Pat) bool {
	for _, p := range pats {
		if !relTestable(p) {
			return false
		}
	}
	return true
}

// relTest is the condition under which a pattern matches an
// element, or nil where it always matches.
//
// Callers must ask relTestable first. A constructor pattern is not
// testable here, because extracting what it binds needs a "case"
// of its own: a datatype has no total accessor for a
// constructor's argument, and no value to give the branch that
// does not match.
func relTest(sys *types.System, pat core.Pat,
	element core.Exp,
) core.Exp {
	// lint: sort until '^\t}' where '^\tcase '
	switch p := pat.(type) {
	case *core.AsPat:
		return relTest(sys, p.Body, element)
	case *core.ConsPat:
		// A non-empty list, whose head and tail must match in
		// turn.
		tests := []core.Exp{notExp(sys, listIsNull(sys, element))}
		tests = addTest(sys, tests, p.Head, listHd(sys, element))
		tests = addTest(sys, tests, p.Tail, listTl(sys, element))
		return composeConjuncts(sys, tests)
	case *core.IDPat, *core.WildcardPat:
		return nil
	case *core.ListPat:
		// A list of exactly this length, whose items must match in
		// turn.
		if len(p.Args) == 0 {
			return listIsNull(sys, element)
		}
		tests := []core.Exp{equalExp(sys, listLength(sys, element),
			intLiteral(sys, len(p.Args)))}
		for i, item := range p.Args {
			tests = addTest(sys, tests, item,
				listNth(sys, element, i))
		}
		return composeConjuncts(sys, tests)
	case *core.LiteralPat:
		return equalExp(sys, element, &core.Literal{
			T: p.T, Kind: p.Kind, Value: p.Value,
		})
	case *core.TuplePat:
		var tests []core.Exp
		for i, arg := range p.Args {
			tests = addTest(sys, tests, arg,
				fieldAt(sys, element, i))
		}
		if len(tests) == 0 {
			return nil
		}
		return composeConjuncts(sys, tests)
	default:
		panic("not testable: " + pat.Op().String())
	}
}

// intLiteral is a count as an int literal. The counts here are a
// pattern's length and a position within it, both bounded by the
// source text.
func intLiteral(sys *types.System, n int) core.Exp {
	return &core.Literal{
		T: sys.Int, Kind: ast.IntLiteralOp,
		//nolint:gosec // a pattern's length fits in an int32.
		Value: int32(n),
	}
}

// addTest appends a pattern's test, where it has one.
func addTest(sys *types.System, tests []core.Exp, pat core.Pat,
	element core.Exp,
) []core.Exp {
	if test := relTest(sys, pat, element); test != nil {
		return append(tests, test)
	}
	return tests
}

// equalExp is "a = b".
func equalExp(sys *types.System, a, b core.Exp) core.Exp {
	pairT := sys.Tuple(a.Type(), b.Type())
	return &core.Apply{
		T: sys.Bool,
		Fn: &core.ID{Pat: &core.IDPat{
			T: sys.Fn(pairT, sys.Bool), Name: eqOpName,
		}},
		Arg: &core.Tuple{T: pairT, Args: []core.Exp{a, b}},
	}
}

// notExp is "not e".
func notExp(sys *types.System, e core.Exp) core.Exp {
	return &core.Apply{
		T: sys.Bool,
		Fn: &core.ID{Pat: &core.IDPat{
			T: sys.Fn(sys.Bool, sys.Bool), Name: notName,
		}},
		Arg: e,
	}
}

// listCall applies a one-argument list built-in to a list.
func listCall(sys *types.System, name string, list core.Exp,
	result types.Type,
) core.Exp {
	return &core.Apply{
		T: result,
		Fn: &core.ID{Pat: &core.IDPat{
			T: sys.Fn(list.Type(), result), Name: name,
		}},
		Arg: list,
	}
}

func listIsNull(sys *types.System, list core.Exp) core.Exp {
	return listCall(sys, "List.null", list, sys.Bool)
}

func listLength(sys *types.System, list core.Exp) core.Exp {
	return listCall(sys, "List.length", list, sys.Int)
}

func listHd(sys *types.System, list core.Exp) core.Exp {
	return listCall(sys, "List.hd", list,
		types.ElemOf(list.Type()))
}

func listTl(sys *types.System, list core.Exp) core.Exp {
	return listCall(sys, "List.tl", list, list.Type())
}

// listNth is "List.nth (list, i)".
func listNth(sys *types.System, list core.Exp, i int) core.Exp {
	elem := types.ElemOf(list.Type())
	pairT := sys.Tuple(list.Type(), sys.Int)
	return &core.Apply{
		T: elem,
		Fn: &core.ID{Pat: &core.IDPat{
			T: sys.Fn(pairT, elem), Name: "List.nth",
		}},
		Arg: &core.Tuple{T: pairT, Args: []core.Exp{
			list, intLiteral(sys, i),
		}},
	}
}
