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

package ast

import (
	"strings"

	"github.com/hydromatic/morel-go/internal/token"
)

// AttributeKind is an attribute's scope, which its "@"-count
// denotes, following OCaml.
type AttributeKind int

// The attribute kinds.
const (
	// AttrExp, "[@a]", attaches to the expression or type atom
	// before it.
	AttrExp AttributeKind = iota
	// AttrDecl, "[@@a]", attaches to a declaration, and may stand
	// before or after it.
	AttrDecl
	// AttrFloating, "[@@@a]", attaches to nothing; it stands alone
	// as an item of its own.
	AttrFloating
)

// Marker is the "@"s that introduce an attribute of this kind.
func (k AttributeKind) Marker() string {
	return strings.Repeat("@", int(k)+1)
}

// Attribute is metadata attached to an expression, a declaration
// or a type: a name, which may be dotted, and an optional
// payload, which is either an expression or -- after ":" -- a
// type.
//
// Attributes are inert. They are recorded here and shown by
// Sys.parseTree; nothing else reads them.
type Attribute struct {
	base

	Name string
	// Payload is the expression after the name, or nil.
	Payload Expr
	// TypePayload is the type after ":", or nil. At most one of
	// Payload and TypePayload is set.
	TypePayload Type
	Kind        AttributeKind
}

// NewAttribute returns an attribute with an optional expression
// payload.
func NewAttribute(span token.Span, kind AttributeKind, name string,
	payload Expr,
) *Attribute {
	return &Attribute{
		base: base{span}, Name: name, Payload: payload, Kind: kind,
	}
}

// NewAttributeWithType returns an attribute whose payload is a
// type.
func NewAttributeWithType(span token.Span, kind AttributeKind,
	name string, payload Type,
) *Attribute {
	return &Attribute{
		base: base{span}, Name: name, TypePayload: payload,
		Kind: kind,
	}
}

// Op implements Node.
func (*Attribute) Op() Op { return AttributeOp }

// AttributedExp is an expression with attributes, "e [@a] [@b]".
type AttributedExp struct {
	exprBase

	Exp   Expr
	Attrs []*Attribute
}

// NewAttributedExp returns an attributed expression.
func NewAttributedExp(span token.Span, exp Expr,
	attrs []*Attribute,
) *AttributedExp {
	return &AttributedExp{
		exprBase: exprBase{base{span}}, Exp: exp, Attrs: attrs,
	}
}

// Op implements Node.
func (*AttributedExp) Op() Op { return AttributedExpOp }
