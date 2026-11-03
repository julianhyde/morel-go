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
)

// mapExp returns an expression with f applied to each of its
// immediate sub-expressions, rebuilt only where one changed.
//
// What the translator rewrites -- a correlation's binding, a
// parameter -- does not change any type, so a node is rebuilt as a
// copy of itself with new children.
func mapExp(e core.Exp, f func(core.Exp) core.Exp) core.Exp {
	if r, isRel := e.(core.Rel); isRel {
		return mapRel(r, f)
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		fn, arg := f(e.Fn), f(e.Arg)
		if fn == e.Fn && arg == e.Arg {
			return e
		}
		c := *e
		c.Fn, c.Arg = fn, arg
		return &c
	case *core.Case:
		exp := f(e.Exp)
		changed := exp != e.Exp
		matches := make([]core.Match, len(e.Matches))
		for i, m := range e.Matches {
			matches[i] = m
			matches[i].Exp = f(m.Exp)
			changed = changed || matches[i].Exp != m.Exp
		}
		if !changed {
			return e
		}
		c := *e
		c.Exp, c.Matches = exp, matches
		return &c
	case *core.Fn:
		body := f(e.Exp)
		if body == e.Exp {
			return e
		}
		c := *e
		c.Exp = body
		return &c
	case *core.Let:
		return mapLet(e, f)
	case *core.List:
		args, changed := mapExps(e.Args, f)
		if !changed {
			return e
		}
		c := *e
		c.Args = args
		return &c
	case *core.Tuple:
		args, changed := mapExps(e.Args, f)
		if !changed {
			return e
		}
		c := *e
		c.Args = args
		return &c
	default:
		return e
	}
}

// mapRel is mapExp for a node of the relational tree.
func mapRel(e core.Rel, f func(core.Exp) core.Exp) core.Exp {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Filter:
		input, cond := f(e.Input), f(e.Condition)
		if input == e.Input && cond == e.Condition {
			return e
		}
		c := *e
		c.Input, c.Condition = input, cond
		return &c
	case *core.Group:
		return mapGroup(e, f)
	case *core.Join:
		left, right, cond := f(e.Left), f(e.Right), f(e.Condition)
		if left == e.Left && right == e.Right && cond == e.Condition {
			return e
		}
		c := *e
		c.Left, c.Right, c.Condition = left, right, cond
		return &c
	case *core.Project:
		input, exp := f(e.Input), f(e.Exp)
		if input == e.Input && exp == e.Exp {
			return e
		}
		c := *e
		c.Input, c.Exp = input, exp
		return &c
	case *core.SetRel:
		args, changed := mapExps(e.Args, f)
		if !changed {
			return e
		}
		c := *e
		c.Args = args
		return &c
	case *core.Skip:
		input, count := f(e.Input), f(e.Count)
		if input == e.Input && count == e.Count {
			return e
		}
		c := *e
		c.Input, c.Count = input, count
		return &c
	case *core.Sort:
		input, exp := f(e.Input), f(e.Exp)
		if input == e.Input && exp == e.Exp {
			return e
		}
		c := *e
		c.Input, c.Exp = input, exp
		return &c
	case *core.Take:
		input, count := f(e.Input), f(e.Count)
		if input == e.Input && count == e.Count {
			return e
		}
		c := *e
		c.Input, c.Count = input, count
		return &c
	case *core.Unorder:
		input := f(e.Input)
		if input == e.Input {
			return e
		}
		c := *e
		c.Input = input
		return &c
	default:
		return e
	}
}

func mapExps(exps []core.Exp, f func(core.Exp) core.Exp) ([]core.Exp,
	bool,
) {
	out := make([]core.Exp, len(exps))
	changed := false
	for i, e := range exps {
		out[i] = f(e)
		changed = changed || out[i] != e
	}
	return out, changed
}

func mapGroup(e *core.Group, f func(core.Exp) core.Exp) core.Exp {
	input := f(e.Input)
	changed := input != e.Input
	keys := make([]core.RelGroupKey, len(e.Keys))
	for i, k := range e.Keys {
		keys[i] = k
		keys[i].Exp = f(k.Exp)
		changed = changed || keys[i].Exp != k.Exp
	}
	aggs := make([]core.RelGroupAgg, len(e.Aggs))
	for i, a := range e.Aggs {
		aggs[i] = a
		if a.Arg != nil {
			aggs[i].Arg = f(a.Arg)
			changed = changed || aggs[i].Arg != a.Arg
		}
	}
	if !changed {
		return e
	}
	c := *e
	c.Input, c.Keys, c.Aggs = input, keys, aggs
	return &c
}

func mapLet(e *core.Let, f func(core.Exp) core.Exp) core.Exp {
	body := f(e.Exp)
	decl := e.Decl
	if d, isVal := decl.(*core.NonRecValDecl); isVal {
		if exp := f(d.Exp); exp != d.Exp {
			c := *d
			c.Exp = exp
			decl = &c
		}
	}
	if body == e.Exp && decl == e.Decl {
		return e
	}
	return &core.Let{Decl: decl, Exp: body}
}

// substitute returns an expression with each variable in a map
// replaced by what the map holds for it.
func substitute(e core.Exp, values map[*core.IDPat]core.Exp) core.Exp {
	if id, isID := e.(*core.ID); isID {
		if v, ok := values[id.Pat]; ok {
			return v
		}
		return e
	}
	return mapExp(e, func(c core.Exp) core.Exp {
		return substitute(c, values)
	})
}

// End rewrite.go
