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

package core_test

import (
	"testing"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/token"
	"github.com/hydromatic/morel-go/internal/types"
)

// Tests for the relational tree's datatype: the element type each
// constructor derives (morel#449 spec.md §3) and the kind (§4).
// The "checked by" column of §4's table names a query for every
// row; the kind cases below are those queries, as trees.

// relFixture is a type system and a few leaves to build over.
type relFixture struct {
	sys *types.System
	// ints is "[1, 2, 3] : int list", intBag the same as a bag.
	ints, intBag, reals core.Exp
}

func newRelFixture() *relFixture {
	sys := types.NewSystem()
	sys.DeclareDatatype("bag", 1)
	sys.DeclareDatatype("option", 1)
	f := &relFixture{sys: sys}
	f.ints = f.leaf(sys.List(sys.Int))
	f.intBag = f.leaf(sys.Bag(sys.Int))
	f.reals = f.leaf(sys.List(sys.Real))
	return f
}

// leaf is a collection-typed expression standing for a leaf: a
// leaf is a bare expression, so any expression of the right type
// will do.
func (f *relFixture) leaf(t types.Type) core.Exp {
	return &core.ID{Pat: &core.IDPat{T: t, Name: "leaf"}}
}

// input0 is "$0" at the given element type.
func (f *relFixture) input0(t types.Type) core.Exp {
	return core.NewInput(t, 0)
}

// yes is the condition a node has where the query wrote none: a
// cross join's, and a filter that keeps everything.
func (f *relFixture) yes() core.Exp {
	return &core.Literal{
		T: f.sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
	}
}

func (f *relFixture) intLit(v int32) core.Exp {
	return &core.Literal{
		T: f.sys.Int, Kind: ast.IntLiteralOp, Value: v,
	}
}

func TestRelTypes(t *testing.T) {
	f := newRelFixture()
	sys := f.sys
	tests := []struct {
		name string
		node core.Rel
		want string
	}{{
		// A filter emits its input's rows unchanged, so its type
		// is its input's.
		name: "filter",
		node: core.NewFilter(f.ints, f.yes()),
		want: "int list",
	}, {
		// The element type is the type of the expression, and
		// nothing had to decide: there is no atomization rule.
		name: "project to a scalar",
		node: core.NewProject(sys, f.ints,
			f.input0(sys.Int)),
		want: "int list",
	}, {
		name: "project keeps the input's kind",
		node: core.NewProject(sys, f.intBag,
			f.input0(sys.Int)),
		want: "int bag",
	}, {
		// §4: "from i in [1,2,3], j in bag [i]" is a bag, a join
		// being a nested loop.
		name: "join of a list and a bag is a bag",
		node: core.NewJoin(sys, core.InnerJoin, nil, f.ints,
			f.intBag, f.yes()),
		want: "(int * int) bag",
	}, {
		name: "join of two lists is a list",
		node: core.NewJoin(sys, core.InnerJoin, nil, f.ints,
			f.reals, f.yes()),
		want: "(int * real) list",
	}, {
		// The right side may be absent, so its component is an
		// option. The condition still sees "$1 : int".
		name: "left join options the right components",
		node: core.NewJoin(sys, core.LeftJoin, nil, f.ints,
			f.reals, f.yes()),
		want: "(int * real option) list",
	}, {
		name: "full join options both sides",
		node: core.NewJoin(sys, core.FullJoin, nil, f.ints,
			f.reals, f.yes()),
		want: "(int option * real option) list",
	}, {
		// Components are flat and in order, and the same in both
		// associations: reassociating changes no type.
		name: "a join of a join has three components",
		node: core.NewJoin(sys, core.InnerJoin, nil,
			core.NewJoin(sys, core.InnerJoin, nil, f.ints,
				f.reals, f.yes()),
			f.ints, f.yes()),
		want: "(int * real * int) list",
	}, {
		// A filter between two joins passes its input's
		// components through, or the element would re-associate
		// when something dropped the filter.
		name: "a filter between joins keeps the components flat",
		node: core.NewJoin(sys, core.InnerJoin, nil,
			core.NewFilter(
				core.NewJoin(sys, core.InnerJoin, nil, f.ints,
					f.reals, f.yes()),
				f.yes()),
			f.ints, f.yes()),
		want: "(int * real * int) list",
	}, {
		// Wrapping is additive: two chained outer joins give
		// "int option option".
		name: "chained outer joins wrap twice",
		node: core.NewJoin(sys, core.LeftJoin, nil, f.reals,
			core.NewJoin(sys, core.LeftJoin, nil, f.reals,
				f.ints, f.yes()),
			f.yes()),
		want: "(real * real option * int option option) list",
	}, {
		// A record whether there is one label or many.
		name: "group of one key is still a record",
		node: core.NewGroup(sys, f.ints,
			[]core.RelGroupKey{{
				Label: "j", Exp: f.input0(sys.Int),
			}}, nil),
		want: "{j:int} list",
	}, {
		name: "group keeps the input's kind",
		node: core.NewGroup(sys, f.intBag,
			[]core.RelGroupKey{{
				Label: "j", Exp: f.input0(sys.Int),
			}}, nil),
		want: "{j:int} bag",
	}, {
		// The record of no fields is unit, which is Morel's own
		// rule and not a rule of the tree.
		name: "group with no keys or aggregates is a unit record",
		node: core.NewGroup(sys, f.ints, nil, nil),
		want: "unit list",
	}, {
		name: "sort is always a list",
		node: core.NewSort(sys, f.intBag, f.input0(sys.Int),
			token.Span{}),
		want: "int list",
	}, {
		name: "unorder is always a bag",
		node: core.NewUnorder(sys, f.ints),
		want: "int bag",
	}, {
		name: "skip keeps its input's type",
		node: core.NewSkip(f.ints, f.intLit(2)),
		want: "int list",
	}, {
		name: "take keeps its input's type",
		node: core.NewTake(f.intBag, f.intLit(2)),
		want: "int bag",
	}, {
		// §4: "from i in [1,2] union [3]" is a list.
		name: "union of lists is a list",
		node: core.NewSetRel(sys, core.UnionSet, true,
			[]core.Exp{f.ints, f.ints}),
		want: "int list",
	}, {
		name: "union with a bag is a bag",
		node: core.NewSetRel(sys, core.UnionSet, false,
			[]core.Exp{f.ints, f.intBag}),
		want: "int bag",
	}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.node.Type().String(); got != test.want {
				t.Errorf("type = %s, want %s", got, test.want)
			}
		})
	}
}

// TestRelShape covers what a node says about itself: the operator
// name plan text writes, and the inputs it writes below that line.
func TestRelShape(t *testing.T) {
	f := newRelFixture()
	sys := f.sys
	join := core.NewJoin(sys, core.LeftJoin, nil, f.ints, f.reals,
		f.yes())
	tests := []struct {
		node   core.Rel
		opName string
		inputs int
	}{
		{core.NewFilter(f.ints, f.yes()), "filter", 1},
		{
			core.NewProject(sys, f.ints, f.input0(sys.Int)),
			"project", 1,
		},
		{join, "join", 2},
		{core.NewGroup(sys, f.ints, nil, nil), "group", 1},
		{core.NewSort(sys, f.ints, f.input0(sys.Int),
			token.Span{}), "sort", 1},
		{core.NewUnorder(sys, f.ints), "unorder", 1},
		{core.NewSkip(f.ints, f.intLit(1)), "skip", 1},
		{core.NewTake(f.ints, f.intLit(1)), "take", 1},
		{
			core.NewSetRel(sys, core.ExceptSet, true,
				[]core.Exp{f.ints, f.ints, f.ints}),
			"except", 3,
		},
	}
	for _, test := range tests {
		t.Run(test.opName, func(t *testing.T) {
			if got := test.node.OpName(); got != test.opName {
				t.Errorf("opName = %s, want %s", got, test.opName)
			}
			if got := len(test.node.Inputs()); got != test.inputs {
				t.Errorf("inputs = %d, want %d", got, test.inputs)
			}
		})
	}
	// An inner join's kind is omitted from plan text; the others
	// are written.
	if got := core.InnerJoin.OpName(); got != "" {
		t.Errorf("inner join opName = %q, want empty", got)
	}
	if got := core.LeftJoin.OpName(); got != "left" {
		t.Errorf("left join opName = %q, want \"left\"", got)
	}
	// Components are flat, in order, and counted the same in both
	// associations.
	if got := core.ComponentCount(join); got != 2 {
		t.Errorf("join components = %d, want 2", got)
	}
	if got := core.ComponentCount(f.ints); got != 1 {
		t.Errorf("leaf components = %d, want 1", got)
	}
	// "$0" and "$1" name themselves as plan text writes them.
	if got := core.NewInput(sys.Int, 0).Name(); got != "$0" {
		t.Errorf("input 0 = %s, want $0", got)
	}
	if got := core.NewInput(sys.Int, 1).Name(); got != "$1" {
		t.Errorf("input 1 = %s, want $1", got)
	}
}
