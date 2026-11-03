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
	"strconv"
	"strings"
	"unicode"

	"google.golang.org/protobuf/proto"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/spark/connect"
	"github.com/hydromatic/morel-go/internal/types"
)

// Translates a Morel query, as the relational tree holds it, to a
// Spark plan; morel-java's SparkTranslator.
//
// Connect's Relation is a conventional operator tree, and so is the
// relational tree, so the translation is close to one node for one
// node. The hinge is the row: a query binds one (the "e" of "from e
// in emps") and refers to a column as a field of it, which is what
// Spark calls a subquery alias and a qualified attribute. So the
// alias goes on the scanned collection, and "#b $0" becomes "e.b".
//
// Plan ids are assigned in the order the text prints them -- the
// outermost node is 0 -- so that a plan's text depends on the query
// and nothing else.

// nonEmptyName is what the table calls "Relational.nonEmpty", which
// is what "exists" is.
const nonEmptyName = "Relational.nonEmpty"

// The arities that the core tree's shapes have.
const (
	// setOpArity is how many inputs Spark's set operation takes.
	setOpArity = 2
	// logicalArity is how many rules "andalso" and "orelse" have
	// when the core tree writes them as a "case".
	logicalArity = 2
)

// translator is one translation, of one query.
type translator struct {
	sys            *types.System
	fns            *FunctionTable
	localRowCounts []int
	// witnesses holds, for a row that an outer join may not find,
	// the column that says whether it was found, by the name the
	// row goes by.
	witnesses map[string]string
	// references are the relations that expressions in the query
	// refer to, which Spark carries beside the root rather than
	// inside it. A correlated subquery is one.
	references []*connect.Relation
	nextPlanID int64
}

// scope is what an expression's names mean: the names of the rows
// its node reads ("$0" and, in a join's condition, "$1"), and the
// names of the components of a row that a join made.
type scope struct {
	inputs []string
	parts  []string
}

// Translate translates a query to a plan. The query is a node of the
// relational tree, or the collection that the query reads, if it
// reads one whole.
func Translate(sys *types.System, query core.Exp) (*Plan, error) {
	fns, err := Functions()
	if err != nil {
		return nil, err
	}
	t := &translator{sys: sys, fns: fns, witnesses: map[string]string{}}
	query = normalize(sys, query)
	query = unlet(query, nil, map[*core.IDPat]core.Exp{})
	root, err := t.rel(query)
	if err != nil {
		return nil, err
	}
	return &Plan{
		proto:          &connect.Plan{OpType: &connect.Plan_Root{Root: root}},
		localRowCounts: t.localRowCounts,
	}, nil
}

func (t *translator) planID() int64 {
	id := t.nextPlanID
	t.nextPlanID++
	return id
}

func common(planID int64) *connect.RelationCommon {
	return &connect.RelationCommon{PlanId: proto.Int64(planID)}
}

// rel translates a relational node, wrapping it where its own
// expressions refer to relations: Spark keeps those beside the node,
// not inside it, and the wrapper is what holds them. The wrapper
// takes the plan id above the node's, which is the order the text
// prints them.
func (t *translator) rel(e core.Exp) (*connect.Relation, error) {
	r, isRel := e.(core.Rel)
	if !isRel {
		// The query is the collection itself -- "from e in emps",
		// with no step but the scan -- so the plan is the read.
		return t.leaf(e)
	}
	if p, isProject := r.(*core.Project); isProject &&
		isWholeOfInput(p) {
		// Morel distinguishes a bag of one-field records from a bag
		// of values, and a projection turns the one into the other.
		// Spark does not: a relation always has named columns, so
		// the projection is nothing to do, and takes no plan id.
		return t.rel(p.Input)
	}
	if !t.refersToRelation(r) {
		return t.relNode(r, t.planID())
	}
	wrapperID := t.planID()
	enclosing := t.references
	t.references = nil
	node, err := t.relNode(r, t.planID())
	if err != nil {
		return nil, err
	}
	own := t.references
	t.references = enclosing
	return &connect.Relation{
		Common: common(wrapperID),
		RelType: &connect.Relation_WithRelations{
			WithRelations: &connect.WithRelations{
				Root: node, References: own,
			},
		},
	}, nil
}

// isWholeOfInput reports whether a projection reads the whole of its
// input: the one field of a one-field row, which leaves the relation
// as it was.
func isWholeOfInput(p *core.Project) bool {
	name, isField := fieldOfInput(p.Exp, 0)
	if !isField {
		return false
	}
	record, isRecord := types.ElemOf(p.Input.Type()).(*types.Record)
	return isRecord && len(record.Fields) == 1 &&
		record.Fields[0].Label == name
}

// fieldOfInput returns the field that an expression reads of a
// node's input, "#f $k", and whether it is one.
func fieldOfInput(e core.Exp, ordinal int) (string, bool) {
	apply, isApply := e.(*core.Apply)
	if !isApply {
		return "", false
	}
	sel, isSel := apply.Fn.(*core.Selector)
	input, isInput := apply.Arg.(*core.Input)
	if !isSel || !isInput || input.Ordinal != ordinal {
		return "", false
	}
	return sel.Name, true
}

// refersToRelation reports whether a node's own expressions ask a
// question of a query.
func (t *translator) refersToRelation(r core.Rel) bool {
	switch r := r.(type) {
	case *core.Filter:
		return t.containsSubquery(r.Condition)
	case *core.Project:
		return t.containsSubquery(r.Exp)
	}
	return false
}

// containsSubquery reports whether an expression asks a question of
// a query. The function table says which built-ins do.
func (t *translator) containsSubquery(e core.Exp) bool {
	found := false
	var visit func(e core.Exp) core.Exp
	visit = func(e core.Exp) core.Exp {
		if apply, isApply := e.(*core.Apply); isApply {
			if name := builtinName(apply.Fn); name != "" {
				if entry := t.fns.Lookup(name); entry != nil &&
					entry.Kind == Subquery {
					found = true
				}
			}
		}
		return mapExp(e, visit)
	}
	visit(e)
	return found
}

// relNode translates one relational node, under a given plan id.
func (t *translator) relNode(r core.Rel, planID int64) (
	*connect.Relation, error,
) {
	b := &connect.Relation{}
	var err error
	// lint: sort until '^\t}' where '^\tcase '
	switch r := r.(type) {
	case *core.Filter:
		err = t.filter(r, b)
	case *core.Group:
		err = t.group(r, b)
	case *core.Join:
		err = t.join(r, b)
	case *core.Project:
		err = t.project(r, b)
	case *core.SetRel:
		err = t.setRel(r, b)
	case *core.Skip:
		var input *connect.Relation
		input, err = t.rel(r.Input)
		if err != nil {
			return nil, err
		}
		var n int32
		n, err = count(r.Count, "skip")
		b.RelType = &connect.Relation_Offset{
			Offset: &connect.Offset{Input: input, Offset: n},
		}
	case *core.Sort:
		err = t.sort(r, b)
	case *core.Take:
		var input *connect.Relation
		input, err = t.rel(r.Input)
		if err != nil {
			return nil, err
		}
		var n int32
		n, err = count(r.Count, "take")
		b.RelType = &connect.Relation_Limit{
			Limit: &connect.Limit{Input: input, Limit: n},
		}
	default:
		err = unsupported("relational operator " + javaOp(r))
	}
	if err != nil {
		return nil, err
	}
	b.Common = common(planID)
	return b, nil
}

func (t *translator) filter(r *core.Filter, b *connect.Relation) error {
	input, err := t.input(r.Input, r.Name)
	if err != nil {
		return err
	}
	cond, err := t.exp(r.Condition,
		scope{inputs: []string{r.Name}, parts: parts(r.Input)})
	if err != nil {
		return err
	}
	b.RelType = &connect.Relation_Filter{
		Filter: &connect.Filter{Input: input, Condition: cond},
	}
	return nil
}

func (t *translator) project(r *core.Project, b *connect.Relation) error {
	input, err := t.input(r.Input, r.Name)
	if err != nil {
		return err
	}
	exps, err := t.projections(r.Exp,
		scope{inputs: []string{r.Name}, parts: parts(r.Input)},
		types.ElemOf(r.Type()))
	if err != nil {
		return err
	}
	b.RelType = &connect.Relation_Project{
		Project: &connect.Project{Input: input, Expressions: exps},
	}
	return nil
}

func (t *translator) group(r *core.Group, b *connect.Relation) error {
	sc := scope{inputs: []string{r.Name}, parts: parts(r.Input)}
	if len(r.Aggs) == 0 {
		// Grouping without aggregating asks only for distinct rows,
		// which Spark does by projecting the keys and deduplicating
		// them. One node becomes two, so the inner one takes the
		// next plan id.
		keyID := t.planID()
		keyInput, err := t.input(r.Input, r.Name)
		if err != nil {
			return err
		}
		keys := &connect.Project{Input: keyInput}
		for _, k := range r.Keys {
			e, err := t.exp(k.Exp, sc)
			if err != nil {
				return err
			}
			keys.Expressions = append(keys.Expressions, named(e, k.Label))
		}
		b.RelType = &connect.Relation_Deduplicate{
			Deduplicate: &connect.Deduplicate{
				Input: &connect.Relation{
					Common:  common(keyID),
					RelType: &connect.Relation_Project{Project: keys},
				},
				AllColumnsAsKeys: proto.Bool(true),
				WithinWatermark:  proto.Bool(false),
			},
		}
		return nil
	}
	input, err := t.input(r.Input, r.Name)
	if err != nil {
		return err
	}
	agg := &connect.Aggregate{
		Input:     input,
		GroupType: connect.Aggregate_GROUP_TYPE_GROUPBY,
	}
	for _, k := range r.Keys {
		e, err := t.exp(k.Exp, sc)
		if err != nil {
			return err
		}
		agg.GroupingExpressions = append(agg.GroupingExpressions,
			named(e, k.Label))
	}
	for _, a := range r.Aggs {
		e, err := t.aggregate(a, sc)
		if err != nil {
			return err
		}
		agg.AggregateExpressions = append(agg.AggregateExpressions,
			named(e, a.Label))
	}
	b.RelType = &connect.Relation_Aggregate{Aggregate: agg}
	return nil
}

func (t *translator) sort(r *core.Sort, b *connect.Relation) error {
	input, err := t.input(r.Input, r.Name)
	if err != nil {
		return err
	}
	s := &connect.Sort{Input: input}
	err = t.sortOrder(r.Exp, false,
		scope{inputs: []string{r.Name}, parts: parts(r.Input)},
		func(o *connect.Expression_SortOrder) {
			s.Order = append(s.Order, o)
		})
	if err != nil {
		return err
	}
	// A query orders its whole result, not each partition of it.
	s.IsGlobal = proto.Bool(true)
	b.RelType = &connect.Relation_Sort{Sort: s}
	return nil
}

func (t *translator) setRel(r *core.SetRel, b *connect.Relation) error {
	if len(r.Args) != setOpArity {
		// Spark's set operation takes two inputs; Morel's takes any
		// number, and nothing yet folds the one into the other.
		return unsupported(javaOp(r) + " of " + strconv.Itoa(len(r.Args)) +
			" inputs")
	}
	left, err := t.rel(r.Args[0])
	if err != nil {
		return err
	}
	right, err := t.rel(r.Args[1])
	if err != nil {
		return err
	}
	var kind connect.SetOperation_SetOpType
	// lint: sort until '^\t}' where '^\tcase '
	switch r.Kind {
	case core.ExceptSet:
		kind = connect.SetOperation_SET_OP_TYPE_EXCEPT
	case core.IntersectSet:
		kind = connect.SetOperation_SET_OP_TYPE_INTERSECT
	default:
		kind = connect.SetOperation_SET_OP_TYPE_UNION
	}
	b.RelType = &connect.Relation_SetOp{
		SetOp: &connect.SetOperation{
			LeftInput:  left,
			RightInput: right,
			SetOpType:  kind,
			// Morel's set operators are over bags and keep every row,
			// unless the query asked for distinct.
			IsAll:               proto.Bool(!r.Distinct),
			ByName:              proto.Bool(false),
			AllowMissingColumns: proto.Bool(false),
		},
	}
	return nil
}

func (t *translator) join(r *core.Join, b *connect.Relation) error {
	if r.Kind == core.LeftJoin {
		if key := keyColumn(r.Condition); key != "" {
			t.witnesses[r.RightName] = key
		}
	}
	left, err := t.input(r.Left, r.LeftName)
	if err != nil {
		return err
	}
	right, err := t.input(r.Right, r.RightName)
	if err != nil {
		return err
	}
	cond, err := t.exp(r.Condition,
		scope{inputs: []string{r.LeftName, r.RightName}})
	if err != nil {
		return err
	}
	var kind connect.Join_JoinType
	// lint: sort until '^\t}' where '^\tcase '
	switch r.Kind {
	case core.FullJoin:
		kind = connect.Join_JOIN_TYPE_FULL_OUTER
	case core.LeftJoin:
		kind = connect.Join_JOIN_TYPE_LEFT_OUTER
	case core.RightJoin:
		kind = connect.Join_JOIN_TYPE_RIGHT_OUTER
	default:
		kind = connect.Join_JOIN_TYPE_INNER
	}
	b.RelType = &connect.Relation_Join{
		Join: &connect.Join{
			Left: left, Right: right, JoinCondition: cond, JoinType: kind,
		},
	}
	return nil
}

// input translates a node's input. A relational input translates as
// itself; anything else is the collection the query scans, and takes
// the row's name as its alias.
func (t *translator) input(e core.Exp, name string) (*connect.Relation,
	error,
) {
	if _, isRel := e.(core.Rel); isRel {
		return t.rel(e)
	}
	planID := t.planID()
	leaf, err := t.leaf(e)
	if err != nil {
		return nil, err
	}
	return &connect.Relation{
		Common: common(planID),
		RelType: &connect.Relation_SubqueryAlias{
			SubqueryAlias: &connect.SubqueryAlias{Input: leaf, Alias: name},
		},
	}, nil
}

// leaf translates the collection a query scans.
func (t *translator) leaf(e core.Exp) (*connect.Relation, error) {
	planID := t.planID()
	if list, isList := e.(*core.List); isList {
		rows := make([]eval.Val, len(list.Args))
		for i, row := range list.Args {
			v, err := constValue(row)
			if err != nil {
				return nil, err
			}
			rows[i] = v
		}
		return t.localRelation(planID, rows, list.T)
	}
	if lit, isLit := e.(*core.Literal); isLit &&
		lit.Kind == ast.ValueLiteralOp {
		// A table arrives as its value, which knows the name Spark
		// has for it. The name is no guide: "scott.emps" may be a
		// table of the server, a data set of this session, or a
		// record whose field holds a collection, and only the first
		// is a scan.
		if table, isTable := lit.Value.(*eval.SparkTable); isTable {
			return tableRead(planID, table.QualifiedName()), nil
		}
		if table := eval.SparkTableOf(lit.Value); table != nil {
			return tableRead(planID, table.QualifiedName()), nil
		}
		// A collection the query reached through a name rather than
		// writing out, and not a table of a catalog: its rows cross
		// as a local relation, as an inline collection's do.
		if rows, isRows := lit.Value.([]eval.Val); isRows {
			return t.localRelation(planID, rows, lit.T)
		}
	}
	return nil, unsupported("collection " + javaOp(e))
}

func tableRead(planID int64, name string) *connect.Relation {
	return &connect.Relation{
		Common: common(planID),
		RelType: &connect.Relation_Read{
			Read: &connect.Read{
				ReadType: &connect.Read_NamedTable_{
					NamedTable: &connect.Read_NamedTable{
						UnparsedIdentifier: name,
					},
				},
			},
		},
	}
}

// localRelation translates a collection of rows as a local relation:
// the rows as an Arrow batch, and the schema that says what the
// bytes mean.
func (t *translator) localRelation(planID int64, rows []eval.Val,
	collectionType types.Type,
) (*connect.Relation, error) {
	elemType := types.ElemOf(collectionType)
	t.localRowCounts = append(t.localRowCounts, len(rows))
	data, err := EncodeArrow(rows, elemType)
	if err != nil {
		return nil, err
	}
	schema, err := SchemaJSON(elemType)
	if err != nil {
		return nil, err
	}
	return &connect.Relation{
		Common: common(planID),
		RelType: &connect.Relation_LocalRelation{
			LocalRelation: &connect.LocalRelation{
				Data: data, Schema: proto.String(schema),
			},
		},
	}, nil
}

// constValue returns the Morel value of a constant expression.
//
// The rows of an inline collection are written out in the query,
// and Spark carries them as data rather than as expressions, so what
// it wants is their values.
func constValue(e core.Exp) (eval.Val, error) {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		if d, ok := decimalLiteral(e); ok {
			return d, nil
		}
		if con, isCon := e.Fn.(*core.Con); isCon && con.Name == "SOME" {
			v, err := constValue(e.Arg)
			if err != nil {
				return nil, err
			}
			return eval.SomeVal(v), nil
		}
	case *core.Con:
		if !e.HasArg && e.Name == "NONE" {
			return eval.NoneVal, nil
		}
	case *core.Literal:
		//exhaustive:ignore
		switch e.Kind {
		case ast.BoolLiteralOp, ast.IntLiteralOp, ast.RealLiteralOp,
			ast.StringLiteralOp, ast.ValueLiteralOp:
			return e.Value, nil
		}
	case *core.Tuple:
		fields := make([]eval.Val, len(e.Args))
		for i, arg := range e.Args {
			v, err := constValue(arg)
			if err != nil {
				return nil, err
			}
			fields[i] = v
		}
		return fields, nil
	}
	return nil, unsupported("a row that is " + javaOp(e))
}

// decimalLiteral returns the value of "decimal s", where s is a
// string literal, and whether the expression is one. It is how a
// program writes a decimal constant.
func decimalLiteral(e core.Exp) (eval.Decimal, bool) {
	apply, isApply := e.(*core.Apply)
	if !isApply || builtinName(apply.Fn) != decimalType {
		return eval.Decimal{}, false
	}
	lit, isLit := apply.Arg.(*core.Literal)
	if !isLit || lit.Kind != ast.StringLiteralOp {
		return eval.Decimal{}, false
	}
	s, _ := lit.Value.(string)
	return eval.DecParseExact(s)
}

// count returns how many rows a "take" or a "skip" counts. Spark
// wants a number, so a count that is not a literal cannot be pushed
// down.
func count(e core.Exp, what string) (int32, error) {
	if lit, isLit := e.(*core.Literal); isLit &&
		lit.Kind == ast.IntLiteralOp {
		n, _ := lit.Value.(int32)
		return n, nil
	}
	return 0, unsupported(what + " of " + javaOp(e))
}

// projections translates the expression a "yield" computes into one
// Spark expression per output field. A field whose value is the
// input field of the same name needs no alias; anything else is
// aliased to the output name.
func (t *translator) projections(e core.Exp, sc scope,
	elemType types.Type,
) ([]*connect.Expression, error) {
	tuple, isTuple := e.(*core.Tuple)
	if !isTuple {
		// A query can yield one unnamed column -- "yield d.dname" --
		// and then there is no record of names to draw an alias from.
		x, err := t.exp(e, sc)
		if err != nil {
			return nil, err
		}
		return []*connect.Expression{x}, nil
	}
	names := labels(elemType)
	exps := make([]*connect.Expression, 0, len(tuple.Args))
	for i, arg := range tuple.Args {
		x, err := t.exp(arg, sc)
		if err != nil {
			return nil, err
		}
		name := names[i]
		if field, isField := fieldName(arg); isField && field == name {
			exps = append(exps, x)
		} else {
			exps = append(exps, alias(x, name))
		}
	}
	return exps, nil
}

// labels are the field names of a record or tuple type.
func labels(t types.Type) []string {
	switch t := t.(type) {
	case *types.Record:
		names := make([]string, len(t.Fields))
		for i, f := range t.Fields {
			names[i] = f.Label
		}
		return names
	case *types.Tuple:
		names := make([]string, len(t.Args))
		for i := range t.Args {
			names[i] = strconv.Itoa(i + 1)
		}
		return names
	}
	return nil
}

// fieldName returns the name of the field that an expression reads,
// and whether it reads one.
func fieldName(e core.Exp) (string, bool) {
	if apply, isApply := e.(*core.Apply); isApply {
		if sel, isSel := apply.Fn.(*core.Selector); isSel {
			return sel.Name, true
		}
	}
	return "", false
}

// isRow reports whether an expression is a row: a node's input, or
// a name a row was bound to.
func isRow(e core.Exp) bool {
	switch e.(type) {
	case *core.Input, *core.ID:
		return true
	}
	return false
}

// exp translates an expression.
func (t *translator) exp(e core.Exp, sc scope) (*connect.Expression,
	error,
) {
	if d, ok := decimalLiteral(e); ok {
		// Morel's decimal is arbitrary-precision, and Spark's is not,
		// so the literal carries the narrowest decimal type that
		// holds the value -- the type Spark's own parser gives the
		// same literal written as SQL.
		return decimalExp(d), nil
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		if sel, isSel := e.Fn.(*core.Selector); isSel {
			if isRow(e.Arg) && len(sc.parts) > 0 &&
				sel.Index < len(sc.parts) {
				// A whole input of a join, not a column of one:
				// "yield {d.dname, e}" asks for all of "e".
				return t.component(sc.parts[sel.Index], e.T)
			}
			if q, ok := qualifier(e.Arg, sc); ok {
				return attribute(qualified(q, sel.Name)), nil
			}
		}
		var args []core.Exp
		fn := flatten(e, &args)
		if name := builtinName(fn); name != "" {
			return t.function(name, args, sc)
		}
		return nil, unsupported("call to " + javaOp(e.Fn))
	case *core.Case:
		if name, args, ok := logical(e); ok {
			return t.function(name, args, sc)
		}
	case *core.ID:
		// A name that is not a column of the query: a plan's
		// parameter, say, or a value that could not be replaced
		// with what it holds. Naming it says which.
		return nil, unsupported("the variable " + e.Pat.Name)
	case *core.Input:
		return nil, unsupported("the variable " + rowVariable(sc, e))
	case *core.Literal:
		return literalExp(e)
	}
	return nil, unsupported("expression " + javaOp(e))
}

func literalExp(e *core.Literal) (*connect.Expression, error) {
	l := &connect.Expression_Literal{}
	//exhaustive:ignore
	// lint: sort until '^\t}' where '^\tcase '
	switch e.Kind {
	case ast.BoolLiteralOp:
		v, _ := e.Value.(bool)
		l.LiteralType = &connect.Expression_Literal_Boolean{Boolean: v}
	case ast.IntLiteralOp:
		v, _ := e.Value.(int32)
		l.LiteralType = &connect.Expression_Literal_Integer{Integer: v}
	case ast.RealLiteralOp:
		v, _ := e.Value.(float32)
		l.LiteralType = &connect.Expression_Literal_Double{
			Double: realAsDouble(v),
		}
	case ast.StringLiteralOp:
		v, _ := e.Value.(string)
		l.LiteralType = &connect.Expression_Literal_String_{String_: v}
	case ast.ValueLiteralOp:
		if d, isDecimal := e.Value.(eval.Decimal); isDecimal {
			return decimalExp(d), nil
		}
		return nil, unsupported("expression " + javaOp(e))
	default:
		return nil, unsupported("expression " + javaOp(e))
	}
	return literal(l), nil
}

// realAsDouble is a real as the double it was written as: the
// shortest decimal that identifies it, read as a double, so that
// "0.1" is the double 0.1 and not the float nearest it.
func realAsDouble(f float32) float64 {
	d, _ := strconv.ParseFloat(
		strconv.FormatFloat(float64(f), 'g', -1, 32), 64)
	return d
}

// bigDecimal is a decimal as Java's BigDecimal would hold it, at a
// scale that Spark allows (Spark has no negative scale, so "2E+3"
// becomes 2000): its plain text, its precision and its scale.
func bigDecimal(d eval.Decimal) (string, int32, int32) {
	plain := d.ToString('-')
	if strings.ContainsAny(plain, "E") {
		plain = plainDecimal(plain)
	}
	digits := strings.TrimPrefix(plain, "-")
	scale := 0
	if i := strings.IndexByte(digits, '.'); i >= 0 {
		scale = len(digits) - i - 1
		digits = digits[:i] + digits[i+1:]
	}
	digits = strings.TrimLeft(digits, "0")
	precision := max(len(digits), 1)
	// A Morel decimal has at most 34 digits, and a scale of at most
	// 6176, so neither overflows.
	return plain, int32(precision), int32(scale) //nolint:gosec // see above
}

// plainDecimal expands a decimal in scientific notation, "1.5E~3" or
// "1.5E-3", into plain notation.
func plainDecimal(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	mantissa, exponent, _ := strings.Cut(s, "E")
	exp, _ := strconv.Atoi(strings.Replace(exponent, "~", "-", 1))
	digits := strings.Replace(mantissa, ".", "", 1)
	point := 1 + exp
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		point = dot + exp
	}
	switch {
	case point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		return sign + digits + strings.Repeat("0", point-len(digits))
	default:
		return sign + digits[:point] + "." + digits[point:]
	}
}

func decimalExp(d eval.Decimal) *connect.Expression {
	plain, precision, scale := bigDecimal(d)
	return literal(&connect.Expression_Literal{
		LiteralType: &connect.Expression_Literal_Decimal_{
			Decimal: &connect.Expression_Literal_Decimal{
				Value:     plain,
				Precision: proto.Int32(max(precision, scale+1)),
				Scale:     proto.Int32(scale),
			},
		},
	})
}

// rowVariable is the name of a row as morel-java's core tree has it:
// "$e" for the row a query named "e", "$0" for one it did not.
func rowVariable(sc scope, input *core.Input) string {
	if input.Ordinal < len(sc.inputs) && sc.inputs[input.Ordinal] != "" {
		return "$" + sc.inputs[input.Ordinal]
	}
	return input.Name()
}

// logical returns the operator and operands of "a andalso b" or
// "a orelse b", which the core tree writes as a "case", and whether
// the expression is one.
func logical(c *core.Case) (string, []core.Exp, bool) {
	if len(c.Matches) != logicalArity {
		return "", nil, false
	}
	first, isLit := c.Matches[0].Pat.(*core.LiteralPat)
	_, isWild := c.Matches[1].Pat.(*core.WildcardPat)
	if !isLit || !isWild || first.Value != true {
		return "", nil, false
	}
	if isBoolLiteral(c.Matches[1].Exp, false) {
		return "$.andalso", []core.Exp{c.Exp, c.Matches[0].Exp}, true
	}
	if isBoolLiteral(c.Matches[0].Exp, true) {
		return "$.orelse", []core.Exp{c.Exp, c.Matches[1].Exp}, true
	}
	return "", nil, false
}

func isBoolLiteral(e core.Exp, b bool) bool {
	lit, isLit := e.(*core.Literal)
	return isLit && lit.Kind == ast.BoolLiteralOp && lit.Value == b
}

// arguments returns the arguments of a call: a tuple's fields, or
// the one argument of a one-argument call.
func arguments(arg core.Exp) []core.Exp {
	if tuple, isTuple := arg.(*core.Tuple); isTuple {
		return tuple.Args
	}
	return []core.Exp{arg}
}

// flatten peels a call down to the function it calls, adding the
// arguments it meets to args in the order they were written.
//
// A built-in takes its arguments one way or the other:
// "String.substring (s, 0, 2)" is one application of a tuple, and
// "String.isPrefix "S" s" is two applications, one per argument.
// Both arrive here as the same list, which is what the function
// table's templates number.
func flatten(e core.Exp, args *[]core.Exp) core.Exp {
	apply, isApply := e.(*core.Apply)
	if !isApply || builtinName(e) != "" {
		// Not a call, or a member of a structure, "String.size",
		// which is a function and not a call of one.
		return e
	}
	fn := flatten(apply.Fn, args)
	*args = append(*args, arguments(apply.Arg)...)
	return fn
}

// builtinName returns the name of the built-in that an expression
// is, "String.size" or "op +", or "" if it is not a built-in.
func builtinName(e core.Exp) string {
	switch e := e.(type) {
	case *core.Apply:
		sel, isSel := e.Fn.(*core.Selector)
		id, isID := e.Arg.(*core.ID)
		if isSel && isID {
			return id.Pat.Name + "." + sel.Name
		}
	case *core.ID:
		return e.Pat.Name
	}
	return ""
}

// function translates a call to a built-in.
func (t *translator) function(name string, args []core.Exp,
	sc scope,
) (*connect.Expression, error) {
	entry := t.fns.Lookup(name)
	if entry == nil {
		return nil, unsupported("built-in " + MLName(name) +
			", which the table omits")
	}
	//exhaustive:ignore
	// lint: sort until '^\t}' where '^\tcase '
	switch entry.Kind {
	case Direct:
		f := &connect.Expression_UnresolvedFunction{
			FunctionName: entry.Spark,
		}
		for _, arg := range args {
			x, err := t.exp(arg, sc)
			if err != nil {
				return nil, err
			}
			f.Arguments = append(f.Arguments, x)
		}
		return unresolved(f), nil
	case Rewritten:
		// The template is SQL, so its arguments must be SQL too, and
		// so must anything inside them. Spark parses what comes out.
		s, err := entry.Template.Expand(func(i int) (string, error) {
			return t.sql(args[i], sc)
		})
		if err != nil {
			return nil, err
		}
		return &connect.Expression{
			ExprType: &connect.Expression_ExpressionString_{
				ExpressionString: &connect.Expression_ExpressionString{
					Expression: s,
				},
			},
		}, nil
	case Subquery:
		return t.subquery(entry, args, sc)
	}
	return nil, unsupported(entry.Morel + ", which the table calls " +
		entry.Kind.String())
}

// aggregate translates an aggregate function over a group: the
// function the table names, applied to what the query aggregates.
// An aggregate with no argument counts rows, and Spark counts them
// by counting a constant.
func (t *translator) aggregate(a core.RelGroupAgg, sc scope) (
	*connect.Expression, error,
) {
	name := builtinName(a.Fn)
	if name == "" {
		return nil, unsupported("aggregate " + javaOp(a.Fn))
	}
	entry := t.fns.Lookup(name)
	if entry == nil {
		return nil, unsupported("aggregate " + MLName(name) +
			", which the table omits")
	}
	if entry.Kind != Aggregate {
		return nil, unsupported(entry.Morel +
			" over a group, which the table calls " + entry.Kind.String())
	}
	f := &connect.Expression_UnresolvedFunction{FunctionName: entry.Spark}
	if lit, isLit := a.Arg.(*core.Literal); a.Arg == nil ||
		isLit && lit.Kind == ast.UnitLiteralOp {
		f.Arguments = append(f.Arguments, literal(
			&connect.Expression_Literal{
				LiteralType: &connect.Expression_Literal_Integer{Integer: 1},
			}))
	} else {
		x, err := t.exp(a.Arg, sc)
		if err != nil {
			return nil, err
		}
		f.Arguments = append(f.Arguments, x)
	}
	return unresolved(f), nil
}

// named names an expression, unless it already reads the field of
// that name: a key called "deptno" that reads "deptno" needs no
// alias, and an aggregate always does.
func named(e *connect.Expression, name string) *connect.Expression {
	if a := e.GetUnresolvedAttribute(); a != nil {
		id := a.GetUnparsedIdentifier()
		if strings.HasSuffix(id, "."+name) || id == name {
			return e
		}
	}
	return alias(e, name)
}

func alias(e *connect.Expression, name string) *connect.Expression {
	return &connect.Expression{
		ExprType: &connect.Expression_Alias_{
			Alias: &connect.Expression_Alias{Expr: e, Name: []string{name}},
		},
	}
}

// sortOrder translates the keys a query orders by, which are a tuple
// when there is more than one, and each of which may be wrapped in
// "DESC". Spark puts nulls where that direction puts them: last when
// descending, first when ascending.
func (t *translator) sortOrder(e core.Exp, descending bool, sc scope,
	add func(*connect.Expression_SortOrder),
) error {
	if apply, isApply := e.(*core.Apply); isApply {
		if con, isCon := apply.Fn.(*core.Con); isCon &&
			con.Name == "DESC" && con.Datatype == "descending" {
			return t.sortOrder(apply.Arg, !descending, sc, add)
		}
	}
	if tuple, isTuple := e.(*core.Tuple); isTuple {
		for _, arg := range tuple.Args {
			err := t.sortOrder(arg, descending, sc, add)
			if err != nil {
				return err
			}
		}
		return nil
	}
	child, err := t.exp(e, sc)
	if err != nil {
		return err
	}
	o := &connect.Expression_SortOrder{
		Child:        child,
		Direction:    connect.Expression_SortOrder_SORT_DIRECTION_ASCENDING,
		NullOrdering: connect.Expression_SortOrder_SORT_NULLS_FIRST,
	}
	if descending {
		o.Direction = connect.Expression_SortOrder_SORT_DIRECTION_DESCENDING
		o.NullOrdering = connect.Expression_SortOrder_SORT_NULLS_LAST
	}
	add(o)
	return nil
}

// subquery translates a call that asks a question of a query:
// whether it has a row, whether it holds a value, or what its one
// value is. The query goes beside the root, under a plan id of its
// own, and the expression quotes that id.
//
// The query may read the outer row, which is what makes it
// correlated; nothing special is needed for that, because the outer
// row prints under the name it has, and Spark resolves it against
// the enclosing relation.
func (t *translator) subquery(entry *Entry, args []core.Exp,
	sc scope,
) (*connect.Expression, error) {
	var (
		kind     connect.SubqueryExpression_SubqueryType
		relation *connect.Relation
		negated  bool
		err      error
	)
	// lint: sort until '^\t}' where '^\tcase '
	switch entry.Morel {
	case "Relational.only":
		relation, err = t.relation(args[0])
		kind = connect.SubqueryExpression_SUBQUERY_TYPE_SCALAR
	case "op elem", "op notelem":
		relation, err = t.elemRelation(args[1], args[0], t.planID(), sc)
		kind = connect.SubqueryExpression_SUBQUERY_TYPE_EXISTS
		negated = entry.Morel == "op notelem"
	case nonEmptyName, "Relational.empty", "List.null":
		relation, err = t.relation(args[0])
		kind = connect.SubqueryExpression_SUBQUERY_TYPE_EXISTS
		// Spark has no NOT EXISTS of its own; "empty" is "exists"
		// denied.
		negated = entry.Morel != nonEmptyName
	default:
		return nil, unsupported(entry.Morel +
			", which the table calls a subquery")
	}
	if err != nil {
		return nil, err
	}
	// The expression quotes the plan id of the relation it asks
	// about, which the relation itself knows.
	t.references = append(t.references, relation)
	x := &connect.Expression{
		ExprType: &connect.Expression_SubqueryExpression{
			SubqueryExpression: &connect.SubqueryExpression{
				PlanId:       relation.GetCommon().GetPlanId(),
				SubqueryType: kind,
			},
		},
	}
	if negated {
		return call("not", x), nil
	}
	return x, nil
}

// relation translates the query a subquery expression asks about.
func (t *translator) relation(e core.Exp) (*connect.Relation, error) {
	if _, isRel := e.(core.Rel); !isRel {
		return nil, unsupported("a subquery over " + javaOp(e))
	}
	return t.rel(e)
}

// elemRelation translates the collection of an "elem" as the
// relation of an EXISTS subquery: not the values the query yields,
// but the rows that hold the value asked about.
//
// Spark Connect 4.0 has no IN subquery, so "x elem q" becomes
// "exists q where v = x", where v is what q yields. The projection
// comes off -- EXISTS does not look at it -- and the expression it
// yielded goes into the condition, which is also the only place it
// can go. Where q filters already, the equality joins that condition
// rather than standing in a second filter above it.
func (t *translator) elemRelation(collection, value core.Exp,
	planID int64, outer scope,
) (*connect.Relation, error) {
	project, isProject := collection.(*core.Project)
	if !isProject {
		return nil, unsupported("elem over " + javaOp(collection))
	}
	ps := parts(project.Input)
	inner := scope{inputs: []string{project.Name}, parts: ps}
	yielded, err := t.exp(project.Exp, inner)
	if err != nil {
		return nil, err
	}
	asked, err := t.exp(value, scope{inputs: outer.inputs, parts: ps})
	if err != nil {
		return nil, err
	}
	equality := call("==", yielded, asked)
	f := &connect.Filter{}
	if filter, isFilter := project.Input.(*core.Filter); isFilter {
		f.Input, err = t.input(filter.Input, filter.Name)
		if err != nil {
			return nil, err
		}
		cond, cerr := t.exp(filter.Condition,
			scope{inputs: []string{filter.Name}, parts: ps})
		if cerr != nil {
			return nil, cerr
		}
		f.Condition = call("and", cond, equality)
	} else {
		f.Input, err = t.input(project.Input, project.Name)
		if err != nil {
			return nil, err
		}
		f.Condition = equality
	}
	return &connect.Relation{
		Common:  common(planID),
		RelType: &connect.Relation_Filter{Filter: f},
	}, nil
}

// call builds a call to a Spark function.
func call(name string, args ...*connect.Expression) *connect.Expression {
	return unresolved(&connect.Expression_UnresolvedFunction{
		FunctionName: name, Arguments: args,
	})
}

func unresolved(f *connect.Expression_UnresolvedFunction,
) *connect.Expression {
	return &connect.Expression{
		ExprType: &connect.Expression_UnresolvedFunction_{
			UnresolvedFunction: f,
		},
	}
}

// component translates a reference to a whole input of a join: a
// struct of its columns, and, where the input may be absent, null
// instead of the struct when it was.
//
// Spark gives an absent side a row of nulls rather than an absent
// row, so the only way to tell is to ask whether a column that
// cannot be null is null. A row all of whose columns are optional
// has no such column, and the two cases are then the same bytes, so
// such a join is refused.
func (t *translator) component(name string, typ types.Type) (
	*connect.Expression, error,
) {
	rowType, isNullable, err := nullable(typ)
	if err != nil {
		return nil, err
	}
	fields := labels(rowType)
	if fields == nil {
		return nil, unsupported("a whole input of type " +
			rowType.String())
	}
	s := &connect.Expression_UnresolvedFunction{FunctionName: "struct"}
	for _, field := range fields {
		s.Arguments = append(s.Arguments,
			alias(attribute(name+"."+field), field))
	}
	structExp := unresolved(s)
	if !isNullable {
		return structExp, nil
	}
	witness, found := t.witnesses[name]
	if !found {
		return nil, unsupported("a row of this join that may be " +
			"absent, because nothing in the join's condition says " +
			"whether a row was found")
	}
	null := literal(&connect.Expression_Literal{
		LiteralType: &connect.Expression_Literal_Null{
			Null: &connect.DataType{
				Kind: &connect.DataType_Null{Null: &connect.DataType_NULL{}},
			},
		},
	})
	return call("when", call("isNull", attribute(name+"."+witness)), null,
		structExp), nil
}

// keyColumn returns a column of the right row's join key, or "" if
// the condition has none.
//
// This is the column that says whether a left join found the row: a
// key cannot be null in a row that matched, because a null key
// matches nothing, so asking whether it is null asks exactly the
// right question. The types cannot answer it: a catalog column is a
// plain type whether or not it holds nulls.
func keyColumn(condition core.Exp) string {
	var args []core.Exp
	var name string
	switch c := condition.(type) {
	case *core.Case:
		n, a, ok := logical(c)
		if !ok {
			return ""
		}
		name, args = n, a
	case *core.Apply:
		name = TableName(builtinName(c.Fn))
		args = arguments(c.Arg)
	default:
		return ""
	}
	switch name {
	case "$.andalso", "Bool.andalso":
		// Any conjunct may hold the key.
		for _, arg := range args {
			if column := keyColumn(arg); column != "" {
				return column
			}
		}
	case "op =":
		for _, arg := range args {
			if column, isColumn := fieldOfInput(arg, 1); isColumn {
				return column
			}
		}
	}
	return ""
}

// sql renders an expression as SQL, for a template to put its
// arguments into.
//
// Every compound expression is parenthesized, whatever its operator:
// the text goes to Spark's parser, and parenthesizing
// unconditionally is both shorter to write and safer to read than a
// table of precedences.
func (t *translator) sql(e core.Exp, sc scope) (string, error) {
	if d, ok := decimalLiteral(e); ok {
		// "BD" is how SQL spells a decimal literal; without it,
		// Spark would read "2" as an int and "2.5" as a double.
		plain, _, _ := bigDecimal(d)
		return plain + "BD", nil
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		if sel, isSel := e.Fn.(*core.Selector); isSel {
			if q, ok := qualifier(e.Arg, sc); ok {
				return qualified(q, sel.Name), nil
			}
		}
		var args []core.Exp
		fn := flatten(e, &args)
		if name := builtinName(fn); name != "" {
			return t.sqlCall(name, args, sc)
		}
	case *core.Case:
		if name, args, ok := logical(e); ok {
			return t.sqlCall(name, args, sc)
		}
	case *core.Literal:
		//exhaustive:ignore
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch e.Kind {
		case ast.BoolLiteralOp, ast.IntLiteralOp:
			return eval.PlanString(e.Value), nil
		case ast.RealLiteralOp:
			v, _ := e.Value.(float32)
			return javaReal(float64(v), float32Bits), nil
		case ast.StringLiteralOp:
			v, _ := e.Value.(string)
			return "'" + strings.ReplaceAll(v, "'", "''") + "'", nil
		case ast.ValueLiteralOp:
			if d, isDecimal := e.Value.(eval.Decimal); isDecimal {
				plain, _, _ := bigDecimal(d)
				return plain + "BD", nil
			}
		}
	}
	return "", unsupported("expression " + javaOp(e) +
		" inside a SQL template")
}

// sqlCall renders a call to a built-in as SQL.
func (t *translator) sqlCall(name string, args []core.Exp,
	sc scope,
) (string, error) {
	entry := t.fns.Lookup(name)
	if entry == nil {
		return "", unsupported("built-in " + javaConstant(name) +
			" inside a SQL template, which the table omits")
	}
	//exhaustive:ignore
	switch entry.Kind {
	case Rewritten:
		return entry.Template.Expand(func(i int) (string, error) {
			return t.sql(args[i], sc)
		})
	case Direct:
		if len(args) == 2 && !isWord(entry.Spark) {
			a, err := t.sql(args[0], sc)
			if err != nil {
				return "", err
			}
			b, err := t.sql(args[1], sc)
			if err != nil {
				return "", err
			}
			return "(" + a + " " + entry.Spark + " " + b + ")", nil
		}
		parts := make([]string, len(args))
		for i, arg := range args {
			s, err := t.sql(arg, sc)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return entry.Spark + "(" + strings.Join(parts, ", ") + ")", nil
	}
	return "", unsupported(javaConstant(name) +
		" inside a SQL template, which the table calls " +
		strings.ToUpper(entry.Kind.String()))
}

// isWord reports whether a Spark name is a word, and so written as a
// call, rather than a symbol written between its arguments.
func isWord(name string) bool {
	return name != "" && unicode.IsLetter(rune(name[0]))
}

// javaConstant is the name that morel-java gives a built-in's
// constant, "STRING_SIZE" for "String.size", which is what its
// messages about SQL templates say.
func javaConstant(name string) string {
	name = TableName(name)
	var b strings.Builder
	for i, c := range strings.ReplaceAll(name, ".", "_") {
		if unicode.IsUpper(c) && i > 0 && name[i-1] != '.' {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(c))
	}
	return b.String()
}

func attribute(identifier string) *connect.Expression {
	return &connect.Expression{
		ExprType: &connect.Expression_UnresolvedAttribute_{
			UnresolvedAttribute: &connect.Expression_UnresolvedAttribute{
				UnparsedIdentifier: identifier,
				IsMetadataColumn:   proto.Bool(false),
			},
		},
	}
}

func literal(l *connect.Expression_Literal) *connect.Expression {
	return &connect.Expression{
		ExprType: &connect.Expression_Literal_{Literal: l},
	}
}

// qualified is a column, qualified by the row it belongs to unless
// the row has no name.
func qualified(qualifier, name string) string {
	if qualifier == "" {
		return name
	}
	return qualifier + "." + name
}

// qualifier returns the alias that qualifies a field read out of an
// expression, and whether the expression is something a field can be
// read from.
//
// A field of the row is qualified by the row's own name. A field of
// a component of the row -- what a projection over a join reads --
// is qualified by the name of the input that contributed it, which
// the scope's parts list in order.
func qualifier(e core.Exp, sc scope) (string, bool) {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		if sel, isSel := e.Fn.(*core.Selector); isSel && isRow(e.Arg) &&
			sel.Index < len(sc.parts) {
			return sc.parts[sel.Index], true
		}
	case *core.ID:
		return elementName(e.Pat.Name), true
	case *core.Input:
		if e.Ordinal < len(sc.inputs) {
			return sc.inputs[e.Ordinal], true
		}
		return "", true
	}
	return "", false
}

// elementName is the name a row variable gives a plan: "e" for
// "$e", and "" for a row the compiler named, "$0".
func elementName(name string) string {
	if len(name) > 1 && name[0] == '$' && !unicode.IsDigit(
		rune(name[1])) && name != "$ordinal" {
		return name[1:]
	}
	if strings.HasPrefix(name, "$") {
		return ""
	}
	return name
}

// parts returns the names of a node's input's components: a join's
// two sides, or nothing at all, since only a join concatenates rows.
func parts(input core.Exp) []string {
	// lint: sort until '^\t}' where '^\tcase '
	switch input := input.(type) {
	case *core.Filter:
		// These pass their element through, so a node above one
		// still reads the components of whatever join is below it.
		return parts(input.Input)
	case *core.Join:
		return []string{input.LeftName, input.RightName}
	case *core.Skip:
		return parts(input.Input)
	case *core.Sort:
		return parts(input.Input)
	case *core.Take:
		return parts(input.Input)
	case *core.Unorder:
		return parts(input.Input)
	}
	return nil
}

// javaOp is the name morel-java gives the operator of an expression,
// which its messages say.
func javaOp(e core.Exp) string {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Con:
		return "ID"
	case *core.Input:
		return "ID"
	case *core.List:
		return "APPLY"
	case *core.Literal:
		//exhaustive:ignore
		switch e.Kind {
		case ast.ValueLiteralOp:
			return "VALUE_LITERAL"
		default:
			return strings.ToUpper(e.Kind.String())
		}
	case *core.Selector:
		return "RECORD_SELECTOR"
	}
	return strings.ToUpper(e.Op().String())
}

// End translator.go
