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

package spark_test

import (
	"reflect"
	"testing"

	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/spark"
	"github.com/hydromatic/morel-go/internal/types"
)

func TestJavaReal(t *testing.T) {
	for _, c := range []struct {
		f    float64
		want string
	}{
		{0, "0.0"},
		{1, "1.0"},
		{2.5, "2.5"},
		{0.001, "0.001"},
		{0.0001, "1.0E-4"},
		{1234567, "1234567.0"},
		{1e7, "1.0E7"},
		{-12.25, "-12.25"},
		{123456789012, "1.23456789012E11"},
	} {
		if got := spark.JavaDouble(c.f); got != c.want {
			t.Errorf("javaReal(%v) = %q, want %q", c.f, got, c.want)
		}
	}
}

func TestEscapeBytes(t *testing.T) {
	got := spark.EscapeBytes([]byte("a'b\"c\\\n\x01é"))
	want := `a\'b\"c\\\n\001\303\251`
	if got != want {
		t.Errorf("escapeBytes = %q, want %q", got, want)
	}
}

func TestTemplate(t *testing.T) {
	tmpl, err := spark.NewTemplate("f({1}, {{x}, {0})")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.ArgCount() != 2 {
		t.Errorf("argCount = %d", tmpl.ArgCount())
	}
	s, err := tmpl.Expand(func(i int) (string, error) {
		return []string{"a", "b"}[i], nil
	})
	if err != nil || s != "f(b, {x}, a)" {
		t.Errorf("expand = %q, %v", s, err)
	}
	_, err = spark.NewTemplate("f({x})")
	if err == nil {
		t.Error("expected an error for a bad placeholder")
	}
}

func TestFunctions(t *testing.T) {
	fns, err := spark.Functions()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"op +": "op +", "op /": "Real./", "size": "String.size",
		"Int.+": "op +", "not": "Bool.not", "count": "Relational.count",
	} {
		e := fns.Lookup(name)
		if e == nil || e.Morel != want {
			t.Errorf("Lookup(%q) = %v, want %s", name, e, want)
		}
	}
}

func TestArrowRoundTrip(t *testing.T) {
	sys := types.NewSystem()
	dec := sys.Named("decimal")
	rowType := sys.Record([]types.Field{
		{Label: "a", Type: sys.Int},
		{Label: "b", Type: sys.Named("option", sys.String)},
		{Label: "c", Type: dec},
		{Label: "d", Type: sys.Real},
		{Label: "e", Type: sys.Bool},
	})
	d1, _ := eval.DecParseExact("12.5")
	d2, _ := eval.DecParseExact("~3")
	rows := []eval.Val{
		[]eval.Val{int32(1), eval.SomeVal("x"), d1, float32(1.5), true},
		[]eval.Val{int32(-7), eval.NoneVal, d2, float32(-2), false},
	}
	data, err := spark.EncodeArrow(rows, rowType)
	if err != nil {
		t.Fatal(err)
	}
	got, err := spark.DecodeArrow([][]byte{data}, rowType)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rows) {
		t.Errorf("round trip: got %v, want %v", got, rows)
	}
}

// End spark_test.go
