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

import "github.com/hydromatic/morel-go/internal/ast"

// soleReplace returns the record's one modifier, if it is a
// plain "replace" with assignments -- the only form the compiler
// implements so far, and the one that "with" used to write.
//
// Every other form parses, so that the grammar is complete, but
// is reported here until the modifiers are typed and lowered.
func soleReplace(record *ast.Record) (*ast.AssignModifier,
	error,
) {
	var sole *ast.AssignModifier
	for _, m := range record.Modifiers {
		assign, ok := m.(*ast.AssignModifier)
		if !ok || assign.Verb != ast.ReplaceVerb ||
			assign.Lenient {
			return nil, &Error{
				Span: m.Span(),
				Msg: "record modifier '" + m.Verbs() +
					"' is not supported",
			}
		}
		if sole != nil {
			return nil, &Error{
				Span: m.Span(),
				Msg:  "a chain of record modifiers is not supported",
			}
		}
		sole = assign
	}
	return sole, nil
}
