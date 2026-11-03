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
	"os"
	"strconv"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/compile"
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/types"
)

// The Spark structure. Its implementations live on the kernel,
// because the offline connection's catalog is the session's
// foreign data sets.
//
// This port has no Spark adapter. So "mock:" is the only connection
// that opens; any other URI, and preparing a plan, raise "Spark"
// with error class CONNECTION, as morel-java does where its adapter
// is absent.

// sparkRemoteEnv is the environment variable that names the default
// server.
const sparkRemoteEnv = "SPARK_REMOTE"

// sparkName is the name that "sparkPushDown" sends queries to.
const sparkName = "spark"

// sparkBuiltins returns the Spark structure's implementations for
// NewKernel to inject.
func (k *Kernel) sparkBuiltins() map[string]eval.Val {
	adapterAbsent := func(eval.Val) (eval.Val, error) {
		return nil, eval.SparkError("CONNECTION",
			eval.SparkAdapterMessage)
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
		// A plan can only be made by "prepare", which needs the
		// adapter; so "execute" and "toString" are never given one.
		"Spark.execute":  eval.Fn(adapterAbsent),
		"Spark.prepare":  eval.Fn(adapterAbsent),
		"Spark.remote":   eval.Fn(adapterAbsent),
		"Spark.toString": eval.Fn(adapterAbsent),
		"Spark.using":    eval.Fn(k.sparkUsing),
	}
}

// sparkConnection returns the connection a value holds.
func sparkConnection(v eval.Val) *eval.SparkConnection {
	c, _ := v.(*eval.SparkConnection)
	return c
}

// sparkOpen opens a connection and returns the record
// "{catalog, connection}" that "Spark.connect" returns.
func (k *Kernel) sparkOpen(uri string) (eval.Val, error) {
	if uri != eval.MockSparkURI {
		return nil, eval.SparkError("CONNECTION",
			eval.SparkAdapterMessage)
	}
	c := eval.NewMockSparkConnection(k.dataSets)
	return &eval.SparkConnectionRecord{
		Connection: c, Catalog: c.Catalog(),
	}, nil
}

// sparkOpenDefault opens a connection to the server that
// SPARK_REMOTE names.
func (k *Kernel) sparkOpenDefault() (eval.Val, error) {
	uri := os.Getenv(sparkRemoteEnv)
	if uri == "" {
		return nil, eval.SparkError("CONNECTION",
			"No default Spark Connect server: "+sparkRemoteEnv+
				" is not set")
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
// This port has no Spark adapter, so a query pushed down raises
// "Spark" with error class CONNECTION. That is not a refusal to
// translate, so "sparkFallback" does not evaluate it here instead.
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
