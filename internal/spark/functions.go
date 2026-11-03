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
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/parse"
)

// How each Morel operator reaches Spark: the table that
// "functions.sml" holds as data, read for the translator's use.
//
// The table is data, in Morel, so that every implementation can
// share one copy. It is read rather than evaluated: every entry is
// a record of string literals, so the parse tree is the value.

//go:embed functions.sml
var functionsSource string

// Kind is how an operator reaches Spark.
type Kind int

// The kinds of entry.
const (
	// Aggregate is an aggregate function, applied over a group.
	Aggregate Kind = iota
	// Direct says that Spark has the operator, under the name the
	// entry gives.
	Direct
	// Rewritten says that the entry gives an expression template.
	Rewritten
	// Subquery says that the operator becomes a subquery.
	Subquery
	// Unsupported says that Spark cannot do it.
	Unsupported
)

// kindNames are the kinds as the table writes them.
var kindNames = map[string]Kind{
	// lint: sort until '^}' where '^\t"'
	"aggregate":   Aggregate,
	"direct":      Direct,
	"rewritten":   Rewritten,
	"subquery":    Subquery,
	"unsupported": Unsupported,
}

// String is the kind as the table writes it.
func (k Kind) String() string {
	for name, kind := range kindNames {
		if kind == k {
			return name
		}
	}
	return "unknown"
}

// Entry is one row of the table.
type Entry struct {
	// Morel is the operator as the table names it, such as "op +".
	Morel string
	Kind  Kind
	// Spark is Spark's name for it, or the template that replaces
	// it.
	Spark string
	// Divergence is a known semantic difference, or empty.
	Divergence string
	// Template is the template of a rewritten entry, and nil
	// otherwise.
	Template *Template
}

// FunctionTable is the table, by the name it gives each operator.
type FunctionTable struct {
	entries map[string]*Entry
	order   []*Entry
}

var (
	tableOnce sync.Once
	table     *FunctionTable
	errTable  error
)

// Functions returns the table, reading it on first use.
func Functions() (*FunctionTable, error) {
	tableOnce.Do(func() {
		table, errTable = parseFunctions(functionsSource)
	})
	return table, errTable
}

// Entries returns every entry, in the order the file lists them.
func (t *FunctionTable) Entries() []*Entry { return t.order }

// Get returns the entry the table names "name", or nil.
func (t *FunctionTable) Get(name string) *Entry { return t.entries[name] }

// Lookup returns the entry for a built-in, as the core tree names
// it, or nil if the table does not mention it.
//
// The core tree names a built-in by its binding: a member of a
// structure, "String.size", or a top-level name, "op +", "size". A
// top-level alias is looked up as the member it stands for, and an
// operator overloaded on its operands' type is looked up as the
// table names it, "op +"; failing that, an instance is looked up as
// its operator, so that "Int.+" is "op +" as it is in morel-java.
func (t *FunctionTable) Lookup(name string) *Entry {
	name = TableName(name)
	if e := t.entries[name]; e != nil {
		return e
	}
	if _, member, isMember := strings.Cut(name, "."); isMember {
		return t.entries["op "+member]
	}
	return nil
}

// TableName is the name the table gives the built-in that the core
// tree calls "name".
func TableName(name string) string {
	if alias, ok := builtinAliases[name]; ok {
		return alias
	}
	return name
}

// MLName is a built-in's own name, without its structure: "size" for
// "String.size", "+" for "op +".
func MLName(name string) string {
	name = TableName(name)
	if op, isOp := strings.CutPrefix(name, "op "); isOp {
		return op
	}
	if _, member, isMember := strings.Cut(name, "."); isMember {
		return member
	}
	return name
}

// builtinAliases maps the names that the core tree gives built-ins,
// where they differ from the table's, to the table's.
var builtinAliases = map[string]string{
	// lint: sort until '^}' where '^\t"'
	"concat":    "String.concat",
	"count":     "Relational.count",
	"explode":   "String.explode",
	"getOpt":    "Option.getOpt",
	"hd":        "List.hd",
	"implode":   "String.implode",
	"isSome":    "Option.isSome",
	"length":    "List.length",
	"max":       "Relational.max",
	"min":       "Relational.min",
	"nonEmpty":  "Relational.nonEmpty",
	"not":       "Bool.not",
	"null":      "List.null",
	"only":      "Relational.only",
	"op /":      "Real./",
	"op @":      "List.@",
	"op ^":      "String.^",
	"rev":       "List.rev",
	"size":      "String.size",
	"substring": "String.substring",
	"sum":       "Relational.sum",
	"valOf":     "Option.valOf",
}

func parseFunctions(src string) (*FunctionTable, error) {
	const file = "functions.sml"
	exp, err := parse.Expr(file, src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	list, isList := exp.(*ast.ListExp)
	if !isList {
		return nil, fmt.Errorf("%s: expected a list, got %s", file,
			exp.Op())
	}
	t := &FunctionTable{entries: map[string]*Entry{}}
	for _, arg := range list.Args {
		e, err := parseEntry(arg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if t.entries[e.Morel] != nil {
			return nil, fmt.Errorf("%s: duplicate entry %s", file,
				e.Morel)
		}
		t.entries[e.Morel] = e
		t.order = append(t.order, e)
	}
	return t, nil
}

// entryFields are the fields of an entry, as the file names them.
var entryFields = []string{"morel", "kind", "spark", "divergence"}

func parseEntry(exp ast.Expr) (*Entry, error) {
	record, isRecord := exp.(*ast.Record)
	if !isRecord {
		return nil, fmt.Errorf("expected a record, got %s", exp.Op())
	}
	fields := map[string]string{}
	for _, f := range record.Fields {
		lit, isLit := f.Exp.(*ast.Literal)
		if !isLit || lit.Kind != ast.StringLiteralOp {
			return nil, fmt.Errorf("expected a string literal, got %s",
				f.Exp.Op())
		}
		fields[f.Label] = lit.Value
	}
	get := func(name string) (string, error) {
		v, ok := fields[name]
		if !ok {
			return "", fmt.Errorf("entry has no '%s' field", name)
		}
		return v, nil
	}
	var e Entry
	values := make([]string, len(entryFields))
	for i, name := range entryFields {
		v, err := get(name)
		if err != nil {
			return nil, err
		}
		values[i] = v
	}
	e.Morel, e.Spark, e.Divergence = values[0], values[2], values[3]
	kind := values[1]
	k, ok := kindNames[kind]
	if !ok {
		return nil, fmt.Errorf("unknown kind %s", kind)
	}
	e.Kind = k
	if k == Rewritten {
		tmpl, err := NewTemplate(e.Spark)
		if err != nil {
			return nil, err
		}
		e.Template = tmpl
	}
	return &e, nil
}

// End functions.go
