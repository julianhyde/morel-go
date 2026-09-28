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

package core

import (
	"fmt"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/token"
	"github.com/hydromatic/morel-go/internal/types"
)

// The relational tree: morel#449's replacement for the step list
// a query is written as.
//
// A node denotes a collection. Its type is a *kind* -- list or bag
// -- applied to an element type, which may be any Morel type at
// any depth. There is no binding list, no "atom" flag, and no
// row/scalar distinction: an element's type is the type of the
// expression that constructs it, so nothing has to decide.
//
// The tree is a closed algebra: every constructor takes
// collections and returns a collection. A node *is* an
// expression, so a query may appear wherever an expression may,
// and an input needs no wrapper -- a leaf is simply an expression
// of collection type, and the boundary between the tree and the
// rest of Core is wherever the expression stops being a node.
//
// An expression inside a node may name the environment enclosing
// the tree, "$0" (the element of the node's input) and, for a
// join, "$1" (the element of its right input). It may not reach
// the elements of nodes further down: "$0" is rebound by every
// node and does not accumulate.
//
// The contract is morel-java's spec.md, which is frozen, and the
// conformance suite is "rel-tree.smli". Where this disagrees with
// the spec, the spec is right.

// Rel is a node of the relational tree: an expression whose type
// is a collection type.
type Rel interface {
	Exp

	// OpName is the node's operator as plan text writes it.
	OpName() string

	// Inputs are the node's inputs, in the order plan text writes
	// them.
	Inputs() []Exp

	rel()
}

// relBase is what every node has: its type, which carries both the
// element type and the kind.
type relBase struct {
	T types.Type
}

// Type implements Exp.
func (r *relBase) Type() types.Type { return r.T }

func (*relBase) exp() {}
func (*relBase) rel() {}

// ElemType is the type of the elements a node emits.
func ElemType(r Rel) types.Type { return types.ElemOf(r.Type()) }

// Input is "$0" or "$1": the element of a node's input. It is
// never a record label, never appears in an element type, and is
// not an ordinal encoding of a field -- fields are addressed by
// label, inputs by position.
type Input struct {
	T types.Type
	// Ordinal is 0 for the input's element, and 1 for the right
	// input's element, which only a join has.
	Ordinal int
}

// Op implements Exp.
func (*Input) Op() ast.Op { return ast.InputOp }

// Type implements Exp.
func (i *Input) Type() types.Type { return i.T }

// Name is "$0" or "$1", as plan text writes it.
func (i *Input) Name() string { return fmt.Sprintf("$%d", i.Ordinal) }

func (*Input) exp() {}

// Filter drops the elements for which a condition, an expression
// over "$0", is false.
type Filter struct {
	relBase

	Input     Exp
	Condition Exp
}

// Op implements Exp.
func (*Filter) Op() ast.Op { return ast.FilterOp }

// OpName implements Rel.
func (*Filter) OpName() string { return "filter" }

// Inputs implements Rel.
func (f *Filter) Inputs() []Exp { return []Exp{f.Input} }

// Project maps each element to one element, via an expression
// over "$0".
type Project struct {
	relBase

	Input Exp
	Exp   Exp
}

// Op implements Exp.
func (*Project) Op() ast.Op { return ast.ProjectOp }

// OpName implements Rel.
func (*Project) OpName() string { return "project" }

// Inputs implements Rel.
func (p *Project) Inputs() []Exp { return []Exp{p.Input} }

// JoinType is which side of a join may be absent from a row.
type JoinType int

// The four join kinds. A side the kind can leave absent has each
// of its components wrapped in "option".
const (
	InnerJoin JoinType = iota
	LeftJoin
	RightJoin
	FullJoin
)

// OpName is the kind as plan text writes it, and is empty for an
// inner join, which plan text omits.
func (k JoinType) OpName() string {
	// lint: sort until '^\t}' where '^\tcase '
	switch k {
	case FullJoin:
		return "full"
	case LeftJoin:
		return "left"
	case RightJoin:
		return "right"
	default:
		return ""
	}
}

// LeftIsOption reports whether the join may leave its left side
// absent.
func (k JoinType) LeftIsOption() bool {
	return k == RightJoin || k == FullJoin
}

// RightIsOption reports whether the join may leave its right side
// absent.
func (k JoinType) RightIsOption() bool {
	return k == LeftJoin || k == FullJoin
}

// Join pairs the elements of two inputs. Its element is the
// inputs' components concatenated, so "(A join B) join C" and
// "A join (B join C)" have the same three components in the same
// order: reassociating changes no type, and nothing above the node
// rewrites.
//
// The condition is evaluated on candidate pairs, where both
// elements are present, so it sees "$0 : t0" and "$1 : t1"
// whatever the kind. The element is the output row, including rows
// that matched nothing, so a component of a side the kind can
// leave absent is an option. That asymmetry is what makes
// "on a = b" mean what it says.
type Join struct {
	relBase

	Kind JoinType
	// Binder names the left element inside Right, or is nil where
	// the right input does not read it.
	//
	// This is what makes a join *dependent*: the right input is a
	// tree of its own and rebinds "$0", so it cannot say "$0" and
	// mean the left element. The binder is in scope in Right only,
	// never in Condition, which says "$0" and "$1" as any join's
	// does.
	//
	// It is a scoping device, not a mode: dependence is a free
	// occurrence of it, and decorrelation is dropping it.
	Binder    *IDPat
	Left      Exp
	Right     Exp
	Condition Exp
}

// Op implements Exp.
func (*Join) Op() ast.Op { return ast.JoinOp }

// OpName implements Rel.
func (*Join) OpName() string { return "join" }

// Inputs implements Rel.
func (j *Join) Inputs() []Exp { return []Exp{j.Left, j.Right} }

// RelGroupKey is one grouping key: a label, and an expression over
// "$0".
//
// Pat is the variable the query bound the key to, which an
// aggregate of the same group may name -- "group k compute
// {x = (fn vs => k + List.length vs) over v}". A tree has no
// names, and the plan text reads the label; but a reference is to
// a pattern, and morel-go compares patterns by identity where
// morel-java compares them by name and type, so a lowering that
// minted a fresh pattern of the same name would leave the
// reference dangling.
type RelGroupKey struct {
	Label string
	Exp   Exp
	Pat   *IDPat
}

// RelGroupAgg is one aggregate: a label, the aggregate function,
// and the expression over "$0" it is applied to, which is nil for
// an aggregate over the whole element.
//
// Span is where the aggregate was written. An exception it raises
// -- Empty, from the minimum of no rows -- is reported there, so
// the tree carries it even though nothing about the tree needs
// it: a node the lowering rebuilds without it answers correctly
// and blames nowhere.
type RelGroupAgg struct {
	Label string
	Fn    Exp
	Arg   Exp
	T     types.Type
	Span  token.Span
}

// Group groups elements by zero or more keys, computing zero or
// more aggregates. Its element is a record of the keys and the
// aggregates -- a record whether there is one label or many,
// because an element whose *shape* depended on how many labels
// there were would make dropping one of two labels a change of
// shape that nothing above could rewrite locally. Where a query
// wants the bare value, a projection of the field says so, and a
// projection is a node a rule can see.
//
// "distinct" is this node with the whole element as its only key
// and no aggregates; "compute" is this node with no keys, plus the
// extraction of the single element that the enclosing expression
// performs.
type Group struct {
	relBase

	Input Exp
	// Keys and Aggs are in label order, and their labels are
	// distinct.
	Keys []RelGroupKey
	Aggs []RelGroupAgg
}

// Op implements Exp.
func (*Group) Op() ast.Op { return ast.GroupOp }

// OpName implements Rel.
func (*Group) OpName() string { return "group" }

// Inputs implements Rel.
func (g *Group) Inputs() []Exp { return []Exp{g.Input} }

// Sort orders elements by an expression over "$0". Its output is
// always a list.
type Sort struct {
	relBase

	Input Exp
	Exp   Exp
	// Span is where the query wrote the ordering expression. An
	// exception the sort raises -- comparison not defined, from
	// ordering by a function -- is reported there, so the tree
	// carries it even though nothing about the tree needs it: a
	// node the lowering rebuilds without it answers correctly and
	// blames nowhere. RelGroupAgg carries one for the same
	// reason, and morel-java's nodes are all built at position
	// zero, so this is morel-go's to keep.
	Span token.Span
}

// Op implements Exp.
func (*Sort) Op() ast.Op { return ast.SortOp }

// OpName implements Rel.
func (*Sort) OpName() string { return "sort" }

// Inputs implements Rel.
func (s *Sort) Inputs() []Exp { return []Exp{s.Input} }

// Unorder forgets an ordering. Its output is always a bag.
// With Sort it is the pair that unorder pushdown manipulates, and
// the reason kinds are in a node's type rather than a property of
// the runtime value.
type Unorder struct {
	relBase

	Input Exp
}

// Op implements Exp.
func (*Unorder) Op() ast.Op { return ast.UnorderOp }

// OpName implements Rel.
func (*Unorder) OpName() string { return "unorder" }

// Inputs implements Rel.
func (u *Unorder) Inputs() []Exp { return []Exp{u.Input} }

// Skip drops the first Count elements. The count is evaluated
// once, before any element exists, so it cannot mention "$0".
type Skip struct {
	relBase

	Input Exp
	Count Exp
}

// Op implements Exp.
func (*Skip) Op() ast.Op { return ast.SkipOp }

// OpName implements Rel.
func (*Skip) OpName() string { return "skip" }

// Inputs implements Rel.
func (s *Skip) Inputs() []Exp { return []Exp{s.Input} }

// Take keeps the first Count elements. Like Skip's, the
// count is evaluated before any element exists.
type Take struct {
	relBase

	Input Exp
	Count Exp
}

// Op implements Exp.
func (*Take) Op() ast.Op { return ast.TakeOp }

// OpName implements Rel.
func (*Take) OpName() string { return "take" }

// Inputs implements Rel.
func (t *Take) Inputs() []Exp { return []Exp{t.Input} }

// SetKind is which set operator a SetRel is.
type SetKind int

// The three set operators.
const (
	UnionSet SetKind = iota
	IntersectSet
	ExceptSet
)

// OpName is the operator as plan text writes it.
func (k SetKind) OpName() string {
	// lint: sort until '^\t}' where '^\tcase '
	switch k {
	case ExceptSet:
		return "except"
	case IntersectSet:
		return "intersect"
	default:
		return "union"
	}
}

// SetRel is a set operator over n inputs, whose element types must
// all be equal. Distinct is what distinguishes "union" from
// "union all".
type SetRel struct {
	relBase

	Kind     SetKind
	Distinct bool
	Args     []Exp
}

// Op implements Exp.
func (s *SetRel) Op() ast.Op {
	// lint: sort until '^\t}' where '^\tcase '
	switch s.Kind {
	case ExceptSet:
		return ast.ExceptOp
	case IntersectSet:
		return ast.IntersectOp
	default:
		return ast.UnionOp
	}
}

// OpName implements Rel.
func (s *SetRel) OpName() string { return s.Kind.OpName() }

// Inputs implements Rel.
func (s *SetRel) Inputs() []Exp { return s.Args }
