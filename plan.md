<!--
{% comment %}
Licensed to Julian Hyde under one or more contributor license
agreements.  See the NOTICE file distributed with this work
for additional information regarding copyright ownership.
Julian Hyde licenses this file to you under the Apache
License, Version 2.0 (the "License"); you may not use this
file except in compliance with the License.  You may obtain a
copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
either express or implied.  See the License for the specific
language governing permissions and limitations under the
License.
{% endcomment %}
-->
# Bootstrap plan (issue #1)

Plan for bootstrapping morel-go by replicating commits from
[morel-java](https://github.com/hydromatic/morel) and
[morel-rust](https://github.com/hydromatic/morel-rust), based on a
review of both projects' full commit histories (567 and 223 commits
respectively, as of 2026-07-13).

## Goal

"Mature development state" means parity with morel-rust as of its
commit `e0779b86` (2026-04-27, predicate inversion,
hydromatic/morel#217). By that point the difficult type-inference
changes — constraint unification, `over`/`inst` overloading (#237),
polymorphic datatypes (#70), ordered/unordered collections (#273),
aggregate adaptation (#271) — were behind the project, and adding
new features and built-in functions was routine.

Out of scope (rust did these after `e0779b86`): tail-call
optimization (#151; but see task 3.3 — frames are shaped for it),
Datalog (#323), `raise` (#364), file reader and progressive types
(#209), FBBT (#373), `Word` (#396), outer joins (#75), `yieldAll`
(#257), `?.` (#378), binder syntax (#387), the interactive-shell
phase (#45, morel#413/#414), and everything Calcite (dual.smli
runs local-only, as in rust; Calcite-only hunks are simply not
pulled). One deliberate exception: the Wadler-Leijen printing
engine (#398) is pulled *forward* into phase 3, so that output
matches java's present-day `.smli` format exactly; the `PP`
structure's user-facing surface can come whenever.

## Method

Component by component, going light on tests at first. The corpus
grows incrementally rather than being imported up front — a
whole-file import (with rust-style in-file disablement, or a
ledger of expected failures) forces the port to track the java
timeline everywhere at once.

1. Build one component at a time: parser, then type inference,
   then printing and evaluation, then the rest. For each feature,
   pull in the hunks of `.smli` that were added to morel-java for
   that feature and have not changed significantly since (check
   the java file's history for stability).
2. Pulled hunks are **verbatim** from java's present-day files —
   never adapted for Go. Going light means pulling few hunks, not
   editing them.
3. **No in-file disablement.** Rust's `set("mode","validate")`
   brackets and `(* ... *)` disablement made it very hard to tell
   which sections were disabled (96 brackets across 19 files
   remain there today). In morel-go, a section exists in a `.smli`
   file only when it passes; "disabled" is simply "absent", and
   what is absent is exactly what the divergence report shows.
   Sole exception: `Sys.plan` output is matched best-effort, and
   a plan line that is infeasible to match may be commented out —
   greppable, and still counted as divergence by the report.
4. `etc/check-convergence.py` is the progress metric: per-file
   divergence from morel-java must never increase, and trends
   monotonically downward. Its per-file report is the project
   dashboard.
5. Test cheaply before evaluation exists: `Sys.parseTree` verifies
   ASTs from scripts (no Go unit tests for the parser); `:t` lines
   verify type inference without evaluation (needing only builtin
   *types* from `lib/*.sig`, no implementations); the
   Wadler-Leijen printing engine (morel#398) comes in early so
   output matches java's present-day format exactly and pulled
   hunks pass without re-blessing.
6. Once a file has caught up with java, ordinary propagation rules
   apply: every changed section moves verbatim; see `agents.md`.

The test scripts are implementation-independent and shared across
the three ports; matching them is the project's acceptance
criterion.

## Lessons from morel-rust (day-1 rules)

Reversals and fix-cascades in the rust history, and the rule each
implies for this port:

| Rust pitfall | Cost there | Rule here |
| --- | --- | --- |
| `Shell` conflated kernel, script runner, and terminal; statement splitting duplicated 3x; config duplicated | Refactored 11 months in | `Kernel`, `ScriptRunner`, `StatementSplitter` as separate units from day 1; one `Config`, owned by `Kernel` |
| Line-scan statement splitter; test files edited to fit it | Permanent fix tax + divergence to undo | Tokenizer-driven splitter before importing the corpus; never edit a test to fit the harness |
| Query typing built before constraint-capable unification | Relational tests stuck in validate mode ~5 months | Constraint hooks (overloads, default-to-int, collection kind) in the initial unifier |
| Output formats diverged from java (real printing, span format, ad-hoc pretty-printer) | Every `.smli` re-blessed repeatedly | Java-identical printing before enabling large test regions |
| Recursion/closures/linking designed piecemeal; TCO retrofitted | Largest bug farm in the log | Frames, capture analysis, link table, let-rec, and TCO shape designed as one unit |
| No convergence gate for the first 2 months of propagation | >= 5 propagations silently dropped test sections | Gate + propagation ledger active from the first propagation |
| In-file disablement (`set("mode","validate")`, `(* ... *)`) | Hard to audit which sections were disabled; self-inflicted divergence | No disablement mechanism at all: a section enters a `.smli` file only when it passes; absence is the only disabled state, auditable via the divergence report |
| Constructor representation churned 3x | Repeated rework | Constructors carry (datatype, ordinal) from day 1; comparators are type-directed |
| Arity-encoded builtins broke on partial application | Special-case eta-expansion bolted on | Builtins uniformly curried, partial-application-safe |
| Panics converted to catchable errors late | Span plumbing retrofitted everywhere | User-triggerable failures return span-carrying errors from day 1 |
| Invented encodings/flags, later reverted to match java | Round trips (`-c` vs `-e`, record encodings) | When java has an encoding, CLI flag, or format: copy it exactly, first time |

Final designs adopted directly from morel-java (skipping its own
dead ends): no `suchthat` keyword; set operations as pipeline steps,
not binary operators; `order` takes a single expression (`DESC`
constructor, no `desc` keyword); group/compute in the final `over`
syntax; `Ast` vs typed `Core` IR with Case-only destructuring;
slot-indexed frames.

## Design decisions

| Decision | Choice |
| --- | --- |
| Go version | 1.25 minimum (the two supported releases) |
| Lint | golangci-lint v2, all linters minus a justified disable list; gofumpt + goimports |
| Package layout | `internal/{ast, core, parse, types, compile, eval, shell}`, `cmd/morel` |
| Embedding | Not a goal; everything under `internal/` until needed |
| Test corpus | `testdata/script/`, grown hunk-by-hunk from java's present-day files; every pulled hunk verbatim |
| Parser | Hand-written recursive-descent lexer + parser; the lexer also drives the statement splitter (and later a highlighter) |
| Type inference | Port java's Martelli-Montanari unifier, with constraint hooks from the start |
| Values | `Val` interface, one concrete type per variant; constructors are (datatype, ordinal); performance work deferred until profiled |
| Errors | Span-carrying `MorelError` via ordinary `error` returns; sentinel early-return for sinks; panic only for internal invariants |
| Query execution | Push-based `RowSink` pipeline, java's binding model verbatim (canonical field order, single implicit-label helper, `current` as a rewrite); single-threaded — parallelism is not a goal |
| TCO | Frames shaped for trampolining from day 1; implementation is a fast-follow after the endpoint (rust retrofitted it painfully one day after `e0779b86`) |
| Test disablement | None — a section enters a `.smli` file only when it passes; absence is visible in the divergence report. Sole exception: infeasible `Sys.plan` lines may be commented out (plans are matched best-effort) |
| New tests | Written against morel-java first (upstream), then propagated back; go-only `.smli` files are a temporary escape hatch, flagged by the report tool; `parse.smli` (task 11) is the one sanctioned exception |
| Convergence gate | Per-file divergence from morel-java (source of truth) may never increase, and trends monotonically downward; informational diff against morel-rust (which carries ~6.4k divergent lines of its own) |

## Components and data structures

Java uses object-oriented class hierarchies with visitors; rust
uses sum types with pattern matching, under an ownership model
that disallows shared mutable data. Go should not force one
pattern everywhere: use sealed interfaces (Go's sum type) where
variants carry genuinely different payloads, and bare `any` where
the static type already determines interpretation — which is
exactly how java's evaluator works. Where rust's borrow checker
forced ceremony (`Weak` refs, `Rc<RefCell<>>`, lifetime splits),
Go's GC deletes the problem — but also deletes rust's immutability
guarantees, so three explicit disciplines replace them (below).

| Component | java | rust | Go design |
| --- | --- | --- | --- |
| AST & Core | Class hierarchies + Visitor/Shuttle | Enums + `match` | Sealed interfaces (unexported marker method), one struct per node; `go/ast`-style `Walk`/rewrite funcs. Each node carries an `Op` tag (a `const iota` enum, as in java), so `switch n.Op()` is checked by the `exhaustive` linter — bare type switches are not |
| Types & TypeSystem | `Type` hierarchy + central registry | `Rc<Type>` + interning pool (added late, under profiling pressure) | Sealed `Type` interface; `TypeSystem` interns by canonical key so pointer equality works from day 1 |
| Unifier | Martelli-Montanari, mutable substitution, "action map" hooks | `Rc`-shared terms, `Copy` vars, one-action-per-var limitation (worked around, then redone) | Direct port: `*TypeVar` cells with mutable binding — plain pointers. Constraint hooks are a slice of closures per var (multi-action from day 1). The component where Go is most obviously simpler than rust |
| Values (`Val`) | Bare `Object`; interpretation driven by static type | `enum Val`, "box less" | `type Val = any`, with documented concrete types: `int32` (java's ints are 32-bit; `uint32` awaits `Word`, morel#396), `float32` (real; computed via `float64`, per rust#44), `string`, `rune`, `bool`, unit struct, `[]Val` (lists, tuples, records in canonical field order), `*Closure`, constructor value carrying (datatype, ordinal). This is java's model, not a compromise: comparators, printers, and sinks are type-directed, so a runtime tag would duplicate what `Core` already knows |
| Code (compiled form) | `Code` interface + `describe()` for `Sys.plan` | Tree of closures | `Code` interface with `Eval(*Frame) (Val, error)` and `Describe` — not bare funcs, because `Sys.plan` reproduces java's plan text best-effort, which needs introspectable nodes |
| Frames, closures, recursion | Slot-indexed stack (morel#349, final design), `LinkCode` | `Frame` slots; `Arc<ClosureData>` + `Weak` self-refs (pain) | `Frame` = `[]Val` slots + captured-env pointer, shaped for trampolining. Self-referential closures: assign the field after construction — GC handles cycles. Top-level forward refs: session-owned link table of patchable cells |
| Environments | Immutable compile-time `Environment` vs runtime slots | Same split + lifetime friction | Same two-layer split, zero friction: compile-time `*Environment` parent-linked and persistent; runtime is slots only, indices resolved at compile time |
| Builtin registry | `BuiltIn` enum (479 constants) + `Codes` map + `.sig` cross-check | strum-props metadata (retrofit after 2 latent bugs in a hand-written table) | One package-level table `[]builtin{structure, name, typeString, impl}`, alphabetical (lint-enforced), keyed lookups built at init, validated against `lib/*.sig`. Every impl uniformly curried and partial-application-safe |
| RowSink pipeline | `RowSink`/`RowSinks`, push | Push, `EarlyReturn` advisory error | `RowSink` interface { `Accept(row []Val) error`; `Finish() error` } with sentinel `errEarlyReturn` (the `io.EOF` idiom) |
| Session/Config | `Session` + `Prop` enum | `Rc<RefCell<Session>>` + a config-duplication bug | Plain `*Session` owned by `Kernel`, passed freely; one config by construction |
| Errors/spans | Positioned exceptions | Spans smuggled as strings at first, fixed later | `*MorelError{Span, Kind, Msg}` through ordinary `error` returns from day 1; `Span` a value struct on every node; panic only for internal invariants |
| Pretty printer | Wadler-Leijen (morel#398) | Same, late | Small sealed `Doc` interface (text/line/concat/nest/group) — a tiny closed set where classic sum-type style fits perfectly |
| Lexer/parser | JavaCC grammar | Pest PEG (+ LALRPOP for Datalog) | Hand-written: lexer as a struct with `Next() Token` (shared by splitter and, later, highlighter); recursive-descent parser with precedence table; `go/parser` as the stylistic model |

Trade-offs accepted, and the disciplines that back them:

* **Exhaustiveness** is Go's weak spot (rust checks it at compile
  time). The `Op`-tag pattern recovers it for op switches; type
  switches keep a `default: panic("unreachable")` convention. The
  `exhaustive` linter runs with `default-signifies-exhaustive:
  false`, so a `default` arm does not silence the check.
* **`Val = any`** trades a compile-time check for fidelity to
  java's type-directed model.
* **Immutability is by convention, not enforcement.** Three
  load-bearing disciplines: types are immutable after interning;
  values are immutable after construction (`[]Val` is shared,
  never mutated; `::` copies); environments are never mutated
  (extension allocates).

## The first 50 tasks

Not locked in — tasks will change as we learn. Each task is sized
like a good early morel-rust commit ("Implement `case`, `if`,
negation", "Recursive functions", "`String` structure"): one
sitting, one commit, independently verifiable. Rust's two big-bang
commits — importing every java test at once (`dd0670d1`) and the
whole grammar in one commit (`1ae50309`, the commit where test
files got mutilated to fit the splitter) — are deliberately
unbundled here; the first 80% of each component is much easier
than the last 20%, so the slices below front-load the easy 80%
and make hardening its own task.

Parser, type, and evaluator slices each end by pulling the stable
`.smli` hunks they enable.

### A. Infrastructure (1-8)

- [x] 1. Repo skeleton: license files, CI, golangci-lint,
      `cmd/morel`.
- [x] 2. Project lint test v1: license headers, long lines,
      trailing whitespace, newline at EOF. (Rust touched lint 8
      times in its first 45 commits — invest continuously, keep
      directives extensible.)
- [x] 3. Lexer v1: identifiers, keywords, int/real/string/char
      literals with escapes, symbols, line comments, nested block
      comments; positions/spans.
- [x] 4. Lexer v2: type variables (`'a`), backtick-quoted
      identifiers, `~` negation vs negative literals, scientific
      notation. Go unit tests (internal infrastructure — the
      `.smli` corpus can't test the lexer directly).
- [x] 5. `StatementSplitter` driven by the lexer. Regression tests
      for every case in rust's fix cascade: `;` inside comments
      and strings, `*)` inside `(*)` line comments, `(op *)`,
      nested block comments.
- [x] 6. `Kernel` skeleton (execute(string) -> string; owns
      `Config`, session) + batch `main` reading stdin (rust #2
      equivalent).
- [x] 7. Script harness: run a `.smli` file, echo statements,
      compare `> `-prefixed expected output, idempotency check;
      test list generated from the directory.
- [x] 8. Convergence tooling: port `etc/check-convergence.py`
      (three-way path mapping); divergence report as the project
      dashboard; propagation ledger file.

### B. Parser, in slices (9-19)

- [x] 9. AST scaffolding: node interfaces, spans, ids (pointer
      identity, in Go's case);
      parse-tree rendering framework whose text format matches
      java's `Sys.parseTree` output exactly.
- [x] 10. Micro-evaluator for `Sys.parseTree "..."`: parse an
      application of a dotted name to a string literal, invoke the
      builtin, print `val it = "..." : string`. Establishes the
      curried, span-carrying builtin convention.
- [x] 11. Parse-test scaffold: java's corpus has `Sys.parseTree`
      only in `attribute.smli` (45 uses, attribute-focused), so
      author a small **go-only** `parse.smli` — just a few
      expressions per parser slice — as the temporary test vehicle
      for tasks 12-18. It is scaffolding, not corpus — the one
      sanctioned exception to the tests-originate-upstream rule:
      the divergence report flags it as go-only, and it is deleted
      once real corpus hunks cover the parser and momentum is
      built.
- [x] 12. Expressions I: literals, identifiers, application,
      parentheses, tuples, lists, records, selectors (`#label`,
      `e.field`).
- [x] 13. Expressions II: infix/prefix operators with java's
      precedence table (`+ - * / div mod`, comparisons, `::`, `@`,
      `^`, `o`, `andalso`, `orelse`, unary `~`).
- [x] 14. Expressions III: `if`/`then`/`else`, `fn`, `case` with
      match rules; patterns I (wildcard, literal, id, tuple,
      record, `::`, layered `as`).
- [x] 15. Declarations I: `val`, `val rec`, `fun` (multi-clause,
      multi-parameter), `let ... in ... end`.
- [x] 16. Declarations II: `datatype`, `type` aliases; type
      expressions (tyvars, arrow/tuple/record/list types, type
      annotations).
- [x] 17. Queries I: `from` with `in`/`=`/unbounded scans,
      `where`, `yield`, `join ... on`.
- [x] 18. Queries II: `group`/`compute` (`over` syntax), `order`,
      `distinct`, `skip`/`take`, `union`/`intersect`/`except`,
      `into`/`through`, `exists`/`forall`/`require`, `current`,
      `ordinal`.
- [x] 19. Parser hardening: error messages and spans must match
      the `.smli` corpus exactly, whether or not the java text is
      JavaCC-shaped ("Encountered ...", expected-token lists);
      precedence/associativity corners; the last 20%.
      **Milestone: pulled parse-tree hunks pass.**

### C. Type inference, in slices (20-28)

- [x] 20. Type representations: `TypeSystem` registry, primitives,
      tuple/record/fn/list types, type variables; type-from-string
      parser (bootstraps builtin signatures).
- [x] 21. Unifier: Martelli-Montanari port with occurs check and
      action hooks. Constraint-hook interfaces (overloads,
      default-to-int, collection kind) designed now, even though
      their first users come later. Go unit tests.
- [x] 22. Core IR + `Resolver` skeleton (Ast -> Core from day 1);
      TypeResolver slice I: literals, ids, application, `if`,
      `fn`, `let val`; the `it` binding.
- [ ] 23. `:t` support in the harness; type printing in java's
      exact format (`'a` naming order, parenthesization). Pull the
      first `:t` hunks of `type-inference.smli`.
- [ ] 24. TypeResolver slice II: tuples, records, selectors —
      record typing via unifier actions, java's colon encoding
      verbatim (rust's backtick deviation was reverted).
- [ ] 25. TypeResolver slice III: `case`/patterns; `fun`/`val
      rec`; generalization at `let`; top-level `forall` for free
      tyvars.
- [ ] 26. TypeResolver slice IV: monomorphic datatypes,
      constructor patterns. (Match-coverage analysis is NOT here —
      later, with morel#55.)
- [ ] 27. `.sig` subset parser; builtin types loaded from
      `lib/*.sig` (types only — no implementations needed for
      `:t`); signature checker.
- [ ] 28. Pull stable `type.smli`/`type-inference.smli` hunks
      broadly. (`type-inference.smli` has 230 `:t` statements;
      skip for now the hunks whose expected output includes
      match-coverage warnings — that analysis arrives with
      morel#55, after task 50.)
      **Milestone: pulled `:t` hunks pass — type inference tested
      without evaluation.**

### D. Evaluator, in slices (29-40)

- [ ] 29. `Val` representation — constructors carry (datatype,
      ordinal) from day 1; span-carrying `MorelError`; curried,
      partial-application-safe builtin convention (generalizing
      task 10).
- [ ] 30. `Code`/`Compiler` slice I: literals, vars, builtin
      application; slot-indexed `Frame`; `val it = ...` statement
      output.
- [ ] 31. Wadler-Leijen printing engine (morel#398, pulled
      forward): primitive/list/tuple/record values, wrapping,
      real formatting (compute in float64 — rust#44), so the
      first printed value already matches java's present-day
      format.
- [ ] 32. `case`, `if`, `~`; pattern binding through a single
      walk-all-pattern-names utility (rust's capture bugs came
      from three ad-hoc versions of this).
- [ ] 33. Non-recursive functions: `fn`, multi-clause matches,
      closures.
- [ ] 34. Recursive functions: session-owned link table
      (top-level) + lexical capture (let-rec); frames shaped for
      trampolining.
- [ ] 35. `fun` multi-parameter currying; `val` patterns;
      `let`/sequential declarations; `it` semantics (assigned only
      on success).
- [ ] 36. List/string operators: `nil`, `::`, `@`, `^`; equality
      and comparison via type-directed comparators.
- [ ] 37. Exceptions: raising built-in exceptions (`Div`, `Bind`,
      `Subscript`, ...) with java's report format. (User-defined
      `exception`/`handle` stay out of scope, as in java/rust.)
- [ ] 38. Datatype values: constructor application, `case`
      dispatch on constructors; `SOME`/`NONE` pattern matching.
- [ ] 39. Pull stable hunks: `simple.smli`, `closure.smli`, basic
      `match.smli`, monomorphic `datatype.smli`.
- [ ] 40. Cross-statement correctness pass (rust's largest bug
      farm, worth its own task): closures stored in `it`/`val`s
      and applied in later statements; escaping lambdas calling
      sibling recursive functions; recursive functions called
      from later statements.
      **Milestone: core-language hunks pass end-to-end.**

### E. Standard library, load-bearing first (41-46)

Most functions in most structures are not load-bearing: implement
what later hunks depend on first; fill in breadth early, late, or
opportunistically.

- [ ] 41. Builtin registry mechanics: structure-as-record values,
      one table mapping builtin -> implementation, alphabetical
      order enforced by lint.
- [ ] 42. `General`, `Bool`, `Order`, `Option`.
- [ ] 43. `Int`, `Real`, `Math` (float computed in float64).
- [ ] 44. `String`, `Char`.
- [ ] 45. `List` — the heavily-used core: `map`, `filter`, `foldl`,
      `foldr`, `length`, `rev`, `hd`, `tl`, `nth`, ...
- [ ] 46. `Sys`: `env`, `set`/`show`/`unset`; properties
      `lineWidth`, `printDepth`, `printLength`, `stringDepth`,
      `output`.
      **Milestone: pulled `built-in.smli` hunks pass for these
      structures.**

### F. Relational, first slices (47-50)

- [ ] 47. `Core.From` + `FromBuilder`; typing for scan (`in`),
      `where`, `yield` (lists first; the collection-kind
      constraint hook from task 21 is ready for bags).
- [ ] 48. `RowSink` push pipeline: scan/where/yield sinks; java's
      binding model verbatim — canonical field-name-ordered rows,
      one implicit-label helper, `current` as a rewrite, one
      shared row read/write helper (rust's row-layout bugs took a
      15-commit tail).
- [ ] 49. Joins: comma joins and `join ... on`; nested and
      dependent scans; pattern scans.
- [ ] 50. `group`/`compute` with the basic aggregates (`count`,
      `sum`, `min`, `max`); output matcher + `matchStrict`
      (morel#334) for unordered results.
      **Milestone: first `relational.smli` hunks pass.**

### Beyond task 50 (to the endpoint)

In rough order: `order`/`distinct`/`take`/`skip` evaluation;
set-op steps; quantifiers; `Bag`/`Vector`/`ListPair`/`Either`
structures; in-heap scott dataset (morel#255); ordered/unordered
queries + aggregate adaptation (morel#273, #271, #282, #328); full
`over`/`inst` overloading surface (morel#237); polymorphic
datatypes (morel#70, #205); cross-unit inlining (morel#223, #330);
`Range`, `Variant`, `Time`, `Date` structures (morel#338, #324,
#351, #278, #352); surface conveniences — postfix method calls,
`op` sections, unparser, tabular output, `-e`/`--eval`, `use`
(morel#346, #311, #293, #259, #333, #198); match-coverage analysis
(morel#55); predicate inversion (morel#217).
**Endpoint milestone: parity with morel-rust `e0779b86`.**

## After the endpoint

Fast-follows, in rough order: TCO (morel#151 — frames are already
shaped for it), `raise` (#364), Word (#396), PP (#398), outer joins
(#75), `yieldAll` (#257), `?.` (#378), file reader (#209), FBBT
(#373), Datalog (#323), interactive shell (line editing, history,
highlighting). Calcite/hybrid remains deferred indefinitely.
