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
	"slices"
	"strings"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/types"
	"github.com/hydromatic/morel-go/internal/unify"
)

// Postfix method calls. A member marked "[@@method]" in a signature
// can be called "receiver.member arg" as sugar for the structure
// call. The parser reads "x.f a" as "Apply(Apply(#f, x), a)"; this
// pass rewrites that, when f is a method and x's type is known, into
// the equivalent structure call, which the ordinary resolver then
// handles. Dispatch is by the receiver's head type constructor, so
// "compare" reaches Date.compare on a date and Time.compare on a
// time.

// MethodInfo describes one "[@@method]" member from a signature.
type MethodInfo struct {
	Type      types.Type
	Structure string
	Name      string
	// Target, when set, is the qualified binding the call
	// rewrites to instead of "Structure.Name" — used by an
	// overload (a second method of the same name dispatching on
	// a different receiver type, e.g. Range.contains on a
	// continuous_set) whose implementation hides behind its own
	// binding.
	Target string
}

// methodCandidate is a registered method: the structure whose member
// implements it, the head type constructor it dispatches on, and
// whether its parameter is a tuple (so the receiver splices in).
type methodCandidate struct {
	structure    string
	receiverHead string
	target       string
	paramIsTuple bool
}

// MethodRegistry maps a method name to its candidates, and holds
// each structure's member types for receiver-type inference. For
// a plain identifier receiver, globalType reports the session's
// binding for the name (methods rewrite before type deduction, so
// only already-bound names dispatch).
type MethodRegistry struct {
	byName      map[string][]methodCandidate
	memberTypes map[string]map[string]types.Type
	globalType  func(string) types.Type
}

// NewMethodRegistry builds the registry from the signature's method
// list and its structure bindings.
func NewMethodRegistry(sys *types.System,
	methods []MethodInfo, bindings []Binding,
	globalType func(string) types.Type,
) *MethodRegistry {
	reg := &MethodRegistry{
		byName:      map[string][]methodCandidate{},
		memberTypes: map[string]map[string]types.Type{},
	}
	for _, b := range bindings {
		rec, ok := b.Type.(*types.Record)
		if !ok {
			continue
		}
		m := make(map[string]types.Type, len(rec.Fields))
		for _, f := range rec.Fields {
			m[f.Label] = f.Type
		}
		reg.memberTypes[b.Name] = m
	}
	for _, mi := range methods {
		t := mi.Type
		if ct := CollectionAggType(sys, mi.Name, t); ct != nil &&
			mi.Structure == relationalStructure {
			// An aggregate over a collection takes a list receiver
			// and a bag receiver alike, as its binding does; the
			// signature can only write "bag".
			t = ct
		}
		recv, isTuple := receiverPart(t)
		reg.byName[mi.Name] = append(reg.byName[mi.Name], methodCandidate{
			structure:    mi.Structure,
			receiverHead: typeHead(recv),
			target:       mi.Target,
			paramIsTuple: isTuple,
		})
	}
	reg.globalType = globalType
	return reg
}

// receiverPart returns a method's receiver type — its parameter, or
// the first element when the parameter is a tuple — and whether the
// parameter was a tuple.
func receiverPart(t types.Type) (types.Type, bool) {
	fn, ok := t.(*types.Fn)
	if !ok {
		return t, false
	}
	if tup, ok := fn.Param.(*types.Tuple); ok && len(tup.Args) > 0 {
		return tup.Args[0], true
	}
	return fn.Param, false
}

// relationalStructure is the structure whose aggregates work on a
// list and on a bag alike.
const relationalStructure = "Relational"

// collectionHead is the head of a collection type, whose
// orderedness is not settled: a method declared over one takes a
// list receiver and a bag receiver alike.
const collectionHead = "$collection"

// typeHead is a type's head constructor: a datatype or primitive
// name, "list", or a collection.
func typeHead(t types.Type) string {
	// lint: sort until '^	}' where '^	case '
	switch tt := t.(type) {
	case *types.Collection:
		return collectionHead
	case *types.List:
		return listTyCon
	case *types.Named:
		return tt.Name
	case *types.Primitive:
		return tt.String()
	default:
		return ""
	}
}

// resultHead is the head constructor of a type, or of a function
// type's result.
func resultHead(t types.Type) string {
	if fn, ok := t.(*types.Fn); ok {
		return typeHead(fn.Result)
	}
	return typeHead(t)
}

// RewriteDecl rewrites postfix method calls throughout a
// declaration in place.
func (reg *MethodRegistry) RewriteDecl(decl ast.Decl) {
	if vd, ok := decl.(*ast.ValDecl); ok {
		for _, b := range vd.Binds {
			b.Exp = reg.rewriteExpr(b.Exp)
		}
	}
}

// rewriteExpr rewrites postfix method calls within an expression,
// bottom-up.
func (reg *MethodRegistry) rewriteExpr(e ast.Expr) ast.Expr {
	// lint: sort until '^	}' where '^	case '
	switch x := e.(type) {
	case *ast.AnnotatedExp:
		x.Exp = reg.rewriteExpr(x.Exp)
	case *ast.Apply:
		x.Fn = reg.rewriteExpr(x.Fn)
		x.Arg = reg.rewriteExpr(x.Arg)
		if d, ok := reg.desugar(x); ok {
			return d
		}
	case *ast.Case:
		x.Exp = reg.rewriteExpr(x.Exp)
		reg.rewriteMatches(x.Matches)
	case *ast.Fn:
		reg.rewriteMatches(x.Matches)
	case *ast.If:
		x.Cond = reg.rewriteExpr(x.Cond)
		x.IfTrue = reg.rewriteExpr(x.IfTrue)
		x.IfFalse = reg.rewriteExpr(x.IfFalse)
	case *ast.InfixCall:
		x.A0 = reg.rewriteExpr(x.A0)
		x.A1 = reg.rewriteExpr(x.A1)
	case *ast.Let:
		for _, d := range x.Decls {
			reg.RewriteDecl(d)
		}
		x.Exp = reg.rewriteExpr(x.Exp)
	case *ast.ListExp:
		for i := range x.Args {
			x.Args[i] = reg.rewriteExpr(x.Args[i])
		}
	case *ast.PrefixCall:
		x.A = reg.rewriteExpr(x.A)
	case *ast.Raise:
		x.E = reg.rewriteExpr(x.E)
	case *ast.Record:
		if x.Base != nil {
			x.Base = reg.rewriteExpr(x.Base)
		}
		for i := range x.Fields {
			x.Fields[i].Exp = reg.rewriteExpr(x.Fields[i].Exp)
		}
		for _, m := range x.Modifiers {
			reg.rewriteModifier(m)
		}
	case *ast.Tuple:
		for i := range x.Args {
			x.Args[i] = reg.rewriteExpr(x.Args[i])
		}
	}
	return e
}

func (reg *MethodRegistry) rewriteMatches(matches []*ast.Match) {
	for _, m := range matches {
		m.Exp = reg.rewriteExpr(m.Exp)
	}
}

// rewriteModifier rewrites the expressions a record modifier
// contains, in place.
func (reg *MethodRegistry) rewriteModifier(m ast.Modifier) {
	switch m := m.(type) {
	case *ast.AllModifier:
		m.Exp = reg.rewriteExpr(m.Exp)
	case *ast.AssignModifier:
		for i := range m.Fields {
			m.Fields[i].Exp = reg.rewriteExpr(m.Fields[i].Exp)
		}
	}
}

// desugar rewrites "receiver.method arg" — parsed as
// "Apply(Apply(#method, receiver), arg)" — into the structure call,
// or reports that the apply is not a dispatchable method call.
func (reg *MethodRegistry) desugar(apply *ast.Apply) (ast.Expr, bool) {
	inner, ok := apply.Fn.(*ast.Apply)
	if !ok {
		return nil, false
	}
	sel, ok := inner.Fn.(*ast.RecordSelector)
	if !ok {
		return nil, false
	}
	if _, ok := reg.byName[sel.Name]; !ok {
		return nil, false
	}
	receiver := inner.Arg
	head := reg.receiverHead(receiver)
	if head == "" {
		return nil, false
	}
	return reg.desugarHead(apply, sel.Name, receiver, head)
}

// desugarHead is desugar for a call whose receiver's head type
// constructor is already known — as it is once type deduction has
// reached the call, for a receiver whose type only inference gives.
// It reports false if no method of that name dispatches on that
// head.
func (reg *MethodRegistry) desugarHead(apply *ast.Apply, name string,
	receiver ast.Expr, head string,
) (ast.Expr, bool) {
	var cand *methodCandidate
	for i, c := range reg.byName[name] {
		if headMatches(c.receiverHead, head) {
			cand = &reg.byName[name][i]
			break
		}
	}
	if cand == nil {
		return nil, false
	}
	span := apply.Span()
	var member ast.Expr = ast.NewApply(span,
		ast.NewRecordSelector(span, name),
		ast.NewID(span, cand.structure))
	if cand.target != "" {
		// An overload rewrites to its own hidden binding.
		member = ast.NewID(span, cand.target)
	}
	arg := apply.Arg
	if lit, ok := arg.(*ast.Literal); ok && lit.Kind == ast.UnitLiteralOp {
		// receiver.f () → Structure.f receiver
		return ast.NewApply(span, member, receiver), true
	}
	if cand.paramIsTuple {
		// receiver.f arg → Structure.f (receiver, arg)
		elems := []ast.Expr{receiver}
		if tup, ok := arg.(*ast.Tuple); ok {
			elems = append(elems, tup.Args...)
		} else {
			elems = append(elems, arg)
		}
		return ast.NewApply(span, member, ast.NewTuple(span, elems)), true
	}
	// receiver.f arg → Structure.f receiver arg
	return ast.NewApply(span, ast.NewApply(span, member, receiver), arg), true
}

// headMatches reports whether a method whose receiver is of head
// "param" takes a receiver of head "recv". A method over a
// collection, such as "Relational.max", takes a list and a bag
// alike.
func headMatches(param, recv string) bool {
	if param == collectionHead {
		return recv == listTyCon || recv == bagTyCon
	}
	return param == recv
}

// receiverHead infers the head type constructor of a receiver
// expression structurally, for a "Structure.member" projection or a
// "Structure.member arg" call whose member types are known.
func (reg *MethodRegistry) receiverHead(recv ast.Expr) string {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := recv.(type) {
	case *ast.ID:
		// A bound name's declared type gives the head directly.
		if t := reg.globalType(e.Name); t != nil {
			return typeHead(t)
		}
		return ""
	case *ast.ListExp:
		return listTyCon
	case *ast.Literal:
		return literalHead(e.Kind)
	}
	r, ok := recv.(*ast.Apply)
	if !ok {
		return ""
	}
	// A named function or constructor applied to an argument has
	// the head of its result: "(bag [1, 2]).max ()" is a bag, and
	// "(CLOSED (3, 7)).contains 5" a range.
	if id, ok := r.Fn.(*ast.ID); ok {
		if t := reg.globalType(id.Name); t != nil {
			if fn, isFn := t.(*types.Fn); isFn {
				return typeHead(fn.Result)
			}
		}
	}
	if inner, ok := r.Fn.(*ast.Apply); ok {
		if s, m, ok := structureMember(inner); ok {
			if mt := reg.memberType(s, m); mt != nil {
				return resultHead(mt)
			}
		}
	}
	if s, m, ok := structureMember(r); ok {
		if mt := reg.memberType(s, m); mt != nil {
			return typeHead(mt)
		}
	}
	return ""
}

// literalHead is the head constructor of a literal's type.
func literalHead(kind ast.Op) string {
	// lint: sort until '^\t}' where '^\tcase '
	switch kind {
	case ast.BoolLiteralOp:
		return "bool"
	case ast.CharLiteralOp:
		return "char"
	case ast.IntLiteralOp:
		return "int"
	case ast.RealLiteralOp:
		return realName
	case ast.StringLiteralOp:
		return "string"
	case ast.WordLiteralOp:
		return wordName
	default:
		return ""
	}
}

// structureMember matches "Structure.member" — "Apply(#member,
// ID(Structure))" — and returns the structure and member names.
func structureMember(a *ast.Apply) (string, string, bool) {
	sel, ok := a.Fn.(*ast.RecordSelector)
	if !ok {
		return "", "", false
	}
	id, ok := a.Arg.(*ast.ID)
	if !ok {
		return "", "", false
	}
	return id.Name, sel.Name, true
}

func (reg *MethodRegistry) memberType(structure, member string) types.Type {
	if m, ok := reg.memberTypes[structure]; ok {
		return m[member]
	}
	return nil
}

// methodApply reports the structure call that an apply desugars to,
// if the apply is "receiver.method arg" and the receiver's type
// picks out a method of that name. It is the resolver's half of the
// dispatch: MethodRegistry.RewriteDecl has already rewritten every
// call whose receiver can be typed from the AST alone, so what
// reaches here is a receiver only inference types — one bound by a
// "let", a function parameter, or a query variable.
func (r *typeResolver) methodApply(env typeEnv, apply *ast.Apply,
) (ast.Expr, bool) {
	if r.methods == nil {
		return nil, false
	}
	inner, ok := apply.Fn.(*ast.Apply)
	if !ok {
		return nil, false
	}
	sel, ok := inner.Fn.(*ast.RecordSelector)
	if !ok || sel.Safe {
		return nil, false
	}
	if _, known := r.methods.byName[sel.Name]; !known {
		return nil, false
	}
	id, ok := inner.Arg.(*ast.ID)
	if !ok {
		return nil, false
	}
	t, inScope := env.peek(id.Name)
	if !inScope {
		return nil, false
	}
	head := r.headOfTerm(t)
	if head == "" {
		head = r.solvedHead(t)
	}
	if head == "" {
		return nil, false
	}
	return r.methods.desugarHead(apply, sel.Name, inner.Arg, head)
}

// solvedHead is the head type constructor of a term according to a
// solve of the constraints gathered so far. It answers where
// headOfTerm cannot: a query variable is tied to its source's
// element type only by decomposing the source's collection term,
// which unification does and a scan of the constraints does not. The
// solve is side-effect free — its actions append to a throwaway work
// list — and it is made once per state of the constraints, since a
// receiver is typed the same as long as none have been added.
func (r *typeResolver) solvedHead(t unify.Term) string {
	if r.headSubstAt != len(r.pairs) {
		r.headSubstAt = len(r.pairs)
		subst, err := r.u.Unify(r.pairs, r.actions, r.constraints)
		if err != nil {
			// The program so far does not type. Dispatch nothing;
			// unification reports the conflict when the pass ends.
			subst = nil
		}
		r.headSubst = subst
	}
	if r.headSubst == nil {
		return ""
	}
	return resolvedHead(r.headSubst.Resolve(t))
}

// resolvedHead is the head type constructor of a term that a
// substitution has resolved, or "" if it is a variable or a
// structural term.
func resolvedHead(t unify.Term) string {
	s, isSeq := t.(*unify.Sequence)
	if !isSeq || structuralOp(s.Op) {
		return ""
	}
	if s.Op != collectionTyCon {
		return s.Op
	}
	if len(s.Terms) != collectionArity {
		return ""
	}
	ord, isSeq := s.Terms[1].(*unify.Sequence)
	if !isSeq {
		return ""
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch ord.Op {
	case orderedName:
		return listTyCon
	case unorderedName:
		return bagTyCon
	default:
		return ""
	}
}

// structuralOp reports whether a term operator builds a type from
// others — function, tuple, record — rather than being a type
// constructor a method can dispatch on.
func structuralOp(op string) bool {
	return op == fnTyCon || op == tupleTyCon ||
		strings.HasPrefix(op, recordTyCon)
}

// headOfTerm is the head type constructor of a term — "int", "list",
// a datatype name — as far as the constraints gathered so far
// determine it, or "" if they do not yet. It scans them backwards
// for the terms bound to a variable, following variable-to-variable
// links, which is how a name whose type was inferred earlier in the
// declaration yields its head.
func (r *typeResolver) headOfTerm(t unify.Term) string {
	return r.termHead(t, map[*unify.Var]bool{})
}

func (r *typeResolver) termHead(t unify.Term,
	seen map[*unify.Var]bool,
) string {
	switch t := t.(type) {
	case *unify.Sequence:
		return r.seqHead(t, seen)
	case *unify.Var:
		if seen[t] {
			return ""
		}
		seen[t] = true
		for _, pair := range slices.Backward(r.pairs) {
			if v, isVar := pair.Right.(*unify.Var); !isVar || v != t {
				continue
			}
			if head := r.termHead(pair.Left, seen); head != "" {
				return head
			}
		}
	}
	return ""
}

// seqHead is the head type constructor of a sequence. The structural
// operators — function, tuple, record — are not type constructors,
// and a term built from one has no head.
func (r *typeResolver) seqHead(s *unify.Sequence,
	seen map[*unify.Var]bool,
) string {
	switch {
	case s.Op == collectionTyCon:
		return r.collectionHead(s, seen)
	case structuralOp(s.Op):
		return ""
	default:
		return s.Op
	}
}

// collectionArity is the number of arguments of a collection term:
// its element type and its orderedness.
const collectionArity = 2

// collectionHead resolves a collection term to "list" or "bag" by
// its orderedness, so that dispatch tells the two apart. It is ""
// while the orderedness is undetermined.
func (r *typeResolver) collectionHead(s *unify.Sequence,
	seen map[*unify.Var]bool,
) string {
	if len(s.Terms) != collectionArity {
		return ""
	}
	ord := s.Terms[1]
	for {
		v, isVar := ord.(*unify.Var)
		if !isVar || seen[v] {
			break
		}
		seen[v] = true
		next := r.boundTerm(v)
		if next == nil {
			break
		}
		ord = next
	}
	if atom, isSeq := ord.(*unify.Sequence); isSeq &&
		len(atom.Terms) == 0 {
		switch atom.Op {
		case orderedName:
			return listTyCon
		case unorderedName:
			return bagTyCon
		}
	}
	return ""
}

// boundTerm is the term most recently equated with a variable, or
// nil if the constraints gathered so far bind it to none.
func (r *typeResolver) boundTerm(v *unify.Var) unify.Term {
	for _, pair := range slices.Backward(r.pairs) {
		if v2, isVar := pair.Right.(*unify.Var); isVar && v2 == v {
			return pair.Left
		}
	}
	return nil
}
