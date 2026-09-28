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
	"strings"

	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/types"
)

// Checks the invariants of a relational tree (core.Rel), which are
// morel#449 spec.md §5.
//
// Every node's type is the one its inputs and expressions derive,
// every expression has the type its position requires, and "$0"
// and "$1" occur only where a node binds them.
//
// Run it after translation and after every rule firing. A rule
// that produces a tree the validator rejects is wrong, and it is
// much cheaper to find that here than in the wrong answers it
// would otherwise cause.

// relScope is which input references an expression may make.
type relScope int

const (
	// scopeNone is an expression evaluated where no element
	// exists: a skip or take count, an ifEmpty expression, a leaf.
	scopeNone relScope = iota
	// scopeZero is an expression over the node's element.
	scopeZero
	// scopeZeroOne is a join's condition, over both elements.
	scopeZeroOne
)

func (s relScope) allows(ordinal int) bool {
	switch s {
	case scopeZero:
		return ordinal == 0
	case scopeZeroOne:
		return ordinal == 0 || ordinal == 1
	default:
		return false
	}
}

// relValidator collects the ways a tree violates the invariants.
type relValidator struct {
	sys        *types.System
	violations []string
}

// RelViolations returns the ways a tree violates the invariants,
// or nil where it is valid.
func RelViolations(sys *types.System, rel core.Rel) []string {
	v := &relValidator{sys: sys}
	v.node(rel)
	return v.violations
}

// CheckRelValid panics where a tree violates the invariants. It is
// the form a pass uses, since a tree that fails here is a bug in
// the pass that built it, not a program error.
func CheckRelValid(sys *types.System, rel core.Rel) {
	if bad := RelViolations(sys, rel); len(bad) > 0 {
		panic("invalid relational tree: " + strings.Join(bad, "; "))
	}
}

func (v *relValidator) badf(format string, args ...any) {
	v.violations = append(v.violations,
		fmt.Sprintf(format, args...))
}

// node validates one node: its inputs, the types of its
// expressions, the scope each is evaluated in, and its own type.
func (v *relValidator) node(rel core.Rel) {
	// lint: sort until '^\t}' where '^\tcase '
	switch r := rel.(type) {
	case *core.Filter:
		v.input(r.Input)
		v.requireType(r.Condition, v.sys.Bool, "filter condition")
		v.scope(r.Condition, scopeZero, "filter condition")
		v.derived(rel, func() core.Rel {
			return core.NewFilter(r.Input, r.Condition)
		})
	case *core.Group:
		v.input(r.Input)
		for _, k := range r.Keys {
			v.scope(k.Exp, scopeZero, "group key")
		}
		for _, a := range r.Aggs {
			v.scope(a.Fn, scopeZero, "aggregate function")
			if a.Arg != nil {
				v.scope(a.Arg, scopeZero, "aggregate argument")
			}
		}
		v.labelsDistinct(r)
		v.derived(rel, func() core.Rel {
			return core.NewGroup(v.sys, r.Input, r.Keys, r.Aggs)
		})
	case *core.Join:
		v.input(r.Left)
		v.input(r.Right)
		v.requireType(r.Condition, v.sys.Bool, "join condition")
		v.scope(r.Condition, scopeZeroOne, "join condition")
		if r.Binder != nil {
			// The binder names the left element inside the right
			// input, and only there. The condition says "$0" and
			// "$1" as any join's does, so an occurrence here is a
			// scope error, not a second way of spelling "$0".
			v.binderNotIn(r.Condition, r.Binder, "join condition")
		}
		v.derived(rel, func() core.Rel {
			return core.NewJoin(v.sys, r.Kind, r.Binder, r.Left,
				r.Right, r.Condition)
		})
	case *core.Project:
		v.input(r.Input)
		v.scope(r.Exp, scopeZero, "project expression")
		v.derived(rel, func() core.Rel {
			return core.NewProject(v.sys, r.Input, r.Exp)
		})
	case *core.SetRel:
		for _, arg := range r.Args {
			v.input(arg)
		}
		if !v.setElemsAgree(r) {
			return
		}
		v.derived(rel, func() core.Rel {
			return core.NewSetRel(v.sys, r.Kind, r.Distinct, r.Args)
		})
	case *core.Skip:
		v.input(r.Input)
		v.requireType(r.Count, v.sys.Int, "skip count")
		// Evaluated before the first element exists.
		v.scope(r.Count, scopeNone, "skip count")
		v.derived(rel, func() core.Rel {
			return core.NewSkip(r.Input, r.Count)
		})
	case *core.Sort:
		v.input(r.Input)
		v.scope(r.Exp, scopeZero, "sort key")
		v.derived(rel, func() core.Rel {
			return core.NewSort(v.sys, r.Input, r.Exp, r.Span)
		})
	case *core.Take:
		v.input(r.Input)
		v.requireType(r.Count, v.sys.Int, "take count")
		v.scope(r.Count, scopeNone, "take count")
		v.derived(rel, func() core.Rel {
			return core.NewTake(r.Input, r.Count)
		})
	case *core.Unorder:
		v.input(r.Input)
		v.derived(rel, func() core.Rel {
			return core.NewUnorder(v.sys, r.Input)
		})
	default:
		v.badf("unknown node: %T", rel)
	}
}

// setElemsAgree reports whether a set operator's inputs have the
// same element type, recording a violation where they do not.
func (v *relValidator) setElemsAgree(r *core.SetRel) bool {
	if len(r.Args) == 0 {
		v.badf("%s has no input", r.OpName())
		return false
	}
	elem := types.ElemOf(r.Args[0].Type())
	ok := true
	for _, arg := range r.Args[1:] {
		if t := types.ElemOf(arg.Type()); t != elem {
			v.badf("%s inputs have different element types: "+
				"%s, %s", r.OpName(), elem, t)
			ok = false
		}
	}
	return ok
}

// labelsDistinct checks spec §5 rule 4 for a group: its keys and
// aggregates together must have distinct labels. A project's
// labels cannot collide -- its element is a record type, and a
// record has each label once -- so a group is the only node where
// the rule can be broken.
func (v *relValidator) labelsDistinct(r *core.Group) {
	seen := map[string]bool{}
	for _, k := range r.Keys {
		if seen[k.Label] {
			v.badf("duplicate label in group: %s", k.Label)
		}
		seen[k.Label] = true
	}
	for _, a := range r.Aggs {
		if seen[a.Label] {
			v.badf("duplicate label in group: %s", a.Label)
		}
		seen[a.Label] = true
	}
}

// input validates an input: a nested node, or a leaf, which must
// be a collection and cannot see the element of the node above it.
func (v *relValidator) input(input core.Exp) {
	if rel, isRel := input.(core.Rel); isRel {
		v.node(rel)
		return
	}
	if !types.IsCollection(input.Type()) {
		v.badf("input must be list or bag: %s", input.Type())
	}
	v.scope(input, scopeNone, "leaf")
}

// derived checks that a node's type is the one derived for it.
// Rebuilding *is* the derivation, so a node whose type disagrees
// was not built by the constructors.
//
// The rebuild is a function, and its panic is caught, because a
// constructor rejects an argument that cannot make a node at all.
// That is a violation like any other here: the validator's job is
// to report what is wrong with a tree, not to fail on it.
func (v *relValidator) derived(rel core.Rel, build func() core.Rel) {
	defer func() {
		if r := recover(); r != nil {
			v.badf("%s cannot be rebuilt: %v", rel.OpName(), r)
		}
	}()
	if d := build(); rel.Type() != d.Type() {
		v.badf("%s has type %s but derives %s", rel.OpName(),
			rel.Type(), d.Type())
	}
}

func (v *relValidator) requireType(e core.Exp, t types.Type,
	what string,
) {
	if e.Type() != t {
		v.badf("%s must be %s: %s", what, t, e.Type())
	}
}

// binderNotIn checks that an expression does not name a join's
// binder.
//
// Unlike scope, this walk does not stop at a nested node: the
// binder is an ordinary name, so a nested tree does not shield an
// occurrence of it the way it rebinds "$0".
func (v *relValidator) binderNotIn(e core.Exp, binder *core.IDPat,
	what string,
) {
	r := &rewriter{}
	r.exp = func(x core.Exp) (core.Exp, bool) {
		if id, isID := x.(*core.ID); isID && id.Pat == binder {
			v.badf("%s cannot reference the join's binder %s",
				what, binder.Name)
		}
		return nil, false
	}
	r.rewriteExp(e)
}

// scope checks that an expression makes no input reference beyond
// those the node binds.
//
// The walk stops at a nested node, whose expressions are in that
// node's scope and not this one; the nested node is validated in
// its own right.
func (v *relValidator) scope(e core.Exp, allowed relScope,
	what string,
) {
	r := &rewriter{}
	r.exp = func(x core.Exp) (core.Exp, bool) {
		switch x := x.(type) {
		case *core.Input:
			if !allowed.allows(x.Ordinal) {
				v.badf("%s cannot reference %s", what, x.Name())
			}
			return x, true
		case core.Rel:
			v.node(x)
			return x, true
		default:
			return nil, false
		}
	}
	r.rewriteExp(e)
}
