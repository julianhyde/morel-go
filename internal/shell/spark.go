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

package shell

import (
	"errors"
	"strconv"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/compile"
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/spark"
	"github.com/hydromatic/morel-go/internal/token"
	"github.com/hydromatic/morel-go/internal/types"
)

// The Spark structure. Its implementations live on the kernel,
// because the offline connection's catalog is the session's
// foreign data sets.
//
// "Spark.prepare" and "Spark.remote" are expanded by the resolver,
// which translates the function they are given; see package spark.

// sparkName is the name that "sparkPushDown" sends queries to.
const sparkName = "spark"

// sparkBuiltins returns the Spark structure's implementations for
// NewKernel to inject.
func (k *Kernel) sparkBuiltins() map[string]eval.Val {
	// The resolver replaces "prepare (c, f)" and "remote (c, f)"
	// with what they make of f, so these run only where one is used
	// as a value rather than applied to a function expression --
	// passed to "map", say -- which they cannot serve, because by
	// then the function is a closure and its tree is gone.
	notApplied := func(name string) eval.Fn {
		return func(eval.Val) (eval.Val, error) {
			return nil, eval.NewSparkException("UNSUPPORTED",
				"Spark."+name+" must be applied to a function expression")
		}
	}
	return map[string]eval.Val{
		// lint: sort until '^\t}' where '^\t\t"'
		"Spark.catalog": eval.Fn(func(arg eval.Val) (eval.Val, error) {
			return sparkConnection(arg).Catalog(), nil
		}),
		"Spark.close": eval.Fn(func(arg eval.Val) (eval.Val, error) {
			sparkConnection(arg).Close()
			return unitResult()
		}),
		"Spark.connect": eval.Fn(func(arg eval.Val) (eval.Val, error) {
			uri, _ := arg.(string)
			return k.sparkOpen(uri)
		}),
		"Spark.connectDefault": eval.Fn(func(eval.Val) (eval.Val, error) {
			return k.sparkOpenDefault()
		}),
		"Spark.execute": eval.Fn(sparkExecute),
		"Spark.prepare": notApplied("prepare"),
		"Spark.remote":  notApplied("remote"),
		"Spark.toString": eval.Fn(func(arg eval.Val) (eval.Val, error) {
			plan, _ := arg.(*spark.PreparedPlan)
			// A plan that has no text says so as itself, not as a
			// Morel exception, as morel-java's does.
			return plan.Text()
		}),
		"Spark.using": eval.Fn(k.sparkUsing),
	}
}

// sparkExecute is "Spark.execute (p, a)": runs plan p with argument
// a.
func sparkExecute(arg eval.Val) (eval.Val, error) {
	args, _ := arg.([]eval.Val)
	plan, _ := args[0].(*spark.PreparedPlan)
	v, err := plan.Execute(args[1])
	if err != nil {
		var e *eval.SparkException
		if errors.As(err, &e) {
			return nil, e.Morel()
		}
		return nil, err
	}
	return v, nil
}

// sparkConnection returns the connection a value holds.
func sparkConnection(v eval.Val) *eval.SparkConnection {
	c, _ := v.(*eval.SparkConnection)
	return c
}

// sparkOpen opens a connection and returns the record
// "{catalog, connection}" that "Spark.connect" returns: the offline
// connection for "mock:", and otherwise a connection to the Spark
// Connect server that the URI names.
func (k *Kernel) sparkOpen(uri string) (eval.Val, error) {
	var c *eval.SparkConnection
	if uri == eval.MockSparkURI {
		c = eval.NewMockSparkConnection(k.dataSets)
	} else {
		var err error
		c, err = spark.Connect(k.sys, uri)
		if err != nil {
			return nil, spark.Raise(err, token.Span{})
		}
	}
	return &eval.SparkConnectionRecord{
		Connection: c, Catalog: c.Catalog(),
	}, nil
}

// BindSpark binds a name to a connection to a Spark Connect server,
// as "Spark.connect" would return it, so that a script can assume
// one; and returns the function that closes it.
func (k *Kernel) BindSpark(name, uri string) (func(), error) {
	v, err := k.sparkOpen(uri)
	if err != nil {
		return nil, err
	}
	record, _ := v.(*eval.SparkConnectionRecord)
	k.bind(name, record.Type(k.sys))
	k.values[name] = record
	k.recordFileBinding(name, record)
	return record.Connection.Close, nil
}

// sparkOpenDefault opens a connection to the server that
// SPARK_REMOTE names.
func (k *Kernel) sparkOpenDefault() (eval.Val, error) {
	uri, err := spark.DefaultURI()
	if err != nil {
		return nil, spark.Raise(err, token.Span{})
	}
	return k.sparkOpen(uri)
}

// sparkUsing is "Spark.using f": a function that opens the default
// connection for each call, applies f to the connection and its
// argument, and closes the connection afterwards, whether or not f
// raised.
func (k *Kernel) sparkUsing(f eval.Val) (eval.Val, error) {
	return eval.Fn(func(x eval.Val) (eval.Val, error) {
		v, err := k.sparkOpenDefault()
		if err != nil {
			return nil, err
		}
		record, _ := v.(*eval.SparkConnectionRecord)
		defer record.Connection.Close()
		return eval.ApplyVal(f, []eval.Val{record.Connection, x})
	}), nil
}

// adaptSpark rewrites the types of the "Spark" structure's members
// that return a catalog, whose type is "{...}" -- a record whose
// fields are not known until the catalog is browsed. The signature
// file writes it as "{}", the nearest thing Morel's type syntax can
// say, and "{}" is unit.
func adaptSpark(sys *types.System, b *compile.Binding) {
	if b.Name != "Spark" {
		return
	}
	record, ok := b.Type.(*types.Record)
	if !ok {
		return
	}
	catalog := sys.ProgressiveRecord(nil)
	connection := sys.Named("connection")
	connectionRecord := sys.Record([]types.Field{
		{Label: "catalog", Type: catalog},
		{Label: "connection", Type: connection},
	})
	fields := make([]types.Field, len(record.Fields))
	for i, f := range record.Fields {
		fields[i] = f
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch f.Label {
		case "catalog":
			fields[i].Type = sys.Fn(connection, catalog)
		case "connect":
			fields[i].Type = sys.Fn(sys.String, connectionRecord)
		case "connectDefault":
			fields[i].Type = sys.Fn(sys.Unit, connectionRecord)
		}
	}
	b.Type = sys.Record(fields)
}

// sparkPushDown rewrites a query to run on the connection bound as
// "spark", if the "sparkPushDown" property asks for that. The query
// "e" becomes "(Spark.remote (spark.connection, fn () => e)) ()". A
// declaration is left alone, because the names a session binds are
// its own, and so is anything but a query with at least one step.
//
// A query Spark cannot do is refused where its plan is made, and
// "sparkFallback" says what happens then; see runPushedDown.
func (k *Kernel) sparkPushDown(n ast.Node) (ast.Node, error) {
	pushDown, _ := strconv.ParseBool(k.config.props["sparkPushDown"])
	if !pushDown {
		return n, nil
	}
	from, isFrom := n.(*ast.From)
	if !isFrom || len(from.Steps) == 0 {
		return n, nil
	}
	span := from.Span()
	if !k.isBound(sparkName) {
		// Pushing down needs a connection, and the rewrite would
		// name one that is not there; "unbound variable spark"
		// would not say why.
		return nil, &compile.Error{
			Span: span,
			Msg: "sparkPushDown is set, but no connection is bound " +
				"as 'spark'; start Morel with --spark, or bind one " +
				"with 'val spark = Spark.connect uri'",
		}
	}
	connection := ast.NewApply(span,
		ast.NewRecordSelector(span, "connection"),
		ast.NewID(span, sparkName))
	remote := ast.NewApply(span, ast.NewRecordSelector(span, "remote"),
		ast.NewID(span, "Spark"))
	fn := ast.NewFn(span, []*ast.Match{
		ast.NewMatch(span, ast.NewTuplePat(span, nil), from),
	})
	return ast.NewApply(span,
		ast.NewApply(span, remote,
			ast.NewTuple(span, []ast.Expr{connection, fn})),
		ast.NewLiteral(span, ast.UnitLiteralOp, "()")), nil
}

// isBound reports whether a name is bound at top level.
func (k *Kernel) isBound(name string) bool {
	for _, b := range k.bindings {
		if b.Name == name {
			return true
		}
	}
	return false
}

// End spark.go
