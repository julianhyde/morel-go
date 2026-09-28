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

//nolint:testpackage // white-box: the unparser is unexported
package compile

import (
	"testing"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/types"
)

// Tests for plan text: morel#449 spec.md §6, the
// cross-implementation contract. The expected text of the first
// two comes from morel-java's conformance suite, rel-tree.smli,
// verbatim.

type planFixture struct {
	sys *types.System
}

func newPlanFixture() *planFixture {
	sys := types.NewSystem()
	sys.DeclareDatatype("bag", 1)
	return &planFixture{sys: sys}
}

func (f *planFixture) i(n int32) core.Exp {
	return &core.Literal{
		T: f.sys.Int, Kind: ast.IntLiteralOp, Value: n,
	}
}

func (f *planFixture) str(s string) core.Exp {
	return &core.Literal{
		T: f.sys.String, Kind: ast.StringLiteralOp, Value: s,
	}
}

// binop is an application of a named binary operator.
func (f *planFixture) binop(name string, a, b core.Exp,
	t types.Type,
) core.Exp {
	pairT := f.sys.Tuple(a.Type(), b.Type())
	return &core.Apply{
		T: t,
		Fn: &core.ID{Pat: &core.IDPat{
			T: f.sys.Fn(pairT, t), Name: name,
		}},
		Arg: &core.Tuple{T: pairT, Args: []core.Exp{a, b}},
	}
}

// field is "#label e".
func (f *planFixture) field(e core.Exp, label string) core.Exp {
	rec, isRec := e.Type().(*types.Record)
	if !isRec {
		panic("not a record: " + e.Type().String())
	}
	for i, fl := range rec.Fields {
		if fl.Label == label {
			return &core.Apply{
				T: fl.Type,
				Fn: &core.Selector{
					T:     f.sys.Fn(e.Type(), fl.Type),
					Name:  label,
					Index: i,
				},
				Arg: e,
			}
		}
	}
	panic("no field " + label)
}

// leaf is a named collection-typed expression.
func (f *planFixture) leaf(name string, t types.Type) core.Exp {
	return &core.ID{Pat: &core.IDPat{T: t, Name: name}}
}

// deptT is scott.depts' element type, whose moniker is 41
// characters and so is abbreviated.
func (f *planFixture) deptT() types.Type {
	return f.sys.Record([]types.Field{
		{Label: "deptno", Type: f.sys.Int},
		{Label: "dname", Type: f.sys.String},
		{Label: "loc", Type: f.sys.String},
	})
}

func TestRelPlan(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	ints := &core.List{T: sys.List(sys.Int), Args: []core.Exp{
		f.i(1), f.i(2), f.i(3),
	}}
	depts := f.leaf("depts", sys.Bag(f.deptT()))
	dept0 := core.NewInput(f.deptT(), 0)
	tests := []struct {
		name string
		rel  core.Exp
		want string
	}{{
		// A leaf is a bare expression and holds no name: the
		// binder "i" is gone, and the filter reads the element as
		// "$0".
		name: "from i in [1, 2, 3] where i > 1",
		rel: core.NewFilter(ints,
			f.binop("op >", core.NewInput(sys.Int, 0), f.i(1),
				sys.Bool)),
		want: "" +
			"filter [$0 > 1] : int list\n" +
			"  [1, 2, 3] : int list\n",
	}, {
		// The element type is read off the expression, so a field
		// access prints as "#deptno $0". The type is 41
		// characters, so it prints as a reference and once in the
		// legend.
		name: "a leaf whose element is a record",
		rel: core.NewProject(sys,
			core.NewFilter(depts,
				f.binop("op >", f.field(dept0, "deptno"), f.i(20),
					sys.Bool)),
			f.field(dept0, "dname")),
		want: "" +
			"project [#dname $0] : string bag\n" +
			"  filter [#deptno $0 > 20] : t$0\n" +
			"    depts : t$0\n" +
			"\n" +
			"t$0 {deptno:int, dname:string, loc:string} bag\n",
	}, {
		// A projection whose expression is "$0" carries no
		// information, so its argument is omitted.
		name: "a projection of the element itself",
		rel: core.NewProject(sys, ints,
			core.NewInput(sys.Int, 0)),
		want: "" +
			"project : int list\n" +
			"  [1, 2, 3] : int list\n",
	}, {
		// An inner join's kind and a true condition are omitted;
		// the components stay flat.
		name: "an inner join with no condition",
		rel: core.NewJoin(sys, core.InnerJoin, nil, ints, ints,
			&core.Literal{
				T: sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
			}),
		want: "" +
			"join : (int * int) list\n" +
			"  [1, 2, 3] : int list\n" +
			"  [1, 2, 3] : int list\n",
	}, {
		// A left join writes its kind.
		name: "a left join writes its kind",
		rel: core.NewJoin(sys, core.LeftJoin, nil, ints, ints,
			f.binop("op =", core.NewInput(sys.Int, 0),
				core.NewInput(sys.Int, 1), sys.Bool)),
		want: "" +
			"join [left] [$0 = $1] : (int * int option) list\n" +
			"  [1, 2, 3] : int list\n" +
			"  [1, 2, 3] : int list\n",
	}, {
		// A group writes its keys and its aggregates, each list
		// in brackets of its own.
		name: "a group writes keys and aggregates",
		rel: core.NewGroup(sys, depts,
			[]core.RelGroupKey{{
				Label: "loc", Exp: f.field(dept0, "loc"),
			}},
			[]core.RelGroupAgg{{
				Label: "c",
				Fn: f.leaf("count",
					sys.Fn(sys.List(sys.Int), sys.Int)),
				T: sys.Int,
			}}),
		want: "" +
			"group [loc = #loc $0] [c = count over ()] : " +
			"{c:int, loc:string} bag\n" +
			"  depts : t$0\n" +
			"\n" +
			"t$0 {deptno:int, dname:string, loc:string} bag\n",
	}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RelPlan(sys, test.rel, 79, true)
			if got != test.want {
				t.Errorf("plan:\n got:\n%s\nwant:\n%s", got,
					test.want)
			}
		})
	}
}

// TestRelPlanWraps covers §6.3's continuation rule: a node line
// that does not fit wraps four from the node, two deeper than its
// inputs, so a continuation cannot be read as a child.
func TestRelPlanWraps(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	depts := f.leaf("depts", sys.Bag(f.deptT()))
	dept0 := core.NewInput(f.deptT(), 0)
	// "andalso" is where a long condition is worth breaking; it
	// is a group of its own, so a condition that fits stays on
	// its line.
	cond := composeConjuncts(sys, []core.Exp{
		f.binop("op =", f.field(dept0, "dname"), f.str("SALES"),
			sys.Bool),
		f.binop("op =", f.field(dept0, "loc"), f.str("CHICAGO"),
			sys.Bool),
	})
	rel := core.NewProject(sys, core.NewFilter(depts, cond),
		f.field(dept0, "dname"))
	want := "" +
		"project [#dname $0] : string bag\n" +
		"  filter [#dname $0 = \"SALES\"\n" +
		"      andalso #loc $0 = \"CHICAGO\"] : t$0\n" +
		"    depts : t$0\n" +
		"\n" +
		"t$0 {deptno:int, dname:string, loc:string} bag\n"
	if got := RelPlan(sys, rel, 50, true); got != want {
		t.Errorf("narrow:\n got:\n%s\nwant:\n%s", got, want)
	}
	// The same tree within a width that fits keeps the condition
	// on its line: layout is not a function of the width for the
	// tree, only for a node's own line.
	wide := "" +
		"project [#dname $0] : string bag\n" +
		"  filter [#dname $0 = \"SALES\" andalso " +
		"#loc $0 = \"CHICAGO\"] : t$0\n" +
		"    depts : t$0\n" +
		"\n" +
		"t$0 {deptno:int, dname:string, loc:string} bag\n"
	if got := RelPlan(sys, rel, 79, true); got != wide {
		t.Errorf("wide:\n got:\n%s\nwant:\n%s", got, wide)
	}
}

// TestRelPlanDecl pins what Sys.planEx will print once grounding
// works on the tree: "val it =", then the tree, at indent two.
//
// The text is morel-java's, from "optimize.smli" on the 449-tree
// branch, for "from x where abs x < 5" replanned to phase 3.
// morel-go produces it today; what it does not yet produce is the
// same *tree* for every query, because its grounding is still the
// step list's. See plan.md M6.
func TestRelPlanDecl(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	ints := &core.List{T: sys.List(sys.Int), Args: []core.Exp{
		f.i(1), f.i(2),
	}}
	rel := core.NewFilter(ints,
		f.binop("op <", core.NewInput(sys.Int, 0), f.i(5), sys.Bool))
	decl := &core.NonRecValDecl{
		Pat: &core.IDPat{T: rel.Type(), Name: "it"}, Exp: rel,
	}
	// A tree's every line ends in a newline, as morel-java's does.
	want := "val it =\n" +
		"  filter [$0 < 5] : int list\n" +
		"    [1, 2] : int list\n"
	if got := RelPlanDecl(sys, decl, 79, false); got != want {
		t.Errorf("planEx:\n got:\n%s\nwant:\n%s", got, want)
	}
	// A declaration whose value is not a query prints as it always
	// did, on one line.
	plain := &core.NonRecValDecl{
		Pat: &core.IDPat{T: sys.Int, Name: "it"}, Exp: f.i(3),
	}
	if got := RelPlanDecl(sys, plain, 79, false); got != "val it = 3" {
		t.Errorf("planEx of a value = %q, want \"val it = 3\"", got)
	}
}
