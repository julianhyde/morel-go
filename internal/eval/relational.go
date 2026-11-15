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
	"maps"
	"math"
	"strconv"
)

// The Relational structure's aggregate functions, used with
// "compute" and "into": count, sum, min, max, empty, and nonEmpty.
// Each takes a collection (a bag or a list, both []Val) and reduces
// it.

// RelationalAggregates returns the aggregate functions, keyed by
// their bare names; the kernel binds each both at top level and as
// a "Relational" member.
func RelationalAggregates() map[string]Fn {
	m := map[string]Fn{
		"count":    relCountFn,
		"empty":    relEmptyFn,
		"max":      relMaxFn,
		"maxBy":    relMaxByFn,
		"min":      relMinFn,
		"minBy":    relMinByFn,
		"nonEmpty": relNonEmptyFn,
		"sum":      SumFn(int32(0)),
	}
	maps.Copy(m, statAggregates())
	return m
}

// RelationalFunctions returns the non-aggregate Relational
// functions, keyed by their bare names; the kernel binds each both
// at top level and as a "Relational" member.
func RelationalFunctions() map[string]Fn {
	return map[string]Fn{
		"compare": relCompareFn,
		"iterate": relIterateFn,
		"only":    onlyFn,
	}
}

// relIterateFn is "iterate init update": the semi-naive fixpoint.
// Starting from init, each round applies update to (all, frontier)
// and keeps the rows not yet seen; it stops when a round adds
// nothing. Deduplicating against seen rows makes cyclic inputs
// terminate.
func relIterateFn(init Val) (Val, error) {
	initList := asList(init)
	return Fn(func(update Val) (Val, error) {
		list := initList
		frontier := initList
		seen := make(map[string]bool, len(initList))
		for _, o := range initList {
			seen[PlanString(o)] = true
		}
		for {
			next, err := ApplyVal(update, []Val{list, frontier})
			if err != nil {
				return nil, err
			}
			var fresh []Val
			for _, o := range asList(next) {
				k := PlanString(o)
				if !seen[k] {
					seen[k] = true
					fresh = append(fresh, o)
				}
			}
			if len(fresh) == 0 {
				return list, nil
			}
			list = append(append([]Val{}, list...), fresh...)
			frontier = fresh
		}
	}), nil
}

// relCompareFn is "compare (x, y)": LESS, EQUAL, or GREATER
// according to the structural order of x and y.
func relCompareFn(arg Val) (Val, error) {
	pair := arg.([]Val) //nolint:forcetypeassert // arg is a pair
	return orderVal(compareVals(pair[0], pair[1])), nil
}

// relCountFn is "count": the number of elements.
func relCountFn(arg Val) (Val, error) {
	//nolint:gosec // a collection never has more than 2^31 elements
	return int32(len(asList(arg))), nil
}

// relEmptyFn is "empty": whether the collection has no elements.
func relEmptyFn(arg Val) (Val, error) {
	return len(asList(arg)) == 0, nil
}

// relNonEmptyFn is "nonEmpty": whether the collection has elements.
func relNonEmptyFn(arg Val) (Val, error) {
	return len(asList(arg)) > 0, nil
}

// SumFn is "sum": the total of a numeric collection. An empty
// collection carries no element to reveal its type, so the zero it
// sums to is supplied by the caller (the compiler, from the static
// result type) -- real 0.0 and word 0w0 rather than int 0.
func SumFn(zero Val) Fn {
	return func(arg Val) (Val, error) {
		return sum(arg, zero)
	}
}

func sum(arg, zero Val) (Val, error) {
	list := asList(arg)
	if len(list) == 0 {
		return zero, nil
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch list[0].(type) {
	case Decimal:
		// Sum exactly, then round once, as morel-java does: aligning
		// every term to the smallest exponent and adding as integers
		// leaves no intermediate rounding, so 0.1 + 0.2 + 12.30 is
		// 12.6 rather than a value a digit off.
		return decSum(list)
	case float32:
		// Accumulate in float32, rounding at each step, as
		// morel-java's Z_SUM_REAL does. Summing in float64 and
		// rounding once is more accurate, but it gives a different
		// answer, and an iterative query -- a fixed point over a
		// real-valued rank -- carries the difference into digits
		// its results print.
		var sum float32
		for _, v := range list {
			sum += asReal(v)
		}
		return sum, nil
	case uint64:
		var sum uint64
		for _, v := range list {
			sum += asWord(v)
		}
		return sum, nil
	default:
		var sum int32
		for _, v := range list {
			sum += asInt(v)
		}
		return sum, nil
	}
}

// relMinFn is "min": the least element; Empty on an empty
// collection.
func relMinFn(arg Val) (Val, error) {
	return relExtreme(arg, -1)
}

// relMaxFn is "max": the greatest element; Empty on an empty
// collection.
func relMaxFn(arg Val) (Val, error) {
	return relExtreme(arg, 1)
}

// relMaxByFn is "maxBy keyFn c": the element of c whose key is
// greatest; Empty on an empty collection.
func relMaxByFn(keyFn Val) (Val, error) {
	return relExtremeBy(keyFn, 1), nil
}

// relMinByFn is "minBy keyFn c": the element of c whose key is
// least; Empty on an empty collection.
func relMinByFn(keyFn Val) (Val, error) {
	return relExtremeBy(keyFn, -1), nil
}

// relExtremeBy returns the function that takes a collection and
// gives the element whose key is least (sign -1) or greatest
// (sign 1). Keys are compared as values of their type, as max and
// min compare elements, so a key that is a word is ordered as
// unsigned and one that is a tuple, record, list or datatype is
// ordered as Morel orders it.
//
// Where several elements tie for the extreme key the first is
// returned, which for an unordered collection is not specified.
func relExtremeBy(keyFn Val, sign int) Fn {
	return func(arg Val) (Val, error) {
		list := asList(arg)
		if len(list) == 0 {
			return nil, &MorelError{Exn: ExnEmpty}
		}
		best := list[0]
		bestKey, err := ApplyVal(keyFn, best)
		if err != nil {
			return nil, err
		}
		for _, v := range list[1:] {
			key, err := ApplyVal(keyFn, v)
			if err != nil {
				return nil, err
			}
			if compareVals(key, bestKey)*sign > 0 {
				best, bestKey = v, key
			}
		}
		return best, nil
	}
}

// relExtreme returns the least (sign -1) or greatest (sign 1)
// element, comparing type-directed.
func relExtreme(arg Val, sign int) (Val, error) {
	list := asList(arg)
	if len(list) == 0 {
		return nil, &MorelError{Exn: ExnEmpty}
	}
	best := list[0]
	for _, v := range list[1:] {
		if compareVals(v, best)*sign > 0 {
			best = v
		}
	}
	return best, nil
}

// The aggregates that hydromatic/morel#503 added: the mean, standard
// deviation and variance, and an "Opt" and a "Some" form of each
// aggregate. An "Opt" form returns NONE where the plain form raises,
// and SOME of its result otherwise. A "Some" form takes options,
// ignores NONE, and returns NONE if there are too few SOMEs: it is
// SQL's aggregate of the same name, which ignores nulls.
func statAggregates() map[string]Fn {
	m := map[string]Fn{
		"avg":       meanFn(false, false),
		"avgOpt":    meanFn(true, false),
		"avgSome":   meanFn(true, true),
		"countSome": countSomeFn,
		"maxOpt":    optFn(relMaxFn, false),
		"maxSome":   optFn(relMaxFn, true),
		"minOpt":    optFn(relMinFn, false),
		"minSome":   optFn(relMinFn, true),
		"sumOpt":    optFn(SumFn(int32(0)), false),
		"sumSome":   optFn(SumFn(int32(0)), true),
	}
	for _, d := range []struct {
		name           string
		sample, stddev bool
	}{
		{"stddevPop", false, true},
		{"stddevSamp", true, true},
		{"varPop", false, false},
		{"varSamp", true, false},
	} {
		m[d.name] = deviationFn(d.sample, d.stddev, false, false)
		m[d.name+"Opt"] = deviationFn(d.sample, d.stddev, true, false)
		m[d.name+"Some"] = deviationFn(d.sample, d.stddev, true, true)
	}
	return m
}

// someValues returns the values of the SOME elements of a collection
// of options.
func someValues(list []Val) []Val {
	values := make([]Val, 0, len(list))
	for _, o := range list {
		if c, isCon := o.(Con); isCon && c.Arg != nil {
			values = append(values, c.Arg)
		}
	}
	return values
}

// countSomeFn is "countSome": the number of SOME elements, SQL's
// COUNT(x).
func countSomeFn(arg Val) (Val, error) {
	//nolint:gosec // a collection never has more than 2^31 elements
	return int32(len(someValues(asList(arg)))), nil
}

// optFn is the "Opt" form of an aggregate (or, if some, its "Some"
// form): NONE for an empty collection (of SOMEs), and otherwise SOME
// of the aggregate.
func optFn(f Fn, some bool) Fn {
	return func(arg Val) (Val, error) {
		list := asList(arg)
		if some {
			list = someValues(list)
		}
		if len(list) == 0 {
			return noneVal, nil
		}
		v, err := f(list)
		if err != nil {
			return nil, err
		}
		return someVal(v), nil
	}
}

// meanFn is "avg", or its "Opt" or "Some" form. The mean of int
// values is a real, computed in double and rounded to real at the
// end; of real values, a real; of decimal values, a decimal, the
// exact sum divided by the count and rounded, as "/" is. The plain
// form raises Empty on an empty collection.
func meanFn(opt, some bool) Fn {
	return func(arg Val) (Val, error) {
		list := asList(arg)
		if some {
			list = someValues(list)
		}
		if len(list) == 0 {
			if opt {
				return noneVal, nil
			}
			return nil, &MorelError{Exn: ExnEmpty}
		}
		v, err := mean(list)
		if err != nil || !opt {
			return v, err
		}
		return someVal(v), nil
	}
}

func mean(list []Val) (Val, error) {
	n := len(list)
	// lint: sort until '^\t}' where '^\tcase '
	switch list[0].(type) {
	case Decimal:
		s, err := decSum(list)
		if err != nil {
			return nil, err
		}
		count, _ := decFromIntFn(int32(n)) //nolint:gosec // see count
		return decDivFn([]Val{s, count})
	case float32:
		var s float64
		for _, v := range list {
			s += float64(asReal(v))
		}
		return float32(s / float64(n)), nil
	default:
		var s int64
		for _, v := range list {
			s += int64(asInt(v))
		}
		return float32(float64(s) / float64(n)), nil
	}
}

// deviationFn is a standard deviation or variance, sample or
// population, or its "Opt" or "Some" form. The elements may be int,
// real or decimal, and the result is a real, computed in double. A
// sample statistic needs at least two elements, and a population
// statistic one; with fewer, the plain form raises Empty.
func deviationFn(sample, stddev, opt, some bool) Fn {
	return func(arg Val) (Val, error) {
		list := asList(arg)
		if some {
			list = someValues(list)
		}
		n := len(list)
		least := 1
		if sample {
			least = 2
		}
		if n < least {
			if opt {
				return noneVal, nil
			}
			return nil, &MorelError{Exn: ExnEmpty}
		}
		var s float64
		for _, v := range list {
			s += toDouble(v)
		}
		m := s / float64(n)
		var squares float64
		for _, v := range list {
			d := toDouble(v) - m
			squares += d * d
		}
		divisor := n
		if sample {
			divisor = n - 1
		}
		variance := squares / float64(divisor)
		result := variance
		if stddev {
			result = math.Sqrt(variance)
		}
		if opt {
			return someVal(float32(result)), nil
		}
		return float32(result), nil
	}
}

// toDouble is a number as a double.
func toDouble(v Val) float64 {
	// lint: sort until '^\t}' where '^\tcase '
	switch v := v.(type) {
	case Decimal:
		f, _ := strconv.ParseFloat(v.ToString('-'), 64)
		return f
	case float32:
		return float64(v)
	default:
		return float64(asInt(v))
	}
}
