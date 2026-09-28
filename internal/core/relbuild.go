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
	"slices"

	"github.com/hydromatic/morel-go/internal/token"
	"github.com/hydromatic/morel-go/internal/types"
)

// Constructors for the relational tree, which derive each node's
// type from its inputs and arguments. They are the whole of
// morel#449 spec.md §3 (element types) and §4 (kinds): a node's
// type is derived, never supplied, so a tree cannot be built whose
// type disagrees with its shape.
//
// A constructor panics where its arguments cannot make a node at
// all -- an input that is not a collection, a condition that is
// not bool. The validator (spec §5) checks the rest, and is where
// a rule's mistake is caught; these guards are for a caller that
// has gone wrong in the one way a type derivation cannot absorb.

// NewInput is "$0" or "$1": the element of a node's input.
func NewInput(elemType types.Type, ordinal int) *Input {
	return &Input{T: elemType, Ordinal: ordinal}
}

// NewFilter drops the elements for which a bool expression over
// "$0" is false. Its type is its input's.
func NewFilter(input, condition Exp) *Filter {
	checkCollection(input, "filter input")
	checkBool(condition, "filter condition")
	return &Filter{
		relBase: relBase{T: input.Type()},
		Input:   input, Condition: condition,
	}
}

// NewProject maps each element through an expression over "$0".
// The element type is the type of that expression, and the kind is
// the input's.
func NewProject(sys *types.System, input, exp Exp) *Project {
	checkCollection(input, "project input")
	t := sys.CollectionOf(types.IsOrdered(input.Type()), exp.Type())
	return &Project{
		relBase: relBase{T: t}, Input: input, Exp: exp,
	}
}

// NewJoin pairs two inputs. A binder, where the right input reads
// it, names the left element there and makes the join dependent;
// where the right input does not read it, the caller should pass
// nil, so that "binder != nil" is a reliable test for dependence
// and two trees that mean the same thing do not print differently.
//
// The kind decides which side's components are options, and the
// output is a list only where both inputs are.
func NewJoin(sys *types.System, kind JoinType, binder *IDPat,
	left, right, condition Exp,
) *Join {
	checkCollection(left, "join left input")
	checkCollection(right, "join right input")
	checkBool(condition, "join condition")
	ordered := types.IsOrdered(left.Type()) &&
		types.IsOrdered(right.Type())
	t := sys.CollectionOf(ordered,
		JoinElemType(sys, kind, left, right))
	return &Join{
		relBase: relBase{T: t}, Kind: kind, Binder: binder,
		Left: left, Right: right, Condition: condition,
	}
}

// JoinElemType is a join's element type: its inputs' components in
// order, each component of a side the kind can leave absent
// wrapped in "option".
//
// The wrapping is per component, which is Morel's own rule:
// "left join (j, k) in pairs" binds "j : int option" and
// "k : int option", not "(int * int) option". It is additive, so
// two chained outer joins give "int option option".
func JoinElemType(sys *types.System, kind JoinType,
	left, right Exp,
) types.Type {
	lefts, rights := ComponentTypes(left), ComponentTypes(right)
	args := make([]types.Type, 0, len(lefts)+len(rights))
	for _, t := range lefts {
		if kind.LeftIsOption() {
			t = sys.Named("option", t)
		}
		args = append(args, t)
	}
	for _, t := range rights {
		if kind.RightIsOption() {
			t = sys.Named("option", t)
		}
		args = append(args, t)
	}
	return sys.Tuple(args...)
}

// NewGroup groups by keys and computes aggregates. The element
// is a record of them, whether there is one label or many; see
// RelGroup.
func NewGroup(sys *types.System, input Exp, keys []RelGroupKey,
	aggs []RelGroupAgg,
) *Group {
	checkCollection(input, "group input")
	fields := make([]types.Field, 0, len(keys)+len(aggs))
	for _, k := range keys {
		fields = append(fields,
			types.Field{Label: k.Label, Type: k.Exp.Type()})
	}
	for _, a := range aggs {
		fields = append(fields,
			types.Field{Label: a.Label, Type: a.T})
	}
	slices.SortFunc(fields, func(a, b types.Field) int {
		switch {
		case types.LabelLess(a.Label, b.Label):
			return -1
		case types.LabelLess(b.Label, a.Label):
			return 1
		default:
			panic("duplicate label in group: " + a.Label)
		}
	})
	t := sys.CollectionOf(types.IsOrdered(input.Type()),
		sys.Record(fields))
	keys = slices.Clone(keys)
	aggs = slices.Clone(aggs)
	slices.SortFunc(keys, func(a, b RelGroupKey) int {
		return labelCmp(a.Label, b.Label)
	})
	slices.SortFunc(aggs, func(a, b RelGroupAgg) int {
		return labelCmp(a.Label, b.Label)
	})
	return &Group{
		relBase: relBase{T: t}, Input: input,
		Keys: keys, Aggs: aggs,
	}
}

// labelCmp orders labels as the record type orders its fields.
func labelCmp(a, b string) int {
	switch {
	case types.LabelLess(a, b):
		return -1
	case types.LabelLess(b, a):
		return 1
	default:
		return 0
	}
}

// NewSort orders elements by an expression. Its output is a list,
// whatever its input was.
//
// Span is where the ordering expression was written, and is in
// the signature rather than set afterwards so that a rewrite has
// to say what it means to do with it; see Sort.Span. A node
// grounding invents has none, and blames nowhere.
func NewSort(sys *types.System, input, exp Exp,
	span token.Span,
) *Sort {
	checkCollection(input, "sort input")
	t := sys.List(types.ElemOf(input.Type()))
	return &Sort{
		relBase: relBase{T: t}, Input: input, Exp: exp, Span: span,
	}
}

// NewUnorder forgets an ordering. Its output is a bag.
func NewUnorder(sys *types.System, input Exp) *Unorder {
	checkCollection(input, "unorder input")
	t := sys.Bag(types.ElemOf(input.Type()))
	return &Unorder{relBase: relBase{T: t}, Input: input}
}

// NewSkip drops the first count elements.
func NewSkip(input, count Exp) *Skip {
	checkCollection(input, "skip input")
	return &Skip{
		relBase: relBase{T: input.Type()},
		Input:   input, Count: count,
	}
}

// NewTake keeps the first count elements.
func NewTake(input, count Exp) *Take {
	checkCollection(input, "take input")
	return &Take{
		relBase: relBase{T: input.Type()},
		Input:   input, Count: count,
	}
}

// NewSetRel is a set operator over its inputs, whose element types
// must agree. The output is a list only where every input is.
func NewSetRel(sys *types.System, kind SetKind, distinct bool,
	args []Exp,
) *SetRel {
	if len(args) == 0 {
		panic("set operator needs an input")
	}
	ordered := true
	for _, arg := range args {
		checkCollection(arg, "set operator input")
		ordered = ordered && types.IsOrdered(arg.Type())
	}
	elem := types.ElemOf(args[0].Type())
	for _, arg := range args[1:] {
		if types.ElemOf(arg.Type()) != elem {
			panic(fmt.Sprintf(
				"set operator inputs must agree: %s vs %s",
				elem, types.ElemOf(arg.Type())))
		}
	}
	return &SetRel{
		relBase: relBase{T: sys.CollectionOf(ordered, elem)},
		Kind:    kind, Distinct: distinct, Args: slices.Clone(args),
	}
}

// Components: what a join concatenates.
//
// A node that changes which rows there are, or in what order, but
// not what a row *is*, passes its input's components through. A
// filter emits its input's rows unchanged, so its element is its
// input's element. Saying otherwise -- one component, because the
// node is not a join -- stays invisible until something removes
// the filter: "join(filter(join(a, b)), c)" would have element
// "((a, b), c)" where "join(join(a, b), c)" has "(a, b, c)", and a
// projection written for one shape would then read the other.

// elementPreserved is the input whose components a node passes
// through, or nil where the node's element is its own.
func elementPreserved(node Exp) Exp {
	// lint: sort until '^\t}' where '^\tcase '
	switch n := node.(type) {
	case *Filter:
		return n.Input
	case *Skip:
		return n.Input
	case *Sort:
		return n.Input
	case *Take:
		return n.Input
	case *Unorder:
		return n.Input
	default:
		return nil
	}
}

// ComponentCount is how many components a node's element has.
func ComponentCount(node Exp) int {
	if input := elementPreserved(node); input != nil {
		return ComponentCount(input)
	}
	if join, isJoin := node.(*Join); isJoin {
		return ComponentCount(join.Left) + ComponentCount(join.Right)
	}
	return 1
}

// ComponentTypes are the types of a node's components. A join's
// are read off its own element rather than recomputed from its
// inputs, so that an outer join's option-wrapping is not applied
// twice.
func ComponentTypes(node Exp) []types.Type {
	if input := elementPreserved(node); input != nil {
		return ComponentTypes(input)
	}
	elem := types.ElemOf(node.Type())
	if _, isJoin := node.(*Join); isJoin {
		if tuple, ok := elem.(*types.Tuple); ok {
			return slices.Clone(tuple.Args)
		}
	}
	return []types.Type{elem}
}

func checkCollection(e Exp, what string) {
	if !types.IsCollection(e.Type()) {
		panic(fmt.Sprintf("%s is not a collection: %s",
			what, e.Type()))
	}
}

func checkBool(e Exp, what string) {
	if e.Type().String() != "bool" {
		panic(fmt.Sprintf("%s must be bool: %s", what, e.Type()))
	}
}
