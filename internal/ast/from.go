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

import "github.com/hydromatic/morel-go/internal/token"

// FromStep is one step of a from expression's pipeline.
type FromStep interface {
	Node
	fromStep()
}

type stepBase struct{ base }

func (stepBase) fromStep() {}

// ScanKind distinguishes the forms of a scan.
type ScanKind int

// The scan forms: "pat in exp", "pat = exp", and an unbounded
// "pat".
const (
	ScanIn ScanKind = iota
	ScanEq
	ScanUnbounded
)

// Scan is one source of a from expression: "pat in exp",
// "pat = exp", or an unbounded "pat"; On is the join condition,
// or nil. A scan introduced by "join" is the same node; joins
// unparse as comma scans.
type Scan struct {
	stepBase

	Pat  Pat
	Exp  Expr
	On   Expr
	Kind ScanKind
}

// NewScan returns a scan step.
func NewScan(span token.Span, kind ScanKind, pat Pat, exp,
	on Expr,
) *Scan {
	return &Scan{
		stepBase: stepBase{base{span}},
		Pat:      pat,
		Exp:      exp,
		On:       on,
		Kind:     kind,
	}
}

// Op implements Node.
func (*Scan) Op() Op { return ScanOp }

// WhereStep is a "where exp" step.
type WhereStep struct {
	stepBase

	Exp Expr
}

// NewWhereStep returns a where step.
func NewWhereStep(span token.Span, exp Expr) *WhereStep {
	return &WhereStep{stepBase: stepBase{base{span}}, Exp: exp}
}

// Op implements Node.
func (*WhereStep) Op() Op { return WhereOp }

// YieldStep is a "yield exp" step.
type YieldStep struct {
	stepBase

	Exp Expr
}

// NewYieldStep returns a yield step.
func NewYieldStep(span token.Span, exp Expr) *YieldStep {
	return &YieldStep{stepBase: stepBase{base{span}}, Exp: exp}
}

// Op implements Node.
func (*YieldStep) Op() Op { return YieldOp }

// From is a query expression: "from scan, ... step ...".
type From struct {
	exprBase

	Steps []FromStep
}

// NewFrom returns a from expression.
func NewFrom(span token.Span, steps []FromStep) *From {
	return &From{exprBase: exprBase{base{span}}, Steps: steps}
}

// Op implements Node.
func (*From) Op() Op { return FromOp }
