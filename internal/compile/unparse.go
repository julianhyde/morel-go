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
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/parse"
	"github.com/hydromatic/morel-go/internal/pp"
	"github.com/hydromatic/morel-go/internal/types"
)

// resolveBuiltins rewrites an identifier that names a structure
// member -- "op ^", or the alias "size" -- into the member form
// itself, "String.^" and "String.size".
//
// morel-java does this in its inliner, which replaces such an
// identifier with the built-in's function literal, and the
// distinction is what a plan's rendering turns on: an operator
// still written as an identifier renders infix, `"one:" ^ s`,
// while a resolved member renders `#^ String ("one:", s)`. That
// is why the initial plan and an optimized one spell the same
// application differently.
//
// morel-go's inliner leaves the identifier alone, so the
// substitution happens here instead, on the tree a plan is
// rendered from and never on the tree that is evaluated.
func resolveBuiltins(sys *types.System, d core.Decl) core.Decl {
	r := &rewriter{sys: sys}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		id, ok := e.(*core.ID)
		if !ok || strings.Contains(id.Pat.Name, ".") {
			return nil, false
		}
		qualified := planFnName(id.Pat.Name, id.Pat.T)
		structName, member, ok := strings.Cut(qualified, ".")
		if !ok {
			return nil, false
		}
		return &core.Apply{
			T:   id.Pat.T,
			Fn:  &core.Selector{T: id.Pat.T, Name: member},
			Arg: &core.ID{Pat: &core.IDPat{Name: structName}},
		}, true
	}
	return r.rewriteDecl(d)
}

// UnparseDecl renders a Core declaration as one line of source
// text — the form Sys.planEx returns. Distinct variables sharing
// a name are renumbered in first-appearance order: the first
// keeps the name, later ones append "_1", "_2", and so on.
func UnparseDecl(sys *types.System, decl core.Decl) string {
	return UnparseDeclWidth(sys, decl, 0)
}

// UnparseDeclWidth is UnparseDecl laid out to fit a width; a width of
// 0 is no width, and prints what UnparseDecl prints.
func UnparseDeclWidth(sys *types.System, decl core.Decl, width int) string {
	u := &unparser{sys: sys, seen: map[string][]*core.IDPat{}, width: width}
	u.decl(decl)
	return u.render()
}

// Operator contexts: an operator's left and right binding powers.
// A sub-expression is parenthesized when the context binds
// tighter than the expression's own operator.
const (
	// precQuery is the binding of a query, and of "fn", "let" and
	// "case": each has an open-ended tail, so each parenthesizes
	// where a step keyword could otherwise be read as its own.
	precQuery   = 1
	precOrelse  = 1 // orelse: left 2, right 3
	precAndalso = 2
	precCompare = 4 // = <> < <= > >= elem: non-associative
	precCons    = 5 // :: @: right-associative
	precPlus    = 6 // + - ^
	precTimes   = 7
	precApply   = 8
	precAtom    = 99
)

// binding is an operator's left and right binding powers,
// derived from precedence and associativity: each power is twice
// the precedence, the far side of the associativity one tighter.
func binding(prec int, assoc rune) (int, int) {
	lo := prec + prec
	hi := lo + 1
	switch assoc {
	case 'l':
		return lo, hi
	case 'r':
		return hi, lo
	default:
		return hi, hi
	}
}

// unparser accumulates the rendering.
type unparser struct {
	sys  *types.System
	seen map[string][]*core.IDPat

	// The text is built as a document, so that a layout can be chosen
	// to fit a width. A run of characters with no break in it
	// accumulates in pending and becomes one text when a break
	// arrives, which keeps the document small and lets the unparse
	// methods go on appending characters. Until a break point is
	// offered -- a group around something that may be laid out either
	// way -- the document is a flat concatenation, and renders the
	// same at every width. A width of 0 means no width: every group is
	// laid out flat, so nothing printed changes.
	pending strings.Builder
	docs    []pp.Doc
	frames  []frame
	width   int

	// gen and genCount renumber generated binders; see name.
	gen      map[*core.IDPat]string
	genCount map[string]int
	// treeMode is whether a relational node breaks out onto lines
	// of its own, which is what plan text prints; see relTree.
	treeMode bool
	// typeNames are the monikers a legend has been asked for, in
	// the order they were asked for; see typeRef.
	typeNames []string
	// relDefs are the relations broken out of the expressions that
	// held them, in the order they were first referred to, and
	// relParamCache what each reads from outside itself; see
	// relRef.
	relDefs       []core.Rel
	relParamCache map[core.Rel][]*core.IDPat
	// boundInPlan is every variable the plan binds, which is what
	// distinguishes a fragment's parameter from a global it names.
	boundInPlan map[*core.IDPat]bool
	// inlined is whether the expression has been through the
	// inliner. morel-java's inliner replaces "elem" and "notelem"
	// with calls that its printer writes as "op elem (a, b)", where
	// before inlining it writes "a elem b"; nothing here changes the
	// expression, so the stage decides.
	inlined bool
}

// frame is an open region: where in docs it began, how far a break
// inside it indents, and whether it decides for itself whether to
// break.
type frame struct {
	start  int
	indent int
	group  bool
}

func (u *unparser) put(s string) { u.pending.WriteString(s) }

// flush turns the pending text into a document.
func (u *unparser) flush() {
	if u.pending.Len() > 0 {
		u.docs = append(u.docs, pp.Text(u.pending.String()))
		u.pending.Reset()
	}
}

// startGroup begins a region that is laid out on one line if it
// fits and broken at its soft breaks otherwise; a broken line is
// indented by indent from where the region began.
func (u *unparser) startGroup(indent int) {
	u.start(indent, true)
}

// startNest begins a region that is indented but not grouped. A
// group decides for itself whether to break; a nest only says
// where a break lands. It is what a "let" wants: its four breaks
// are one decision, and two of the parts they enclose are
// indented.
func (u *unparser) startNest(indent int) {
	u.start(indent, false)
}

func (u *unparser) start(indent int, group bool) {
	u.flush()
	u.frames = append(u.frames,
		frame{start: len(u.docs), indent: indent, group: group})
}

// endGroup ends the region that startGroup began.
func (u *unparser) endGroup() { u.end() }

// endNest ends the region that startNest began.
func (u *unparser) endNest() { u.end() }

func (u *unparser) end() {
	u.flush()
	f := u.frames[len(u.frames)-1]
	u.frames = u.frames[:len(u.frames)-1]
	inner := pp.Concat(u.docs[f.start:]...)
	d := pp.Nest(f.indent, inner)
	if f.group {
		d = pp.Group(d)
	}
	u.docs = append(u.docs[:f.start], d)
}

// hardBreak is a break that is always taken. Tree mode uses it
// between nodes, which are one to a line whatever the width.
func (u *unparser) hardBreak() {
	u.flush()
	u.docs = append(u.docs, pp.HardLine())
}

// softBreak offers a line break, which is a space if the group it is
// in fits on a line.
func (u *unparser) softBreak() {
	u.flush()
	u.docs = append(u.docs, pp.Line())
}

// render returns the text, laid out to fit the width.
func (u *unparser) render() string {
	u.flush()
	width := u.width
	if width <= 0 {
		width = math.MaxInt32
	}
	return pp.Render(width, pp.Concat(u.docs...))
}

// name renders a variable, renumbering repeats of its name.
//
// A generated name -- one the printer or a pass made, which a "$"
// marks and which no identifier can contain -- is renumbered
// differently: the whole sequence is numbered from zero in order
// of first occurrence, per prefix, so that "v$123", "v$110",
// "v$200", "v$110" print as "v$0", "v$1", "v$2", "v$1". That is
// what makes the text depend on the query and nothing else, and
// it survives nesting, which a rule about allocation does not:
// two nested trees may each allocate "v$0", and a printer sees
// the whole text and numbers what it finds.
func (u *unparser) name(pat *core.IDPat) {
	if prefix, isGen := genPrefix(pat.Name); isGen &&
		os.Getenv("MOREL_REL_RAW") == "" {
		u.put(u.genName(pat, prefix))
		return
	}
	list := u.seen[pat.Name]
	for i, p := range list {
		if p == pat {
			u.put(suffixed(pat.Name, i))
			return
		}
	}
	u.seen[pat.Name] = append(list, pat)
	u.put(suffixed(pat.Name, len(list)))
}

// genPrefix splits a generated name into its prefix and reports
// whether it is one: "v$12" is generated with prefix "v", and
// "x" is not. Each prefix is numbered in its own sequence, so a
// tree's "v$" and a lowering's "w$" do not interleave. A binder
// named after a node's input, "$0" or "$1", is a "v$" too.
func genPrefix(name string) (string, bool) {
	if name == "$0" || name == "$1" {
		return "v", true
	}
	i := strings.IndexByte(name, '$')
	if i <= 0 || i == len(name)-1 {
		return "", false
	}
	for _, c := range name[i+1:] {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return name[:i], true
}

// genName is a generated binder's name in the text: its prefix
// and its position in that prefix's sequence.
func (u *unparser) genName(pat *core.IDPat, prefix string) string {
	if u.gen == nil {
		u.gen = map[*core.IDPat]string{}
	}
	if name, ok := u.gen[pat]; ok {
		return name
	}
	if u.genCount == nil {
		u.genCount = map[string]int{}
	}
	name := prefix + "$" + strconv.Itoa(u.genCount[prefix])
	u.genCount[prefix]++
	u.gen[pat] = name
	return name
}

// realLiteralText renders a real literal as morel-java writes one:
// a whole number keeps its ".0", so that "1000.0" reads back as a
// real and not an int.
func realLiteralText(v float32) string {
	s := strconv.FormatFloat(float64(v), 'g', -1, 32)
	if strings.ContainsAny(s, ".eEIN") {
		return s
	}
	return s + ".0"
}

// suffixed quotes a name that has to be quoted to be read back --
// a reserved word such as "left" -- and appends the renumbering
// suffix. The suffix goes outside the quotes, so that what is
// marked as the reserved word is the name the user wrote.
func suffixed(name string, i int) string {
	quoted := parse.QuoteIdent(name)
	if i == 0 {
		return quoted
	}
	return quoted + "_" + strconv.Itoa(i)
}

// decl renders a declaration.
func (u *unparser) decl(d core.Decl) {
	switch d := d.(type) {
	case *core.NonRecValDecl:
		if u.treeMode || u.width > 0 {
			// The value may start on the next line, indented two,
			// which is what a "let" wants: "let" belongs at the
			// head of a line of its own, not trailing an "=".
			u.startGroup(letIndent)
			u.put("val ")
			u.pat(d.Pat)
			u.put(" =")
			u.softBreak()
			u.exp(d.Exp, 0, 0)
			u.endGroup()
			return
		}
		u.put("val ")
		u.pat(d.Pat)
		u.put(" = ")
		u.exp(d.Exp, 0, 0)
	case *core.RecValDecl:
		u.put("val rec ")
		for i, b := range d.Binds {
			if i > 0 {
				u.put(" and ")
			}
			u.pat(b.Pat)
			u.put(" = ")
			u.exp(b.Exp, 0, 0)
		}
	}
}

// exp renders an expression in an operator context.
func (u *unparser) exp(e core.Exp, left, right int) {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		u.apply(e, left, right)
	case *core.Case:
		u.caseExp(e, left, right)
	case *core.Con:
		u.put(e.Name)
	case *core.Fn:
		u.wrap(left, right, 1, 1, func() {
			u.put("fn ")
			u.name(e.IDPat)
			u.put(" => ")
			u.exp(e.Exp, 0, 0)
		})
	case *core.From:
		u.wrap(left, right, 1, 1, func() { u.from(e) })
	case *core.ID:
		u.name(e.Pat)
	case *core.Input:
		// "$0" is the element of the node's input, and "$1", for a
		// join, the element of its right input. They are never
		// record labels and never appear in an element type:
		// fields are addressed by label, inputs by position.
		u.put(e.Name())
	case *core.Let:
		u.wrap(left, right, 1, 1, func() { u.letExp(e) })
	case *core.List:
		// A list literal is an application underneath, so as an
		// argument it parenthesizes.
		l, r := binding(precApply, 'l')
		u.wrap(left, right, l, r, func() {
			u.put("[")
			u.exps(e.Args)
			u.put("]")
		})
	case *core.Literal:
		u.literal(e)
	case *core.Ordinal:
		// The row's position, which a node that counts its rows
		// binds as "$ordinal".
		u.put("$ordinal")
	case *core.RangeList:
		u.rangeList(e)
	case *core.Selector:
		u.put(selectorText(e.Name))
	case *core.Tuple:
		u.tuple(e)
	case core.Rel:
		// A relational operator is the first non-whitespace on its
		// line, so a relation reached from inside an expression
		// cannot print here. In tree mode it is broken out and
		// referred to; elsewhere it prints in place, in prefix
		// form, as any other expression does.
		if u.treeMode {
			// A reference has the shape of an application, so it
			// is parenthesized where an application would be.
			l, r := binding(precApply, 'l')
			u.wrap(left, right, l, r, func() {
				u.put(u.relRef(e))
			})
			return
		}
		if lowered := u.lowered(e); lowered != nil {
			u.exp(lowered, left, right)
			return
		}
		u.put(e.OpName())
		u.relArgs(e)
		for _, input := range e.Inputs() {
			u.put(" ")
			u.exp(input, precApply, right)
		}
	default:
		u.put("?")
	}
}

// lowered is the step list a node lowers to, or nil where the
// lowering declines.
//
// Outside tree mode this printer writes Morel, and Morel has no
// node: a query is a "from" with steps. So the boundary the
// compiler draws is drawn here too -- a tree is what the passes
// carry, a step list is what is written and what runs -- and the
// prefix form below is what is left when the lowering cannot.
//
// It is scaffolding, and says so: once "Sys.planEx" prints the
// tree (spec.md §6, and M7), nothing outside tree mode has a node
// to print.
func (u *unparser) lowered(rel core.Rel) core.Exp {
	if u.sys == nil {
		return nil
	}
	exp, _ := LowerRel(u.sys, rel)
	if exp == nil {
		return nil
	}
	if _, isRel := exp.(core.Rel); isRel {
		// A node the lowering could not take apart, which would
		// print as this again.
		return nil
	}
	return exp
}

// letIndent is how far a "let" indents its declaration and its
// body when it breaks.
const letIndent = 2

// letExp renders a "let".
//
// One decision, four breaks: a "let" that does not fit becomes
// "let", its declaration, "in", its body and "end", each on a
// line, with the declaration and the body indented two. One
// group, so they are taken together; nests rather than groups
// inside it, so the two that are indented do not decide for
// themselves.
//
// Only in a plan. Elsewhere there is no width to fit, and a
// relation still prints in place, whose own line breaks would
// fall inside this indentation.
func (u *unparser) letExp(e *core.Let) {
	if !u.treeMode && u.width == 0 {
		u.put("let ")
		u.decl(e.Decl)
		u.put(" in ")
		u.exp(e.Exp, 0, 0)
		u.put(" end")
		return
	}
	u.startGroup(0)
	u.startNest(letIndent)
	u.put("let")
	u.softBreak()
	u.decl(e.Decl)
	u.endNest()
	u.softBreak()
	u.put("in")
	u.startNest(letIndent)
	u.softBreak()
	u.exp(e.Exp, 0, 0)
	u.endNest()
	u.softBreak()
	u.put("end")
	u.endGroup()
}

// wrap parenthesizes body when the context binds tighter than the
// node.
func (u *unparser) wrap(left, right, nodeLeft, nodeRight int,
	body func(),
) {
	if left > nodeLeft || nodeRight < right {
		u.put("(")
		body()
		u.put(")")
		return
	}
	body()
}

// exps renders a comma-separated list.
func (u *unparser) exps(args []core.Exp) {
	for i, a := range args {
		if i > 0 {
			u.put(", ")
		}
		u.exp(a, 0, 0)
	}
}

// tuple renders a tuple, as a record when its type is one.
func (u *unparser) tuple(e *core.Tuple) {
	// A record or tuple may break after a comma, one field to a
	// line, which is the other place a plan's lines get long.
	if rec, ok := e.T.(*types.Record); ok {
		u.startGroup(0)
		u.put("{")
		for i, f := range rec.Fields {
			if i > 0 {
				u.put(",")
				u.softBreak()
			}
			u.put(f.Label + " = ")
			u.exp(e.Args[i], 0, 0)
		}
		u.put("}")
		u.endGroup()
		return
	}
	if len(e.Args) == 0 {
		u.put("()")
		return
	}
	u.startGroup(0)
	u.put("(")
	for i, arg := range e.Args {
		if i > 0 {
			u.put(",")
			u.softBreak()
		}
		u.exp(arg, 0, 0)
	}
	u.put(")")
	u.endGroup()
}

// literal renders a constant.
func (u *unparser) literal(e *core.Literal) {
	// lint: sort until '^\t}' where '^\tcase '
	switch v := e.Value.(type) {
	case *eval.RangeExtent:
		u.put(strconv.Quote(v.T.String()))
	case bool:
		u.put(strconv.FormatBool(v))
	case core.Unit:
		u.put("()")
	case float32:
		u.put(negText(realLiteralText(v)))
	case int32:
		if e.Kind == ast.CharLiteralOp {
			u.put(charText(v))
			return
		}
		u.put(negText(strconv.FormatInt(int64(v), 10)))
	case string:
		u.put(strconv.Quote(v))
	case uint64:
		u.put(fmt.Sprintf("0wx%X", v))
	default:
		u.put("?")
	}
}

// negText renders a numeric text with Morel's "~" negation.
func negText(s string) string {
	if strings.HasPrefix(s, "-") {
		return "~" + s[1:]
	}
	return s
}

// charText renders a character literal.
func charText(c rune) string {
	switch c {
	case '"':
		return `#"\""`
	case '\\':
		return `#"\\"`
	default:
		return `#"` + string(c) + `"`
	}
}

// rangeList renders a range list as morel-java's Core writes it: a
// call of "Range.flatten" on a list of range constructors, so that
// "[1 .. 5, 10]" is "#flatten Range ([CLOSED (1, 5), POINT 10])".
func (u *unparser) rangeList(e *core.RangeList) {
	u.put("#flatten Range ([")
	for i, item := range e.Items {
		if i > 0 {
			u.put(", ")
		}
		u.rangeItem(item)
	}
	u.put("])")
}

// rangeItem renders one range constructor and its bounds.
func (u *unparser) rangeItem(item core.RangeItem) {
	one := func(name string, bound core.Exp) {
		u.put(name + " ")
		_, r := binding(precApply, 'l')
		u.exp(bound, r, 0)
	}
	two := func(name string) {
		u.put(name + " (")
		u.exp(item.Lo, 0, 0)
		u.put(", ")
		u.exp(item.Hi, 0, 0)
		u.put(")")
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch item.Kind {
	case ast.RangeAll:
		u.put("ALL")
	case ast.RangeAtLeast:
		one("AT_LEAST", item.Lo)
	case ast.RangeAtMost:
		one("AT_MOST", item.Hi)
	case ast.RangeClosed:
		two("CLOSED")
	case ast.RangeClosedOpen:
		two("CLOSED_OPEN")
	case ast.RangeGreaterThan:
		one("GREATER_THAN", item.Lo)
	case ast.RangeLessThan:
		one("LESS_THAN", item.Hi)
	case ast.RangeOpen:
		two("OPEN")
	case ast.RangeOpenClosed:
		two("OPEN_CLOSED")
	case ast.RangePoint:
		one("POINT", item.Lo)
	}
}

// infixName maps a core operator name to its infix spelling, for
// the operators that render infix. It covers every infix operator
// of the grammar, as morel-java's Op.BY_OP_NAME does: an operator
// written as an identifier -- "op ^" -- has not been resolved to
// the structure member that implements it, and so still renders
// as the operator it was written as. See infixOf.
func infixName(name string) (string, int, rune) {
	// lint: sort until '^\t}' where '^\tcase '
	switch name {
	case eqOpName, opElem, opGe, opGt, opLe, opLt, opNe, opNotElem:
		return strings.TrimPrefix(name, "op "), precCompare, 'n'
	case opAt, opCons:
		return strings.TrimPrefix(name, "op "), precCons, 'r'
	case opCaret, opMinus, opPlus:
		return strings.TrimPrefix(name, "op "), precPlus, 'l'
	case opDiv, opMod, opTimes:
		return strings.TrimPrefix(name, "op "), precTimes, 'l'
	default:
		return "", 0, 0
	}
}

// infixOf returns the infix spelling of an application's
// function, and whether it has one.
//
// An unresolved operator -- a bare "op X" identifier -- always
// renders infix. A resolved structure member renders as
// "#member Structure", with the sole exception of "Int.+" and
// "Real.+", which morel-java keeps infix (Resolver.toOp); that is
// why a plan can read "#* Int (x, y + 3)", mixing the two forms
// in one expression.
func infixOf(fn core.Exp) (string, int, rune) {
	name := builtinName(fn)
	if op, prec, assoc := infixName(name); op != "" {
		if _, isID := fn.(*core.ID); isID {
			return op, prec, assoc
		}
		return "", 0, 0
	}
	switch name {
	case "Int.+", "Real.+":
		return "+", precPlus, 'l'
	default:
		return "", 0, 0
	}
}

// apply renders an application: infix for the comparison and plus
// family, "#member Structure" for other qualified builtins, the
// extent form for internal extents, and juxtaposition otherwise.
func (u *unparser) apply(e *core.Apply, left, right int) {
	name := builtinName(e.Fn)
	if name == ExtentName {
		lit, ok := e.Arg.(*core.Literal)
		if ok {
			l, r := binding(precApply, 'l')
			u.wrap(left, right, l, r, func() {
				u.put("extent ")
				u.literal(lit)
			})
			return
		}
	}
	if op, prec, assoc := infixOf(e.Fn); op != "" &&
		(!u.inlined || op != "elem" && op != "notelem") {
		if tuple, ok := e.Arg.(*core.Tuple); ok &&
			len(tuple.Args) == 2 {
			l, r := binding(prec, assoc)
			// A non-associative operator's operand is parenthesized
			// when it is an operator of the same precedence:
			// "b = (i = 0)".
			innerL, innerR := l, r
			if assoc == 'n' {
				innerL, innerR = l+1, r+1
			}
			u.wrap(left, right, l, r, func() {
				u.exp(tuple.Args[0], left, innerL)
				u.put(" " + op + " ")
				u.exp(tuple.Args[1], innerR, right)
			})
			return
		}
	}
	fnText, ok := u.applyFnText(e)
	l, r := binding(precApply, 'l')
	if id, isID := e.Fn.(*core.ID); isID {
		if op, _, _ := infixName(id.Pat.Name); op != "" &&
			(!u.inlined || op != "elem" && op != "notelem") {
			// An infix operator applied to something other than a
			// pair is not an infix call; it is written as an
			// application of the operator's name: "`op +` p".
			fnText, ok = "`"+id.Pat.Name+"`", true
		}
	}
	if fnText == "~" {
		// A prefix operator has no left operand, so anything to
		// its left parenthesizes it: "2 * (~ x)", never "2 * ~ x".
		l = 0
	}
	u.wrap(left, right, l, r, func() {
		if ok {
			u.put(fnText)
		} else {
			u.exp(e.Fn, left, l)
		}
		u.put(" ")
		u.exp(e.Arg, r, right)
	})
}

// applyFnText renders an application's function position as text:
// "not", "op elem", "#member Structure", or a record selection.
func (u *unparser) applyFnText(e *core.Apply) (string, bool) {
	// lint: sort until '^\t}' where '^\tcase '
	switch fn := e.Fn.(type) {
	case *core.Apply:
		// A qualified builtin: Structure.member.
		if name := builtinName(e.Fn); name != "" &&
			strings.Contains(name, ".") {
			return sharpName(name), true
		}
		return "", false
	case *core.ID:
		name := fn.Pat.Name
		if name == notName {
			return notName, true
		}
		if name == opNegate {
			// A prefix operator, written before its one operand;
			// it binds more tightly than any binary operator, so
			// "~ $0 + 1" needs no parentheses and "~ ($0 + 1)"
			// keeps them.
			return "~", true
		}
		qualified := planFnName(name, fn.Pat.T)
		if strings.Contains(qualified, ".") {
			return sharpName(qualified), true
		}
		if strings.HasPrefix(name, "op ") ||
			strings.Contains(name, ".") {
			if strings.Contains(name, ".") {
				return sharpName(name), true
			}
			return name, true
		}
		return "", false
	case *core.Selector:
		return selectorText(fn.Name), true
	default:
		return "", false
	}
}

// sharpName renders "Structure.member" as "#member Structure", or
// "#`*` Structure" for a member that is an operator.
func sharpName(qualified string) string {
	dot := strings.Index(qualified, ".")
	return selectorText(qualified[dot+1:]) + " " + qualified[:dot]
}

// selectorText renders a record selector, "#label" or "#1". A label
// that is not letters, digits, underscores and primes -- an operator
// such as "*" -- is enclosed in back-ticks, "#`*`", which the parser
// reads back as the same selector.
func selectorText(label string) string {
	if isPlainLabel(label) {
		return "#" + label
	}
	return "#`" + strings.ReplaceAll(label, "`", "``") + "`"
}

// isPlainLabel reports whether a label can follow "#" without
// quoting: non-empty, and letters, digits, underscores and primes.
func isPlainLabel(label string) bool {
	if label == "" {
		return false
	}
	for _, r := range label {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' &&
			r != '\'' {
			return false
		}
	}
	return true
}

// caseExp renders a case, spelling the boolean-connective
// encodings back as operators. An andalso or orelse chain is a group
// of its own, so an outer one breaks before an inner one does, and a
// condition that fits stays on its line; they are where a long plan
// line is worth breaking.
func (u *unparser) caseExp(e *core.Case, left, right int) {
	if cond, ifTrue, ifFalse, ok := asBoolCase(e); ok {
		if isBoolLiteral(ifFalse, false) &&
			!isBoolLiteral(ifTrue, true) {
			l, r := binding(precAndalso, 'l')
			u.wrap(left, right, l, r, func() {
				u.startGroup(0)
				u.exp(cond, left, l)
				u.softBreak()
				u.put("andalso ")
				u.exp(ifTrue, r, right)
				u.endGroup()
			})
			return
		}
		if isBoolLiteral(ifTrue, true) &&
			!isBoolLiteral(ifFalse, false) {
			l, r := binding(precOrelse, 'l')
			u.wrap(left, right, l, r, func() {
				u.startGroup(0)
				u.exp(cond, left, l)
				u.softBreak()
				u.put("orelse ")
				u.exp(ifFalse, r, right)
				u.endGroup()
			})
			return
		}
	}
	u.wrap(left, right, 1, 1, func() {
		// A "case" that does not fit puts each arm after the first
		// on a line of its own, behind the "|" that introduces it.
		// Only where there is a width to fit.
		layout := u.treeMode || u.width > 0
		if layout {
			u.startGroup(letIndent)
		}
		u.put("case ")
		u.exp(e.Exp, 0, 0)
		u.put(" of ")
		for i, m := range e.Matches {
			if i > 0 {
				if layout {
					u.softBreak()
					u.put("| ")
				} else {
					u.put(" | ")
				}
			}
			u.pat(m.Pat)
			u.put(" => ")
			u.exp(m.Exp, 0, 0)
		}
		if layout {
			u.endGroup()
		}
	})
}

// from renders a query.
func (u *unparser) from(e *core.From) {
	switch e.Kind {
	case ast.ExistsOp:
		u.put("exists")
	case ast.ForallOp:
		u.put("forall")
	default:
		u.put("from")
	}
	scans := 0
	var rowVar *core.IDPat
	rowVars := 0
	for _, step := range e.Steps {
		u.step(step, &scans, &rowVar, &rowVars)
	}
}

// step renders one query step.
func (u *unparser) step(step core.FromStep, scans *int,
	rowVar **core.IDPat, rowVars *int,
) {
	// lint: sort until '^\t}' where '^\tcase '
	switch s := step.(type) {
	case *core.Distinct:
		// A distinct over a single variable is spelled as an
		// atom group.
		if *rowVars == 1 {
			u.put(" group ")
			u.name(*rowVar)
		} else {
			u.put(" distinct")
		}
	case *core.GroupStep:
		u.group(s)
	case *core.Into:
		u.put(" into ")
		u.exp(s.Fn, 0, 0)
	case *core.Order:
		u.put(" order ")
		u.exp(s.Exp, 0, 0)
	case *core.Scan:
		if *scans == 0 {
			u.put(" ")
		} else {
			u.put(" join ")
		}
		*scans++
		u.scanPat(s.Pat)
		if id, ok := s.Pat.(*core.IDPat); ok {
			*rowVar = id
		}
		*rowVars += len(core.PatIDs(s.Pat))
		if isInfiniteExtent(s.Exp) {
			u.put(" : " + collectionElem(s.Exp.Type()).String())
			return
		}
		u.put(" in ")
		u.exp(s.Exp, precQuery+1, precQuery+1)
	case *core.SkipStep:
		u.put(" skip ")
		u.exp(s.Exp, 0, 0)
	case *core.TakeStep:
		u.put(" take ")
		u.exp(s.Exp, 0, 0)
	case *core.Through:
		u.put(" through ")
		u.scanPat(s.Pat)
		u.put(" in ")
		u.exp(s.Fn, 0, 0)
	case *core.Where:
		u.put(" where ")
		u.exp(s.Exp, 0, 0)
	case *core.Yield:
		u.yield(s, rowVar, rowVars)
	}
}

// yield renders a yield step.
func (u *unparser) yield(s *core.Yield, rowVar **core.IDPat,
	rowVars *int,
) {
	if s.Fields != nil {
		u.put(" yield {")
		for i, f := range s.Fields {
			if i > 0 {
				u.put(", ")
			}
			u.put(f.Pat.Name + " = ")
			u.exp(f.Exp, 0, 0)
		}
		u.put("}")
		*rowVars = len(s.Fields)
		if len(s.Fields) == 1 {
			*rowVar = s.Fields[0].Pat
		}
		return
	}
	u.put(" yield ")
	u.exp(s.Exp, 0, 0)
	if id, ok := s.Exp.(*core.ID); ok {
		*rowVar = id.Pat
		*rowVars = 1
	}
}

// group renders a group step.
func (u *unparser) group(s *core.GroupStep) {
	u.put(" group ")
	if len(s.Keys) == 1 && len(s.Aggs) == 0 {
		u.exp(s.Keys[0].Exp, 0, 0)
		return
	}
	u.put("{")
	for i, k := range s.Keys {
		if i > 0 {
			u.put(", ")
		}
		u.put(k.Pat.Name + " = ")
		u.exp(k.Exp, 0, 0)
	}
	u.put("}")
	if len(s.Aggs) > 0 {
		u.put(" compute {")
		for i, a := range s.Aggs {
			if i > 0 {
				u.put(", ")
			}
			u.put(a.Pat.Name + " = ")
			u.exp(a.Fn, 0, 0)
			if a.Arg != nil {
				u.put(" over ")
				u.exp(a.Arg, 0, 0)
			}
		}
		u.put("}")
	}
}

// scanPat renders a scan's pattern, parenthesizing record
// patterns.
func (u *unparser) scanPat(p core.Pat) {
	if tp, ok := p.(*core.TuplePat); ok {
		if _, isRec := tp.T.(*types.Record); isRec {
			u.put("(")
			u.pat(p)
			u.put(")")
			return
		}
	}
	u.pat(p)
}

// pat renders a pattern.
func (u *unparser) pat(p core.Pat) {
	// lint: sort until '^\t}' where '^\tcase '
	switch p := p.(type) {
	case *core.AsPat:
		u.name(p.Pat)
		u.put(" as ")
		u.pat(p.Body)
	case *core.Con0Pat:
		u.put(p.Name)
	case *core.ConPat:
		u.put(p.Name + "(")
		u.pat(p.Arg)
		u.put(")")
	case *core.ConsPat:
		u.pat(p.Head)
		u.put(" :: ")
		u.pat(p.Tail)
	case *core.IDPat:
		u.name(p)
	case *core.ListPat:
		u.put("[")
		for i, a := range p.Args {
			if i > 0 {
				u.put(", ")
			}
			u.pat(a)
		}
		u.put("]")
	case *core.LiteralPat:
		u.literal(&core.Literal{
			T: p.T, Kind: p.Kind,
			Value: p.Value,
		})
	case *core.TuplePat:
		if rec, ok := p.T.(*types.Record); ok {
			u.put("{")
			for i, f := range rec.Fields {
				if i > 0 {
					u.put(", ")
				}
				u.put(f.Label + " = ")
				u.pat(p.Args[i])
			}
			u.put("}")
			return
		}
		u.put("(")
		for i, a := range p.Args {
			if i > 0 {
				u.put(", ")
			}
			u.pat(a)
		}
		u.put(")")
	case *core.WildcardPat:
		u.put("_")
	default:
		u.put("?")
	}
}
