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
	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/spark"
	"github.com/hydromatic/morel-go/internal/token"
	"github.com/hydromatic/morel-go/internal/types"
)

// The two intrinsics of the "Spark" structure, which the resolver
// expands rather than compiles, because what they do depends on the
// tree of the function they are given, which a closure no longer
// has.
const (
	sparkPrepareName = "Spark.prepare"
	sparkRemoteName  = "Spark.remote"
)

// sparkPrepare expands "Spark.prepare (c, f)": translates f for
// connection c, and returns the plan as a literal, so that a query
// is translated once, here, rather than each time its plan is used.
// "Spark.remote (c, f)" is "prepare" and "execute" in one step, and
// its value is a function that runs the plan.
//
// A refusal -- something Spark cannot do -- is a Spark exception,
// raised where the statement is.
func (r *resolver) sparkPrepare(name string, apply *ast.Apply,
	t types.Type, arg core.Exp,
) (core.Exp, error) {
	tuple, isTuple := arg.(*core.Tuple)
	if !isTuple || len(tuple.Args) != 2 {
		return nil, &Error{
			Span: apply.Span(),
			Msg:  "prepare expects a connection and a function",
		}
	}
	fn := tuple.Args[1]
	query, err := r.sparkQuery(fn)
	if err != nil {
		return nil, err
	}
	// The plan runs on the connection it was prepared for, so the
	// plan holds it; and it computes a value of the type the program
	// was typed against, so it holds that too.
	v, _ := r.valueOf(tuple.Args[0])
	connection, isConnection := v.(*eval.SparkConnection)
	if !isConnection {
		return nil, eval.NewSparkException("UNSUPPORTED",
			"Spark cannot see which connection to prepare on; bind "+
				"the connection to a name")
	}
	sys := r.typeMap.sys
	resultType := types.Unalias(fn.Type())
	if f, isFn := resultType.(*types.Fn); isFn {
		resultType = f.Result
	}
	elemType := types.ElemOf(resultType)
	collection := elemType != nil
	if !collection {
		elemType = resultType
	}
	plan, err := spark.Prepare(connection, sys, r.sourced(query),
		sparkParams(fn), elemType, collection)
	if err != nil {
		return nil, err
	}
	var value eval.Val = plan
	if name == sparkRemoteName {
		value = spark.Remote(plan, apply.Span())
	}
	return &core.Literal{T: t, Kind: ast.ValueLiteralOp, Value: value},
		nil
}

// StatementSpan is where a statement is, as morel-java places it:
// where its expression is, which for a postfix call "x.f ()" is the
// call's last token, and nowhere for a declaration. An exception
// raised while the statement was compiled is raised there.
func StatementSpan(resolved *Resolved, n ast.Node) token.Span {
	e, isExp := n.(ast.Expr)
	if !isExp {
		return token.Span{}
	}
	if apply, isApply := e.(*ast.Apply); isApply && resolved != nil {
		if call, isMethod := resolved.TypeMap.methodCalls[apply]; isMethod {
			return call.Span()
		}
	}
	return e.Span()
}

// isSparkCatalog reports whether a browsable value is a Spark
// catalog or one of its databases, which raise "Spark" for a field
// they do not have, as Spark itself would, rather than leaving the
// field to be a type error.
func isSparkCatalog(b eval.Browsable) bool {
	switch b.(type) {
	case *eval.SparkCatalog, *eval.SparkDatabase:
		return true
	}
	return false
}

// sparkQuery returns the query that a function computes: its first
// query, as a tree; or, if it has none, its body, if that is a
// collection. A query and nothing else has a plan; a function whose
// body is not relational has none.
func (r *resolver) sparkQuery(fn core.Exp) (core.Exp, error) {
	var from *core.From
	finder := &rewriter{sys: r.typeMap.sys}
	finder.exp = func(e core.Exp) (core.Exp, bool) {
		if f, isFrom := e.(*core.From); isFrom || from != nil {
			if from == nil {
				from = f
			}
			return e, true
		}
		return nil, false
	}
	finder.rewriteExp(fn)
	if from != nil {
		tree, reason := TranslateFrom(r.typeMap.sys, from)
		if tree == nil {
			return nil, eval.NewSparkException("UNSUPPORTED",
				"Spark cannot do a query that has no relational tree: "+
					reason)
		}
		return tree, nil
	}
	body := fn
	if f, isFn := fn.(*core.Fn); isFn {
		body = f.Exp
	}
	if types.ElemOf(body.Type()) != nil {
		return body, nil
	}
	return nil, eval.NewSparkException("UNSUPPORTED",
		"Spark cannot do a function that is not a query")
}

// sparkParams returns the parameters of the function a plan was
// prepared from, in the order the argument holds them; none if it
// has none.
//
// A function of unit has a parameter in the tree but none to pass,
// which is the same thing as none. A function of several arguments
// is a function of a tuple, which the core destructures -- "fn (d,
// j) => e" is "fn v => case v of (d, j) => e" -- so its parameters
// are the names that case binds.
func sparkParams(fn core.Exp) []*core.IDPat {
	f, isFn := fn.(*core.Fn)
	if !isFn || f.IDPat == nil || f.IDPat.T.String() == unitName {
		return nil
	}
	if pats := tupleParams(f); pats != nil {
		return pats
	}
	return []*core.IDPat{f.IDPat}
}

// tupleParams returns the names that a function of a tuple
// destructures its parameter into, or nil if it does not.
func tupleParams(f *core.Fn) []*core.IDPat {
	c, isCase := f.Exp.(*core.Case)
	if !isCase || len(c.Matches) != 1 {
		return nil
	}
	if id, isID := c.Exp.(*core.ID); !isID || id.Pat != f.IDPat {
		return nil
	}
	tuple, isTuple := c.Matches[0].Pat.(*core.TuplePat)
	if !isTuple {
		return nil
	}
	pats := make([]*core.IDPat, 0, len(tuple.Args))
	for _, p := range tuple.Args {
		idPat, isIDPat := p.(*core.IDPat)
		if !isIDPat {
			return nil
		}
		pats = append(pats, idPat)
	}
	return pats
}

// sourced replaces each collection a query reads with the value it
// denotes, so that the translator can see what it is.
//
// A query names its sources -- "spark.catalog.scott.emps",
// "scott.emps", "rates" -- and what crosses to Spark depends on what
// each one is, not on what it is called: a table of a connection's
// catalog is a table scan, and every other collection is rows to
// send. Only the environment knows which, and this is where the
// environment still reaches the plan. A name whose value is a
// constant -- a number, a string -- is replaced by the constant, as
// it would be inlined.
//
// A name the environment cannot value is left as it is -- a plan's
// own parameter, for one -- and the translator refuses what it
// cannot read.
func (r *resolver) sourced(query core.Exp) core.Exp {
	rw := &rewriter{sys: r.typeMap.sys}
	rw.exp = func(e core.Exp) (core.Exp, bool) {
		switch e.(type) {
		case *core.ID, *core.Apply:
		default:
			return nil, false
		}
		collection := types.ElemOf(e.Type()) != nil
		if !collection && !isConstantType(e.Type()) {
			return nil, false
		}
		v, ok := r.valueOf(e)
		if !ok || !isConstantValue(v, collection) {
			return nil, false
		}
		return spark.ValueLiteral(e.Type(), v), true
	}
	return rw.rewriteExp(query)
}

// isConstantType reports whether a value of a type can stand in a
// plan as a literal.
func isConstantType(t types.Type) bool {
	switch t.String() {
	case boolName, intName, realName, stringName, decimalName:
		return true
	}
	return false
}

// isConstantValue reports whether a value is one that sourced can
// put in the plan: a collection, if a collection was wanted, or a
// constant.
func isConstantValue(v eval.Val, collection bool) bool {
	switch v.(type) {
	case *eval.SparkTable, []eval.Val:
		return collection
	case bool, int32, float32, string, eval.Decimal:
		return !collection
	}
	return false
}

// valueOf returns the value an expression has in the session, and
// whether it has one: a name bound by an earlier statement, or a
// field of one.
//
// A table of a connection's catalog is the table, not its rows, so
// that reading what the table is does not read the table.
func (r *resolver) valueOf(e core.Exp) (eval.Val, bool) {
	switch e := e.(type) {
	case *core.ID:
		if r.freePats[freeKey{e.Pat.Name, e.Pat.T}] != e.Pat {
			// Bound in this statement, so it has no value yet.
			return nil, false
		}
		v, ok := r.values[e.Pat.Name]
		return v, ok
	case *core.Apply:
		sel, isSel := e.Fn.(*core.Selector)
		if !isSel {
			return nil, false
		}
		base, ok := r.valueOf(e.Arg)
		if !ok {
			return nil, false
		}
		return sparkField(base, sel)
	}
	return nil, false
}

// sparkField returns the field that a selector selects of a value.
func sparkField(v eval.Val, sel *core.Selector) (eval.Val, bool) {
	// lint: sort until '^\t}' where '^\tcase '
	switch v := v.(type) {
	case *eval.SparkCatalog:
		d, isDatabase := v.Entry(sel.Name).(*eval.SparkDatabase)
		if isDatabase {
			return d, true
		}
	case *eval.SparkConnectionRecord:
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch sel.Name {
		case "catalog":
			return v.Catalog, true
		case "connection":
			return v.Connection, true
		}
	case *eval.SparkDatabase:
		t, err := v.Table(sel.Name)
		if err == nil && t != nil {
			return t, true
		}
	case []eval.Val:
		if sel.Index < len(v) {
			if rel, isRelation := v[sel.Index].(eval.Relation); isRelation {
				// A data set's table: its rows.
				return rel.Rows, true
			}
			return v[sel.Index], true
		}
	}
	return nil, false
}

// End spark.go
