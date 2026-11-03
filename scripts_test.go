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

package morel_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hydromatic/morel-go/internal/shell"
)

// TestScripts runs every script under testdata/script and checks it
// against its expected output.
//
// Which file holds that output, and what form it takes, follows from
// the extension, as it does in morel-java: a ".smli" script carries
// its own on "> "-prefixed lines and must reproduce itself, while a
// ".sml" script's is the separate transcript in "<name>.sml.out".
// One walker covers both.
func TestScripts(t *testing.T) {
	root := "testdata/script"
	var files []string
	err := filepath.WalkDir(root,
		func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".smli") ||
				strings.HasSuffix(path, ".sml") {
				files = append(files, path)
			}
			return nil
		})
	if err != nil || len(files) == 0 {
		t.Skipf("no scripts in %s", root)
	}
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		t.Run(rel, func(t *testing.T) { checkScript(t, f, rel) })
	}
}

// checkScript runs the script at path `f` and compares what it
// produced with what is expected of it, in whichever form its
// extension calls for.
func checkScript(t *testing.T, f, rel string) {
	t.Helper()
	src, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	conditions := checkRequires(t, rel, string(src))
	kernel := shell.NewKernel(rel)
	if slices.Contains(conditions, "spark") {
		// The script assumes that "spark" is bound to a live
		// connection, as Spark.connect would return it; bind it, and
		// close it afterwards.
		closeSpark := bindSpark(t, kernel)
		defer closeSpark()
	}
	// Scripts resolve data files (Datalog .input) against the
	// "directory" property; testdata is the analog of java's
	// src/test/resources. "use" resolves against the script's own
	// directory, as java's harness sets scriptDirectory.
	kernel.Config().Directory = "testdata"
	if rel == "file.smli" {
		// "file.smli" browses the file system, so it starts in the
		// data directory, where what it finds is predictable;
		// java's harness does the same.
		kernel.Config().Directory = "testdata/data"
	}
	kernel.Config().ScriptDirectory = filepath.Dir(f)
	if strings.HasSuffix(f, ".smli") {
		checkIdempotent(t, kernel, rel, string(src))
	} else {
		checkTranscript(t, kernel, f, rel, string(src))
	}
}

// requiresPattern matches a script's "(*) requires: <conditions>"
// directive.
var requiresPattern = regexp.MustCompile(`^\(\*\) requires: (.*)`)

// requiresLines is how far into a script the directive is looked
// for: it belongs in the header.
const requiresLines = 40

// checkRequires skips the test if the script's header has a
// directive "(*) requires: <conditions>" with a condition that does
// not hold. Several conditions, separated by spaces, must all hold.
// It returns the conditions.
func checkRequires(t *testing.T, rel, src string) []string {
	t.Helper()
	lines := strings.Split(src, "\n")
	var conditions []string
	for _, line := range lines[:min(len(lines), requiresLines)] {
		m := requiresPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		for condition := range strings.FieldsSeq(m[1]) {
			if !holds(t, condition) {
				t.Skipf("%s requires %s", rel, condition)
			}
			conditions = append(conditions, condition)
		}
	}
	return conditions
}

// sparkHolds reports whether a live Spark Connect server is
// available: the environment variable MOREL_SPARK is set, the analog
// of morel-java's system property "morel.spark", and SPARK_REMOTE
// names the server.
func sparkHolds() bool {
	return os.Getenv("MOREL_SPARK") != "" && os.Getenv("SPARK_REMOTE") != ""
}

// bindSpark binds "spark" to a connection to the server that
// SPARK_REMOTE names, and returns the function that closes it.
//
// Nothing is added to the URI: each table of the test container
// says which of its columns hold nulls, in its own "morel.nullable"
// property, because Spark reports every column of a stored table as
// nullable whatever the schema said.
func bindSpark(t *testing.T, kernel *shell.Kernel) func() {
	t.Helper()
	closeSpark, err := kernel.BindSpark("spark", os.Getenv("SPARK_REMOTE"))
	if err != nil {
		t.Fatal(err)
	}
	return closeSpark
}

// TestScriptTripleSpark runs triple.smli a third time, with every
// query pushed down to Spark: the same queries, the same expected
// answers, and a third way of computing them. A query that Spark
// cannot do, or answers differently, fails here. A query that stops
// being pushed down is a regression, so it fails rather than
// answering locally with a warning, which is what a session would
// rather have.
func TestScriptTripleSpark(t *testing.T) {
	if !sparkHolds() {
		t.Skip("requires spark")
	}
	const f = "testdata/script/triple.smli"
	src, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	kernel := shell.NewKernel("triple.smli")
	kernel.Config().Directory = "testdata"
	kernel.Config().ScriptDirectory = filepath.Dir(f)
	closeSpark := bindSpark(t, kernel)
	defer closeSpark()
	kernel.Config().SetProp("sparkPushDown", "true")
	kernel.Config().SetProp("sparkFallback", "false")
	checkIdempotent(t, kernel, "triple.smli", string(src))
}

// holds reports whether a "requires:" condition holds. This port
// has the Spark adapter, so "spark-adapter" holds, and
// "spark-prepare", which says that the "Spark" structure has
// "prepare"; "no-spark-adapter" does not. "spark" holds if a server
// is available; see sparkHolds.
func holds(t *testing.T, condition string) bool {
	t.Helper()
	// lint: sort until '^\t}' where '^\tcase '
	switch condition {
	case "no-spark-adapter":
		return false
	case "spark":
		return sparkHolds()
	case "spark-adapter":
		return true
	case "spark-prepare":
		return true
	}
	t.Fatalf("unknown condition '%s' in requires directive", condition)
	return false
}

// checkIdempotent checks that a ".smli" script reproduces itself:
// its expected output is the file it is written in.
func checkIdempotent(t *testing.T, kernel *shell.Kernel,
	rel, src string,
) {
	t.Helper()
	got, err := shell.RunScript(kernel, rel, src)
	if err != nil {
		t.Fatal(err)
	}
	if got != src {
		t.Errorf("not idempotent:%s", firstDiff(src, got))
	}
}

// checkTranscript checks that a ".sml" script reproduces the
// transcript in its companion ".sml.out" file.
func checkTranscript(t *testing.T, kernel *shell.Kernel,
	f, rel, src string,
) {
	t.Helper()
	want, err := os.ReadFile(f + ".out")
	if err != nil {
		t.Fatalf("no transcript: %v", err)
	}
	got, err := shell.RunSmlScript(kernel, rel, src)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("transcript differs:%s",
			firstDiff(string(want), got))
	}
}

// firstDiff renders the first line where want and got differ.
func firstDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	for i := range max(len(wl), len(gl)) {
		w, g := "<eof>", "<eof>"
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return "\n line " + strconv.Itoa(i+1) +
				":\n want " + w + "\n  got " + g
		}
	}
	return " (lengths differ)"
}
