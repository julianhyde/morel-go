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

import "github.com/hydromatic/morel-go/internal/core"

// stackLayout is where a variable would be on morel-java's stack,
// which is what the plan's "stack(offset N)" counts: N is the
// number of values live above the variable's slot, plus one.
//
// The frames that execute are laid out differently -- a slot per
// variable, for the whole function -- so the layout is kept
// alongside them, only for the plan. Its rules are morel-java's.
// A function, and each arm of a case, starts an empty stack: the
// variables it reads from outside, in the order it first reads
// them; then the peers of a recursive declaration it belongs to;
// then the variables of its pattern. A let pushes what it binds
// until its body ends, and a query pushes what each step binds,
// a yield or group replacing the row above the query's base.
//
// A layout is immutable; pushing makes a new one, and a scope
// restores the one it began with.
type stackLayout struct {
	pat   *core.IDPat
	index int
	next  *stackLayout
	// depth is the number of values live on the stack.
	depth int
}

// get returns the slot a variable has in the layout.
func (l *stackLayout) get(pat *core.IDPat) (int, bool) {
	for ; l != nil; l = l.next {
		if l.pat == pat {
			return l.index, true
		}
	}
	return 0, false
}

// push returns a layout with each variable in a new slot.
func (l *stackLayout) push(pats []*core.IDPat) *stackLayout {
	depth := l.size()
	for _, pat := range pats {
		l = &stackLayout{pat: pat, index: depth, next: l, depth: depth + 1}
		depth++
	}
	return l
}

// size is the number of values live on the stack.
func (l *stackLayout) size() int {
	if l == nil {
		return 0
	}
	return l.depth
}

// offset is how the plan reads a variable: its distance from the
// top of the stack, counting from one, or 0 where it is not on
// the stack.
func (l *stackLayout) offset(pat *core.IDPat) int {
	if index, ok := l.get(pat); ok {
		return l.size() - index
	}
	return 0
}

// frame is the layout at the start of a function or case arm
// whose bodies are exps, each binding its own pats: what the
// bodies read from l, then peers, then the arm's own variables.
func (l *stackLayout) frame(r *rewriter, exps []core.Exp,
	patsOf [][]*core.IDPat, peers []*core.IDPat,
) []*stackLayout {
	var captures []*core.IDPat
	seen := map[*core.IDPat]bool{}
	for i, exp := range exps {
		own := map[*core.IDPat]bool{}
		for _, p := range patsOf[i] {
			own[p] = true
		}
		r.exp = func(e core.Exp) (core.Exp, bool) {
			if id, ok := e.(*core.ID); ok && !own[id.Pat] &&
				!seen[id.Pat] {
				if _, live := l.get(id.Pat); live {
					seen[id.Pat] = true
					captures = append(captures, id.Pat)
				}
			}
			return nil, false
		}
		r.rewriteExp(exp)
	}
	frames := make([]*stackLayout, len(exps))
	for i := range exps {
		var f *stackLayout
		f = f.push(captures)
		f = f.push(peers)
		frames[i] = f.push(patsOf[i])
	}
	return frames
}
