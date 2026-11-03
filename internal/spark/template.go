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

package spark

import (
	"errors"
	"strconv"
	"strings"
)

// Template is a string in which a number in braces stands for an
// argument, and a doubled opening brace for a literal one. The
// function table's templates are SQL, but nothing here knows that.
//
// Expand asks for each argument as it meets it, rather than being
// handed them all, so an argument that the template does not use is
// never rendered -- and an argument that cannot be rendered only
// matters if the template wants it.
//
// A template is checked when it is made, so that a malformed one is
// refused where it is written rather than where it is used.
type Template struct {
	pattern  string
	argCount int
}

// NewTemplate makes a template, checking that it is well formed.
func NewTemplate(pattern string) (*Template, error) {
	t := &Template{pattern: pattern}
	maxArg := -1
	err := t.scan(func(byte) {}, func(i int) error {
		maxArg = max(maxArg, i)
		return nil
	})
	if err != nil {
		return nil, err
	}
	t.argCount = maxArg + 1
	return t, nil
}

// ArgCount is how many arguments the template uses: one more than
// the greatest placeholder in it.
func (t *Template) ArgCount() int { return t.argCount }

// String returns the pattern.
func (t *Template) String() string { return t.pattern }

// Expand expands the template, calling arg for the text of each
// argument it uses, in the order it meets them.
func (t *Template) Expand(arg func(i int) (string, error)) (string,
	error,
) {
	var b strings.Builder
	err := t.scan(func(c byte) { b.WriteByte(c) },
		func(i int) error {
			s, err := arg(i)
			if err != nil {
				return err
			}
			b.WriteString(s)
			return nil
		})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

// scan walks the pattern, reporting each literal character and each
// argument.
func (t *Template) scan(literal func(c byte),
	argument func(i int) error,
) error {
	p := t.pattern
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c != '{' {
			literal(c)
			continue
		}
		if i+1 < len(p) && p[i+1] == '{' {
			literal('{')
			i++
			continue
		}
		end := strings.IndexByte(p[i:], '}')
		if end < 0 {
			return errors.New("unclosed placeholder in template [" +
				p + "]")
		}
		s := p[i+1 : i+end]
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return errors.New("placeholder [" + s + "] in template [" +
				p + "] is not an argument number")
		}
		err = argument(n)
		if err != nil {
			return err
		}
		i += end
	}
	return nil
}

// End template.go
