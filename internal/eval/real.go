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

package eval

import (
	"math"
	"strconv"
	"strings"
)

// The Real and Math structures. Everything computes in float64
// and rounds once to float32 (rust#44).

func asReal(v Val) float32 {
	f, ok := v.(float32)
	if !ok {
		panic("expected real")
	}
	return f
}

func asRealPair(arg Val) (float64, float64) {
	a, b := asPair(arg)
	return float64(asReal(a)), float64(asReal(b))
}

// real1 adapts a float64 function of one argument.
func real1(f func(a float64) float64) Fn {
	return func(arg Val) (Val, error) {
		return float32(f(float64(asReal(arg)))), nil
	}
}

// real2 adapts a float64 function of a pair.
func real2(f func(a, b float64) float64) Fn {
	return func(arg Val) (Val, error) {
		a, b := asRealPair(arg)
		return float32(f(a, b)), nil
	}
}

// realTest adapts a float64 predicate.
func realTest(f func(a float64) bool) Fn {
	return func(arg Val) (Val, error) {
		return f(float64(asReal(arg))), nil
	}
}

// realPairTest adapts a float64 predicate of a pair.
func realPairTest(f func(a, b float64) bool) Fn {
	return func(arg Val) (Val, error) {
		a, b := asRealPair(arg)
		return f(a, b), nil
	}
}

// realCompareFn is "Real.compare (a, b)"; it raises Unordered if
// either argument is NaN.
func realCompareFn(arg Val) (Val, error) {
	a, b := asRealPair(arg)
	if math.IsNaN(a) || math.IsNaN(b) {
		return nil, &MorelError{Exn: "Unordered"}
	}
	switch {
	case a < b:
		return orderVal(-1), nil
	case a > b:
		return orderVal(1), nil
	default:
		return orderVal(0), nil
	}
}

// realSignFn is "Real.sign r": ~1, 0, or 1; NaN raises Domain.
func realSignFn(arg Val) (Val, error) {
	r := float64(asReal(arg))
	switch {
	case math.IsNaN(r):
		return nil, &MorelError{Exn: "Domain"}
	case r < 0:
		return int32(-1), nil
	case r > 0:
		return int32(1), nil
	default:
		return int32(0), nil
	}
}

// realMinFn is "Real.min (a, b)"; if one argument is NaN, the
// other.
func realMinFn(arg Val) (Val, error) {
	a, b := asRealPair(arg)
	switch {
	case math.IsNaN(a):
		return float32(b), nil
	case math.IsNaN(b):
		return float32(a), nil
	default:
		return float32(math.Min(a, b)), nil
	}
}

// realMaxFn is "Real.max (a, b)"; if one argument is NaN, the
// other.
func realMaxFn(arg Val) (Val, error) {
	a, b := asRealPair(arg)
	switch {
	case math.IsNaN(a):
		return float32(b), nil
	case math.IsNaN(b):
		return float32(a), nil
	default:
		return float32(math.Max(a, b)), nil
	}
}

// realCheckFloatFn is "Real.checkFloat r": r, unless it is an
// infinity (Overflow) or NaN (Div).
func realCheckFloatFn(arg Val) (Val, error) {
	r := float64(asReal(arg))
	switch {
	case math.IsInf(r, 0):
		return nil, &MorelError{Exn: ExnOverflow}
	case math.IsNaN(r):
		return nil, &MorelError{Exn: ExnDiv}
	default:
		return asReal(arg), nil
	}
}

// realToIntFn adapts a float64 rounding function to a real->int
// built-in that raises Overflow when the result does not fit.
func realToIntFn(round func(float64) float64) Fn {
	return func(arg Val) (Val, error) {
		r := round(float64(asReal(arg)))
		if math.IsNaN(r) || r < math.MinInt32 ||
			r > math.MaxInt32 {
			return nil, &MorelError{Exn: ExnOverflow}
		}
		return int32(r), nil
	}
}

// realSplitFn is "Real.split r": its fractional and whole parts,
// as the record {frac, whole}.
func realSplitFn(arg Val) (Val, error) {
	r := float64(asReal(arg))
	var frac, whole float64
	switch {
	case r == 0 || math.IsNaN(r):
		frac, whole = r, r
	case math.IsInf(r, 0):
		frac, whole = math.Copysign(0, r), r
	default:
		whole = math.Trunc(r)
		frac = r - whole
	}
	return []Val{float32(frac), float32(whole)}, nil
}

// realModFn is "Real.realMod r", the fractional part of r.
func realModFn(arg Val) (Val, error) {
	v, err := realSplitFn(arg)
	if err != nil {
		return nil, err
	}
	return asList(v)[0], nil
}

// realToManExpFn is "Real.toManExp r": mantissa and exponent, as
// the record {exp, man}, with the mantissa in [0.5, 1).
func realToManExpFn(arg Val) (Val, error) {
	r := float64(asReal(arg))
	// Exponents for the IEEE special cases, as SML reports them.
	const specialExp, zeroExp = 129, -126
	var man float64
	var exp int
	switch {
	case math.IsNaN(r) || math.IsInf(r, 0):
		man, exp = r, specialExp
	case r == 0:
		man, exp = r, zeroExp
	default:
		man, exp = math.Frexp(r)
	}
	return []Val{int32(exp), float32(man)}, nil
}

// realFromManExpFn is "Real.fromManExp {exp, man}".
func realFromManExpFn(arg Val) (Val, error) {
	vals := asList(arg)
	exp := asInt(vals[0])
	man := float64(asReal(vals[1]))
	return float32(math.Ldexp(man, int(exp))), nil
}

// realFromStringFn is "Real.fromString s": parses the longest
// prefix of s that looks like a real, returning NONE if there is
// none.
func realFromStringFn(arg Val) (Val, error) {
	s := asString(arg)
	i := 0
	accept := func(test func(byte) bool) bool {
		if i < len(s) && test(s[i]) {
			i++
			return true
		}
		return false
	}
	digit := func(c byte) bool { return c >= '0' && c <= '9' }
	sign := func(c byte) bool {
		return c == '~' || c == '-' || c == '+'
	}
	accept(sign)
	digits := 0
	for accept(digit) {
		digits++
	}
	dot := i
	if accept(func(c byte) bool { return c == '.' }) {
		fracDigits := 0
		for accept(digit) {
			fracDigits++
		}
		if fracDigits == 0 {
			i = dot
		} else {
			digits += fracDigits
		}
	}
	if digits == 0 {
		return noneVal, nil
	}
	e := i
	if accept(func(c byte) bool { return c == 'e' || c == 'E' }) {
		accept(sign)
		expDigits := 0
		for accept(digit) {
			expDigits++
		}
		if expDigits == 0 {
			i = e
		}
	}
	text := strings.ReplaceAll(s[:i], "~", "-")
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return noneVal, nil //nolint:nilerr // NONE, not an error
	}
	return someVal(float32(f)), nil
}

// minNormal is the smallest positive normal float32.
const minNormal = 1.1754943508222875e-38

// realFromIntFn is "Real.fromInt i" (and top-level "real").
func realFromIntFn(arg Val) (Val, error) {
	return float32(asInt(arg)), nil
}

// realToStringFn is "Real.toString r".
func realToStringFn(arg Val) (Val, error) {
	return FormatReal(asReal(arg)), nil
}

// FormatReal renders a real the way java's Codes.floatToString
// does, matching Standard ML's Real.toString: the shortest
// decimal digits that round-trip the float32; plain decimal
// notation for magnitudes in [1e-3, 1e7) and scientific notation
// otherwise; a trailing ".0" dropped (1.0 prints as "1", 1.0e10
// as "1E10"); and "~" for minus, in exponents too.
func FormatReal(f float32) string {
	f64 := float64(f)
	switch {
	case math.IsNaN(f64):
		return "nan"
	case math.IsInf(f64, 1):
		return "inf"
	case math.IsInf(f64, -1):
		return "~inf"
	}
	s := strconv.FormatFloat(f64, 'E', -1, 32)
	mantissa, expText, _ := strings.Cut(s, "E")
	exp, err := strconv.Atoi(expText)
	if err != nil {
		return s
	}
	neg := strings.HasPrefix(mantissa, "-")
	digits := strings.ReplaceAll(
		strings.TrimPrefix(mantissa, "-"), ".", "")
	var b strings.Builder
	if neg {
		b.WriteString("~")
	}
	const loExp, hiExp = -3, 7
	if exp >= loExp && exp < hiExp {
		writeDecimal(&b, digits, exp)
	} else {
		writeScientific(&b, digits, exp)
	}
	// Real.minPos: SML reports 1.4E~45, though "1E~45" denotes
	// the same float.
	result := b.String()
	if strings.HasSuffix(result, "1E~45") {
		result = strings.Replace(result, "1E~45", "1.4E~45", 1)
	}
	return result
}

// writeDecimal renders digits with the decimal point after the
// digit at position exp: digits "15" with exp 0 is "1.5", digits
// "1" with exp -3 is "0.001", digits "1" with exp 2 is "100".
func writeDecimal(b *strings.Builder, digits string, exp int) {
	if exp < 0 {
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -exp-1))
		b.WriteString(digits)
		return
	}
	if len(digits) <= exp+1 {
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", exp+1-len(digits)))
		return
	}
	b.WriteString(digits[:exp+1])
	b.WriteString(".")
	b.WriteString(digits[exp+1:])
}

// writeScientific renders "d.dddEx", dropping a ".0" mantissa
// ("1E10" rather than "1.0E10") and using "~" for a negative
// exponent.
func writeScientific(b *strings.Builder, digits string, exp int) {
	b.WriteString(digits[:1])
	if len(digits) > 1 {
		b.WriteString(".")
		b.WriteString(digits[1:])
	}
	b.WriteString("E")
	if exp < 0 {
		b.WriteString("~")
		exp = -exp
	}
	b.WriteString(strconv.Itoa(exp))
}
