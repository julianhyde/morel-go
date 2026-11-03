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
	"strconv"
	"strings"

	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/spark/connect"
)

// Plan is a Spark plan: what "Spark.prepare" makes of a Morel
// function, and what "execute" runs.
//
// Its text is the plan's own, in Spark's protobuf text format, with
// one difference: a LocalRelation shows the number of rows it
// carries rather than the Arrow bytes that carry them. The bytes are
// neither readable nor stable, and the row count is what a reader
// wants.
type Plan struct {
	proto          *connect.Plan
	localRowCounts []int
}

// Proto returns the plan, to send to the server.
func (p *Plan) Proto() *connect.Plan { return p.proto }

// Text returns the plan as text.
func (p *Plan) Text() string {
	text := Text(p.proto)
	if len(p.localRowCounts) == 0 {
		return strings.TrimSuffix(text, "\n")
	}
	// A local relation's rows become a count. The relations appear
	// in the text in the order the translator made them, because
	// the translator walks the tree in the order the text prints it,
	// so the counts can be consumed in order.
	var b strings.Builder
	i := 0
	for line := range strings.SplitSeq(text, "\n") {
		indent, _, found := strings.Cut(line, `data: "`)
		if found && strings.TrimSpace(indent) == "" &&
			i < len(p.localRowCounts) {
			rows := p.localRowCounts[i]
			i++
			unit := " rows]"
			if rows == 1 {
				unit = " row]"
			}
			b.WriteString(indent + "data: [" + strconv.Itoa(rows) +
				unit + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}
	// The split made an empty last line of the newline that ended
	// the text; it has been written as a newline of its own.
	s := strings.TrimSuffix(b.String(), "\n")
	return strings.TrimSuffix(s, "\n")
}

// unsupported is the refusal to translate something.
func unsupported(what string) error {
	return eval.NewSparkException("UNSUPPORTED", "Spark cannot do "+what)
}

// End plan.go
