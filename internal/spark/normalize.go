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

package spark

import (
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/types"
)

// Before translation, the tree is put in the shape morel-java's
// resolver gives the same query, so that the plan is the same plan.
//
// The two trees differ where a query's element is one field, which
// morel-go's tree sometimes carries as a bare value and wraps again
// above: a set operator over one-field records is a set operator
// over the values, rewrapped ("{n = $0}" above "union" above "#n
// $0"); an "order" after "group" sorts the records and then unwraps
// them where morel-java unwraps and then sorts. Each rewrite here
// moves a wrapping or an unwrapping across the node that the other
// tree has it on the other side of.

// normalize returns a tree in morel-java's shape.
func normalize(sys *types.System, e core.Exp) core.Exp {
	e = mapExp(e, func(c core.Exp) core.Exp { return normalize(sys, c) })
	if p, isProject := e.(*core.Project); isProject {
		return normalizeProject(sys, p)
	}
	return e
}

func normalizeProject(sys *types.System, p *core.Project) core.Exp {
	if field, isWrap := wrapField(p); isWrap {
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch input := p.Input.(type) {
		case *core.Project:
			// A wrapping of an unwrapping is the records it began
			// with.
			if f, isField := fieldOfInput(input.Exp, 0); isField &&
				f == field && types.ElemOf(input.Input.Type()) ==
				types.ElemOf(p.Type()) {
				return input.Input
			}
		case *core.SetRel:
			// Wrap each input, rather than what the inputs make.
			args := make([]core.Exp, len(input.Args))
			for i, arg := range input.Args {
				args[i] = normalizeProject(sys,
					wrap(sys, arg, field, p.Name))
			}
			return core.NewSetRel(sys, input.Kind, input.Distinct, args)
		case *core.Sort:
			// Wrap, and sort the records by the field.
			inner := normalizeProject(sys,
				wrap(sys, input.Input, field, p.Name))
			elem := types.ElemOf(inner.Type())
			key, ok := rewrap(sys, input.Exp, field, elem)
			if !ok {
				return p
			}
			return core.Named(core.NewSort(sys, inner, key, input.Span),
				input.Name)
		}
		return p
	}
	if field, isField := fieldOfInput(p.Exp, 0); isField {
		// Unwrap, and sort the values, rather than sorting records
		// and then unwrapping them.
		if sort, isSort := p.Input.(*core.Sort); isSort {
			key, ok := unwrap(sort.Exp, field, types.ElemOf(p.Type()))
			if !ok {
				return p
			}
			inner := core.Named(core.NewProject(sys, sort.Input, p.Exp),
				p.Name)
			return core.Named(core.NewSort(sys, inner, key, sort.Span),
				sort.Name)
		}
	}
	return p
}

// wrapField returns the field that a projection wraps its input's
// element in, "{f = $0}", and whether it is such a projection.
func wrapField(p *core.Project) (string, bool) {
	tuple, isTuple := p.Exp.(*core.Tuple)
	if !isTuple || len(tuple.Args) != 1 {
		return "", false
	}
	if input, isInput := tuple.Args[0].(*core.Input); !isInput ||
		input.Ordinal != 0 {
		return "", false
	}
	record, isRecord := tuple.T.(*types.Record)
	if !isRecord || len(record.Fields) != 1 {
		return "", false
	}
	return record.Fields[0].Label, true
}

// wrap is the projection "{f = $0}" over an input.
func wrap(sys *types.System, input core.Exp, field,
	name string,
) *core.Project {
	elem := types.ElemOf(input.Type())
	record := sys.Record([]types.Field{{Label: field, Type: elem}})
	p := core.NewProject(sys, input, &core.Tuple{
		T: record, Args: []core.Exp{core.NewInput(elem, 0)},
	})
	p.Name = name
	return p
}

// rewrap rewrites an expression over a bare value, "$0", as one
// over the record that holds it in a field, "#f $0".
func rewrap(sys *types.System, e core.Exp, field string,
	record types.Type,
) (core.Exp, bool) {
	r, isRecord := record.(*types.Record)
	if !isRecord {
		return nil, false
	}
	ok := true
	var rw func(e core.Exp) core.Exp
	rw = func(e core.Exp) core.Exp {
		switch e := e.(type) {
		case *core.Input:
			if e.Ordinal != 0 {
				ok = false
				return e
			}
			return &core.Apply{
				T: e.T,
				Fn: &core.Selector{
					T: sys.Fn(r, e.T), Name: field, Index: 0,
				},
				Arg: core.NewInput(r, 0),
			}
		case core.Rel, *core.Let:
			// A node of its own, or a binding a node may read: not
			// worth following.
			ok = false
			return e
		}
		return mapExp(e, rw)
	}
	out := rw(e)
	return out, ok
}

// unwrap rewrites an expression over a record that holds a value in
// a field, "#f $0", as one over the bare value, "$0". It cannot if
// the expression reads the record any other way.
func unwrap(e core.Exp, field string, value types.Type) (core.Exp,
	bool,
) {
	ok := true
	var rw func(e core.Exp) core.Exp
	rw = func(e core.Exp) core.Exp {
		if f, isField := fieldOfInput(e, 0); isField && f == field {
			return core.NewInput(value, 0)
		}
		switch e.(type) {
		case *core.Input, core.Rel, *core.Let:
			ok = false
			return e
		}
		return mapExp(e, rw)
	}
	out := rw(e)
	return out, ok
}

// unlet returns a query with the bindings that correlate a subquery
// substituted away.
//
// A subquery that reads the enclosing row captures it in a "let",
// and names the binding where it means the row: "let val v$0 = $0
// in nonEmpty (... v$0 ...) end". Spark needs no binding, because a
// subquery resolves a name against the enclosing relation, and a
// "let" is in the way of every path that reads an expression -- a
// filter's condition, a projection's record -- so they come off
// before anything else looks. The binding becomes a variable named
// for the row, "$d", as morel-java's core tree names it.
func unlet(e core.Exp, inputs []string,
	values map[*core.IDPat]core.Exp,
) core.Exp {
	if r, isRel := e.(core.Rel); isRel {
		return unletRel(r, values)
	}
	switch e := e.(type) {
	case *core.ID:
		if v, ok := values[e.Pat]; ok {
			return v
		}
		return e
	case *core.Let:
		if pat, row, isCorrelation := correlation(e); isCorrelation {
			if input, isInput := row.(*core.Input); isInput {
				name := input.Name()
				if input.Ordinal < len(inputs) &&
					inputs[input.Ordinal] != "" {
					name = "$" + inputs[input.Ordinal]
				}
				values[pat] = &core.ID{
					Pat: &core.IDPat{T: input.T, Name: name},
				}
			} else {
				values[pat] = unlet(row, inputs, values)
			}
			return unlet(e.Exp, inputs, values)
		}
	}
	return mapExp(e, func(c core.Exp) core.Exp {
		return unlet(c, inputs, values)
	})
}

// correlation returns the variable a "let" binds and the row it
// binds it to, and whether the "let" is a correlation: one that
// binds a variable to a row, or to another such variable.
func correlation(e *core.Let) (*core.IDPat, core.Exp, bool) {
	d, isVal := e.Decl.(*core.NonRecValDecl)
	if !isVal {
		return nil, nil, false
	}
	pat, isID := d.Pat.(*core.IDPat)
	if !isID {
		return nil, nil, false
	}
	switch d.Exp.(type) {
	case *core.Input, *core.ID:
		return pat, d.Exp, true
	}
	return nil, nil, false
}

// unletRel is unlet for a node of the relational tree, whose
// expressions read its own rows.
func unletRel(e core.Rel, values map[*core.IDPat]core.Exp) core.Exp {
	in := func(names ...string) func(core.Exp) core.Exp {
		return func(c core.Exp) core.Exp { return unlet(c, names, values) }
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Filter:
		c := *e
		c.Input = unlet(e.Input, nil, values)
		c.Condition = in(e.Name)(e.Condition)
		return &c
	case *core.Group:
		return mapGroup(e, func(c core.Exp) core.Exp {
			if c == e.Input {
				return unlet(c, nil, values)
			}
			return in(e.Name)(c)
		})
	case *core.Join:
		c := *e
		c.Left = unlet(e.Left, nil, values)
		c.Right = unlet(e.Right, nil, values)
		c.Condition = in(e.LeftName, e.RightName)(e.Condition)
		return &c
	case *core.Project:
		c := *e
		c.Input = unlet(e.Input, nil, values)
		c.Exp = in(e.Name)(e.Exp)
		return &c
	case *core.Sort:
		c := *e
		c.Input = unlet(e.Input, nil, values)
		c.Exp = in(e.Name)(e.Exp)
		return &c
	}
	return mapExp(e, in())
}

// End normalize.go
