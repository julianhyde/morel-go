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
	"fmt"
	"strings"

	"github.com/hydromatic/morel-go/internal/core"
)

// DebugUnbound reports, for a declaration, every generated binder
// ("v$", "w$") that is read but declared nowhere in it, and
// whether a binder of the same name is declared elsewhere -- a
// reference to a copy of a pattern rather than the pattern.
func DebugUnbound(decl core.Decl) string {
	declared := map[*core.IDPat]bool{}
	byName := map[string][]*core.IDPat{}
	var reads []*core.IDPat
	declare := func(p core.Pat) {
		for _, id := range core.PatIDs(p) {
			declared[id] = true
			byName[id.Name] = append(byName[id.Name], id)
		}
	}
	r := &rewriter{}
	r.exp = func(e core.Exp) (core.Exp, bool) {
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch e := e.(type) {
		case *core.Case:
			for _, m := range e.Matches {
				declare(m.Pat)
			}
		case *core.Fn:
			declare(e.IDPat)
		case *core.From:
			for _, s := range e.Steps {
				for _, p := range stepPats(s) {
					declare(p)
				}
			}
		case *core.ID:
			reads = append(reads, e.Pat)
		case *core.Join:
			if e.Binder != nil {
				declare(e.Binder)
			}
		case *core.Let:
			if d, ok := e.Decl.(*core.NonRecValDecl); ok {
				declare(d.Pat)
			}
		}
		return nil, false
	}
	r.rewriteDecl(decl)
	var b strings.Builder
	for _, p := range reads {
		if declared[p] || !strings.Contains(p.Name, "$") {
			continue
		}
		fmt.Fprintf(&b, "unbound %s (%p); same name declared: %d\n",
			p.Name, p, len(byName[p.Name]))
	}
	return b.String()
}

// stepPats is the patterns a step declares.
func stepPats(s core.FromStep) []core.Pat {
	var pats []core.Pat
	// lint: sort until '^\t}' where '^\tcase '
	switch s := s.(type) {
	case *core.GroupStep:
		for _, k := range s.Keys {
			pats = append(pats, k.Pat)
		}
		for _, a := range s.Aggs {
			pats = append(pats, a.Pat)
		}
	case *core.Scan:
		pats = append(pats, s.Pat)
	case *core.Through:
		pats = append(pats, s.Pat)
	case *core.Yield:
		for _, f := range s.Fields {
			pats = append(pats, f.Pat)
		}
	}
	return pats
}
