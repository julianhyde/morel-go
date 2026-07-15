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

package shell

import (
	"math"
	"strconv"

	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/pp"
	"github.com/hydromatic/morel-go/internal/types"
)

// prettyBinding renders "val name = value : type", choosing line
// breaks with the layout engine. The value stays on the "val
// ... =" line only if it fits there entirely flat; otherwise the
// whole value moves to its own line, indented by 2, where it is
// free to wrap. Likewise the type stays on the value's last line
// if it fits flat there. This matches how SML/NJ lays out a
// binding.
func (c *Config) prettyBinding(name string, v eval.Val,
	t types.Type,
) string {
	valueDoc := c.valueDoc(t, v, 1)
	typeDoc := pp.Text(t.String())
	const indent = 2
	valuePart := pp.Union(
		pp.Beside(pp.Text(" "), pp.Flatten(valueDoc)),
		pp.Nest(indent, pp.Beside(pp.HardLine(), valueDoc)))
	typePart := pp.Union(
		pp.Beside(pp.Text(" : "), typeDoc),
		pp.Nest(indent, pp.Concat(pp.HardLine(), pp.Text(": "),
			typeDoc)))
	doc := pp.Concat(pp.Text("val "+name+" ="), valuePart,
		typePart)
	return pp.Render(c.width(), doc)
}

// width is the page width passed to the renderer. Rendering at
// lineWidth - 1 matches SML/NJ's right margin: its printer
// allows one more column than ours before breaking.
func (c *Config) width() int {
	if c.LineWidth < 0 {
		return math.MaxInt32
	}
	return c.LineWidth - 1
}

// valueDoc builds the layout of a value, directed by the value's
// static type. Beyond printDepth, a nested value prints as "#".
func (c *Config) valueDoc(t types.Type, v eval.Val,
	depth int,
) pp.Doc {
	if c.PrintDepth >= 0 && depth > c.PrintDepth {
		return pp.Text("#")
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch t := t.(type) {
	case *types.Fn:
		return pp.Text("fn")
	case *types.List:
		return c.seqDoc("[", "]", c.elementDocs(t.Elem, v, depth))
	case *types.Named:
		return c.conDoc(t, v, depth)
	case *types.Primitive:
		return pp.Text(c.primitiveString(t, v))
	case *types.Record:
		vals := asVals(v)
		docs := make([]pp.Doc, len(t.Fields))
		for i, field := range t.Fields {
			docs[i] = pp.Beside(pp.Text(field.Label+"="),
				c.valueDoc(field.Type, vals[i], depth+1))
		}
		return c.seqDoc("{", "}", docs)
	case *types.Tuple:
		vals := asVals(v)
		docs := make([]pp.Doc, len(t.Args))
		for i, arg := range t.Args {
			docs[i] = c.valueDoc(arg, vals[i], depth+1)
		}
		return c.seqDoc("(", ")", docs)
	case *types.Var:
		return pp.Text("fn")
	default:
		return pp.Text("fn")
	}
}

// elementDocs builds the layouts of a list's elements; beyond
// printLength, the remaining elements print as one "...".
func (c *Config) elementDocs(elemType types.Type, v eval.Val,
	depth int,
) []pp.Doc {
	var docs []pp.Doc
	for i, elem := range asVals(v) {
		if c.PrintLength >= 0 && i >= c.PrintLength {
			docs = append(docs, pp.Text("..."))
			break
		}
		docs = append(docs, c.valueDoc(elemType, elem, depth+1))
	}
	return docs
}

// seqDoc lays out a bracketed sequence (a list, record, or
// tuple). Elements fill across lines: as many as fit share a
// line, and each element is treated as a unit (a record in a
// list of records stays together, and the list wraps between
// records). There is no space after the comma, the way SML/NJ
// prints values. Continuation lines align under the first
// element.
func (c *Config) seqDoc(open, closing string,
	docs []pp.Doc,
) pp.Doc {
	if len(docs) == 0 {
		return pp.Text(open + closing)
	}
	items := make([]pp.Doc, len(docs))
	for i, d := range docs {
		if i < len(docs)-1 {
			items[i] = pp.Beside(d, pp.Text(","))
		} else {
			items[i] = d
		}
	}
	return pp.Concat(pp.Text(open),
		pp.Align(pp.Fill(pp.Empty(), items)), pp.Text(closing))
}

// conDoc lays out a datatype value such as "SOME 4". Only an
// argument that is itself a constructor application needs
// parentheses ("SOME (SOME 4)"); tuples, lists, and negative
// numbers delimit themselves ("SOME (1,2)", "SOME [1]",
// "SOME ~1"), as probed against java.
func (c *Config) conDoc(t *types.Named, v eval.Val,
	depth int,
) pp.Doc {
	con, ok := v.(eval.Con)
	if !ok {
		return pp.Text("fn")
	}
	if con.Arg == nil {
		return pp.Text(con.Name)
	}
	argDoc := c.valueDoc(c.conArgType(t, con), con.Arg, depth+1)
	if argCon, isCon := con.Arg.(eval.Con); isCon &&
		argCon.Arg != nil {
		argDoc = pp.Concat(pp.Text("("), argDoc, pp.Text(")"))
	}
	return pp.Concat(pp.Text(con.Name), pp.Text(" "), argDoc)
}

// conArgType is the type of a constructor value's argument: the
// constructor's declared argument type with the datatype's type
// arguments substituted in.
func (c *Config) conArgType(t *types.Named,
	con eval.Con,
) types.Type {
	if c.sys != nil {
		if tc, ok := c.sys.LookupTyCon(con.Name); ok &&
			tc.Arg != nil {
			return c.sys.Substitute(tc.Arg, t.Args)
		}
	}
	if len(t.Args) == 1 {
		return t.Args[0]
	}
	return t
}

func (c *Config) primitiveString(t *types.Primitive,
	v eval.Val,
) string {
	// lint: sort until '^\t}' where '^\tcase '
	switch t.String() {
	case "bool":
		v2, _ := v.(bool)
		return strconv.FormatBool(v2)
	case "char":
		v2, _ := v.(rune)
		return `#"` + eval.CharToString(v2) + `"`
	case "int":
		v2, _ := v.(int32)
		return eval.FormatInt(v2)
	case "real":
		v2, _ := v.(float32)
		return eval.FormatReal(v2)
	case "string":
		v2, _ := v.(string)
		if c.StringDepth >= 0 && len(v2) > c.StringDepth {
			return `"` + escapeString(v2[:c.StringDepth]) +
				`#"`
		}
		return `"` + escapeString(v2) + `"`
	default:
		return "()"
	}
}

func asVals(v eval.Val) []eval.Val {
	vals, ok := v.([]eval.Val)
	if !ok {
		return nil
	}
	return vals
}
