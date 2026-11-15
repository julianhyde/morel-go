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
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HashScanStage is a join whose condition has an equality between a
// key over the input row and a key over the scanned element. It puts
// the elements into buckets by RightKey once, then pairs each input
// row only with the elements of the bucket its LeftKey selects, and
// evaluates the whole condition, Cond, over each such pair. The
// result, and its order, are those of the nested loop: rows in input
// order, and for each, the elements of its bucket in source order.
//
// Source does not depend on the input row, and neither the condition
// nor the keys read the "ordinal". For a left join, Left is set, the
// pattern's slots are wrapped in SOME, and an input row with no match
// is emitted with them NONE.
type HashScanStage struct {
	Source   Code
	Pat      Pat
	Name     string
	LeftKey  Code
	RightKey Code
	Cond     Code
	Left     bool
	PatSlots []int
	// Emit says what the join passes on: each pair, or, for a
	// semi-join or an anti-join, the input row.
	Emit Emit
}

func (s *HashScanStage) transform(q *fromCode, f *Frame, rows [][]Val,
) ([][]Val, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	buckets, err := s.buckets(q, f, rows[0])
	if err != nil {
		return nil, err
	}
	var out [][]Val
	for _, row := range rows {
		q.restore(f, row)
		k, err := s.LeftKey.Eval(f)
		if err != nil {
			return nil, err
		}
		out, err = s.probe(q, f, row, buckets[HashKey(k)], out)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// buckets evaluates the source, once, and puts its elements into
// buckets by their right key.
func (s *HashScanStage) buckets(q *fromCode, f *Frame, row []Val,
) (map[string][]Val, error) {
	q.restore(f, row)
	coll, err := s.Source.Eval(f)
	if err != nil {
		return nil, err
	}
	buckets := map[string][]Val{}
	for _, elem := range asList(coll) {
		if !s.Pat.Match(elem, f) {
			continue
		}
		k, err := s.RightKey.Eval(f)
		if err != nil {
			return nil, err
		}
		key := HashKey(k)
		buckets[key] = append(buckets[key], elem)
	}
	return buckets, nil
}

// probe pairs an input row with the elements of its bucket, and
// appends to out what the join passes on.
func (s *HashScanStage) probe(q *fromCode, f *Frame, row []Val,
	bucket []Val, out [][]Val,
) ([][]Val, error) {
	matched := false
	for _, elem := range bucket {
		q.restore(f, row)
		if !s.Pat.Match(elem, f) {
			continue
		}
		v, err := s.Cond.Eval(f)
		if err != nil {
			return nil, err
		}
		if b, _ := v.(bool); !b {
			continue
		}
		matched = true
		if s.Emit != EmitPairs {
			break
		}
		if s.Left {
			wrapSome(f, s.PatSlots)
		}
		out = append(out, q.snapshot(f))
	}
	switch {
	case s.Emit == EmitSemi && matched, s.Emit == EmitAnti && !matched:
		out = append(out, row)
	case s.Left && !matched:
		q.restore(f, row)
		setNone(f, s.PatSlots)
		out = append(out, q.snapshot(f))
	}
	return out, nil
}

// HashKey encodes a value of a hashable type -- one whose equality
// is the equality of its representation: not "real" -- as a string
// that two values share exactly when they are equal.
func HashKey(v Val) string {
	var b strings.Builder
	writeHashKey(&b, v)
	return b.String()
}

func writeHashKey(b *strings.Builder, v Val) {
	// lint: sort until '^\t}' where '^\tcase '
	switch v := v.(type) {
	case Con:
		b.WriteString("C")
		b.WriteString(v.Datatype)
		b.WriteString("#")
		b.WriteString(strconv.Itoa(v.Ordinal))
		if v.Arg != nil {
			b.WriteString("(")
			writeHashKey(b, v.Arg)
			b.WriteString(")")
		}
	case []Val:
		b.WriteString("[")
		for i, e := range v {
			if i > 0 {
				b.WriteString(",")
			}
			writeHashKey(b, e)
		}
		b.WriteString("]")
	case string:
		b.WriteString(strconv.Quote(v))
	case time.Time:
		// A date, by the instant it denotes, as "=" compares dates.
		b.WriteString("T")
		b.WriteString(strconv.FormatInt(v.UnixNano(), 10))
	default:
		fmt.Fprintf(b, "%T:%v", v, v)
	}
}

// Andalso returns the code of "a andalso b", which evaluates b only if
// a is true. A join's condition is compiled to it, and the plan shows
// it as "andalso(a, b)", as morel-java's does.
func Andalso(a, b Code) Code {
	return &andalsoCode{a: a, b: b}
}

type andalsoCode struct {
	a, b Code
}

func (c *andalsoCode) Eval(f *Frame) (Val, error) {
	v, err := c.a.Eval(f)
	if err != nil {
		return nil, err
	}
	if b, _ := v.(bool); !b {
		return false, nil
	}
	return c.b.Eval(f)
}

func (c *andalsoCode) Describe() string {
	return "andalso(" + c.a.Describe() + ", " + c.b.Describe() + ")"
}

// ElemSet returns the code of "x elem c" (or, if negate, "x notelem
// c") where c is a constant collection whose elements are hashable,
// which looks x up in a set of the collection's elements.
func ElemSet(code Code, elems []Val, negate bool) Code {
	set := map[string]bool{}
	var distinct []Val
	for _, e := range elems {
		k := HashKey(e)
		if !set[k] {
			set[k] = true
			distinct = append(distinct, e)
		}
	}
	return &elemSetCode{code: code, set: set, elems: distinct, negate: negate}
}

type elemSetCode struct {
	code   Code
	set    map[string]bool
	elems  []Val
	negate bool
}

func (c *elemSetCode) Eval(f *Frame) (Val, error) {
	v, err := c.code.Eval(f)
	if err != nil {
		return nil, err
	}
	return c.set[HashKey(v)] != c.negate, nil
}

func (c *elemSetCode) Describe() string {
	name := "elemSet"
	if c.negate {
		name = "notElemSet"
	}
	return name + "(" + c.code.Describe() + ", set " +
		PlanString(c.elems) + ")"
}
