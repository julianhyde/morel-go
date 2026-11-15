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
	"sort"
	"strings"

	"github.com/hydromatic/morel-go/internal/ast"
	"github.com/hydromatic/morel-go/internal/types"
)

// topBuiltin describes a top-level built-in value: its type and,
// for the operators that SML overloads over the numeric types,
// the type to prefer if unification leaves it undetermined.
type topBuiltin struct {
	typ       string
	preferred string
}

// Types shared by several built-ins.
const (
	numPair         = "'a * 'a -> 'a"
	opTimes         = "op *"
	opPlus          = "op +"
	opMinus         = "op -"
	opDiv           = "op div"
	opMod           = "op mod"
	opNegate        = "op ~"
	absName         = "abs"
	decimalName     = "decimal"
	opDivide        = "op /"
	lengthName      = "length"
	opAt            = "op @"
	opCaret         = "op ^"
	opCons          = "op ::"
	opElem          = "op elem"
	opGe            = "op >="
	opGt            = "op >"
	opLe            = "op <="
	opLt            = "op <"
	opNe            = "op <>"
	opNotElem       = "op notelem"
	notName         = "not"
	comparison      = "'a * 'a -> bool"
	realToInt       = "real -> int"
	strToUnit       = "string -> unit"
	boolName        = "bool"
	intName         = "int"
	sumName         = "sum"
	countName       = "count"
	onlyName        = "only"
	maxByName       = "maxBy"
	minByName       = "minBy"
	realName        = "real"
	wordName        = "word"
	stringName      = "string"
	charName        = "char"
	elemPlanName    = "elem"
	notElemPlanName = "notelem"
	unitName        = "unit"
)

// topBuiltins are the built-in values that no structure's
// signature file declares: the operators, and the top-level
// aliases of common functions.
var topBuiltins = map[string]topBuiltin{
	absName:      {"'a -> 'a", intName},
	"app":        {"('a -> unit) -> 'a list -> unit", ""},
	bagTyCon:     {"'a list -> 'a bag", ""},
	"ceil":       {realToInt, ""},
	"chr":        {"int -> char", ""},
	"compare":    {"'a * 'a -> `order`", ""},
	"concat":     {"string list -> string", ""},
	decimalName:  {"string -> decimal", ""},
	"env":        {"unit -> (string * string) list", ""},
	"exnMessage": {"exn -> string", ""},
	"exnName":    {"exn -> string", ""},
	"explode":    {"string -> char list", ""},
	"fields":     {"(char -> bool) -> string -> string list", ""},
	"floor":      {realToInt, ""},
	"foldl":      {"('a * 'b -> 'b) -> 'b -> 'a list -> 'b", ""},
	"foldr":      {"('a * 'b -> 'b) -> 'b -> 'a list -> 'b", ""},
	"getOpt":     {"'a option * 'a -> 'a", ""},
	"hd":         {"'a list -> 'a", ""},
	"implode":    {"char list -> string", ""},
	"isSome":     {"'a option -> bool", ""},
	lengthName:   {"'a list -> int", ""},
	"map":        {"('a -> 'b) -> 'a list -> 'b list", ""},
	notName:      {"bool -> bool", ""},
	"null":       {"'a list -> bool", ""},
	opTimes:      {numPair, intName},
	opPlus:       {numPair, intName},
	opMinus:      {numPair, intName},
	opDivide:     {numPair, realName},
	opCons:       {"'a * 'a list -> 'a list", ""},
	opLt:         {comparison, ""},
	opLe:         {comparison, ""},
	opNe:         {comparison, ""},
	eqOpName:     {comparison, ""},
	opGt:         {comparison, ""},
	opGe:         {comparison, ""},
	opAt:         {"'a list * 'a list -> 'a list", ""},
	opCaret:      {"string * string -> string", ""},
	// "@" and "^" bind under their bare names too, as morel-java
	// binds them. Only "op @" and "op ^" can be written, so the
	// bare name is reachable only through the environment.
	"@":           {"'a list * 'a list -> 'a list", ""},
	"^":           {"string * string -> string", ""},
	opDiv:         {numPair, intName},
	opMod:         {numPair, intName},
	"op o":        {"('b -> 'c) * ('a -> 'b) -> 'a -> 'c", ""},
	opNegate:      {"'a -> 'a", intName},
	"ord":         {"char -> int", ""},
	"plan":        {"unit -> string", ""},
	"real":        {"int -> real", ""},
	"rev":         {"'a list -> 'a list", ""},
	"round":       {realToInt, ""},
	"set":         {"string * 'a -> unit", ""},
	"show":        {"string -> string", ""},
	"showAll":     {"unit -> (string * string) list", ""},
	"size":        {"string -> int", ""},
	"str":         {"char -> string", ""},
	"substring":   {"string * int * int -> string", ""},
	"tl":          {"'a list -> 'a list", ""},
	"tokens":      {"(char -> bool) -> string -> string list", ""},
	"unset":       {strToUnit, ""},
	"trunc":       {realToInt, ""},
	"use":         {strToUnit, ""},
	"useSilently": {strToUnit, ""},
	"valOf":       {"'a option -> 'a", ""},
	"vector":      {"'a list -> 'a vector", ""},
}

// eqOpName is the top-level binding of the equality operator.
const eqOpName = "op ="

// infixOpNames maps an infix operator's Op to the name of its
// top-level binding.
var infixOpNames = map[ast.Op]string{
	ast.AtOp:      opAt,
	ast.CaretOp:   opCaret,
	ast.ComposeOp: "op o",
	ast.ConsOp:    opCons,
	ast.ElemOp:    opElem,
	ast.NotElemOp: opNotElem,
	ast.DivOp:     opDiv,
	ast.DivideOp:  opDivide,
	ast.EqOp:      eqOpName,
	ast.GeOp:      opGe,
	ast.GtOp:      opGt,
	ast.LeOp:      opLe,
	ast.LtOp:      opLt,
	ast.MinusOp:   opMinus,
	ast.ModOp:     opMod,
	ast.NeOp:      opNe,
	ast.PlusOp:    opPlus,
	ast.TimesOp:   opTimes,
}

// weakeningOps are the operators overloaded at their operand's
// own type, "'a * 'a -> 'a" rather than Standard ML's
// "int * int -> int".
//
// Each computes a value that the operand's type has not been
// shown to contain, so each drops the condition of a checked type
// whatever it was applied to: "s + s" is an "int" even where "s"
// is a "small", and "fun dbl (i: small) = i + i" is
// "int -> int". The parameter is still checked, because a check
// reads the annotation, never the type inference deduced.
//
// "Int.abs" and "Int.max" need nothing: they are declared at a
// concrete "int", so an alias reaching them meets a different type
// and the meet does the work.
var weakeningOps = map[string]bool{
	// lint: sort until '^}' where '^\t'
	absName:  true,
	opDiv:    true,
	opDivide: true,
	opMinus:  true,
	opMod:    true,
	opNegate: true,
	opPlus:   true,
	opTimes:  true,
}

// TopBindings returns the bindings of the top-level built-in
// values, sorted by name.
func TopBindings(sys *types.System) []Binding {
	names := make([]string, 0, len(topBuiltins))
	for name := range topBuiltins {
		names = append(names, name)
	}
	sort.Strings(names)
	bindings := make([]Binding, 0, len(names))
	for _, name := range names {
		t, err := sys.Parse(topBuiltins[name].typ)
		if err != nil {
			panic("bad built-in type for " + name + ": " +
				err.Error())
		}
		bindings = append(bindings,
			Binding{Name: name, Type: t})
	}
	bindings = append(bindings, collectionBindings(sys)...)
	// "file" is the file system, of type "{...}" — a record whose
	// fields are not known until it is browsed. The binding gives
	// it the type it has before any browsing, which is what the
	// environment reports; a statement that uses it sees whatever
	// has been discovered by then.
	return append(bindings,
		Binding{Name: FileName, Type: sys.ProgressiveRecord(nil)})
}

// collectionBindings returns the built-ins whose type involves a
// collection (list or bag) with free orderedness — "elem" and
// "notelem" over a collection, and "only". Their types cannot be
// written as a plain string, so they are built here.
func collectionBindings(sys *types.System) []Binding {
	a := sys.Var(0)
	coll := sys.Collection(a)
	// "x elem c" and "x notelem c": 'a * 'a collection -> bool.
	elemType := sys.Fn(sys.Tuple(a, coll), sys.Bool)
	// Aggregates and "only" adapt to their input's orderedness (a
	// list or a bag), so they take a collection of free
	// orderedness.
	collToElem := sys.Fn(coll, a)        // max, min, sum, only
	collToInt := sys.Fn(coll, sys.Int)   // count
	collToBool := sys.Fn(coll, sys.Bool) // empty, nonEmpty
	// "iterate c f" computes a fixed point, and adapts the same way:
	// a list function on lists and a bag function on bags. Declared
	// here rather than as a plain string, which could say only one
	// of the two.
	iterateType := sys.Fn(coll,
		sys.Fn(sys.Fn(sys.Tuple(coll, coll), coll), coll))
	// "maxBy keyFn c" and "minBy keyFn c": the element whose key is
	// greatest or least. The key is of a type of its own, and is
	// compared as a value of that type, as max and min compare
	// elements.
	byType := sys.Fn(sys.Fn(a, sys.Var(1)), sys.Fn(coll, a))
	return append([]Binding{
		{Name: "iterate", Type: iterateType},
		{Name: maxByName, Type: byType},
		{Name: minByName, Type: byType},
		{Name: opElem, Type: elemType},
		{Name: opNotElem, Type: elemType},
		{Name: countName, Type: collToInt},
		{Name: emptyName, Type: collToBool},
		{Name: nonEmptyName, Type: collToBool},
		{Name: "max", Type: collToElem},
		{Name: "min", Type: collToElem},
		{Name: sumName, Type: collToElem},
		{Name: onlyName, Type: collToElem},
	}, optAggregateBindings(sys, a, coll)...)
}

// optAggregateBindings returns the "Opt" and "Some" forms of the
// collection aggregates, and the statistical aggregates
// (hydromatic/morel#503).
//
// A statistical aggregate -- "avg", "stddevSamp" and so on -- is
// overloaded on its element type, int, real or decimal; the type here
// is its first variant, which is what the environment reports, and
// the type resolver gives each use the overloaded type (see
// statTerm).
func optAggregateBindings(sys *types.System, a, coll types.Type,
) []Binding {
	optA := sys.Named("option", a)
	collOpt := sys.Collection(optA)
	bindings := []Binding{
		{Name: "countSome", Type: sys.Fn(collOpt, sys.Int)},
		{Name: "maxOpt", Type: sys.Fn(coll, optA)},
		{Name: "maxSome", Type: sys.Fn(collOpt, optA)},
		{Name: "minOpt", Type: sys.Fn(coll, optA)},
		{Name: "minSome", Type: sys.Fn(collOpt, optA)},
		{Name: "sumOpt", Type: sys.Fn(coll, optA)},
		{Name: "sumSome", Type: sys.Fn(collOpt, optA)},
	}
	bindings = slices.Grow(bindings, len(statNames)*len(statForms))
	intBag := sys.Bag(sys.Int)
	intOptBag := sys.Bag(sys.Named("option", sys.Int))
	realOpt := sys.Named("option", sys.Real)
	for _, stat := range statNames {
		bindings = append(bindings,
			Binding{Name: stat, Type: sys.Fn(intBag, sys.Real)},
			Binding{Name: stat + "Opt", Type: sys.Fn(intBag, realOpt)},
			Binding{Name: stat + "Some", Type: sys.Fn(intOptBag, realOpt)})
	}
	return bindings
}

// statNames are the statistical aggregates, each of which has an
// "Opt" and a "Some" form.
var statNames = []string{
	avgName, "stddevPop", "stddevSamp", "varPop", "varSamp",
}

// statForms are the forms of a statistical aggregate: plain, "Opt"
// and "Some".
var statForms = []string{"", "Opt", "Some"}

// avgName is the mean, the one statistical aggregate whose result's
// type follows its elements'.
const avgName = "avg"

// IsCollectionAggregate reports whether a member of Bag or
// Relational is one of the aggregates declared over "'a bag" that
// adapt to a list: count, empty, max, min, nonEmpty, only and sum.
func IsCollectionAggregate(member string) bool {
	switch member {
	case countName, emptyName, "max", "min", nonEmptyName, onlyName, sumName,
		"countSome", "maxOpt", "maxSome", "minOpt", "minSome", "sumOpt",
		"sumSome":
		return true
	default:
		return false
	}
}

// CollectionAggType returns the collection-of-free-orderedness type
// of a Relational aggregate or "only" member, or nil for any other
// member. It lets the Relational structure adapt its aggregates to
// a list or a bag, like the top-level aliases.
func CollectionAggType(sys *types.System, member string,
	field types.Type,
) types.Type {
	fn, ok := field.(*types.Fn)
	if !ok {
		return nil
	}
	if IsCollectionAggregate(member) {
		// The member's declared parameter is "'a bag"; replace it
		// with a collection of the same element type.
		elem := bagElem(fn.Param)
		if elem == nil {
			return nil
		}
		return sys.Fn(sys.Collection(elem), fn.Result)
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch member {
	case "iterate":
		// Every bag in the signature becomes a collection; their
		// shared orderedness makes iterate a list function on
		// lists and a bag function on bags.
		if bagElem(fn.Param) == nil {
			return nil
		}
		return bagsToCollections(sys, fn)
	case maxByName, minByName:
		// "keyFn -> 'a bag -> 'a": the collection is the second
		// parameter, so it is the result that adapts.
		inner, isFn := fn.Result.(*types.Fn)
		if !isFn {
			return nil
		}
		elem := bagElem(inner.Param)
		if elem == nil {
			return nil
		}
		return sys.Fn(fn.Param,
			sys.Fn(sys.Collection(elem), inner.Result))
	default:
		return nil
	}
}

// bagElem is the element type of "'a bag", or nil if the type is
// not a bag.
func bagElem(t types.Type) types.Type {
	bag, isBag := t.(*types.Named)
	if !isBag || bag.Name != bagTyCon || len(bag.Args) != 1 {
		return nil
	}
	return bag.Args[0]
}

// OverloadMethods returns the method overloads that signature
// files cannot express — a second member of an existing name
// dispatching on a different receiver type — with the hidden
// bindings their calls rewrite to. Range.contains works on a
// range (the signature's member), a continuous_set, and a
// discrete_set.
func OverloadMethods(sys *types.System) ([]MethodInfo, []Binding) {
	csT, err := sys.Parse("'a continuous_set -> 'a -> bool")
	if err != nil {
		panic(err)
	}
	dsT, err := sys.Parse("'a discrete_set -> 'a -> bool")
	if err != nil {
		panic(err)
	}
	dsComplementT, err := sys.Parse("'a discrete_set -> 'a discrete_set")
	if err != nil {
		panic(err)
	}
	methods := []MethodInfo{
		{
			Type: csT, Structure: rangeStructure, Name: "contains",
			Target: CsContainsName,
		},
		{
			Type: dsT, Structure: rangeStructure, Name: "contains",
			Target: DsContainsName,
		},
		{
			Type: dsComplementT, Structure: rangeStructure,
			Name: "complement", Target: DsComplementName,
		},
	}
	bindings := []Binding{
		{Name: CsContainsName, Type: csT},
		{Name: DsContainsName, Type: dsT},
		{Name: DsComplementName, Type: dsComplementT},
	}
	return methods, bindings
}

// rangeStructure is the structure the set methods belong to.
const rangeStructure = "Range"

// The hidden bindings behind the Range members that a signature
// can only declare once -- "contains" over a continuous and a
// discrete set, and "complement", whose declared form is the
// continuous one. morel-java hides the same members the same way,
// and by these names, which is why they are not qualified by the
// structure they belong to: they are what "Sys.env ()" shows, and
// the two implementations should show the same. The kernel
// registers their implementations.
const (
	CsContainsName   = "$csContains"
	DsContainsName   = "$dsContains"
	DsComplementName = "$dsComplement"
)

// bagsToCollections replaces each bag in a type with a collection
// of the same element.
func bagsToCollections(sys *types.System, t types.Type) types.Type {
	// lint: sort until '^\t}' where '^\tcase '
	switch t := t.(type) {
	case *types.Fn:
		return sys.Fn(bagsToCollections(sys, t.Param),
			bagsToCollections(sys, t.Result))
	case *types.Named:
		if t.Name == bagTyCon && len(t.Args) == 1 {
			return sys.Collection(bagsToCollections(sys, t.Args[0]))
		}
		return t
	case *types.Tuple:
		args := make([]types.Type, len(t.Args))
		for i, arg := range t.Args {
			args[i] = bagsToCollections(sys, arg)
		}
		return sys.Tuple(args...)
	default:
		return t
	}
}

// StatVariants returns the types of the variants of a statistical
// aggregate (see statTerm), as "Sys.env" lists them, one for each
// element type and each kind of collection: "int bag -> real", "int
// list -> real", ... It returns nil for any other name.
func StatVariants(name string) []string {
	base, some := strings.CutSuffix(name, "Some")
	opt := some
	if !some {
		base, opt = strings.CutSuffix(name, "Opt")
	}
	if !slices.Contains(statNames, base) {
		return nil
	}
	var variants []string
	for _, elem := range []string{intName, realName, decimalName} {
		result := realName
		if base == avgName && elem == decimalName {
			result = decimalName
		}
		arg := elem
		if some {
			arg += " option"
		}
		if opt {
			result += " option"
		}
		for _, coll := range []string{"bag", "list"} {
			variants = append(variants, arg+" "+coll+" -> "+result)
		}
	}
	return variants
}
