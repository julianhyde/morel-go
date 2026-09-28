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

//nolint:testpackage // white-box: relValidator is unexported
package compile

import (
	"strings"
	"testing"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/types"
)

// Tests for the relational tree's validator: morel#449 spec.md
// §5's rules, each broken on purpose.
//
// A tree that only the constructors built is always valid, so
// every case here reaches past them and writes a field of a node
// directly -- which is what a rule that goes wrong does, and what
// the validator exists to catch.

type valFixture struct {
	sys          *types.System
	ints, reals  core.Exp
	intBag       core.Exp
	trueExp      core.Exp
	zeroInt      core.Exp
	dollar0Int   core.Exp
	dollar1Int   core.Exp
	dollar0Bool  core.Exp
	binder       *core.IDPat
	binderIntExp core.Exp
}

func newValFixture() *valFixture {
	sys := types.NewSystem()
	sys.DeclareDatatype("bag", 1)
	sys.DeclareDatatype("option", 1)
	leaf := func(t types.Type) core.Exp {
		return &core.ID{Pat: &core.IDPat{T: t, Name: "leaf"}}
	}
	binder := &core.IDPat{T: sys.Int, Name: "d"}
	return &valFixture{
		sys:    sys,
		ints:   leaf(sys.List(sys.Int)),
		reals:  leaf(sys.List(sys.Real)),
		intBag: leaf(sys.Bag(sys.Int)),
		trueExp: &core.Literal{
			T: sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
		},
		zeroInt: &core.Literal{
			T: sys.Int, Kind: ast.IntLiteralOp, Value: int32(0),
		},
		dollar0Int:   core.NewInput(sys.Int, 0),
		dollar1Int:   core.NewInput(sys.Int, 1),
		dollar0Bool:  core.NewInput(sys.Bool, 0),
		binder:       binder,
		binderIntExp: &core.ID{Pat: binder},
	}
}

func TestRelValidator(t *testing.T) {
	f := newValFixture()
	sys := f.sys
	tests := []struct {
		name string
		rel  func() core.Rel
		// want is a fragment of the expected violation, or empty
		// where the tree is valid.
		want string
	}{{
		name: "a tree the constructors built is valid",
		rel: func() core.Rel {
			return core.NewProject(sys,
				core.NewFilter(f.ints, f.dollar0Bool),
				f.dollar0Int)
		},
	}, {
		// Rule 1: a filter's condition is bool.
		name: "a filter condition that is not bool",
		rel: func() core.Rel {
			r := core.NewFilter(f.ints, f.trueExp)
			r.Condition = f.dollar0Int
			return r
		},
		want: "filter condition must be bool",
	}, {
		// Rule 1 and 2 together: rebuilding is the derivation, so
		// a node whose type disagrees with its shape is caught
		// whether the element type or the kind is wrong.
		name: "a node whose type is not the one it derives",
		rel: func() core.Rel {
			r := core.NewProject(sys, f.ints, f.dollar0Int)
			r.T = sys.List(sys.Real)
			return r
		},
		want: "project has type real list but derives int list",
	}, {
		name: "a node whose kind is not the one it derives",
		rel: func() core.Rel {
			r := core.NewUnorder(sys, f.ints)
			r.T = sys.List(sys.Int)
			return r
		},
		want: "unorder has type int list but derives int bag",
	}, {
		// Rule 3: "$0" is the node's own element, and a filter
		// has no second input to name.
		name: "a filter condition that names $1",
		rel: func() core.Rel {
			r := core.NewFilter(f.ints, f.trueExp)
			r.Condition = core.NewInput(sys.Bool, 1)
			return r
		},
		want: "filter condition cannot reference $1",
	}, {
		// Rule 3: a skip count is evaluated before the first
		// element exists.
		name: "a skip count that names $0",
		rel: func() core.Rel {
			r := core.NewSkip(f.ints, f.zeroInt)
			r.Count = f.dollar0Int
			return r
		},
		want: "skip count cannot reference $0",
	}, {
		name: "a take count that names $0",
		rel: func() core.Rel {
			r := core.NewTake(f.ints, f.zeroInt)
			r.Count = f.dollar0Int
			return r
		},
		want: "take count cannot reference $0",
	}, {
		// Rule 3: a leaf is below the node, and cannot see the
		// element of the node above it.
		name: "a leaf that names $0",
		rel: func() core.Rel {
			return core.NewFilter(
				core.NewInput(sys.List(sys.Int), 0), f.trueExp)
		},
		want: "leaf cannot reference $0",
	}, {
		// Rule 3: the binder is in scope in the right input only.
		// An occurrence in the condition is a scope error, not a
		// second way of spelling "$0".
		name: "a join condition that names the binder",
		rel: func() core.Rel {
			r := core.NewJoin(sys, core.InnerJoin, f.binder,
				f.ints, f.ints, f.trueExp)
			r.Condition = &core.Apply{
				T: sys.Bool,
				Fn: &core.ID{Pat: &core.IDPat{
					T:    sys.Fn(sys.Tuple(sys.Int, sys.Int), sys.Bool),
					Name: "op =",
				}},
				Arg: &core.Tuple{
					T:    sys.Tuple(sys.Int, sys.Int),
					Args: []core.Exp{f.dollar0Int, f.binderIntExp},
				},
			}
			return r
		},
		want: "cannot reference the join's binder d",
	}, {
		// A join's condition may name both elements, whatever the
		// kind: it is evaluated on candidate pairs, where both
		// are present.
		name: "a join condition may name $0 and $1",
		rel: func() core.Rel {
			r := core.NewJoin(sys, core.LeftJoin, nil, f.ints,
				f.ints, f.trueExp)
			r.Condition = &core.Apply{
				T: sys.Bool,
				Fn: &core.ID{Pat: &core.IDPat{
					T:    sys.Fn(sys.Tuple(sys.Int, sys.Int), sys.Bool),
					Name: "op =",
				}},
				Arg: &core.Tuple{
					T:    sys.Tuple(sys.Int, sys.Int),
					Args: []core.Exp{f.dollar0Int, f.dollar1Int},
				},
			}
			return r
		},
	}, {
		// Rule 4: within one node, output labels are distinct.
		name: "a group with a duplicate label",
		rel: func() core.Rel {
			r := core.NewGroup(sys, f.ints,
				[]core.RelGroupKey{{
					Label: "j", Exp: f.dollar0Int,
				}}, nil)
			r.Keys = append(r.Keys, core.RelGroupKey{
				Label: "j", Exp: f.dollar0Int,
			})
			return r
		},
		want: "duplicate label in group: j",
	}, {
		// Rule 1: a set operator's inputs must agree.
		name: "a union whose inputs disagree",
		rel: func() core.Rel {
			r := core.NewSetRel(sys, core.UnionSet, true,
				[]core.Exp{f.ints, f.ints})
			r.Args = []core.Exp{f.ints, f.reals}
			return r
		},
		want: "union inputs have different element types",
	}, {
		// An input that is not a collection is not a leaf.
		name: "an input that is not a collection",
		rel: func() core.Rel {
			r := core.NewFilter(f.ints, f.trueExp)
			r.Input = f.zeroInt
			return r
		},
		want: "input must be list or bag: int",
	}, {
		// A nested tree is validated in its own right, and its
		// "$0" is its own input's element, not the outer one.
		name: "a nested tree rebinds $0, and is checked itself",
		rel: func() core.Rel {
			inner := core.NewFilter(f.intBag, f.trueExp)
			inner.Condition = f.dollar0Int
			return core.NewProject(sys, f.ints,
				&core.Apply{
					T: sys.Int,
					Fn: &core.ID{Pat: &core.IDPat{
						T:    sys.Fn(sys.Bag(sys.Int), sys.Int),
						Name: "Bag.length",
					}},
					Arg: inner,
				})
		},
		want: "filter condition must be bool",
	}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RelViolations(sys, test.rel())
			if test.want == "" {
				if len(got) > 0 {
					t.Errorf("want valid, got %v", got)
				}
				return
			}
			if !strings.Contains(strings.Join(got, "; "), test.want) {
				t.Errorf("violations %v, want one containing %q",
					got, test.want)
			}
		})
	}
}
