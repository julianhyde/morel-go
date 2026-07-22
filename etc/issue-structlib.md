# Define derivable structure members in Morel source

## Summary

Most members of the built-in structures are thin wrappers that could be written in Morel itself, in terms of a small set of native primitives, rather than in Go. This issue proposes a general "structure library" mechanism so that a built-in structure's members can be supplied partly by native Go (the primitives) and partly by an embedded Morel source file (everything derivable), with a single loader that any structure can opt into. It removes duplicated logic, makes the derived members readable as Morel, and keeps one source of truth.

The immediate motivation is the `PP` pretty-printer (task 111): its ~15 derived combinators (`hsep`, `vsep`, `sep`, `encloseSep`, `parens`, `punctuate`, ...) are currently Go glue over the ~12 native primitives, and would read far more naturally as Morel. But the mechanism generalizes to the whole library.

## The mechanism

A built-in structure is described in three parts, two of which already exist:

- Types come from `lib/<name>.sig` via `sig.Load` (unchanged) — this is what types the structure and lists its members.
- A small set of `natives` (Go) — the members that must be native because they construct or consume the structure's opaque runtime value, or need a capability the Morel layer can't express.
- An embedded `lib/<name>.sml` — every other member, written in Morel over the natives.

```
type structLib struct {
    name    string              // "PP" — matches lib/PP.sig
    natives map[string]eval.Val // member -> native impl
    src     string              // embedded Morel source for the rest
}
```

Boot sequence, run after `sig.Load` and before the existing structure-building loop, replacing today's per-structure wiring:

```
for each structLib L:
  1. bind natives:  values["L.name.member"] = native impl
  2. loadStructLib(L):
       recType := record type of the binding named L.name          # from the sig
       env := global bindings/values, PLUS each native member
              bound UNQUALIFIED with its type from recType          # an implicit `open`
       for each statement S in Split(L.src):
           (name, ty, val) := evalDecl(env, S)                      # loadScott's body, factored
           env := env + (name : ty = val)                           # later decls see earlier
       for each field f of recType present in env:
           values["L.name.f"] = env[f]                              # wire derived into the slots
# then the existing loop builds each structure record from values["X.member"]
```

Two properties make it work: the natives are seeded into the evaluation environment under their bare names, so the `.sml` references them exactly as members of an ML `struct` reference each other (an implicit `open`), and those bare bindings never leak to the global namespace; and intra-library dependencies fall out for free because statements evaluate in sequence (so `sep` sees `vsep`, `encloseSep` sees `parens`). The existing structure-building loop is unchanged.

This generalizes the existing `loadScott` precedent (an embedded Morel `val scott = {...}` evaluated at startup); `loadStructLib` is `loadScott` extended to many statements in a scoped environment, with the results wired into structure member slots.

## Non-function members work too

The loader evaluates arbitrary declarations, so a member's value can be a plain value as well as a closure. `PP.softLine = group line` is already a non-function derived `doc`. The same enables constant members such as `Math.e = exp 1.0`, `Math.pi = 3.14159265358979`, `Real.posInf = 1.0 / 0.0`, `Real.negInf = ~1.0 / 0.0`, and `Char.minChar = chr 0`.

## Phased plan

The deciding property for what to convert is recursion, because the Inliner (task 96) inlines only non-recursive bindings. A non-recursive member is inlined at its call sites — the call disappears and the body is exposed to constant-folding, case-of-known-constructor, and predicate inversion, ending up as fast as native or faster. A recursive or hot member is not inlined and runs through the evaluator instead of a Go loop, so it regresses. Convert structures whose members are all small and non-recursive; leave the recursive/hot ones native. (Rationale expanded in the comment thread.)

Phase 1 — the mechanism, and two clients chosen as inlining sweet spots (non-recursive throughout): `Fn` (`id`/`const`/`o`/`curry`/`uncurry`/`flip`/`apply`/`equal`/`notEqual` — pure combinators; recursive `repeat` stays native, which tests a mixed native+Morel structure) and `Option` (`isSome`/`getOpt`/`valOf`/`map`/`join`/`filter`/`mapPartial`/`compose` — datatype case-analysis whose inlining unlocks case-of-known-constructor in query predicates).

PP — move PP's ~15 derived combinators from `eval/pp.go` into `lib/PP.sml`, keeping the ~12 native primitives; a `structLib` client once phase 1 has proven the mechanism. No new machinery.

Phase 2 — convert the remaining clear wins across the library (see the manifest below): `Bool`, `Either`, `ListPair`, `Math` (folding `e`/`pi` to literals rather than inlining `exp 1.0`), and the derivable subsets, leaving hot or genuinely-primitive members native.

## Conversion manifest

Fully convertible structures (every member is derivable Morel over `::`/`[]`/`=`/arithmetic):

- Option (10): getOpt, isSome, valOf, filter, join, app, map, mapPartial, compose, composePartial.
- Either (13): isLeft, isRight, asLeft, asRight, map, mapLeft, mapRight, app, appLeft, appRight, fold, proj, partition.
- Fn (12): id, const, apply, o, curry, uncurry, flip, repeat, equal, notEqual.
- ListPair (14): zip, unzip, map, app, all, exists, foldr, foldl, and the `*Eq` variants.
- List (~28): null, length, hd, tl, last, getItem, nth, take, drop, rev, concat, revAppend, app, map, mapPartial, find, filter, partition, foldl, foldr, exists, all, tabulate, collate, mapi, only, except, intersect.

Mostly convertible:

- Bool (7): not, andalso, orelse, implies, toString, fromString.
- Relational (8 of 9): count, empty, nonEmpty, sum, max, min, only, iterate; native compare.
- Vector: the sub-based traversals (map, app, foldl/r, foldli/ri, appi, mapi, find, findi, exists, all, collate, concat, tabulate); native fromList, sub, update, maxLen.
- General (3): o, before, ignore; native exnName, exnMessage.

Partly convertible (a derivable subset):

- Char: succ, pred, is* predicates (isAscii, isAlpha, isDigit, isHexDigit, isOctDigit, isUpper, isLower, isSpace, isCntrl, isGraph, isPrint, isPunct, isAlphaNum), toLower, toUpper, contains, notContains, compare; native ord, chr, min/maxChar, maxOrd, the parse/convert members.
- String: isPrefix, isSuffix, isSubstring, concatWith, str, translate, concat, map, tokens, fields, compare, collate; native size, sub, substring, extract, ^, implode, explode, maxSize, the parse/convert members.
- Int: abs, min, max, sign, sameSign, compare, and minInt/maxInt/precision as literals; native div, mod, quot, rem, the conversions.
- Real: abs, min, max, sign, sameSign, isNan, unordered, compare, posInf, negInf; native rem, the bit-level and format/parse members.
- Math: e, pi, tan, sinh, cosh, tanh, log10, pow, asin, acos; native sqrt, sin, cos, exp, ln, atan, atan2.
- Word: min, max, compare, toString; native the bit operators, conversions, wordSize.

Not worth converting (native by nature): Sys, Interact, Datalog, Variant, Time, Date, Bag, Range, IntInf, IEEEReal, StringCvt.

## Caveats

Performance: a Morel `List.length`/`List.map` runs through the evaluator rather than a Go loop. Keep the hot library traversals native even where they are derivable; the wins are the thin, cold wrappers (Option/Either/Fn, the `is*` predicates, `min`/`max`/`sign`, the enclosure and aggregate combinators) where native code buys nothing.

Edge cases: a few derivations must reproduce native corner behavior exactly — `Int.abs minInt` raising `Overflow`, `Real.abs nan` preserving the sign bit, `Char.chr` bounds. These are convertible but need the same guards, so they are not free.
