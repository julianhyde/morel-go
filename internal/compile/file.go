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
	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/types"
	"github.com/hydromatic/morel-go/internal/unify"
)

// FileName is the name that denotes the root of the file system a
// session browses.
const FileName = "file"

// Files is a session's view of the file system: the directory that
// "file" denotes, and the browsable values -- files, and the
// records and catalogs of Spark connections -- that names bound by
// earlier statements hold, as in
//
//	val s = file.scott;
//	from d in s.depts;
//
// where "s" must be known to be a file for "depts" to be
// discovered on it.
type Files struct {
	// Root is the file that the name "file" denotes.
	Root *eval.File
	// Bound gives the browsable value that a name from an earlier
	// statement is bound to. It is empty in a session that has
	// bound none.
	Bound map[string]eval.Browsable
}

// file returns the browsable value a name denotes, or nil if it
// denotes none.
//
// A name bound by an earlier statement is consulted first, and may
// be bound to nothing (a nil entry), which is how a user's own
// "val file = ..." shadows the file system.
func (f *Files) file(name string) eval.Browsable {
	if f == nil {
		return nil
	}
	if bound, isBound := f.Bound[name]; isBound {
		return bound
	}
	if name == FileName && f.Root != nil {
		return f.Root
	}
	return nil
}

// fileTypeEnv gives the names that denote files their type as it
// currently stands. The type is read afresh at every lookup, not
// held in a binding, because browsing widens it: a statement must
// see whatever has been discovered by the time it is typed.
type fileTypeEnv struct {
	parent typeEnv
	files  *Files
	sys    *types.System
}

func (e *fileTypeEnv) get(r *typeResolver, name string) (
	unify.Term, bool,
) {
	f := e.files.file(name)
	if f == nil {
		return e.parent.get(r, name)
	}
	// Expand the root, so that its type lists the entries a field
	// selection can name. Entries stay unexpanded until named. A
	// value that cannot be browsed -- the catalog of a connection
	// that has been closed -- keeps the type it has; the failure is
	// reported where a field of it is discovered.
	_ = f.Browse()
	return r.typeTerm(f.Type(e.sys), map[int]*unify.Var{}), true
}

// peek reports false for a name that denotes a file: its term is
// built afresh at each lookup, and expanding the file to build one
// is a side effect.
func (e *fileTypeEnv) peek(name string) (unify.Term, bool) {
	if e.files.file(name) != nil {
		return nil, false
	}
	return e.parent.peek(name)
}

func (e *fileTypeEnv) overloads(name string) []*unify.Var {
	return e.parent.overloads(name)
}

// discoverFileFields browses the file system for the fields that
// this declaration selects from it, and reports whether any of
// them widened a file's type. Browsing a Spark catalog can fail, as
// when its connection is closed, and the failure is the exception
// "Spark".
//
// It works from the syntax, not from deduced types: a selector's
// receiver is followed back to the file it denotes, if any. That
// reaches the paths a type cannot — a name bound by an earlier
// statement, a field of a field — with one walk.
//
// Selectors are visited in the order deduction met them, which is
// innermost first, so that "file.scott.depts" discovers "scott"
// before it looks for "depts" inside it.
func (r *typeResolver) discoverFileFields() (bool, error) {
	if r.files == nil {
		return false, nil
	}
	widened := false
	for _, call := range r.selectorCalls {
		f := r.fileOf(call.apply.Arg)
		if f == nil {
			continue
		}
		w, err := f.Discover(call.sel.Name)
		if err != nil {
			return false, err
		}
		if w {
			widened = true
		}
	}
	return widened, nil
}

// fileSelectorParam types a selector whose receiver is a
// directory. Such a selector selects one of the directory's
// entries, so it is the directory that says what the entry's type
// is, and which slot it is in — the receiver's own type may say
// neither, as "Sys.file" is declared as "{...}" however much has
// been browsed.
//
// It equates the selector's result with the entry's type, and
// returns the parameter type the selector should have: the
// directory's. Where the receiver's type does say, it says the
// same, so nothing is lost by preferring the directory. An
// expression that denotes no directory, or an entry that does not
// exist, leaves the selector as it was.
func (r *typeResolver) fileSelectorParam(apply *ast.Apply,
	sel *ast.RecordSelector, v *unify.Var, param unify.Term,
) unify.Term {
	parent := r.fileOf(apply.Arg)
	if parent == nil {
		return param
	}
	child := parent.Entry(sel.Name)
	if child == nil {
		return param
	}
	r.equiv(v, r.typeTerm(child.Type(r.sys), map[int]*unify.Var{}))
	return r.typeTerm(parent.Type(r.sys), map[int]*unify.Var{})
}

// fileOf returns the browsable value an expression denotes, or nil
// if it denotes something else. It follows a name, and a chain of
// field selections from one: "file", "file.scott", and "s.depts"
// where "s" is bound to a file.
func (r *typeResolver) fileOf(e ast.Expr) eval.Browsable {
	switch e := e.(type) {
	case *ast.ID:
		return r.files.file(e.Name)
	case *ast.Apply:
		sel, isSel := e.Fn.(*ast.RecordSelector)
		if !isSel || sel.Safe {
			return nil
		}
		parent := r.fileOf(e.Arg)
		if parent == nil {
			// A member of a structure, such as "Sys.file", which
			// is the same file system as the bare "file".
			if id, isID := e.Arg.(*ast.ID); isID {
				return r.files.file(id.Name + "." + sel.Name)
			}
			return nil
		}
		return parent.Entry(sel.Name)
	}
	return nil
}

// End file.go
