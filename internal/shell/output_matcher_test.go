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

package shell_test

import (
	"strings"
	"testing"

	"github.com/hydromatic/morel-go/internal/shell"
)

func lines(ss ...string) string {
	return strings.Join(ss, "\n")
}

// TestTablesEquivalent checks that a bag's table rows compare as a
// multiset, and everything else about the table as text.
func TestTablesEquivalent(t *testing.T) {
	sys := shell.KernelSysForTest()
	table := lines(
		"count deptno",
		"----- ------",
		"    3     10",
		"    5     20",
		"",
		"val it : {count:int, deptno:int} bag")
	permuted := lines(
		"count deptno",
		"----- ------",
		"    5     20",
		"    3     10",
		"",
		"val it : {count:int, deptno:int} bag")
	check := func(name string, actual, expected string, want bool) {
		t.Helper()
		got := shell.EquivalentOutputForTest(sys, actual, expected)
		if got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	// row returns s with one row replaced.
	row := func(s, from, to string) string {
		return strings.Replace(s, from, to, 1)
	}
	check("same", table, table, true)
	check("permuted", permuted, table, true)
	// A list's rows are in order.
	list := strings.ReplaceAll(table, "} bag", "} list")
	check("list", strings.ReplaceAll(permuted, "} bag", "} list"), list, false)
	// A different row, a duplicated row, a different header, and a
	// different type are all differences.
	check("row", row(permuted, "    3     10", "    4     10"), table, false)
	check("dup", row(permuted, "    3     10", "    5     20"), table, false)
	check("header", row(permuted, "count", "total"), table, false)
	check("type", row(permuted, "} bag", "} list"), table, false)
	// A truncated table shows which rows it shows.
	truncated := lines(
		"count deptno",
		"----- ------",
		"    3     10",
		"...",
		"",
		"val it : {count:int, deptno:int} bag")
	check("truncated", truncated, truncated, true)
	check("truncated other",
		row(truncated, "    3     10", "    5     20"), truncated, false)
	// A row with a nested collection spans several lines, so such a
	// table is compared as text.
	nested := lines(
		"deptno emps",
		"------ -----",
		"    10 CLARK",
		"       KING",
		"    20 SMITH",
		"",
		"val it : {deptno:int, emps:string list} bag")
	nestedPermuted := lines(
		"deptno emps",
		"------ -----",
		"    20 SMITH",
		"    10 CLARK",
		"       KING",
		"",
		"val it : {deptno:int, emps:string list} bag")
	check("nested same", nested, nested, true)
	check("nested permuted", nestedPermuted, nested, false)
}
