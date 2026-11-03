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
	"errors"
	"strconv"
	"strings"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/token"
	"github.com/hydromatic/morel-go/internal/types"
)

// PreparedPlan is a plan prepared on a connection: the value of
// Morel's "('a, 'b) plan".
//
// "prepare" translates a function for a connection, and afterwards
// both are needed: the plan to print or to run, and the connection
// to run it on. So is the Morel type of the result, because that is
// the type the program was typed against, and the rows have to come
// back as values of it.
//
// A function with parameters is held as the query rather than as a
// plan, and translated at each execution with the argument in place
// of them. Spark has nowhere in a plan to keep a parameter, so the
// argument has to reach the plan before it is sent.
//
// It prints as "<plan>"; asking for its text is what
// "Spark.toString" is for.
type PreparedPlan struct {
	connection *eval.SparkConnection
	sys        *types.System
	query      core.Exp
	params     []*core.IDPat
	plan       *Plan
	elemType   types.Type
	collection bool
}

// Prepare prepares a plan.
//
// The query is a node of the relational tree, or the collection the
// function returns; params are the function's parameters, in the
// order the argument holds them, or none; elemType is the Morel type
// of a row of the result; and collection says whether the result is
// a collection of rows rather than one value. A query with no
// parameters is translated once, here, so that its plan is the same
// plan however often it is used.
func Prepare(connection *eval.SparkConnection, sys *types.System,
	query core.Exp, params []*core.IDPat, elemType types.Type,
	collection bool,
) (*PreparedPlan, error) {
	p := &PreparedPlan{
		connection: connection, sys: sys, query: query, params: params,
		elemType: elemType, collection: collection,
	}
	if len(params) == 0 {
		plan, err := Translate(sys, query)
		if err != nil {
			return nil, err
		}
		p.plan = plan
	}
	return p, nil
}

// String returns how a plan prints.
func (*PreparedPlan) String() string { return "<plan>" }

// Opaque implements eval.Opaque.
func (p *PreparedPlan) Opaque() string { return p.String() }

// PlanDescription implements eval.Described: it describes the plan
// where a plan description shows it, as "Sys.plan" does. A plan with
// a parameter has no text until it is executed, so what it can say
// is which parameters it is waiting for.
func (p *PreparedPlan) PlanDescription() string {
	if p.plan != nil {
		return "spark(plan " + p.plan.Text() + ")"
	}
	names := make([]string, len(p.params))
	for i, param := range p.params {
		names[i] = param.Name
	}
	return "spark(parameters [" + strings.Join(names, ", ") + "])"
}

// Text returns the plan as text.
func (p *PreparedPlan) Text() (string, error) {
	if p.plan == nil {
		return "", eval.NewSparkException("UNSUPPORTED",
			"A plan with a parameter has no text until it is given "+
				"one; execute it instead")
	}
	return p.plan.Text(), nil
}

// Execute executes the plan on an argument and returns its result:
// the rows, or, where the query computes a single value, that value.
func (p *PreparedPlan) Execute(arg eval.Val) (eval.Val, error) {
	plan := p.plan
	if plan == nil {
		var err error
		plan, err = Translate(p.sys, p.substitute(arg))
		if err != nil {
			return nil, err
		}
	}
	rows, err := p.connection.Execute(plan, p.elemType)
	if err != nil {
		return nil, err
	}
	if p.collection {
		return rows, nil
	}
	if len(rows) != 1 {
		return nil, eval.NewSparkException("CONNECTION",
			"Query computes one value, but Spark returned "+
				strconv.Itoa(len(rows))+" rows")
	}
	return rows[0], nil
}

// substitute returns the query with an argument in place of its
// parameters. One parameter takes the whole argument; several take
// its components, one each, because an argument of several values
// is a tuple.
func (p *PreparedPlan) substitute(arg eval.Val) core.Exp {
	values := map[*core.IDPat]core.Exp{}
	if len(p.params) == 1 {
		values[p.params[0]] = valueLiteral(p.params[0].T, arg)
	} else {
		args, _ := arg.([]eval.Val)
		for i, param := range p.params {
			if i < len(args) {
				values[param] = valueLiteral(param.T, args[i])
			}
		}
	}
	return substitute(p.query, values)
}

// valueLiteral returns a literal of a given type whose value is v: a
// literal of a primitive type, or else a value literal, which holds
// the value as it is -- a decimal, say.
func valueLiteral(t types.Type, v eval.Val) *core.Literal {
	kind := ast.ValueLiteralOp
	// lint: sort until '^\t}' where '^\tcase '
	switch t.String() {
	case "char":
		kind = ast.CharLiteralOp
	case "unit":
		kind = ast.UnitLiteralOp
	case boolType:
		kind = ast.BoolLiteralOp
	case intType:
		kind = ast.IntLiteralOp
	case realType:
		kind = ast.RealLiteralOp
	case stringType:
		kind = ast.StringLiteralOp
	}
	return &core.Literal{T: t, Kind: kind, Value: v}
}

// ValueLiteral is valueLiteral, for the compiler, which replaces a
// name whose value is known with the value.
func ValueLiteral(t types.Type, v eval.Val) *core.Literal {
	return valueLiteral(t, v)
}

// Remote returns the function that "Spark.remote (c, f)" is: one of
// the same type as f, whose work is done on the connection. A
// failure on Spark raises where the call to "remote" is.
func Remote(p *PreparedPlan, span token.Span) eval.Val {
	return &remote{plan: p, span: span}
}

// remote is the function that "Spark.remote" makes.
type remote struct {
	plan *PreparedPlan
	span token.Span
}

// ApplyTo implements eval.Applier.
func (r *remote) ApplyTo(arg eval.Val) (eval.Val, error) {
	v, err := r.plan.Execute(arg)
	if err != nil {
		return nil, Raise(err, r.span)
	}
	return v, nil
}

// PlanDescription implements eval.Described. The plan a query was
// pushed down to is what a reader of a plan description wants to
// see, as the plan a query was pushed down to Calcite is; so this
// says "spark(plan ...)" where that says "calcite(plan ...)".
func (r *remote) PlanDescription() string {
	return r.plan.PlanDescription()
}

// Raise converts a Spark failure to the Morel exception it raises,
// raised at a given place; any other error is returned as it is.
func Raise(err error, span token.Span) error {
	var e *eval.SparkException
	if errors.As(err, &e) {
		m := e.Morel()
		m.Span = span
		return m
	}
	return err
}

// End prepared.go
