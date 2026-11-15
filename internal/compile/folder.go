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
	"math"

	"github.com/hydromatic/morel-go/internal/core"
	"github.com/hydromatic/morel-go/internal/eval"
)

// The rules for constant folding: which expressions may be evaluated
// at compile time. They are morel-java's Folder (hydromatic/morel#519).
//
// An expression is folded if it constructs a value from constants, or
// converts a constant from one type to another without losing
// information, or reads a field of a constant. Evaluating it at
// compile time gives the same value as evaluating it each time it is
// reached, and costs nothing at run time. A call that would raise an
// exception is not folded; it raises when it is reached, if it is
// reached.
//
// The compiler folds such an expression to constant code; the inliner
// reads a field off a record whose other fields are constant.

// foldable are the built-in functions that construct a value, or
// convert a value to another type without losing information, and so
// may be evaluated at compile time if their argument is constant.
//
// Not included: functions that round ("Real.floor", "Decimal.toReal")
// or truncate ("Time.toSeconds"), which lose information; functions
// that format or parse ("Int.toString", "Date.fromString"); and
// functions that depend on the environment ("Date.fromTimeLocal",
// which depends on the time zone).
var foldable = map[string]bool{
	// lint: sort until '^}' where '^\t"'
	"Bag.fromList":          true,
	"Char.chr":              true,
	"Char.ord":              true,
	"Decimal.decimal":       true,
	"Decimal.fromInt":       true,
	"Decimal.fromReal":      true,
	"Int.fromInt":           true,
	"Int.fromLarge":         true,
	"Int.toInt":             true,
	"Int.toLarge":           true,
	"Real.fromInt":          true,
	"String.str":            true,
	"Time.fromMicroseconds": true,
	"Time.fromMilliseconds": true,
	"Time.fromNanoseconds":  true,
	"Time.fromSeconds":      true,
	"Vector.fromList":       true,
	"Word.fromInt":          true,
	"Word.fromLargeWord":    true,
	"Word.toLargeWord":      true,
}

// foldableName is the qualified name of the built-in that an
// application calls, if it is foldable; "" otherwise.
func foldableName(apply *core.Apply) string {
	name := builtinName(apply.Fn)
	if id, isID := apply.Fn.(*core.ID); isID {
		name = planFnName(id.Pat.Name, id.Pat.T)
		if name == decimalName {
			name = "Decimal.decimal"
		}
	}
	if foldable[name] {
		return name
	}
	return ""
}

// isLossless reports whether a call to a foldable built-in loses no
// information. Every foldable built-in is lossless, except
// "Real.fromInt", because a real has 24 bits of precision and cannot
// hold every int; so "Real.fromInt 7" is folded, but not
// "Real.fromInt 16777217".
func isLossless(name string, arg core.Exp) bool {
	if name != "Real.fromInt" {
		return true
	}
	lit, isLit := arg.(*core.Literal)
	if !isLit {
		return false
	}
	i, isInt := lit.Value.(int32)
	if !isInt {
		return false
	}
	f := float32(i)
	// Compare as float64: converting the float32 of math.MaxInt32,
	// which is 2^31, back to int32 would overflow.
	return float64(f) == float64(i) && float64(f) <= math.MaxInt32
}

// isConstant reports whether an expression constructs a value from
// constants, and so may be compiled to code that returns that value:
// a literal (other than a table, which is read each time); a tuple or
// list whose elements are constant; a datatype constructor applied to
// a constant; a call to a foldable built-in with a constant argument;
// or a field of a constant.
func isConstant(e core.Exp) bool {
	// lint: sort until '^\t}' where '^\tcase '
	switch e := e.(type) {
	case *core.Apply:
		// Look at the function before the argument, so that a call to
		// a function that cannot be folded does not scan its argument;
		// the compiler asks again of each argument.
		switch fn := e.Fn.(type) {
		case *core.Con:
			return fn.Datatype != variantTypeName && isConstant(e.Arg)
		case *core.Selector:
			return isConstant(e.Arg)
		default:
			name := foldableName(e)
			return name != "" && isConstant(e.Arg) && isLossless(name, e.Arg)
		}
	case *core.Con:
		return e.Datatype != variantTypeName && !e.HasArg
	case *core.List:
		return allConstant(e.Args)
	case *core.Literal:
		switch e.Value.(type) {
		case eval.Relation, *eval.Relation, eval.Fn:
			// A table is read when the code runs.
			return false
		default:
			return true
		}
	case *core.Tuple:
		return allConstant(e.Args)
	default:
		return false
	}
}

// allConstant reports whether every expression is constant.
func allConstant(exps []core.Exp) bool {
	for _, e := range exps {
		if !isConstant(e) {
			return false
		}
	}
	return true
}

// foldCode replaces code that computes a constant, which exp is, with
// the value it computes, evaluated now rather than each time the code
// runs; or returns the code unchanged if evaluating it raises, so that
// it raises at run time, if it is reached.
func foldCode(exp core.Exp, code eval.Code) eval.Code {
	switch exp.(type) {
	case *core.Apply, *core.List, *core.Tuple:
	default:
		return code
	}
	if _, isConst := eval.ConstantValue(code); isConst || !isConstant(exp) {
		return code
	}
	v, err := code.Eval(eval.NewFrame(0))
	if err != nil {
		return code
	}
	return eval.Constant(v)
}

// compileElemSet compiles "x elem c" and "x notelem c", where c is a
// constant collection of hashable values, to code that looks x up in
// a set; ok is false if the call is not such.
func (c *compiler) compileElemSet(e *core.Apply, name string,
) (eval.Code, bool, error) {
	if name != elemPlanName && name != notElemPlanName {
		return nil, false, nil
	}
	tuple, isTuple := e.Arg.(*core.Tuple)
	if !isTuple || len(tuple.Args) != 2 || !isConstant(tuple.Args[1]) ||
		!isHashable(tuple.Args[0].Type()) {
		return nil, false, nil
	}
	collCode, err := c.compileExp(tuple.Args[1])
	if err != nil {
		return nil, false, err
	}
	coll, isConst := eval.ConstantValue(collCode)
	elems, isList := coll.([]eval.Val)
	if !isConst || !isList {
		return nil, false, nil
	}
	code, err := c.compileExp(tuple.Args[0])
	if err != nil {
		return nil, false, err
	}
	return eval.ElemSet(code, elems, name == notElemPlanName), true, nil
}

// constantRecordField folds a field of a record or tuple whose other
// fields are constant, such as "#b {a = 1, b = x}", to that field,
// "x". If another field is not constant, it might raise, so the record
// is evaluated.
func constantRecordField(fn, arg core.Exp) (core.Exp, bool) {
	sel, isSel := fn.(*core.Selector)
	tuple, isTuple := arg.(*core.Tuple)
	if !isSel || !isTuple || sel.Index >= len(tuple.Args) {
		return nil, false
	}
	for i, a := range tuple.Args {
		if i != sel.Index && !isConstant(a) {
			return nil, false
		}
	}
	return tuple.Args[sel.Index], true
}
