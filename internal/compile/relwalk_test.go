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

//nolint:testpackage // white-box: the walkers are unexported
package compile

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/token"
	"github.com/hydromatic/morel-go/internal/types"
)

// Tests that the passes which walk an expression walk a
// relational node too: morel#449's M7, whose first step is that a
// tree can survive into a pass without being mistaken for a leaf.
//
// Each of these fails silently without the node case, which is
// why they are here. An unwalked node makes the analyzer's use
// count too low, and morel-java's plan.md says what that costs:
// "a use count that is too high only makes the inliner decline,
// while one that is too low makes it substitute across a node
// boundary".

// walkTree is a tree that uses "xs" twice and binds one name: a
// dependent join whose right input reads the binder.
//
//	join [v]
//	  xs
//	  filter [$0 = v] xs
func walkTree(f *planFixture) (core.Exp, *core.IDPat, *core.IDPat) {
	sys := f.sys
	xsPat := &core.IDPat{T: sys.List(sys.Int), Name: "xs"}
	xs := &core.ID{Pat: xsPat}
	binder := &core.IDPat{T: sys.Int, Name: "v"}
	right := core.NewFilter(xs,
		f.binop("=", core.NewInput(sys.Int, 0),
			&core.ID{Pat: binder}, sys.Bool))
	join := core.NewJoin(sys, core.InnerJoin, binder, xs, right,
		&core.Literal{
			T: sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
		})
	return join, xsPat, binder
}

func TestAnalyzerWalksNode(t *testing.T) {
	f := newPlanFixture()
	tree, xsPat, binder := walkTree(f)
	a := &analyzer{uses: map[*core.IDPat]*useInfo{}}
	a.exp(tree)
	if got := a.uses[xsPat]; got == nil || got.count != 2 {
		t.Errorf("xs used twice in the tree, analyzer counted %v",
			countOf(a.uses[xsPat]))
	}
	if got := a.uses[binder]; got == nil || !got.declared {
		t.Errorf("the join declares its binder; analyzer says " +
			"it is free")
	}
}

// countOf renders a use count, or -1 where the variable was not
// seen at all.
func countOf(info *useInfo) int {
	if info == nil {
		return -1
	}
	return info.count
}

func TestCloneCopiesNode(t *testing.T) {
	f := newPlanFixture()
	tree, xsPat, binder := walkTree(f)
	fresh := map[*core.IDPat]*core.IDPat{}
	fresh[xsPat] = &core.IDPat{T: xsPat.T, Name: "ys"}
	copy1 := cloneExp(tree, fresh)
	if copy1 == tree {
		t.Fatal("clone returned the node it was given")
	}
	// The free variable is substituted, and the binder the node
	// declares is renumbered rather than shared.
	a := &analyzer{uses: map[*core.IDPat]*useInfo{}}
	a.exp(copy1)
	if a.uses[xsPat] != nil {
		t.Error("the copy still reads the original's xs")
	}
	if a.uses[fresh[xsPat]] == nil ||
		a.uses[fresh[xsPat]].count != 2 {
		t.Errorf("the copy reads ys twice, analyzer counted %d",
			countOf(a.uses[fresh[xsPat]]))
	}
	if a.uses[binder] != nil {
		t.Error("the copy shares the original's binder")
	}
	if tree.Type() != copy1.Type() {
		t.Errorf("a copy renames and changes no type: %s became %s",
			tree.Type(), copy1.Type())
	}
}

// TestRewriteKeepsGroupKeyPat pins that a rewrite carries a
// group key's pattern across. Dropping it renames the key after
// its label, and the label of a distinct's key is "$0" -- a name
// no query wrote, which then reaches the plan.
func TestRewriteKeepsGroupKeyPat(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	xsPat := &core.IDPat{T: sys.List(sys.Int), Name: "xs"}
	keyPat := &core.IDPat{T: sys.Int, Name: "x"}
	group := core.NewGroup(sys, &core.ID{Pat: xsPat},
		[]core.RelGroupKey{{
			Label: "$0", Exp: core.NewInput(sys.Int, 0),
			Pat: keyPat,
		}}, nil)
	// A rewrite that changes the input, so the group is rebuilt.
	r := &rewriter{sys: sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		if id, isID := e.(*core.ID); isID && id.Pat == xsPat {
			return &core.ID{Pat: &core.IDPat{
				T: xsPat.T, Name: "ys",
			}}, true
		}
		return nil, false
	}
	out := r.rewriteExp(group)
	g, isGroup := out.(*core.Group)
	if !isGroup {
		t.Fatalf("want a group, got %T", out)
	}
	if g == group {
		t.Fatal("the input changed; the group should be rebuilt")
	}
	if g.Keys[0].Pat != keyPat {
		t.Errorf("the rewrite dropped the key's pattern: %v",
			g.Keys[0].Pat)
	}
}

// TestCoverageWalksNode pins that the coverage checker reaches a
// "case" written inside a node's expression. A tree is a query,
// and a query's expressions are ordinary Morel; a non-exhaustive
// match there is as much a warning as one anywhere else, and an
// unwalked node makes it silent.
func TestCoverageWalksNode(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	// "case $0 of 1 => true" -- no branch for anything else.
	kase := &core.Case{
		T:   sys.Bool,
		Exp: core.NewInput(sys.Int, 0),
		Matches: []core.Match{{
			Pat: &core.LiteralPat{
				T: sys.Int, Kind: ast.IntLiteralOp, Value: int32(1),
			},
			Exp: &core.Literal{
				T: sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
			},
		}},
	}
	xs := &core.ID{Pat: &core.IDPat{
		T: sys.List(sys.Int), Name: "xs",
	}}
	tree := core.NewFilter(xs, kase)
	decl := &core.NonRecValDecl{
		Pat: &core.IDPat{T: tree.Type(), Name: "it"}, Exp: tree,
	}
	warnings, err := CheckCoverage(sys, decl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 1 {
		t.Errorf("the filter's case is not exhaustive; want one "+
			"warning, got %d", len(warnings))
	}
}

// TestCompilerLowersNode pins M7's second increment: the compiler
// lowers a tree at its own boundary, so a tree reaching code
// generation runs rather than failing to compile.
//
// The query is "from i in [1, 2, 3] take 2", written as a tree: a
// "take" over a leaf, whose expressions need no built-in, so what
// the test exercises is the lowering and not the environment.
// Nothing hands the compiler a tree yet; this is the door, and
// the test is what says it opens.
func TestCompilerLowersNode(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	ints := &core.List{
		T:    sys.List(sys.Int),
		Args: []core.Exp{f.i(1), f.i(2), f.i(3)},
	}
	tree := core.NewTake(ints, f.i(2))
	c := &compiler{
		values: map[string]eval.Val{},
		slots:  map[*core.IDPat]int{},
		sys:    sys,
	}
	code, err := c.compileExp(tree)
	if err != nil {
		t.Fatalf("a tree should compile, by lowering: %v", err)
	}
	got, err := code.Eval(eval.NewFrame(c.nSlots))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "[1 2]"
	if fmt.Sprint(got) != want {
		t.Errorf("from i in [1, 2, 3] take 2 = %v, want %s",
			got, want)
	}
}

// TestUnparseLowersNode pins that the printer draws the same
// boundary the compiler does: outside tree mode it writes Morel,
// and Morel has no node, so a tree prints as the query it lowers
// to.
//
// This is what lets the resolver start returning a tree without
// moving a line of the corpus: "Sys.planEx" prints what it
// printed. Once planEx prints the tree itself, this goes.
func TestUnparseLowersNode(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	ints := &core.List{
		T:    sys.List(sys.Int),
		Args: []core.Exp{f.i(1), f.i(2), f.i(3)},
	}
	tree := core.NewTake(ints, f.i(2))
	decl := &core.NonRecValDecl{
		Pat: &core.IDPat{T: tree.Type(), Name: "it"}, Exp: tree,
	}
	got := UnparseDecl(sys, decl)
	want := "val it = from v$0 in [1, 2, 3] take 2"
	if got != want {
		t.Errorf("unparse of a tree:\n got %s\nwant %s", got, want)
	}
}

// TestInlinerRewritesNode pins that the inliner can rebuild a
// node. Its rewriter had no type system, which was harmless while
// nothing rewrote a node and a nil dereference the moment the
// resolver returned one -- a node's type is derived from its
// parts, so rebuilding one needs the system that derives it.
func TestInlinerRewritesNode(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	// "let val xs = [1, 2, 3] in sort [$0] xs end", as a tree
	// whose leaf is the variable the inliner substitutes. A
	// "sort", because its constructor derives the node's type and
	// so is one that needs the system; a "take" carries its
	// input's type across and would not have caught this.
	xsPat := &core.IDPat{T: sys.List(sys.Int), Name: "xs"}
	ints := &core.List{
		T:    sys.List(sys.Int),
		Args: []core.Exp{f.i(1), f.i(2), f.i(3)},
	}
	tree := core.NewSort(sys, &core.ID{Pat: xsPat},
		core.NewInput(sys.Int, 0), token.Span{})
	decl := &core.NonRecValDecl{
		Pat: &core.IDPat{T: tree.Type(), Name: "it"},
		Exp: &core.Let{
			Decl: &core.NonRecValDecl{Pat: xsPat, Exp: ints},
			Exp:  tree,
		},
	}
	env := &InlineEnv{
		Exps:  map[string]core.Exp{},
		Known: func(string) bool { return false },
	}
	out := Inline(sys, decl, env, 2)
	got := UnparseDecl(sys, out)
	want := "val it = from v$0 in [1, 2, 3] order v$0"
	if got != want {
		t.Errorf("the inliner should substitute into the node:"+
			"\n got %s\nwant %s", got, want)
	}
}

// TestTranslateBindsNestedTree pins that the translation mints a
// binder for the element before a *nested tree* can read it.
//
// A node rebinds "$0" to its own input, so an access expression
// written inside one reads the inner element; and once written
// there is nothing to tell it from the node's own "$0", because
// both are the same text. So the binder is minted in the same
// pass that writes "$0": "v$N" where the substitution crosses
// into a node, "$0" everywhere else.
//
// A nested *step list* needs none of this -- it has no "$0" of
// its own -- and `bindRow` puts that binding in afterwards.
func TestTranslateBindsNestedTree(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	xsPat := &core.IDPat{T: sys.List(sys.Int), Name: "xs"}
	ysPat := &core.IDPat{T: sys.List(sys.Int), Name: "ys"}
	ePat := &core.IDPat{T: sys.Int, Name: "e"}
	// The condition is "p (sort [$0] (filter [$0 = e] ys))": a
	// tree, nested in an expression, that reads the outer scan's
	// variable.
	nested := core.NewSort(sys,
		core.NewFilter(&core.ID{Pat: ysPat},
			f.binop("=", core.NewInput(sys.Int, 0),
				&core.ID{Pat: ePat}, sys.Bool)),
		core.NewInput(sys.Int, 0), token.Span{})
	pPat := &core.IDPat{
		T: sys.Fn(sys.List(sys.Int), sys.Bool), Name: "p",
	}
	cond := &core.Apply{
		T: sys.Bool, Fn: &core.ID{Pat: pPat}, Arg: nested,
	}
	from := &core.From{
		T:    sys.List(sys.Int),
		Kind: ast.FromOp,
		Steps: []core.FromStep{
			&core.Scan{
				Pat: ePat, Exp: &core.ID{Pat: xsPat},
				Join: ast.ScanOp,
			},
			&core.Where{Exp: cond},
		},
	}
	tree, reason := TranslateFrom(sys, from)
	if tree == nil {
		t.Fatalf("translate declined: %s", reason)
	}
	got := UnparseDecl(sys, &core.NonRecValDecl{
		Pat: &core.IDPat{T: tree.Type(), Name: "it"}, Exp: tree,
	})
	// The element is bound, and the nested tree reads the name.
	// Without the binder the nested tree says "$0", which is its
	// own element -- "e = e", which is always true.
	if !strings.Contains(got, "val v$1 = v$0 ") {
		t.Errorf("no binder minted for the nested tree: %s", got)
	}
	if strings.Contains(got, "v$2, v$2") {
		t.Errorf("the nested tree captured the element: %s", got)
	}
}

// TestTranslateLeavesIndependentTree is the other side of
// TestTranslateBindsNestedTree: a nested tree that reads nothing
// the enclosing query binds needs no binder, and minting one
// anyway leaves "let val v$0 = $0" where a collection belongs --
// which is what a set operator's branch looked like, and what
// reached code generation as a bare "$0".
func TestTranslateLeavesIndependentTree(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	xsPat := &core.IDPat{T: sys.List(sys.Int), Name: "xs"}
	ysPat := &core.IDPat{T: sys.List(sys.Int), Name: "ys"}
	ePat := &core.IDPat{T: sys.Int, Name: "e"}
	// "p (sort [$0] ys)": a tree, and independent of the row.
	nested := core.NewSort(sys, &core.ID{Pat: ysPat},
		core.NewInput(sys.Int, 0), token.Span{})
	pPat := &core.IDPat{
		T: sys.Fn(sys.List(sys.Int), sys.Bool), Name: "p",
	}
	from := &core.From{
		T:    sys.List(sys.Int),
		Kind: ast.FromOp,
		Steps: []core.FromStep{
			&core.Scan{
				Pat: ePat, Exp: &core.ID{Pat: xsPat},
				Join: ast.ScanOp,
			},
			&core.Where{Exp: &core.Apply{
				T: sys.Bool, Fn: &core.ID{Pat: pPat}, Arg: nested,
			}},
		},
	}
	tree, reason := TranslateFrom(sys, from)
	if tree == nil {
		t.Fatalf("translate declined: %s", reason)
	}
	got := UnparseDecl(sys, &core.NonRecValDecl{
		Pat: &core.IDPat{T: tree.Type(), Name: "it"}, Exp: tree,
	})
	if strings.Contains(got, "val v$0 = ") {
		t.Errorf("a binder was minted for an independent tree: %s",
			got)
	}
}

// TestInlinerKeepsElementBinding pins that the inliner leaves
// "let val v$0 = $0" alone. The binding is atomic, so the inliner
// would substitute it -- and putting "$0" back inside the node it
// was minted to cross makes the node's own element answer, which
// is the capture the binder exists to prevent.
func TestInlinerKeepsElementBinding(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	ysPat := &core.IDPat{T: sys.List(sys.Int), Name: "ys"}
	v := &core.IDPat{T: sys.Int, Name: "v$0"}
	pPat := &core.IDPat{
		T: sys.Fn(sys.List(sys.Int), sys.Bool), Name: "p",
	}
	// "filter [let val v$0 = $0 in p (filter [$0 = v$0] ys) end] xs"
	inner := core.NewFilter(&core.ID{Pat: ysPat},
		f.binop("=", core.NewInput(sys.Int, 0),
			&core.ID{Pat: v}, sys.Bool))
	cond := &core.Let{
		Decl: &core.NonRecValDecl{
			Pat: v, Exp: core.NewInput(sys.Int, 0),
		},
		Exp: &core.Apply{
			T: sys.Bool, Fn: &core.ID{Pat: pPat}, Arg: inner,
		},
	}
	xsPat := &core.IDPat{T: sys.List(sys.Int), Name: "xs"}
	tree := core.NewFilter(&core.ID{Pat: xsPat}, cond)
	decl := &core.NonRecValDecl{
		Pat: &core.IDPat{T: tree.Type(), Name: "it"}, Exp: tree,
	}
	env := &InlineEnv{
		Exps:  map[string]core.Exp{},
		Known: func(string) bool { return false },
	}
	out := Inline(sys, decl, env, 2)
	nv, isVal := out.(*core.NonRecValDecl)
	if !isVal {
		t.Fatalf("want a value declaration, got %T", out)
	}
	filter, isFilter := nv.Exp.(*core.Filter)
	if !isFilter {
		t.Fatalf("want a filter, got %T", nv.Exp)
	}
	if _, kept := filter.Condition.(*core.Let); !kept {
		t.Errorf("the inliner substituted the element binding "+
			"away: %T", filter.Condition)
	}
}

// TestContainsUnboundedSeesLeaf pins that the gate which decides
// whether grounding runs at all sees an unbounded *leaf*, not
// only an unbounded scan.
//
// The translation makes trees of nested queries -- it must, or
// "Sys.planOf" could not print the "r$N" relations rel-tree.smli
// pins -- so a query that needs grounding can end up inside a
// tree with no scan of it left. Reading only the steps, the gate
// said no, grounding never ran, and the extent reached the
// evaluator as "infinite: int".
func TestContainsUnboundedSeesLeaf(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	extent := &core.Apply{
		T: sys.List(sys.Int),
		Fn: &core.ID{Pat: &core.IDPat{
			T: sys.Fn(sys.Int, sys.List(sys.Int)), Name: ExtentName,
		}},
		Arg: &core.Literal{
			T: sys.Int, Kind: ast.IntLiteralOp,
			Value: eval.NewRangeExtent(sys, sys.Int, nil),
		},
	}
	tru := &core.Literal{
		T: sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
	}
	decl := func(leaf core.Exp) core.Decl {
		tree := core.NewFilter(leaf, tru)
		return &core.NonRecValDecl{
			Pat: &core.IDPat{T: tree.Type(), Name: "it"}, Exp: tree,
		}
	}
	if !ContainsUnbounded(decl(extent)) {
		t.Error("a filter over an extent leaf needs grounding")
	}
	ints := &core.List{
		T:    sys.List(sys.Int),
		Args: []core.Exp{f.i(1), f.i(2)},
	}
	if ContainsUnbounded(decl(ints)) {
		t.Error("a filter over a list leaf needs no grounding")
	}
}

// TestSortKeepsSpan pins that a sort's position survives the
// round trip. The "order" step carries where the query wrote its
// expression, and an exception the sort raises -- comparison not
// defined, from ordering by a function -- is reported there. A
// node that lost it answers correctly and blames nowhere, and a
// compile error with no position prints nothing in a script, so
// the message vanished rather than moved.
//
// morel-java builds every node at position zero, so this is
// morel-go's to keep; "RelGroupAgg" already keeps one for the
// same reason.
func TestSortKeepsSpan(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	span := token.Span{
		Start: token.Pos{Line: 1, Col: 21},
		End:   token.Pos{Line: 1, Col: 32},
	}
	iPat := &core.IDPat{T: sys.Int, Name: "i"}
	from := &core.From{
		T:    sys.List(sys.Int),
		Kind: ast.FromOp,
		Steps: []core.FromStep{
			&core.Scan{
				Pat: iPat,
				Exp: &core.List{
					T: sys.List(sys.Int), Args: []core.Exp{f.i(1)},
				},
				Join: ast.ScanOp,
			},
			&core.Order{Exp: &core.ID{Pat: iPat}, Span: span},
		},
	}
	tree, reason := TranslateFrom(sys, from)
	if tree == nil {
		t.Fatalf("translate declined: %s", reason)
	}
	sort, isSort := tree.(*core.Sort)
	if !isSort {
		t.Fatalf("want a sort, got %T", tree)
	}
	if sort.Span != span {
		t.Errorf("the translation dropped the span: %v", sort.Span)
	}
	lowered, lr := LowerRel(sys, sort)
	out, isFrom := lowered.(*core.From)
	if !isFrom {
		t.Fatalf("lower declined: %s", lr)
	}
	for _, step := range out.Steps {
		if order, isOrder := step.(*core.Order); isOrder {
			if order.Span != span {
				t.Errorf("the lowering dropped the span: %v",
					order.Span)
			}
			return
		}
	}
	t.Error("the lowering left no order step")
}

// TestRelLeafPats pins that a tree can say what its leaves were
// called. A tree holds no names, but a query with several binders
// ends in a projection naming its element's components after
// them, and a join concatenates its inputs' components, so
// component k is leaf k.
//
// Grounding needs this: it names what it builds after the leaf it
// bounds, and once the resolver returns a tree there is no step
// list to read the names from.
func TestRelLeafPats(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	xs := &core.ID{Pat: &core.IDPat{
		T: sys.List(sys.Int), Name: "xs",
	}}
	ys := &core.ID{Pat: &core.IDPat{
		T: sys.List(sys.String), Name: "ys",
	}}
	join := core.NewJoin(sys, core.InnerJoin, nil, xs, ys,
		&core.Literal{
			T: sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
		})
	listT, isList := join.Type().(*types.List)
	if !isList {
		t.Fatalf("want a list, got %s", join.Type())
	}
	elem := listT.Elem
	recT := sys.Record([]types.Field{
		{Label: "a", Type: sys.Int},
		{Label: "b", Type: sys.String},
	})
	project := core.NewProject(sys, join, &core.Tuple{
		T: recT,
		Args: []core.Exp{
			fieldAt(sys, core.NewInput(elem, 0), 0),
			fieldAt(sys, core.NewInput(elem, 0), 1),
		},
	})
	pats := RelLeafPats(project)
	if len(pats) != 2 {
		t.Fatalf("want two leaf names, got %d", len(pats))
	}
	if pats[0].Name != "a" || pats[0].T != sys.Int {
		t.Errorf("leaf 0 is %s : %s, want a : int",
			pats[0].Name, pats[0].T)
	}
	if pats[1].Name != "b" || pats[1].T != sys.String {
		t.Errorf("leaf 1 is %s : %s, want b : string",
			pats[1].Name, pats[1].T)
	}
	// A query with one binder has no such projection, and its
	// name is simply gone -- as it is in morel-java.
	if got := RelLeafPats(core.NewFilter(xs, &core.Literal{
		T: sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
	})); got != nil {
		t.Errorf("a filter names no leaf, got %v", got)
	}
}

// TestGrounderGroundsNode pins that the grounder sees a tree.
//
// A tree with an unbounded leaf and nothing to bound it by is not
// grounded, and says so. Today the grounder walked past it, and
// the extent reached the evaluator as "infinite: int" -- which is
// what the M7 probe produced until the gate and this both learned
// a node.
func TestGrounderGroundsNode(t *testing.T) {
	f := newPlanFixture()
	sys := f.sys
	extent := &core.Apply{
		T: sys.List(sys.Int),
		Fn: &core.ID{Pat: &core.IDPat{
			T: sys.Fn(sys.Int, sys.List(sys.Int)), Name: ExtentName,
		}},
		Arg: &core.Literal{
			T: sys.Int, Kind: ast.IntLiteralOp,
			Value: eval.NewRangeExtent(sys, sys.Int, nil),
		},
	}
	tree := core.NewFilter(extent, &core.Literal{
		T: sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
	})
	decl := &core.NonRecValDecl{
		Pat: &core.IDPat{T: tree.Type(), Name: "it"}, Exp: tree,
	}
	_, err := Ground(decl, sys, nil)
	if err == nil {
		t.Fatal("a tree whose leaf nothing bounds is not grounded")
	}
	// The tree does not name this leaf -- a query with one binder
	// has no projection to read a name back from -- so the
	// message says what it can, which is the one the step list's
	// engine uses in the same position.
	if !strings.Contains(err.Error(), "cannot enumerate") {
		t.Errorf("want an enumerate error, got %v", err)
	}
	// Where the tree does name it, the message does too.
	xs := &core.ID{Pat: &core.IDPat{
		T: sys.List(sys.Int), Name: "xs",
	}}
	join := core.NewJoin(sys, core.InnerJoin, nil, xs, extent,
		&core.Literal{
			T: sys.Bool, Kind: ast.BoolLiteralOp, Value: true,
		})
	joinT, isList := join.Type().(*types.List)
	if !isList {
		t.Fatalf("want a list, got %s", join.Type())
	}
	recT := sys.Record([]types.Field{
		{Label: "a", Type: sys.Int},
		{Label: "b", Type: sys.Int},
	})
	named := core.NewProject(sys, join, &core.Tuple{
		T: recT,
		Args: []core.Exp{
			fieldAt(sys, core.NewInput(joinT.Elem, 0), 0),
			fieldAt(sys, core.NewInput(joinT.Elem, 0), 1),
		},
	})
	_, err = Ground(&core.NonRecValDecl{
		Pat: &core.IDPat{T: named.Type(), Name: "it"}, Exp: named,
	}, sys, nil)
	if err == nil || !strings.Contains(err.Error(),
		"pattern 'b' is not grounded") {
		t.Errorf("want the second leaf named, got %v", err)
	}
}
