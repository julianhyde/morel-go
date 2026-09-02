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

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/token"
)

// fieldSource says where one field of a modified record gets its
// value.
type fieldSource interface{ fieldSource() }

// keptField keeps the value of a field of the record the modifier
// was applied to. The name may differ, if the modifier is a
// "rename".
type keptField struct{ field string }

// assignedField is assigned the value of an expression.
//
// sameType says whether the field keeps the type it had. It is
// false if the field is being added, because then there is no type
// to keep, and if the modifier is "lenient", which is what
// "lenient" means.
type assignedField struct {
	exp      ast.Expr
	sameType bool
}

// takenField is assigned a field of the argument of an "all"
// modifier. sameType means what it does in assignedField.
type takenField struct {
	field    string
	sameType bool
}

func (keptField) fieldSource()     {}
func (assignedField) fieldSource() {}
func (takenField) fieldSource()    {}

// modField is one field of the record a modifier produces: its
// label, and where its value comes from.
type modField struct {
	label string
	src   fieldSource
}

// applyModifier applies m to a record whose fields are fields,
// returning where each field of the result gets its value.
//
// It also checks the labels the modifier mentions against the
// fields it is applied to, and reports one that the verb says is
// an error present, or an error absent.
//
// allFields gives the field names of the modifier's argument, if
// it is an "all" modifier, and is nil otherwise.
//
// The type resolver reads the result to deduce the type of the
// modified record, and the core resolver reads it again to build
// the record. Deriving it twice from the same rules is what keeps
// the type and the value in step.
func applyModifier(m ast.Modifier, fields, allFields []string) (
	[]modField, error,
) {
	// lint: sort until '^	}' where '^	case '
	switch m := m.(type) {
	case *ast.AllModifier:
		return assignAllFields(m, fields, allFields)
	case *ast.AssignModifier:
		return assignFields(m, fields)
	case *ast.RemoveModifier:
		return removeFields(m, fields)
	case *ast.RenameModifier:
		return renameFields(m, fields)
	default:
		return nil, &Error{
			Span: m.Span(), Msg: "unknown record modifier",
		}
	}
}

// assignFields applies an "extend" or "replace" modifier, in
// either case taking each label to whichever of the verb's two
// cases it falls in: the record has the label already, or it does
// not.
func assignFields(m *ast.AssignModifier, fields []string) (
	[]modField, error,
) {
	labelled, err := labelFields(m.Fields)
	if err != nil {
		return nil, err
	}
	assigned := map[string]ast.Expr{}
	for _, f := range labelled {
		err = checkAssign(m.Verb, f.Label,
			slices.Contains(fields, f.Label), f.LabelSpan)
		if err != nil {
			return nil, err
		}
		assigned[f.Label] = f.Exp
	}

	// Fields the record has: assigned, or kept as they were.
	var args []modField
	for _, field := range fields {
		exp, isAssigned := assigned[field]
		if !isAssigned || m.Verb.Exists() == ast.ExistsSkip {
			args = append(args, modField{field, keptField{field}})
			continue
		}
		args = append(args, modField{
			field, assignedField{exp, !m.Lenient},
		})
	}

	// Labels the record does not have: added, or ignored. An added
	// field has no type to keep, so "lenient" does not arise.
	if m.Verb.Absent() == ast.AbsentAdd {
		for _, f := range labelled {
			if !slices.Contains(fields, f.Label) {
				args = append(args, modField{
					f.Label, assignedField{f.Exp, false},
				})
			}
		}
	}
	return args, nil
}

// labelFields makes a modifier's implicit labels explicit, so
// that "{r replace a}" assigns the value of "a" to field "a", and
// gives every label a source range to blame: its own if it was
// written, else its expression's.
func labelFields(fields []ast.Field) ([]ast.Field, error) {
	labelled := make([]ast.Field, len(fields))
	for i, f := range fields {
		if f.Label == "" {
			f.Label = implicitLabel(f.Exp)
			if f.Label == "" {
				return nil, &Error{
					Span: f.Exp.Span(),
					Msg: cannotDeriveLabel + " " +
						ast.UnparseExpr(f.Exp),
				}
			}
		}
		if f.LabelSpan == (token.Span{}) {
			f.LabelSpan = f.Exp.Span()
		}
		if slices.ContainsFunc(labelled[:i], func(g ast.Field) bool {
			return g.Label == f.Label
		}) {
			return nil, duplicateField(f.Label, f.LabelSpan)
		}
		labelled[i] = f
	}
	return labelled, nil
}

// assignAllFields applies an "extend all" or "replace all"
// modifier: the same rules as assignFields, for every field of
// the modifier's record-valued argument.
func assignAllFields(m *ast.AllModifier, fields, allFields []string) (
	[]modField, error,
) {
	for _, field := range allFields {
		err := checkAssign(m.Verb, field,
			slices.Contains(fields, field), m.Exp.Span())
		if err != nil {
			return nil, err
		}
	}

	var args []modField
	for _, field := range fields {
		if !slices.Contains(allFields, field) ||
			m.Verb.Exists() == ast.ExistsSkip {
			args = append(args, modField{field, keptField{field}})
			continue
		}
		args = append(args, modField{
			field, takenField{field, !m.Lenient},
		})
	}

	if m.Verb.Absent() == ast.AbsentAdd {
		for _, field := range allFields {
			if !slices.Contains(fields, field) {
				args = append(args, modField{
					field, takenField{field, false},
				})
			}
		}
	}
	return args, nil
}

// checkAssign reports a label that falls in the case its verb
// makes an error: one that exists where the verb adds, or one
// that is absent where the verb assigns.
func checkAssign(verb ast.ModifierVerb, label string, exists bool,
	span token.Span,
) error {
	switch {
	case exists && verb.Exists() == ast.ExistsError:
		return fieldExists(label, span)
	case !exists && verb.Absent() == ast.AbsentError:
		return fieldNotFound(label, span)
	default:
		return nil
	}
}

// renameFields applies a "rename" modifier. It takes the value of
// each label on the right, which must exist, and gives it to the
// label on the left, which must not survive the renaming.
func renameFields(m *ast.RenameModifier, fields []string) (
	[]modField, error,
) {
	var sources []string
	for _, pair := range m.Pairs {
		if !slices.Contains(fields, pair.From.Name) {
			return nil, fieldNotFound(pair.From.Name, pair.From.Span)
		}
		if slices.Contains(sources, pair.From.Name) {
			return nil, duplicateField(pair.From.Name, pair.From.Span)
		}
		sources = append(sources, pair.From.Name)
	}
	var args []modField
	for _, field := range fields {
		if !slices.Contains(sources, field) {
			args = append(args, modField{field, keptField{field}})
		}
	}
	for _, pair := range m.Pairs {
		if slices.ContainsFunc(args, func(a modField) bool {
			return a.label == pair.To.Name
		}) {
			return nil, fieldExists(pair.To.Name, pair.To.Span)
		}
		args = append(args, modField{
			pair.To.Name, keptField{pair.From.Name},
		})
	}
	return args, nil
}

// removeFields applies a "remove" modifier.
func removeFields(m *ast.RemoveModifier, fields []string) (
	[]modField, error,
) {
	var removed []string
	for _, label := range m.Labels {
		if !slices.Contains(fields, label.Name) &&
			m.Verb.Absent() == ast.AbsentError {
			return nil, fieldNotFound(label.Name, label.Span)
		}
		if slices.Contains(removed, label.Name) {
			return nil, duplicateField(label.Name, label.Span)
		}
		removed = append(removed, label.Name)
	}
	var args []modField
	for _, field := range fields {
		if !slices.Contains(removed, field) {
			args = append(args, modField{field, keptField{field}})
		}
	}
	return args, nil
}

// modifierNeedsBase is the error when modifiers follow anything
// but a single unlabeled field.
const modifierNeedsBase = "a record modifier applies to a base " +
	"expression; enclose the expression and its modifiers in braces"

func fieldNotFound(field string, span token.Span) error {
	return &Error{
		Span: span, Msg: "field '" + field + "' does not exist",
	}
}

func fieldExists(field string, span token.Span) error {
	return &Error{
		Span: span, Msg: "field '" + field + "' already exists",
	}
}

func duplicateField(field string, span token.Span) error {
	return &Error{
		Span: span,
		Msg:  "duplicate field '" + field + "' in record",
	}
}

// yieldRecord returns the record whose fields a "yield" step
// binds: the expression itself, or the body of a chain of "let"s
// the user wrote; nil if the step yields anything else. A record
// with modifiers is a record, and the fields it binds are the ones
// the last modifier produced.
//
// The type resolver and the core resolver apply the same test to
// the same tree, so that the bindings the one deduces are the ones
// the other creates.
func yieldRecord(exp ast.Expr) *ast.Record {
	for {
		switch e := exp.(type) {
		case *ast.Let:
			exp = e.Exp
		case *ast.Record:
			return e
		default:
			return nil
		}
	}
}
