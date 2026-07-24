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
(#209), FBBT (#373), outer joins (#75), `yieldAll`
(#257), `?.` (#378), binder syntax (#387), the interactive-shell
polish phase (#45, morel#413/#414 — line editing, history,
highlighting; the basic terminal shell itself comes early, tasks
46a-46c), and everything Calcite (dual.smli
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

### Corpus regeneration with `etc/pull-passing.py`

Once the tool exists, the corpus is no longer grown by hand (steps
1-2 above); instead `etc/pull-passing.py` is run after each commit
that adds functionality. It starts from morel-java's copy of each
`.smli` file and drops every statement morel-go cannot reproduce,
to a fixed point; because it only deletes whole statements, the
result is an ordered subsequence of java that round-trips through
morel-go. A commit's `.smli` additions therefore reflect precisely
the functionality that commit enabled.

Operating rules:

- Run `etc/pull-passing.py --apply` as part of each commit that
  adds functionality, folding the regenerated `.smli` files into
  that commit. (They cannot be a separate later commit: a commit
  that changes existing output — type wrapping, string escaping —
  would otherwise leave the old corpus failing to round-trip, so
  the corpus must move with the code that changed it.)
- A file morel-go crashes on — e.g. `tail-recursion.smli`, whose
  unbounded recursion overflows the stack — is skipped and left as
  it is; the crash is a signal for a future task, not a blocker.
- A java file go does not have yet is created only when enough of
  it passes (`--min-pass`, `--min-statements`); the rest are
  deferred and reported with their pass rate.
- Because each run regenerates whole files rather than applying
  diffs, successive runs never conflict: coverage grows
  monotonically as functionality lands, and updating a section
  (dropping lines and adding others) is rare.

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
- [x] 23. `:t` support in the harness; type printing in java's
      exact format (`'a` naming order, parenthesization). Pull the
      first `:t` hunks of `type-inference.smli`.
- [x] 24. TypeResolver slice II: tuples, records, selectors —
      record typing via unifier actions, java's colon encoding
      verbatim (rust's backtick deviation was reverted).
- [x] 25. TypeResolver slice III: `case`/patterns; `fun`/`val
      rec`; top-level generalization for free tyvars (like java,
      `let` is monomorphic; confirmed by probe).
- [x] 26. TypeResolver slice IV: monomorphic datatypes,
      constructor patterns. (Match-coverage analysis is NOT here —
      later, with morel#55.)
- [x] 27. `.sig` subset parser; builtin types loaded from
      `lib/*.sig` (types only — no implementations needed for
      `:t`); signature checker.
- [x] 28. Pull stable `type.smli`/`type-inference.smli` hunks
      broadly. (`type-inference.smli` has 230 `:t` statements;
      skip for now the hunks whose expected output includes
      match-coverage warnings — that analysis arrives with
      morel#55, after task 50.)
      **Milestone: pulled `:t` hunks pass — type inference tested
      without evaluation.**

### D. Evaluator, in slices (29-40)

- [x] 29. `Val` representation — constructors carry (datatype,
      ordinal) from day 1; span-carrying `MorelError`; curried,
      partial-application-safe builtin convention (generalizing
      task 10).
- [x] 30. `Code`/`Compiler` slice I: literals, vars, builtin
      application; slot-indexed `Frame`; `val it = ...` statement
      output.
- [x] 31. Wadler-Leijen printing engine (morel#398, pulled
      forward): primitive/list/tuple/record values, wrapping,
      real formatting (compute in float64 — rust#44), so the
      first printed value already matches java's present-day
      format.
- [x] 32. `case`, `if`, `~`; pattern binding through a single
      walk-all-pattern-names utility (rust's capture bugs came
      from three ad-hoc versions of this).
- [x] 33. Non-recursive functions: `fn`, multi-clause matches,
      closures.
- [x] 34. Recursive functions: recursion cells make references
      correct however deeply captured, covering both top-level
      and let-rec (no link table needed until `over`/`inst`);
      frames shaped for trampolining.
- [x] 35. `fun` multi-parameter currying; `val` patterns;
      `let`/sequential declarations; `it` semantics (assigned only
      on success).
- [x] 36. List/string operators: `nil`, `::`, `@`, `^`; equality
      and comparison via type-directed comparators.
- [x] 37. Exceptions: raising built-in exceptions (`Div`, `Bind`,
      `Subscript`, ...) with java's report format. (User-defined
      `exception`/`handle` stay out of scope, as in java/rust.)
- [x] 38. Datatype values: constructor application, `case`
      dispatch on constructors; `SOME`/`NONE` pattern matching.
- [x] 39. Pull stable hunks: `simple.smli`, `closure.smli`, basic
      `match.smli`, monomorphic `datatype.smli`.
- [x] 40. Cross-statement correctness pass (rust's largest bug
      farm, worth its own task): closures stored in `it`/`val`s
      and applied in later statements; escaping lambdas calling
      sibling recursive functions; recursive functions called
      from later statements.
      **Milestone: core-language hunks pass end-to-end.**

### E. Standard library, load-bearing first (41-46)

Most functions in most structures are not load-bearing: implement
what later hunks depend on first; fill in breadth early, late, or
opportunistically.

- [x] 41. Builtin registry mechanics: structure-as-record values,
      one table mapping builtin -> implementation, alphabetical
      order enforced by lint.
- [x] 42. `General`, `Bool`, `Order`, `Option`.
- [x] 43. `Int`, `Real`, `Math` (float computed in float64).

### Retrospective after completing task 43

Reviewed the full commit log; every commit verified
fullMake-green by `git rebase -x`.

What should have come earlier:

- **Recursion design (task 34, rewritten in task 40).** The one
  real rework. Task 34 patched captures after closure creation;
  task 40 found the patch cannot reach a closure created inside
  the init expression and replaced it with indirection cells.
  Root cause: the design-table row "assign the field after
  construction" presumed value capture, but java's closures
  capture an environment. Lesson: design-table rows deserve the
  same probe-first skepticism as output formats.
- **Error framing (deferred at 28, trivial at 39).** Deferred as
  "probe-heavy"; turned out to be ~25 lines once corpus diffs
  revealed java's convention (first token's line renumbers to 1,
  columns keep), and was the largest single corpus unlock.
- **Parser member-name gap (hid in task 19's failure tail).**
  `Int.+`, `Int.div`, `Option.join` cannot be parsed (symbolic
  and keyword names after `.`). Surfaced as unwritable tests in
  tasks 41-43 and now gates `built-in.smli` and the numeric
  hunks — task 43 added ~70 built-ins but moved the corpus only
  4 lines. Now task 43a.
- **`checkNumericOperators` (deferred at 27).** Its absence let
  `true + true` panic the evaluator during the task-39 pull; the
  recover() net contains it, but the corpus error hunks need the
  real check. Now task 43b.

Deferrals that were right: the session link table (34 — patched
closures sufficed; plan amended), let-generalization (25 — java
is monomorphic at let; plan amended), TCO (application is a
single choke point; `tail-recursion.smli` waits), type-doc
wrapping (31), the provisional printer in 30.

Since the branch has not been pushed, the findings are being
reworked into their proper places in the history rather than
landing as late fix-ups. Each rework is one task: rewrite with
`git rebase`, verify every replayed commit fullMake-green with
`-x fullMake`, and reword any later commit message that the move
makes stale. Tasks inserted before continuing:

- [x] 43a. Rework history: fold the recursion-cell commit
      (task 40, "Make recursive references correct however
      deeply captured") into task 34's "Evaluate recursive
      functions", so LetRec is born with cells and the patching
      mechanism never existed. Task 40's commit keeps the
      cross-statement stress tests. Re-amend task 34's plan
      wording.
- [x] 43b. Rework history: move the compile-error framing and
      position normalization out of task 39's corpus-pull commit
      into task 37's "Report runtime exceptions in java's
      format", which becomes "report runtime and compile errors".
      Task 39's commit keeps the corpus pull and the recover()
      net.
- [x] 43c. Parser: probing java showed the member-name gap does
      not exist. Java itself rejects symbolic and keyword names
      after `.` unless backtick-quoted; the corpus writes them
      quoted, and go parses those identically to java's
      parse-tree dumps (verified back to task 41, whose commit
      message wrongly noted the gap and was reworded). What
      gates `built-in.smli` and the numeric hunks is `op`
      sections (morel#311) and `over`/`inst` overloading
      (morel#237), both already scheduled. No parser commit, no
      corpus re-pull.
- [x] 43d. Port java's `checkNumericOperators` (post-unification
      check that `+`-family operands are numeric); insert the
      commit after task 27's operator commit, so the evaluator
      phase never sees ill-typed arithmetic. Pull the
      `true + true`-class error hunks at the tip.
- [x] 43e. Pull the `type-inference.smli` error hunks that the
      error framing supports; first probe java's `:t`
      column-offset quirk (the `(*TYPE_ONLY*)` marker shifts
      line-1 columns by 10).

- [x] 43f. Port java's `checkNoUnresolvedFieldRefs` (the other
      post-unification check, sibling to 43d): a field reference
      `#f x` whose argument type is still a variable is an
      unresolved flex record; a non-record argument gives
      "reference to field f of non-record type ..."; a record or
      tuple lacking the field gives "no field 'f' in type '...'".
      The first two report the argument's span, the last the
      selector's, matching java. Replaces go's flat "no field f"
      (which fired later, during ast→core). Unblocks the
      field-error hunks in `simple.smli` and `type-inference.smli`.

- [x] 44. `String`, `Char`.
- [x] 45. `List` — the heavily-used core: `map`, `filter`, `foldl`,
      `foldr`, `length`, `rev`, `hd`, `tl`, `nth`, ...
- [x] 46. `Sys`: `env`, `set`/`show`/`unset`; properties
      `lineWidth`, `printDepth`, `printLength`, `stringDepth`,
      `output`.
      **Milestone: pulled `built-in.smli` hunks pass for these
      structures.**

### E2. Shell (46a-46c)

The `morel` command takes on java's command-line surface — the
same commands, options, and help text — so scripts and muscle
memory transfer between the ports. `Kernel`, `ScriptRunner`,
and `StatementSplitter` already exist as separate units (a
day-1 rule), so these tasks only add the terminal front end.
Still out of scope: the `darn-*` commands (documentation
cells), `--foreign` (no foreign datasets yet), `--maxUseDepth`
(needs `use`), `--system=false`, and `--color-scheme`
(highlighting arrives with the post-endpoint shell polish).

- [x] 46a. CLI arguments: java's surface — `execute` (the
      default command), `-h`/`--help` printing usage, `-e
      <expr>`/`--eval <expr>`/`--eval=<expr>` (evaluate and
      exit), `--echo`, `--idempotent` (implicit when the first
      file ends in `.smli`), `--directory=DIR`, `--banner=false`,
      `--terminal=dumb`, and file arguments with `-` for stdin;
      `--build`/`--no-build` are accepted no-ops (there is
      nothing to build). Two deviations from java, both faithful
      to it or better: an unrecognized flag is ignored, as java's
      Shell does, so the out-of-scope options (`--foreign`,
      `--color-scheme`, `--maxUseDepth`, `--system`) are
      tolerated rather than fatal; and a missing file reports a
      clean error rather than java's uncaught stack trace.
- [x] 46b. Script execution: `.sml` files and stdin run as
      batches (today's only mode, kept); `.smli` files run
      through the same `RunScript` that the test harness uses,
      idempotent-format output to stdout. One code path for the
      harness and the CLI. `--echo` is accepted but inert:
      java uses it to force output to stdout when it would
      otherwise rewrite the script in place, and morel-go always
      writes to stdout and never rewrites files.
- [x] 46c. Interactive shell: a REPL when stdin is a terminal —
      a banner in java's shape (suppressed by `--banner=false`),
      SML-NJ prompts ("- " primary, "= " continuation) driven
      by the statement splitter, each statement's result printed
      as it completes, ctrl-D ends the session. Plain line
      reading only; editing, history (`~/.morel/history`), and
      highlighting are post-endpoint polish.
- [x] 46d. Lint: port java's testStructureScripts — every
      structure in the model (a `lib/*.sig` file) must have a
      `testdata/script/built-in/<name>.smli` test script. (The
      companion guarantee, that every `.smli` file runs, holds
      by construction: the harness walks the directory.) The
      check and the initial scripts enter with task 41 —
      structures become record values there, so bare member
      references print their types even without implementations.
      Each file holds the largest subset of java's present-day
      built-in file that passes at its commit's tree (run the
      file, keep only the lines that work); every structure task,
      42 through 46, grows its own files as it implements their
      members, and the files converge to java's coverage by
      task 46. `int`/`real`/`sys` finish at task 46, since their
      `output`/`timeZone` sections need the real `Sys`.
      `order.smli`, which no sig demands, comes along as a bonus.
      Verify every replayed commit fullMake-green.
      **Milestone: 28 built-in structure scripts pass; the
      check gates every structure hereafter.**

### F. Standard-library breadth (51-75)

Complete the standard library one structure at a time, in order
of importance. A structure is **complete** when its
`built-in/<name>.smli`, regenerated by `etc/pull-passing.py`,
matches morel-java's — save for two features that are deferred
and then added once, across every file, at the end of the phase
(tasks 74-75): `Sys.plan` output (stubbed) and postfix method
calls (`x.f a`, morel#346). A structure's commit is titled
"`X` structure", and its message states the truth plainly — e.g.
"String structure is complete, and matches the Standard ML Basis
except for postfix method calls." Complete means the *only*
remaining divergence from java is those two kinds; anything else
— a missing member, or a missing `op` section — is not done.
`op` sections come first (task 53), before any structure, so a
structure's `op` tests are part of its completeness.

Each structure's functions live in a snake_case file named for
the structure — `list_pair.go` for `ListPair`, `string_cvt.go`
for `StringCvt` — following the `str.go`/`real.go` precedent.

The load-bearing structures — `String`, `List`, `Int`, `Real`,
`Math`, `Option`, `Bool`, `Char`, `General`, `Vector` — are
already implemented (tasks 42-46, 52); completing them closes the
last member and printing gaps and asserts the match, so their
commits are small. `Int`, `Real`, and `Math` reach `fmt` through
`StringCvt`'s radix, so a minimal `StringCvt` precedes them, and
`Word` follows them as the fourth primitive-backed structure.
`Bag`'s List-like surface lands here as `Vector`'s did (reusing
the List code); only its query typing (morel#273) waits for phase
G. This phase runs before the query slices (G).

- [x] 51. Printing (the convergence lever, not a structure):
      types now render through the `Doc` engine like values, so
      long record/function/tuple/collection types wrap at
      `lineWidth` (ported from java's `Pretty.typeDoc`).
      Record *values* already wrapped (task 31). The single
      biggest convergence step: the built-in scripts grew ~1050
      lines — `real` 373→1048, `math` 50→322, plus char, date,
      option, string, relational, datalog, string-cvt. Deferred:
      showing an empty-record *value* as `{}` rather than `()`
      (only `Sys.file` needs it, and go interns `{}` as unit, so
      it wants a small type-tag change) — `sys`'s remaining delta
      is mostly `Sys.plan`/`showAll`, not this.
- [x] 52. `Vector` — the `'a vector` value type; `fromList`,
      `tabulate`, `sub`, `update`, `length`, `concat`, `map`/
      `mapi`, `foldl`/`foldr`/`foldli`/`foldri`, `app`/`appi`,
      `find`/`findi`, `exists`, `all`, `collate`, `maxLen`.
      Values print as `#[...]`; the element-wise members reuse
      the List code. `vector.smli` grew 109→174. Deferred (each a
      broader gap, not Vector-specific): the `Vector;` structure
      dump (keyword-quoted record labels + cross-field type-var
      numbering), the `op +` first-class-operator foldl, and the
      `vector` top-level shorthand (sig-typed alias binding).
- [x] 53. `op` sections — an operator used as a first-class
      value: `(op @)`, `op +` passed to a fold, `List.foldl
      (op +)`. First, before any structure, so a structure's `op`
      tests count toward its completeness.
- [x] 54. `String` structure — complete the built-in file: the
      last members and printed forms that java's tests exercise.
- [x] 55. `List` structure — complete.
- [x] 56. `Bag` structure — implement the `'a bag` value type and
      its List-like surface (`nil`, `null`, `fromList`, `toList`,
      `length`, `hd`, `tl`, `getItem`, `take`, `drop`, `concat`,
      `app`, `map`, `mapPartial`, `find`, `filter`, `partition`,
      `fold`, `exists`, `all`, `tabulate`, `nth`, `only`) by
      reusing the List code, as `Vector` did, printing bag values
      in java's syntax. The bag *typing* for queries (morel#273)
      waits for phase G, but the structure itself is complete now.
- [x] 57. `StringCvt` structure — enough to unblock `fmt`: the
      `radix` datatype (`BIN`/`OCT`/`DEC`/`HEX`) and `padLeft`/
      `padRight`. The scanner-based members (`scanString`, the
      reader combinators) wait for reader types, noted in the
      commit as the exception.
- [x] 58. `Int` structure — complete; `Int.fmt` uses task 57's
      radix.
- [x] 59. `Real` structure — complete; `Real.fmt` likewise.
      (morel-java's `floor`/`ceil`/`round` bugs — wrong results on
      negatives, and saturating on infinity/NaN where SML/NJ raises
      `Overflow`/`Domain` — were both found while doing this task and
      fixed upstream; morel-go matched SML/NJ throughout, and those
      rows now reproduce.)
- [x] 60. `Math` structure — complete.
- [x] 61. `Word` structure (morel#396) — the `word` type over an
      unsigned 64-bit value (`wordSize` is 64, per morel-java, not
      the 32 assumed here; morel-rust has no Word), `0w`/`0wx`
      literals, and the members (`fromInt`/`toInt`, the bitwise and
      arithmetic operators, `fmt` via task 57's radix, `compare`);
      the fourth primitive-backed structure, after Int/Real/Math.
- [x] 62. `Option` structure — complete.
- [x] 63. `Bool` structure — complete.
- [x] 64. `Char` structure — complete (the last unimplemented
      members, `toCString`/`fromCString`, are now in). One block of
      `built-in/char.smli` remains unpulled beyond the postfix
      hunks: `testChar`, which exercises the classification
      predicates through a `group`/`compute`/`order` query, so it
      waits for the phase-H relational features.
- [x] 65. `General` structure — complete, save for `exnName` and
      `exnMessage`: those need first-class `exn` values (the built-in
      exceptions and `Fail` bound as values), a piece of the
      exception system that `raise` (morel#364) also needs, so they
      wait for that feature rather than being built piecemeal here.
- [x] 66. `Vector` structure — complete (finish task 52: the
      `Vector;` dump now prints, given task 51's label quoting and
      contiguous type-variable numbering).
- [x] 67. `ListPair` structure — `zip`, `unzip`, `map`, `app`,
      `all`, `exists`, `foldl`/`foldr`, and the `*Eq` variants
      that raise `UnequalLengths`.
- [x] 68. `Either` structure — the `('a, 'b) either` datatype
      (`INL`/`INR`); `isLeft`/`isRight`, `asLeft`/`asRight`,
      `map`/`mapLeft`/`mapRight`, `app`/`appLeft`/`appRight`,
      `fold`, `proj`, `partition`.
- [x] 69. `Fn` structure — combinators `id`, `const`, `apply`,
      `o`, `curry`, `uncurry`, `flip`, `repeat`, `equal`,
      `notEqual`.
- [x] 70. `Range` structure — range values (morel#338);
      `contains`, `toList`/`toBag`, `discreteSetOf`/
      `continuousSetOf`, `flatten`, `ranges`, `complement`.
- [x] 71. `Variant` structure — variant values (morel#324);
      `parse`, `print`.
- [x] 72. `Date` structure — the `date` type over Go's `time`
      (morel#278); `date`, `year`/`month`/…/`yearDay`, `compare`,
      `toString`/`fromString`, `fmt`, `toTime`/`fromTime*`. The
      corpus fixes `now` and `timeZone` via `Sys.set`, so tests
      are deterministic.
- [x] 73. `Time` structure — the `time` type (morel#351, #352);
      `fromReal`/`toReal`, `to`/`fromSeconds`…`Nanoseconds`,
      `compare`, `now`, `fmt`, `toString`/`fromString`,
      `zeroTime`.

Two features were deferred by every structure above; each closes
the last of the per-structure divergence in one pass over every
file, so it is done once, here, at the end:

- [x] 74. Postfix method calls (morel#346): `x.f a` desugars to
      the qualified call the receiver's type names (`5.abs ()` is
      `Int.abs 5`, `[1].hd ()` is `List.hd [1]`). A
      `pull-passing.py` run then adds the postfix hunks, absent
      until now, across every structure file.
- [x] 75. `Sys.plan`: emit java's compiled-plan string. A
      `pull-passing.py` run adds the plan hunks across every file
      at once. The expression plans reproduce now — applications
      (`apply`/`apply2`/`apply3` with `fnValue` names), constants,
      and tuples — pulling ~145 lines across the built-in
      structure files. Function-body plans (`match`,
      `stack(offset ...)`), relational plans (`from`/`sink`), and
      optimized plans need features from later phases (H, J-K), so
      those hunks stay unpulled until then; a `pull-passing.py`
      run at each of those milestones folds in the ones it
      unlocks. Since matching is best-effort, no plan line is
      commented out — an unreproducible one is simply absent.

The remaining skeletons stay out of this phase: `datalog`
(morel#323) and `pp` (morel#398 user surface) are post-endpoint
(`word` is now task 61); `relational` is the query engine
(phases G-K); `ieee-real`, `int-inf`, and `interact` are
already at java's coverage (their java files are type-references
only).

### F2. Corpus housekeeping (75b)

Findings from a 2026-07-18 review of the corpus workflow:

- [x] 75b. Run `pull-passing.py --apply` and fold in the two
      top-level files that crossed the creation threshold
      when tasks 70-71 landed but were never generated:
      variant.smli (66/87 statements pass) and range.smli
      (63/147).

### G. Query typing on the orderedness atom (47, 76-82)

Runs after phase F. The `Bag` structure is already complete
(task 56); queries learn to carry orderedness here. Draft
tasks 48-50 of the old phase G are superseded by tasks 83-91.

What the histories teach (morel-rust's commit log; the git
archaeology of morel-java's `TypeResolver`):

- rust's worst fix-cascade — ~15 commits over seven months,
  30+ test sections disabled, aggregate adaptation not
  complete until two months *after* the endpoint commit —
  traces to building group/compute/set-op evaluation
  list-only and retrofitting orderedness (morel#273),
  overloading (#237), and aggregate adaptation (#271)
  afterward. Here, typing carries orderedness before any
  query evaluation exists, and group/compute evaluation
  waits until the signatures and scoping rules are final.
- java replaced its own first representation — distinct
  `list`/`bag` unifier terms reconciled by special-case
  helpers (`mayBeBagOrList`, `isListIfAllAreLists`, magic
  `collectionKind` integers, a `CollectionType` enum) — with
  the orderedness atom (morel#407, java commit 744744b8,
  2026-07-17): a collection is a single internal term
  `$collection(elem, o)`, where `o` is the atom `ordered`
  (a list), the atom `unordered` (a bag), or a free
  variable. "Same orderedness" is then a shared variable
  (plain unification, no constraint); the list/bag meet is
  one 2x2 constraint table; and unconstrained orderedness
  decodes as `bag`. morel-go ports the final design directly
  and never builds the helper family. (#407 is a day old:
  watch java for follow-up fixes before freezing details.)
- The per-step orderedness rules, for reference:

  | Step | Orderedness |
  | --- | --- |
  | empty `from` | ordered (`unit list`) |
  | first scan, `=` scan, `yield`, `group` | same as input |
  | join / extra scan, set ops | list iff all inputs are |
  | `order` | list |
  | `unorder`, sourceless scan | bag |
  | `where`, `distinct`, `skip`, `take` | passthrough |
  | `compute`, `into` | scalar (no collection) |
  | unconstrained | bag, at decode |

- Dead ends skipped: the `suchthat` keyword ([MOREL-129],
  removed [MOREL-202]); the `desc` keyword (#244, #282);
  dual list/bag overloads of the built-in aggregates
  (removed by #271 — bag-parameter signatures plus
  adaptation from day one); the pre-#407 helper family; the
  #258 "skip/take with unbounded variable" error (superseded
  by #217, under which `take 3` over an infinite extent
  works).
- java's `bag.smli` (321 lines) is the orderedness spec in
  miniature; its `:t` hunks grow with every task below. As
  in phase C, `:t` tests the whole phase without evaluation.

- [x] 47. Typing for scan (`in`, `=`), `where`, `yield` over
      lists: a query's rows carry named fields, the element type
      is the sole field or a record of them, `yield` of a record
      literal exposes its fields. The list-scan `:t` hunks pass.
      (`Core.From` + `FromBuilder` land with the evaluator in
      task 85, rather than as an unused IR node now — no
      speculative infrastructure. The collection-kind constraint
      hook from task 21 is ready for bags.)
- [x] 76. Orderedness in the unifier: `ordered`/`unordered`
      atoms, the `$collection(elem, o)` term, and the meet
      primitive `meetOrderedness` (the 2x2 constraint table);
      unconstrained orderedness decodes as `bag`; conflicts
      render in the internal form `list(T) vs bag(T)` (the exact
      form java's unifier prints). Task 21's constraint hooks
      already do fire-when-one-remains pruning, so the meet rides
      on them unchanged. from-typing rebuilt on the new term:
      empty `from` is `unit list`; the first scan shares its
      source's orderedness (`from x in aBag` is a bag); a comma
      scan meets (list iff both are lists); `where`/`yield` pass
      through. The other three primitives — `isCollectionOf`,
      `sameOrderedness`, and `meetCollections` — are deferred to
      the steps that first use them (join, set ops, group,
      through: tasks 78-82), rather than landing as unused code.
      The pull is the collection-typing `:t` hunks of
      type-inference.smli (+22); java writes bag.smli as
      full-eval, so its own hunks wait for query evaluation
      (phase H).
- [x] 77. The query row model, ported from java: the
      rootEnv/stepEnv/element/collection step state
      (java's `Triple`) plus ordered named fields
      (`fieldVars`) with the 0/1/n collapse rule (unit / the
      sole field / a sorted record); scan patterns
      destructure and bind fields; `=` scalar scans; `where`;
      `current` bound in each step's env. Most of this was
      already in place from tasks 47 and 76 (the two-env split,
      the collapse rule, pattern-destructuring scans, scalar
      scans, `where`); the genuinely new piece was binding
      `current` in each step to the current row. The pull is the
      `current` typing hunks of type-inference.smli (+5); uses of
      `current` in `order`/`take`/etc. wait for those steps.
- [x] 78. `yield` typing: a record-literal yield exposes its
      fields to later steps; implicit labels (`yield e.b` is
      field `b`); the singleton-record rule (`yield {x = y}`
      renames rather than wraps); an atom yield becomes a
      single implicitly-labeled field. Record-literal exposure
      was already in place; the yielded element's type is the
      expression's own type (so `{z}` stays `{z:int}`, `(b,a)`
      stays a tuple), which already matched. The new piece was
      exposing a non-record yield's single field, labelled by
      java's implicit-label rule (id name, selector field, or
      "current"), so a later step can name it. Pull: the
      chained-yield hunks of type-inference.smli (+2).
- [x] 79. Join typing: comma scans and `join ... on`
      (`on` is bool); dependent scans; result is a list iff
      all inputs are lists. (Outer joins stay out of scope.)
      Comma scans, dependent scans, and the list-iff-all-lists
      meet (via `meetOrderedness`) were already in place from
      tasks 76-77; the new piece was typing the `on` condition as
      a boolean over the earlier fields and the join scan's
      pattern. Pull: the join `:t` hunks of type-inference.smli
      (+2); most join tests live in relational.smli/bag.smli and
      wait for query evaluation.
- [x] 80. Passthroughs and forcers: `distinct` (no type
      change); `skip`/`take` (counts typed in the root env,
      `int`); `order` (single sort-key expression, forces
      list, warns when a record sort key's fields are not
      alphabetical); `unorder` (forces bag); `ordinal`
      (`int`; post-solve validation that the input is
      ordered); `union`/`intersect`/`except` (arguments typed
      in the root env; n-ary meet). Two niceties deferred as
      they matter only for warning/error hunks that need query
      evaluation or the unparser: the record-sort-key warning
      (needs `order` evaluation and the unparser, task 110) and
      the post-solve "`ordinal` requires an ordered input"
      validation. Pull: the passthrough/forcer `:t` hunks of
      type-inference.smli (+31) and blog.smli (+6).
- [x] 81. `group`/`compute` typing: key and aggregate fields;
      `elements` bound to the input collection in the
      aggregate env; label derivation and duplicate checks;
      the `group` binder; standalone `compute` as a scalar.
      Aggregate linkage via java's `AggKind` classification:
      a known bag- or list-parameter aggregate is decoupled
      from the input's orderedness (adaptation), an
      unknown user function is left free; the POLYMORPHIC
      kind arrives with task 94. Built-in aggregates (`sum`,
      `count`, `min`, `max`, ...) get java's present-day
      bag-parameter signatures from day one. Java combines
      `group X compute Y` into one node; go's parser makes them
      separate steps, so a `group` is typed together with a
      following `compute` (the aggregates over the pre-group
      rows). The bag aggregates `count`/`sum`/`min`/`max`/
      `empty`/`nonEmpty` are top-level; `elements` is bound only
      in a compute clause. Pull: the group/compute `:t` hunks of
      type-inference.smli (+8).
- [x] 82. `exists`/`forall` (bool) and `require` (last step
      of `forall` only); `into` (scalar) and `through`
      (rebinds the row; orderedness from the function's
      result type). `into` reuses the aggregate typing; `through`
      uses the collection machinery; a `forall` whose last step
      is not `require` is an error. Pull: the query-typing hunks
      of type-inference.smli (+38).
      **Milestone: query typing complete, tested without
      evaluation.** The typing tests live in type-inference.smli
      (java writes bag.smli, relational.smli, and such-that.smli
      as full-eval, not `:t`, so those files' own hunks wait for
      query evaluation in phase H — they sit at 18%/7%/6%).

### H. Query evaluation (83-91)

- [x] 83. `OutputMatcher` (morel#334): port java's type-driven
      semantic comparison of expected vs actual output —
      parse both value strings guided by the result
      type, compare bag-typed values as multisets, normalize
      whitespace; `matchStrict` opts out. Wire it into the
      script harness *before* any bag-producing query evaluates:
      rust added this mid-stream in three commits, and
      without it every bag result is a flaky test. The result
      type is parsed from the output's own type suffix, so no
      new plumbing was needed; the harness keeps the expected
      output when it matches actual up to bag order or
      formatting, via an optional Executor capability (so test
      mocks are unaffected). One deliberate deviation from java:
      the prefix before a binding's value must also match, so a
      warning morel-go does not yet emit (match-coverage, task
      98) is not silently swallowed — java is safe there only
      because it always emits the warning. Because the compare
      is structural, it also absorbs formatting differences:
      pulled ~155 lines across simple.smli, type.smli, blog.smli,
      range.smli, and built-in files — including a systematic
      go/java difference in how a long list of records wraps
      (`[` on its own line in java), which a future printer fix
      could make byte-exact.
- [x] 84. In-heap scott dataset (morel#255): the `scott`
      structure (emp/dept/salgrade/bonus) that fuels most of
      relational.smli; pull scott.smli. (scott-queries.smli
      also needs `useSilently`, task 109.) scott is not a
      built-in — scott.smli just binds `val scott = {...}`, an
      in-heap record of bags of records, which morel-go
      evaluates and prints, its bags matched as multisets by
      task 83. It has only 3 statements (below the
      auto-create threshold), so it is created verbatim from
      java, which morel-go now reproduces byte-for-byte. That
      last part needed a printer fix: a bag type had no case in
      the type layout and printed flat, overrunning the line
      width for a long bag-of-records field; it now wraps like a
      list type (a task-51/52 gap).
- [x] 85. `RowSink` push pipeline: scan/where/yield sinks;
      java's binding model verbatim — canonical
      field-name-ordered rows, one implicit-label helper,
      `current` as a rewrite, one shared row read/write
      helper (rust's row-layout bugs took a 15-commit tail).
      Built the whole path a query needs to run: `Core.From`
      with scan/where/yield steps, the resolver lowering
      `ast.From` to it, the compiler building the pipeline over
      frame slots, and an `eval.From` that scans, filters, and
      collects. A row is the scan pattern's bound variables (the
      sole one, or a record of them in label order); with a
      trailing yield the collected value is the yield expression.
      Lists and bags share the `[]Val` representation, so both
      iterate the same way and orderedness comes from the type.
      Scoped to a single `in` scan then `where` filters and an
      optional trailing yield — joins (86), passthrough/forcer
      evaluation (87), set ops (88), group/compute (89),
      quantifiers (90), and `current`/`ordinal`/rebinding yields
      (91) fall through to "not yet convertible" as before. Pull:
      the now-runnable queries across blog.smli (+131),
      type.smli, wordle.smli, misc.smli, range.smli, and
      built-in.smli — 186 fewer divergent lines.
- [x] 86. Join evaluation: comma joins and `join ... on`;
      nested and dependent scans; pattern scans. The pipeline
      runs any number of scans, nested so a later scan sees the
      variables earlier ones bound: a comma join is the cartesian
      product, a dependent scan iterates a per-outer-row source,
      and `join ... on` lowers to a scan then a where. Pull: the
      join queries of blog.smli (+53); relational.smli's joins
      wait for `use`/scott loading (task 109) and group/order
      evaluation.
- [x] 87. `order`/`distinct`/`skip`/`take` evaluation: sort
      via the type-directed comparators (task 36); `DESC`
      sort keys wait for task 93. Reworked the pipeline to a
      snapshot model — each row is a snapshot of the query
      variables' frame slots, restored to evaluate a stage's
      expressions — so stages compose in any order. `order`
      sorts by a key (the comparators extended to compare
      tuples/records lexicographically); `distinct` dedups;
      `skip`/`take` slice, counts evaluated in the root scope.
      Pull: blog.smli (+17) and built-in/relational.smli (+2).
- [x] 88. Set-op evaluation with java's present-day counting
      semantics (morel#321 folded in from the start:
      `intersect`/`except` respect multiplicity; rust shipped
      the naive semantics first and reworked them). A set-op is a
      pipeline stage combining the rows so far with the argument
      collections, matching rows by value: union concatenates
      (distinct dedups), intersect keeps each row at the meet of
      its counts, except is a multiset difference. Pull:
      blog.smli (+22).
- [x] 89. `group`/`compute` evaluation with the built-in
      aggregates (`count`, `sum`, `min`, `max`) and the
      list-input adapters that the bag-parameter signatures
      imply (the runtime half of morel#271); java's
      present-day scoping rules for keys and `compute`
      (settled upstream in the June 2026 fixes rust needed
      after its endpoint). A group step partitions rows by key
      and, per group, writes the key and aggregate values to
      fresh output slots that later steps read (the compiler's
      row-pattern set switches from the scan variables to the
      group's output fields). The aggregates — count, sum, min,
      max, empty, nonEmpty — reduce the group's `[]Val`, so a
      bag-parameter aggregate accepts a list too (no adapter
      needed given go's uniform representation). `group` absorbs
      the following `compute`. Standalone `compute` (a scalar
      result) is not evaluated yet. Pull: blog.smli (+79),
      built-in/relational.smli (+58), built-in.smli (+20),
      simple.smli (+13) — 170 fewer divergent lines.
- [x] 90. Quantifier and terminal steps: `exists`/`forall`
      with short-circuit, `require`, `into`, `through`. An
      `exists` query reduces to whether any row survives, a
      `forall` to whether every row's value (its `require`
      predicate, lowered to a yield) holds. `into f` applies f
      to the whole collection, yielding a scalar; `through pat
      in f` applies f and rebinds pat to each result element (a
      rebinding stage like group). Pull: blog.smli (+10); most
      quantifier/into/through tests live in relational.smli,
      awaiting `current`/scott loading.
- [x] 91. `current`, `ordinal`, `unorder` evaluation
      (morel#265, #276, #277).
      **Milestone: the first big relational.smli pull; the
      `testChar` block deferred at task 64 lands.**

### I. Polymorphism, overloading, aggregates (92-95)

Ordering per the rust retrospective: polymorphic datatypes
before type-based orderings; overloading before aggregate
adaptation is complete.

- [x] 92. Polymorphic datatypes (morel#70, #205):
      `datatype 'a tree = ...`; constructors instantiate at
      use sites; the (datatype, ordinal) value representation
      and type-directed comparators already fit. Big
      convergence step for type.smli and datatype.smli.
- [x] 93. `Descending` datatype and `Relational.compare`
      (morel#282): type-based orderings; `order (DESC e)`
      evaluation via task 87's comparators.
- [x] 94. `over`/`inst` overloading (morel#237): `over`
      declarations, instance registry, constraint-based
      dispatch at application sites (the "A Second Look at
      Overloading" approach); `MultiType` builtins — `elem`/
      `notelem` on bags and lists, `Relational.only`,
      overloaded `abs` (#318). Unblocks the `op` and numeric
      hunks noted at task 43c, and overload.smli's first
      pull.
      Done: `elem`/`notelem`/`only` (via a free-orderedness
      collection type), `abs` (already correct), and local
      (`let`-scoped) `over`/`val inst` type dispatch. `abs`
      needed no change.
- [x] 95. Aggregate adaptation, completed (morel#271):
      the POLYMORPHIC `AggKind` — an overloaded aggregate's
      instance is selected by the input's orderedness; the
      zero-field relation rule (`distinct` vs `group {}`,
      morel#328); `Relational` structure completion
      (built-in/relational.smli). Pull the overload.smli
      aggregate sections.
      Done: aggregates/`only` accept a list or a bag (via a
      free-orderedness collection type) at top level and as
      `Relational` members; the `group {}` zero-field rule.

The remaining overload work — cross-statement (top-level)
`over`/`val inst`, qualified types for an overloaded name used at
an abstract type (the paper's `demo`, morel#237), and
type-directed method dispatch (`(bag [..]).max ()`, `(~3).abs ()`,
postfix.smli) — is blocked on `let`-generalization, which morel-go
lacks. Rather than build it here, propagate it from morel-java
once hydromatic/morel#426 is fixed. See
`etc/issue-overload-let-polymorphism.md`.

### J. Self-contained wins (98, 106, 108, 111-112)

Contained features that converge whole corpus sections without the
predicate-inversion machinery of phase K. Do these next.

- [x] 98. Satisfiability prover + match-coverage analysis
      (morel#55): redundant- and missed-case warnings.
      Budget the prover generously (rust's 315-line first cut
      was rewritten within a month). Unlocks the
      type-inference.smli warning hunks deferred since
      task 28, and most of match.smli.
      Done via Maranget's usefulness algorithm (not a SAT
      prover) over every case/fn/fun, with a targeted "e mod k"
      heuristic; a prerequisite fn-miscompilation bug (a
      constructor-name single-clause pattern bound as a
      variable) was fixed. match.smli +104, type-inference
      +41, datatype +21. A full SAT prover for richer
      arithmetic reasoning can follow if needed.
- [x] 111. `PP` pretty-printer structure (`PP.render`,
      `PP.text`, `PP.line`, ...): built-in/pp.smli, ~19
      statements. Built on the shared internal/pp Wadler-Leijen
      pretty-printer (a single engine, no duplicate); abstract
      (zero-constructor) types now print as `-`. built-in/pp.smli
      121/121.
- [x] 108. Record update: `{e with deptno = 0}` (morel#249);
      used by relational.smli and blog.smli.
      Done: lowered to a let binding the base once, then a record
      built from update expressions and base-field selections;
      typing (unify each update with its base field) was already
      present. blog.smli +25, relational.smli +12, type.smli +14.
- [x] 112. Range-list syntax and finite evaluation:
      `[1 .. 5]`, `[0 ..^ 10, 20]` (morel#372) as list
      constructors that enumerate via task 70's `Range`,
      independent of the generator machinery. Infinite ranges
      in a list (`100..`, `[0..]`) still need phase K.
      Done: new tokens `..`/`..^`/`^..`/`^..^`, ast/core
      `RangeList` nodes, typing that unifies all bounds, and
      finite enumeration over int/char/bool; an unbounded item
      raises Size on enumeration. range.smli +69. Membership in
      an unbounded range (`5 elem [0..]`) awaits phase K.
- [x] 106. Transitive closure builtin: `Relational.iterate`
      (semi-naive fixpoint) as a plain builtin, evaluated
      directly (not inverted). Unlocks the `iterate` uses in
      built-in/relational.smli and much of fixed-point.smli.
      Its generator inversions (transitive-closure and
      bounded-iterate) belong to phase K.
      Done: `iterate init update` runs the semi-naive fixpoint
      (dedup against seen rows terminates cyclic inputs), shared
      by top-level `iterate` (list) and `Relational.iterate`
      (bag). built-in.smli +4; the datalog/regex/fixed-point uses
      need further features (phase K, regex).

### J2. Library structures in Morel (113-115, morel-go#2)

Most built-in structure members are thin wrappers derivable from
a few native primitives. A general `structLib` loader supplies a
structure's members partly natively and partly from an embedded
`lib/<name>.sml`, evaluated at boot with the natives in scope (an
implicit `open`) and wired into the member slots — generalizing
the `loadScott` precedent. Values as well as functions convert
(e.g. `Math.e = exp 1.0`). No corpus convergence; a code-structure
change. See `etc/issue-structlib.md`.

Convert only members that will not become less efficient. The
deciding property is recursion: the task-96 Inliner inlines only
non-recursive bindings, so a non-recursive member is inlined at its
call sites (the call disappears and the body is exposed to
constant-folding, case-of-known-constructor, and predicate
inversion — as fast as native, usually faster), whereas a recursive
or hot member runs through the evaluator instead of a Go loop and
regresses. Pick structures whose members are all small and
non-recursive; leave the recursive/hot ones (`List`/`Vector`
traversals, `Relational` aggregates) native.

- [x] 113. The `structLib` mechanism, and its first two clients,
      chosen as inlining sweet spots (non-recursive throughout):
      `Fn` (`id`/`const`/`o`/`curry`/`uncurry`/`flip`/`apply`/
      `equal`/`notEqual` — pure combinators; recursive `repeat`
      stays native, testing a mixed structure) and `Option`
      (`isSome`/`getOpt`/`valOf`/`map`/`join`/`filter`/
      `mapPartial`/`compose` — datatype case-analysis whose
      inlining unlocks case-of-known-constructor in query
      predicates).
      Done: a boot-time `loadStructLib` evaluates each structure's
      embedded Morel source (`internal/shell/lib/{fn,option}.sml`)
      in a scoped environment and wires the derived members into
      their slots; `id`/`o`/`repeat` and `getOpt`/`isSome`/`valOf`
      stay native. Behaviour-preserving (no corpus delta).
- [x] 114. Make `PP` a `structLib` client (the mechanism now
      exists from task 113): add `PP` to `structLibs()`, move its
      ~15 derived combinators (`hsep`, `vsep`, `sep`, `cat`,
      `fill*`, `parens`/`braces`/`brackets`, `punctuate`,
      `encloseSep`, ...) into `internal/shell/lib/pp.sml` over the
      ~12 native primitives (`empty`, `line`, `text`, `beside`,
      `nest`, `group`, `align`, `fill`, `render`, ...), which are
      seeded unqualified by the implicit open. Shrinks
      `eval/pp.go` to those primitives.
      Done: 15 derived combinators in `internal/shell/lib/pp.sml`
      (`fillSep`/`fillCat` stay native as they need the internal
      `Fill`). Structure records are now built before the
      structLib sources so `pp.sml` can reference `List`, then
      rebuilt to wire in the derived members. Behaviour-preserving;
      built-in/pp.smli stays 121/121.
- [x] 115. Phase 2: convert the remaining clear wins — `Bool`,
      `Either`, `ListPair`, `Math` (folding `e`/`pi` to literals,
      not inlining `exp 1.0`), and the derivable subsets of
      Char/String/Int/Real/Vector/Relational/General. Keep hot
      traversals native (evaluator vs Go loop) and reproduce
      native edge cases (`Int.abs minInt`, `Real.abs nan`,
      `Char.chr` bounds).
      Done the non-regressing clear wins: `Either` fully
      (lib/either.sml, native either.go removed) and `Bool`'s
      `toString`/`fromString`/`andalso`/`orelse`/`implies`
      (lib/bool.sml; `not` and comparisons stay native).
      Behaviour-preserving. Deliberately left native for now (would
      regress or need machinery not yet built): `ListPair` and the
      Char/String/Vector traversals (recursive, not inlinable),
      and `Math` `e`/`pi` (need constant-folding, phase K); the
      Int/Real/Char scalar subsets (`abs`/`min`/`sign`/predicates)
      remain a future increment.

### K0. Sys.plan format parity (116, before phase K)

Match morel-java's `Sys.plan()` output before inlining lands, so
that phase K flips optimize.smli's plans to match rather than
forcing a re-bless. Baseline (via the plan-score harness): of the
216 plans whose query runs, 71 matched java and 145 differed only
in plan format; 60 more need features not yet built. Six format
differences:

- [x] 116a. Type-directed, structure-qualified builtin names
      (`Int.+`, `Real./`, `Int.mod`, `List.map`, `List.@`,
      `General.ignore`; `=`/`<`/`div`/`elem` stay bare). Matches
      71 -> 77; corpus built-in +8, general +4, string +4, list
      +2, sys +2.
- [x] 116b. Collapse curried builtin application to
      `apply2`/`apply3` (java `apply2(fnValue List.app, f, xs)`
      vs go's nested `apply(fnCode apply(...), argCode ...)`).
      Done via a per-builtin curried-arity (arrows in its type);
      the applyCode spine collapses when fully applied. Matches
      77 -> 93; corpus list +7-ish, string, vector.
- [x] 116c. `let1(expCode ..., resultCode ...)` for a single
      non-recursive binding (vs go's `let(..., ...)`). 93 -> 94.
      (Recursive let is java's `let(matchCode0 ..., resultCode
      ...)` — folded into 116e.)
- [x] 116d. `tailApply` in tail position (vs go's `apply`).
      Done: the compiler threads tail-ness (compileTail for a
      closure body, flowing to a let body but not its init or an
      apply argument); a tail single-arg apply renders tailApply.
      94 -> 96; corpus list +4, vector +4.
- [~] 116e. case/match representation: java's
      `match(...)`/`tailApply(fnCode match(...))` vs go's
      `case(...)` (structural). Done: a case renders as
      apply/tailApply(fnCode match(pat, body, ...), argCode
      scrutinee) with per-clause PatDesc and tail-context arm
      bodies; constructors render as `fnValue tyCon`. 96 -> 97.
      Remaining: constructor-body and other code sub-details in
      case-heavy plans.
- [~] 116f. `from` plan shape: java's `sink join`/`sink where`
      vs go's `from(stack(...))` (structural). Structure done:
      `fromCode.Describe` walks the retained stages into the
      nested sink chain (join/where/yield/collect), and selectors
      render as `nth:N`. Remaining: align the inner `exp`/collect
      code rendering so from-plans fully match, and render the
      order/group/set-op sinks.

optimize.smli's inlining-dependent plans (35) wait for phase K
(tasks 96/97); this task makes their vocabulary correct first.
The harness lives at `etc/` scratch (planscore.py).

### K. Unbounded variables, such-that, Datalog (96-97, 99-107)

A large later phase. An unbounded variable
(`from x where x > 3 andalso x < 10`) is grounded at compile time
by inverting the predicates that constrain it, and Datalog
(morel#323) reuses the same machinery. java's final architecture,
ported directly:

- Typing needs no extent knowledge: a sourceless scan gets a
  fresh type variable and forces `bag` (already in task 76's
  rules); everything else happens on typed, *inlined* Core.
- The resolver emits a scan over an internal infinite-extent
  value; a compile-time pass (java's `SuchThatShuttle` →
  `Expander` → `Generators`, from morel#217) accumulates
  `where`/`on` conjuncts step by step and replaces each
  extent with the best *generator* — an efficient inverse of
  a predicate class, carrying cardinality, dependencies
  (`freePats`), uniqueness, and `provenance` (the conjuncts
  it subsumes, which are then deleted). A used pattern with
  no finite generator reports "pattern 'x' is not grounded".
- rust landed morel#217 as one 8.9k-line commit; go splits
  it, and folds java's follow-up fixes (the provenance/
  monotonic-cache refactor, case inversion #341, outer-scope
  filters #347, merged ranges, one-sided ranges, the
  transitive-closure cousin fix) into each piece's first
  landing, so each is born final.
- Out of scope, as before: FBBT (morel#373) — the
  such-that.smli FBBT sections stay unpulled.
- Datalog (morel#323) rides on the same machinery and lands in
  this phase once the generators exist.

- [x] 96. `Analyzer` + `Inliner`: inline non-recursive
      `val`/`fun` bindings into query predicates, with the
      pass-count loop in the compile driver; cross-unit
      inlining (morel#223). A prerequisite for predicate
      inversion — inversion must see through user-defined
      predicates — and for java-matching plans (task 75).
      Done: use analysis (dead/atomic/once-safe), let
      elimination, beta-reduction, singleton-case
      substitution, cross-unit val/fun inlining with
      capture-safe invalidation, rec demotion; plan-score
      98 -> 104 matches (optimize 1 -> 7). Java's limited
      mode at inlinePassCount 0 (cross-unit and constant
      folding still on) is simplified to "no passes";
      revisit with task 97, whose constant-case folding is
      what the corpus's 0-setting guards.
- [x] 97. Constant-`case` inlining (morel#330), so inverted
      and specialized queries simplify to java's plans. Done,
      with java's limited mode at inlinePassCount 0 (folding
      off; cross-unit, beta, singleton-case on). Plan-score
      104 -> 112 matches (optimize 7 -> 15); corpus optimize
      +16, variant +6.
- [x] 99. Extent representation: `RangeExtent` (type plus
      per-path range sets; finite extents materialize),
      internal `Z_EXTENT` builtin (panics if an infinite
      extent reaches evaluation), resolver emits extent scans
      for sourceless `from p` and unorders the query. Finite
      types (bool, unit, char, options over them) evaluate
      end to end: `from b where b orelse not b`. Done, as
      "$.extent" with the full per-path map and finiteness
      recursion (int-with-bounds included, dead until 100/102).
      Plan format matches java (apply(fnValue $.extent,
      argCode constant(bool))). Corpus: optimize char-extent
      line, blog +28; the bool/option/tuple pins live in the
      new backswing.smli until such-that.smli is pullable.
- [x] 100. The `Expander`/`Generators` framework skeleton:
      the generator abstraction (cardinality, freePats,
      unique, sealed, provenance, simplify), the monotonic
      per-pattern cache, conjunct accumulation via
      decomposed `andalso`, the used-pattern grounding check
      ("pattern 'x' is not grounded") and cycle check, the
      shuttle dispatcher gated on containsUnbounded (skipping
      recursive-function bodies). First generators: point
      (`x = e`) and finite-extent. Done: conjuncts decompose
      from the boolCase form (go lowers andalso to a case);
      provenance is pointer identity; simplify and the
      distinct wrap for non-unique generators are deferred to
      the tasks that first need them (103). Corpus: optimize
      +26 (the not-grounded family); point pins in
      backswing.smli; such-that.smli now 48/213 pullable
      (needs 40% to land). FBBT (morel#373) remains a stub
      site in expandFrom, as planned.
- [ ] 101. Collection generator: `x elem coll`; composite
      patterns (`(x, y) elem pairs`) via per-field generators
      rejoined; dependent generators become filtered subquery
      scans with renamed patterns and join conditions;
      `distinct` insertion rules.
- [ ] 102. Range generator: lower/upper bound extraction
      (linear-term helpers; literal bounds pushed into
      infinite-range scans), emitting java's present-day
      `Range`-based shapes — `Bag.fromList (Range.flatten
      [...])`, multi-interval `Range.discreteSetOf`,
      one-sided ranges combined with other constraints. The
      finite range-list syntax is task 112; this task inverts
      the infinite cases.
- [ ] 103. Union generator (`orelse` inverts to a union,
      deduplicated when non-unique) + the `Simplifier` and
      provenance-based cancellation of subsumed `where`
      conjuncts.
- [ ] 104. Exists inversion: `Relational.nonEmpty` of a
      subquery in monotonic position becomes a join or
      semi-join generator; filtering by outer-scope variables
      (morel#347).
- [ ] 105. Function and case inversion: inline a user
      function's body into the constraint set and recurse
      (needs task 96); multi-arm `case` inversion
      (morel#341); the `String.isPrefix` generator.
- [ ] 106b. Transitive-closure generators: the
      transitive-closure and bounded-iterate *inversions* of
      `Relational.iterate` (the builtin lands in task 106,
      phase J), including the tuple and cousin-style variants.
      Unlocks the edges/paths and family sections of
      such-that.smli.
- [ ] 107. `Sys.planEx` (morel#329): re-run compilation of
      the last declaration up to a numbered pass and print
      the Core (builds on task 75's plan printing). Pull the
      plan-assertion hunks — 33 uses in such-that.smli, 38 in
      optimize.smli.

### L. Remaining surface (109-110)

- [ ] 109. `use` and `useSilently` (morel#198), with
      `--maxUseDepth`; unlocks scott-queries.smli.
- [ ] 110. Unparser (morel#41, #293): render AST back to
      source with correct precedence, for warnings and plan
      text. Pull forward earlier (task 80's sort-key warning,
      task 75's plans) if those need more than trivial
      rendering.

**Endpoint milestone: feature parity with morel-rust
`e0779b86`, measured go-style — every remaining line of
`.smli` divergence is attributable to an out-of-scope
feature (Calcite/dual, Datalog, FBBT, exceptions/`raise`,
file reader, outer joins, `yieldAll`, `?.`, binder syntax,
shell polish).** Note that go arrives with java's June-2026
group/compute scoping and set-op semantics built in —
territory where rust at the same milestone still had 33
relational.smli sections disabled.

## Propagate back to morel-java

`.smli` lines where morel-go's formulation is better than
morel-java's and should, at some point, be proposed upstream.

- `built-in/real.smli`: `val nan = abs (Real.posInf /
  Real.posInf)` is host-independent — `abs` forces the positive
  NaN — whereas java's `val nan = Real.posInf / Real.posInf`
  relies on the JVM canonicalizing NaN to positive. morel-go
  inherits the hardware NaN sign (positive on arm64, negative on
  x86-64), so the `abs` form passes on every architecture; the
  bare form is a latent x86-64 failure, which is why morel-go
  keeps the `abs` form.

## Commit-history maintenance

Deferred history surgery: fold or re-order commits so the final
history reads as if each fact were known from the start. Do these
in a quiet moment, replaying with `git rebase -f -x 'fullMake
--no-clean'` so every rewritten commit stays green.

- [ ] H1. Fold the type-variable naming fix into its origin. The
      commit "Name type variables beyond 'z as java does" corrected
      `varName` to number the 27th variable and beyond `'ba`, `'bb`,
      ..., `'zz`, `'baa`, matching java's `TypeVar.name`. Cleave the
      code change (the `varName` rewrite and its `types_test.go`
      cases) from its corpus regenerations and squash it into "Add
      interned type representations and the type-from-string
      bootstrap", which first generated type-variable names, so the
      base-26 scheme is correct from the start. Re-regenerate any
      earlier corpus whose wide dumps this shifts.
- [ ] H2. Strip provenance and process from code comments. Sweep
      the Go source for comments that reference java ("as java
      does", "matching java's ...", "like java"), tasks, bug or
      issue numbers, or anything else ephemeral or about how the
      work was done, and reword them to describe only the behavior.
      Comments should read as if the code had always been this way.
      Prefer folding each edit into the commit that introduced the
      comment.
- [ ] H4. Add `Propagates hydromatic/morel#NNN commit <sha>`
      footers to the two Real propagation commits ("Propagate
      morel-java's Real.floor/ceil/round fix", "Propagate
      morel-java's Real inf/NaN int-conversion fix"), so
      `check-convergence.py --ledger` records them; the ledger
      currently shows zero propagated commits.

## Review: commits since the first mainlining

Reviewed 2026-07-23: the 91 commits a5911d3..b2cac57 (54
substantive, 37 plan-bookkeeping), ahead of the second
history reorganization. (Shas reflect the 2026-07-23
force-push, which amended f7a9384 and the four Sys.plan parity
commits to seat the builtinFnInfo case-sort at its birth;
every commit after f7a9384 was renumbered.) Findings feed the
future ledger; item
numbers R1-R44 are stable references. Two standing constraints
from the first reorganization: commits already published on main
are immutable (a fix whose birth commit is pre-capture becomes a
standalone commit early in the new series, not a squash), and 18
files carry main-only cleanup (comment sweep, corpus trims) that
will conflict with new commits on rebase.

### Bugs to fix before surgery

Fix these as ordinary commits on 1-bootstrap now; the
reorganization then folds each into its birth commit. Confirmed
wrong observable behavior:

- [x] R1. Range enumeration loops forever when the closed upper
      bound is Int.maxInt: succVal wraps int32 and the loop never
      exits. `[2147483646 .. 2147483647];` hangs. Birth c5cd9f8;
      internal/eval/range.go:508-543. Fixed (8014c6a): stop at the
      last element instead of stepping past it; regression test in
      kernel_test.go.
- [x] R2. A recursive type alias crashes the process:
      `type t = t list; val x = [] : t;` is a fatal stack
      overflow in astNamedTerm. Needs an occurs check on alias
      bodies. Birth c0268eb; typeresolver.go:504-511. Fixed
      (20fda2e): a static occurs check over the alias bodies (not
      use-site args, so nested `int t t` still works); direct and
      mutual recursion report "recursive type alias". Regression
      test added.
- [x] R3. SetOpStage.rowValue does row[0] when the scan pattern
      binds zero variables: `from _ in [1,2] union [(),()];`
      panics (masked by the kernel recover, so the statement
      silently prints nothing; java returns 4 units). Birth
      16e4b0f; internal/eval/from.go:303. Fixed (39a5929):
      SetOpStage carries the variable count, so a row value is
      unit for none, the sole value for one, the record for
      several (symmetric rowValue/snapshot). Regression test added.
- [x] R4. relSumFn returns int32 0 for an empty collection of
      any element type; `sum (bag []: real bag) + 1.5;` silently
      prints nothing (swallowed panic). Zero must match the
      element type. Birth afca64e; internal/eval/relational.go:111.
      Fixed (a0a703e): SumFn takes the additive zero as a parameter,
      and the compiler supplies it from the static result type at
      both apply sites (standalone and group aggregate), like java's
      RELATIONAL_SUM macro. Regression tests added.
- [x] R5. The resolver ignores GroupStep.Binder, though typing
      accepts it: `from i in [10,20,11] group b = i mod 2 order
      b;` type-checks then silently prints nothing. Birth
      afca64e; resolver.go toGroupStep. Fixed (f2b4682): a binder
      group emits a following yield that assembles the key and
      aggregate fields into the group row and binds it to the
      binder (a record, or the bare value when the group is an
      atom), mirroring java Resolver; deduceGroup types it the
      same way. Test is the pull: java relational.smli binder
      tests now pass (+34 corpus lines).
- [x] R6. An `into` function is resolved in query scope instead
      of root scope: `from i in [1,2,3] into (fn l => i);`
      evaluates to 3 (reads a leftover frame slot); java rejects
      i as unbound. Birth 5a3a419; resolver.go:511-513 (sibling
      Skip/Take/SetOp cases use env correctly). Fixed (a422c29):
      resolve and type the into function in the root scope (both
      toQueryStep and intoStep used the query scope). Backswing
      test added (relational.smli).
- [x] R7. Step-placement validation is missing across the typing
      layer: `require` outside `forall`, `compute`/`into` inside
      `exists`/`forall`, and non-last `into`/`require` are all
      accepted, yielding wrong values (`from i in [1,2] require i
      > 1;` gives [0,0]; `... into f order i` executes then
      discards the order — a regression when d3b7361 removed the
      yielded guard). One consolidated fix in compile/from.go,
      mirroring java TypeResolver.java:1218-1249. Births
      0e5cd41, 5a3a419, d3b7361. Fixed (4fab689):
      checkStepPlacement validates container and last-step rules
      for compute/into/require (a group's compute is exempt), and
      step spans now include their keyword as java's do. Test is
      the pull: +73 corpus lines across logic, relational, and
      type-inference.
- [x] R8. Match coverage treats a layered pattern as a wildcard
      (no AsPat case in patInfo): `fun f (l as h::t) = 1;` gets
      no nonexhaustive warning, and an `as` clause before `[]`
      triggers a false "match redundant". Birth 056dfb5;
      coverage.go:225-259. Fixed (70ea4e0): patInfo classifies an
      AsPat by its body (it matches exactly what the body does).
      Backswing test added (match.smli).
- [x] R9. The empty-record pattern `{}` does not match unit:
      `val {} = ();` raises Bind. 747620f converted only the
      expression form; add the pattern counterpart
      (resolver.go:931-936). Fixed (previous commit): toPat
      converts an empty record pattern to a wildcard, as the empty
      tuple pattern `()` already is. Test is the pull: +24
      relational.smli lines exercising `{}` scan patterns.
- [x] R10. Non-discrete range bounds silently mis-enumerate:
      `[1.0 .. 2.5]` gives [1] : real list, `["a" .. "b"]` gives
      ["a"]; open-bound succ/pred failure yields an empty item.
      Should be an error (or restricted by typing). Birth
      c5cd9f8; range.go:531,539. Fixed (previous commit): a
      bounded `..` range over a non-discrete type raises Size.
      Because that would also break `elem`, membership now
      compiles to interval containment, which additionally closed
      R24's float-elem/unbounded-Size gap (`5.5 elem [0.0 ..
      10.0]`, `5 elem [0..]`). Test is the pull: +41 range.smli
      lines.
- [x] R11. curriedSpine counts all top-level arrows, so a builtin
      returning a function over-collapses:
      `(String.str o Char.chr) 65` renders one apply2 where java
      renders apply(fnCode apply2(...)). Birth 326422a; compiler.go
      curriedArity, code.go:254-270. Fixed for `o` (9b51c6e): a
      tuple-parameter builtin has curried arity 1, so its result
      function is applied separately; option.smli +4. Residual:
      `Fn.repeat` (a non-tuple builtin returning a function) still
      over-collapses — needs a per-builtin arity, no corpus pin.
      (`Fn.curry` is Morel-derived, a closure, so unaffected.)
- [x] R12. Sys.plan after a `fun` returns "" and clears the
      previous plan: RecValDecl never sets Plan, and kernel.go
      assigns lastCode unconditionally. Birth f7a9384;
      compiler.go:79, kernel.go:484. Fixed (1c6e4c8): RecValDecl
      plans its first bound function, and the kernel keeps the
      previous plan when a declaration has none (datatype/type).
      Backswing test added (built-in.smli). Closes R24's
      "Sys.plan after fun" coverage gap.
- [x] R13. type_string parses its operand as a full application
      chain; java parses at expression9 (atom + dot-chain), so
      `type_string String.size "abc"` gives "int" here but is a
      type error in java. Birth 973fde0; parser.go:358-370. Fixed
      (4f29f94): the operand is parsed with atomSuffixed and the
      application loop juxtaposes any following args, so
      `type_string f x` is `(type_string f) x`. Backswing test
      added (type.smli); the java corpus's precedence tests were
      already passing.
- [x] R14. Output matcher: after a Split error, the stale
      expected block is never reset and concatenates onto the
      next statement's expected output, silently disabling
      semantic matching there (script.go:57-81). Also
      splitOutput can panic on an inverted slice and sits outside
      the recover wrapper (output_matcher.go:33-56,90). Birth
      e965c32. Fixed (65092eb): flush() always drops the expected
      block; splitOutput guards the inverted slice. Go regression
      tests added (harness logic a script cannot express).
- [x] R15. setOrdinal is not wired into GroupStage.partition,
      GroupAggCode, SetOpStage, or ThroughStage, so `ordinal`
      read there sees a stale slot (group key example diverges
      from java). Birth b0f0495. Fixed (332ced0): setOrdinal now
      runs per row in partition and GroupAggCode. SetOpStage and
      ThroughStage evaluate no per-row user expression, so ordinal
      is never read there (no change needed). Backswing test added
      (relational.smli).
- [x] R47. A record expression with a field-punning shorthand — an
      unlabeled field whose label comes from a field selector, e.g.
      `{e.x}` or `{r.x}` — produced no output. toRecord and
      deduceRecordFields derived an unlabeled field's label only
      from a bare `*ast.ID`, so a selector `e.x` (an `*ast.Apply` of
      a RecordSelector) hit the `cannotDeriveLabel` path.
      stepFields/implicitLabel already handle the selector case, so
      a group/compute step field worked, but a plain record
      expression — including a final `yield {e.x, ...}` — did not.
      Fixed (previous commit): both derive the label with
      implicitLabel. High impact: pulled +1272 corpus lines across
      relational, fixed-point, blog, dual, foreign, hybrid, and
      type-inference. Discovered while implementing tabular output.

Latent divergences (no corpus line pins them yet — fix or record
a deliberate decision):

- [x] R16. stack(offset) measures from frame bottom; java's
      StackCode is 1-based from the top. Agrees only for
      single-slot frames; every current pin is offset 1. Birth
      e6eca8f; code.go:79-83. Decision (51e9c4f): deliberately not
      changed. Java's offset is a live stack depth (a push/pop
      model); go uses a flat pre-allocated frame with fixed slot
      indices, and no simple slot->offset mapping matches java
      universally (a read before a later push breaks NSlots-slot).
      The divergence is latent (every pin is offset 1). The
      misleading comment was corrected to state the convention
      accurately; revisit only if a multi-slot stack pin appears
      (would need to track live depth per read point).
- [x] R17. aggKind classifies an aggregate by top-level bindings
      only, ignoring local shadowing (java consults the env);
      and java's validateGroup checks are missing: no "cannot
      derive label" error (silent "current"/"" fallback), no
      duplicate-name check among keys + aggregates. Birth
      66b5b7f; group.go:175. Fixed (6268f03, and the two commits
      after): the "cannot derive label" errors now surface, for a
      record field (with the expression text, part 1) and for a
      non-atom group/compute expression (part 2); validateGroup
      adds the keys-vs-aggregates duplicate check ("in group") and
      deduceCompute the within-record one ("in record"). Pulled
      ~31 corpus lines (simple, relational). Deferred sub-part:
      aggKind still consults only top-level bindings, so a locally
      shadowed aggregate name used in a group is misclassified
      (and the resolver picks the builtin value too) -- obscure and
      unpinned; a fix would thread the local env into aggKind and
      the aggregate resolution. Revisit if a corpus line appears.
- [x] R18. planFnName has no case for `o` (renders "fnValue o",
      java "General.o"); `mod` hardcodes Int.mod; planAliases
      covers only corpus-exercised aliases. Birth aae5246;
      compiler.go:283-356. Fixed `o` -> General.o and made `mod`
      type-directed (65c8789). Residual: planAliases is still only
      as complete as the corpus exercises — extend when new
      aliases appear.
- [x] R19. ScanStage.Name is set only for IDPat, so
      `from (a, b) in ...` renders an empty pattern name in sink
      plans (java prints "pat (a, b)"). Birth 5df965e;
      compiler.go:605-609. Fixed (65c8789): the name is now
      corePatDesc(s.Pat), so tuple/other scan patterns render.
- [x] R20. compileTail threads tail-ness only through Apply and
      Let; a Case in tail position loses it. Resolved by 2571f7e
      (case renders as a match function; arm bodies compile in
      the case's tail context). Birth 1b05b12.
- [x] R21. Typing of a layered record pattern drops labelNames
      (AsPat forwards nil), and deduceCase unwraps only a
      top-level RecordPat; latent until `...` patterns evaluate.
      Birth ecb4c40; typeresolver.go:788,1483. Fixed (previous
      commit): the resolver now expands a record pattern (open or
      closed) against its resolved record type -- named fields to
      their sub-patterns, omitted fields to wildcards -- so `...`
      patterns evaluate end-to-end. The typing infers the full
      record type from the matched value via VarActions, so
      known-type contexts (val, from, case, layered `x as {a,
      ...}`, differing-field match lists) all work without further
      typing changes; the labelNames/deduceCase nuances proved
      moot there. Pulled +27 corpus lines (relational, optimize).
      Deferred: an open-type function parameter `fun f {a, ...}`
      needs row polymorphism (not in the corpus).
- [x] R22. Overloads: unresolved constraints are silently
      dropped at generalization (an unsound `'a -> 'b`), and a
      bare reference to an overloaded name (not in application
      position) falls through to an outer binding. Known,
      documented limitation; decide scope. Birth a8da103;
      typeresolver.go:1064,1349-1374. Decision: deferred. User
      overloading (`over`/`val inst`) is not functional
      end-to-end -- `over f` neither binds nor echoes, and
      overload.smli stays deferred (13% pass) -- so both soundness
      gaps are unreachable and unpinned. (The built-in numeric
      operator overload, a separate mechanism, works.) A real fix
      needs user overloading implemented properly, which the plan
      schedules post-endpoint (see the overload regroup, R32).
      Revisit then.
- [x] R23. Minor cluster. Fixed (previous commit, +13 corpus
      lines): alias/datatype arity mismatch now reports "type
      constructor N given M argument(s), wants K" (matching java,
      type.smli); canonicalTyVars uses a base-26 name so >26 type
      variables no longer emit non-letter bytes; walkExp descends
      into RangeList bounds so a nonexhaustive match inside
      `[e .. f]` is reported; relational.sig now says NONE
      compares first. Not changed (deliberate): loadScott panics
      via the shared evalDecl boot path, which is documented as
      panic-on-failure for embedded, tested resources (like
      sig.Load); the unifier renders a free-orderedness collection
      as "bag" -- consistent with go's type display (`'a bag`,
      matching java's corpus) and unpinned, so changing it would
      create inconsistency. Residual (obscure, unpinned):
      unboundTyVar still skips ExpressionType (`typeof` in a
      datatype-constructor argument), which would need an
      expression walk.

Coverage gaps that let the above hide:

- [x] R24. Relational.iterate landed with type-assertion corpus
      only — no functional transitive-closure test (java has
      them). The sink-pipeline rendering (5df965e) has zero
      corpus. (Sys.plan after `fun` is now pinned in backswing.smli
      with the R12 fix; float-elem range membership and
      unbounded-range Size are pinned — pulled with the R10 fix.)
      Fixed (5417dac): added a functional transitive-closure
      iterate test to backswing.smli; verified the sink-pipeline
      plan renders in java's `from(sink join(... sink where(...
      sink collect(...))))` form. Residuals: Relational.iterate is
      typed `'a bag` (fixed), so java's list-based test does not
      pull -- it needs a free-orderedness collection, a
      higher-order adaptation CollectionAggType does not cover; and
      java's sink-plan corpus region (relational.smli:3043+) is
      blocked by other unported features.
- [x] R25. The kernel's blanket recover() converts evaluator
      panics into "no output", which masked R3 and R4. Add a
      debug escape hatch (env var or prop) that re-panics. Fixed
      (1e3caf2): setting the MOREL_DEBUG environment variable
      re-panics instead of swallowing, so an evaluator bug is
      visible when debugging.

### Content that must not reach main

- [ ] R26. Remove etc/issue-structlib.md: it should never have
      been added to git. Delete it from 1-bootstrap (a normal
      commit now); in the reorganization drop 12f6946 entirely
      and drop the plan commit 7535063's hunk to it. Nothing
      else references the file.
- [ ] R27. Decide the fate of the other morel-java working
      notes: etc/issue-overload-let-polymorphism.md (7a7402d, a
      draft issue for the morel-java repo) and
      etc/survey-divergences.{md,py} (a3fce6e; the .md buckets
      divergences vs morel-java and cites plan phase/task
      numbers, the .py hardcodes ~/dev/morel-go.0 and
      ~/dev/morel.0 paths and the .md cites a wrong script
      name). Recommendation: keep all off main, like reorg.md;
      the survey-driven fixes (6a87f68, ecb4c40, 52544ff,
      5a9292e) have no code dependency on them. If all are
      dropped, b4effe1 dissolves (its etc/*.md lint exemption
      and py-header hunks lose their subjects) — but keep the
      exemption on 1-bootstrap, where the notes live on.
- [ ] R28. etc/push-green.sh is still untracked. Commit it on
      1-bootstrap (with license header) or discard; decide
      before the next push cycle.

### Fixups: squash into birth commits

- [ ] R29. b4effe1 -> a3fce6e (py license header) and the
      etc/*.md lint exemption to the first surviving etc/*.md
      commit. Note TestLint is red at 7a7402d and a3fce6e until
      b4effe1 — the reorganization must not preserve that
      window (subsumed by R27 if the notes are dropped).
- [ ] R30. 52544ff (word-literal patterns, one parser-table
      line) belongs to pre-capture 624ece5 "`Word` structure" —
      published, so keep it as a standalone early commit in the
      new series, not at the tail.
- [ ] R31. 79397d1 (exponential fits()) fixes pre-capture
      a61f86a (Wadler-Leijen engine) — published, so place it
      as a standalone commit before e965c32 "Compare statement
      output semantically", per the NOTE in its own message
      (strip that NOTE when rewording).
- [ ] R32. Overload series regroup: split the over/val-inst
      parsing groundwork out of 8edfefe (OverDecl, Inst flag,
      overDecl parsing, NewValDecl churn) and unite it with
      a8da103 and 7f09db9 as one over/inst commit; 8edfefe
      keeps elem/notelem/only. Also fold 0e5cd41's
      unsupportedFrom2 -> unsupportedStep rename into 66b5b7f
      so the intermediate name never exists, and drop the
      unrelated sml-nj comment block a8da103 added at
      type-inference.smli:358-366.
- [ ] R33. PP chain: squash 6540673's PP-rewiring half into
      0adcccb (write PP against internal/pp from the start; the
      private ppDoc/ppFits/ppRender renderer never exists);
      6540673's abstract-types-print-as-"-" half becomes its
      own small commit or rides along. 2ac6f68 stays as the
      structLib-client rework.
- [ ] R34. structLib chain: fold 93f85fb (lib move) into the
      births — fn.sml/option.sml and the lib.FS embed into
      d771627, pp.sml into 2ac6f68 — so the files are born in
      lib/; fold 8d0890e's src -> file field rename into
      d771627. 8d0890e keeps the consistency tests.
- [ ] R35. b7bf036 -> 056dfb5: drop the 16-line
      type-inference.smli tail from 056dfb5's corpus addition
      and b7bf036 disappears.
- [ ] R36. scott: squash 8798bf6 and 6a87f68 into one "Add the
      scott dataset" commit (corpus scott.smli, embedded
      scott.sml global, dependent corpus pulls); optionally
      split 8798bf6's print.go bag-type line-wrap fix into its
      own printing commit first.
- [ ] R37. Sys.plan parity cluster (aae5246, 326422a, 5df965e,
      1b05b12, and tip commit 2571f7e — plus any further 116x
      parity commits still being appended): each is a real
      increment, but together they change the eval.Apply
      signature twice more and rework applyCode.Describe twice
      after birth. Either squash the cluster into one "match
      java's plan format" commit placed next to
      f7a9384/e6eca8f, or move it adjacent to those births.
      e6eca8f itself is a natural squash into f7a9384 (no
      corpus change).
- [ ] R38. Query-evaluation churn: internal/eval/from.go is
      architecturally rewritten twice in-range (8c260d2 flat
      loop -> 8759818 recursion -> 333eeb1 row-list stages, the
      survivor). Squash at least 8c260d2+8759818, or all three
      into one "evaluate from" commit; this also collapses the
      six-commit churn of TestExecuteItOnlyOnSuccess.
- [ ] R39. d3b7361's stepBinder QuotedIdent support extends
      pre-capture parser commit 8c477ce (published) — leave it
      in d3b7361 or split as a small standalone parser commit.
- [ ] R40. b55547c reworks 66b5b7f's aggregate builtins
      (removes count/sum/max/min from topBuiltins for
      CollectionAggType); consider partial squash so the
      removed entries never exist, and note 66b5b7f's snapshot
      briefly breaks the builtin table's alphabetical order.
      Delete the now-dead bagToElem const (builtin.go:45)
      wherever it lands.

### Splits and re-orderings

- [ ] R41. Split candidates beyond R32/R33/R36: 5a471ea
      bundles five step families (fine together, but its
      ordinal binding belongs with 3560535's row-keyword work);
      b55547c mixes aggregate orderedness with the zero-field
      group rule; 5df965e mixes nth:N selector naming with the
      sink pipeline and smuggles a mechanical case re-sort.
      8c260d2 misplaced toFrom between toFn's doc comment and
      toFn — reattach the comment when rebasing (R23 tree fix
      too).
- [ ] R42. Re-order: 977d7a4 (Either/Bool to Morel) sits five
      commits from the structLib cluster it belongs to — move
      it adjacent to 8d0890e or squash with the phase-1
      commits. ecb4c40/52544ff back-fill pattern features and
      read better before the survey-driven corpus growth.

### Message and comment policy

- [ ] R43. Reword for main — subjects with issue/task refs:
      e965c32 (#334), 8798bf6 (#255), 16e4b0f (#321), afca64e
      (#271), 35755d6 (#249), c5cd9f8 (#372), d771627, 2ac6f68,
      8d0890e, 977d7a4 (all morel-go#2), 1b05b12 (116d),
      2571f7e (116e).
      Bodies with phase/task/issue refs: e08ce9b (#407 + java
      sha), c5cd9f8 ("phase K"), 71bcb04 ("phase K"), 977d7a4
      ("Phase 2", "task-113"), ecb4c40 ("Task 14"), 52544ff
      ("task 64"), 7f09db9 ("task 94"), 5df965e ("116f"),
      b55547c (#271, #328), 79397d1 (reorg NOTE), fa48495 /
      6a87f68 / 7a7402d (morel-java named in body); 056dfb5's
      body says "+11" for a 41-line corpus change.
- [ ] R44. Comment sweep for new code, at birth commits (the
      H2 rule; corpus and etc/ exempt): morel-java/java named
      in unparse.go:74,146 (fa48495); kernel.go:221-222,241,483
      and scott.sml:20 (b55547c, 6a87f68, f7a9384);
      code.go:165,234 (f7a9384, 326422a); group.go:31
      (66b5b7f); compiler.go:254,278,332 (5df965e, aae5246);
      coverage.go:36,45 (056dfb5); from.go:689 (5df965e);
      lint_test.go:35; two more in 2571f7e (compiler.go: "is
      java's fnValue tyCon", "java renders a case as...").
      Issue numbers in comments:
      builtin.go:171,191 and from.go:501 and kernel.go:222
      (morel#271/328 — decide whether upstream-issue refs are
      allowed in comments; they were swept last time);
      kernel.go:142,340, export_test.go:37, lib/lib.go:21,
      lib/either.sml:21 (morel-go#2 — clear violations;
      lib/*.sml is not exempt). resolver.go:110 says "see task
      94b" (7f09db9). Note the pre-capture files were swept on
      main only; on rebase the drift resolves them, but any
      new-commit hunk touching swept lines must adopt main's
      wording.

### Regression tests for the bug fixes

- [ ] R45. Every fix above carries a test, sourced one of two
      ways. (a) If morel-java's corpus already pins the
      behavior, the fix's test is the pull itself: re-run
      pull-passing.py and adopt the newly passing lines. This
      covers R7 (java logic.smli:209-248 pins the four
      step-placement errors), the float-range membership and
      unbounded Size gaps of R10/R24 (java range.smli:543,
      505-513), and Relational.iterate's closure tests (java
      built-in/relational.smli:104+). (b) Otherwise append the
      test statements to a new go-local
      testdata/script/backswing.smli, which the pull never touches
      (the parse.smli precedent); TestScripts picks it up with
      no harness change. Each entry is preceded by a concise
      one- or two-line comment naming the bug and the upstream
      .smli file the lines belong in (e.g. "a recursive type
      alias must be rejected, not overflow the stack; belongs
      in type.smli"). A future "backswing" task in morel-java
      adopts backswing.smli entries into those upstream files; once a
      pull returns them there, delete them from backswing.smli. When
      creating the file, document the exception in agents.md
      beside parse.smli and exempt it from the corpus
      regeneration tooling (pull-passing --apply, era_trim,
      whole-file regens). Go unit tests remain only for what a
      script cannot express (none currently planned).

- [ ] R46. Retire the runSession-based kernel execution tests in
      internal/shell/kernel_test.go (24 functions: TestExecuteLiterals
      through TestExecuteItOnlyOnSuccess), the same way R1-R4 were
      moved out. For each statement, either (a) delete it when the
      morel-java corpus already pins an equivalent query (most basics
      -- literals, arithmetic, lists, closures, datatypes -- are
      covered by simple.smli/type.smli/relational.smli and the
      built-in/*.smli suites), or (b) append it to
      testdata/script/backswing.smli with a comment naming the
      upstream .smli file, when nothing equivalent exists upstream.
      Behavior belongs in scripts, not Go unit tests; drop runSession
      once the last caller is gone. Whatever genuinely cannot be a
      script (e.g. TestExecuteItOnlyOnSuccess, which asserts "it" is
      unchanged after a failed statement -- a session-state property)
      stays as a Go test.

## After the endpoint

Fast-follows, in rough order: TCO (morel#151 — frames are already
shaped for it), `raise` (#364), PP (#398), outer joins
(#75), `yieldAll` (#257), `?.` (#378), file reader (#209), FBBT
(#373), Datalog (#323), shell polish (line editing, history,
highlighting, morel#413/#414). Calcite/hybrid remains deferred
indefinitely.
