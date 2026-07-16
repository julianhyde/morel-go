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
- [ ] 59. `Real` structure — complete; `Real.fmt` likewise.
- [ ] 60. `Math` structure — complete.
- [ ] 61. `Word` structure (morel#396) — the `word` type over a
      `uint32` value, `0wFF`/`0wxFF` literals, and the members
      (`fromInt`/`toInt`, the bitwise and arithmetic operators,
      `fmt` via task 57's radix, `compare`); the fourth
      primitive-backed structure, after Int/Real/Math.
- [ ] 62. `Option` structure — complete.
- [ ] 63. `Bool` structure — complete.
- [ ] 64. `Char` structure — complete.
- [ ] 65. `General` structure — complete.
- [ ] 66. `Vector` structure — complete (finish task 52: the
      `Vector;` dump now prints, given task 51's label quoting and
      contiguous type-variable numbering).
- [ ] 67. `ListPair` structure — `zip`, `unzip`, `map`, `app`,
      `all`, `exists`, `foldl`/`foldr`, and the `*Eq` variants
      that raise `UnequalLengths`.
- [ ] 68. `Either` structure — the `('a, 'b) either` datatype
      (`INL`/`INR`); `isLeft`/`isRight`, `asLeft`/`asRight`,
      `map`/`mapLeft`/`mapRight`, `app`/`appLeft`/`appRight`,
      `fold`, `proj`, `partition`.
- [ ] 69. `Fn` structure — combinators `id`, `const`, `apply`,
      `o`, `curry`, `uncurry`, `flip`, `repeat`, `equal`,
      `notEqual`.
- [ ] 70. `Range` structure — range values (morel#338);
      `contains`, `toList`/`toBag`, `discreteSetOf`/
      `continuousSetOf`, `flatten`, `ranges`, `complement`.
- [ ] 71. `Variant` structure — variant values (morel#324);
      `parse`, `print`.
- [ ] 72. `Date` structure — the `date` type over Go's `time`
      (morel#278); `date`, `year`/`month`/…/`yearDay`, `compare`,
      `toString`/`fromString`, `fmt`, `toTime`/`fromTime*`. The
      corpus fixes `now` and `timeZone` via `Sys.set`, so tests
      are deterministic.
- [ ] 73. `Time` structure — the `time` type (morel#351, #352);
      `fromReal`/`toReal`, `to`/`fromSeconds`…`Nanoseconds`,
      `compare`, `now`, `fmt`, `toString`/`fromString`,
      `zeroTime`.

Two features were deferred by every structure above; each closes
the last of the per-structure divergence in one pass over every
file, so it is done once, here, at the end:

- [ ] 74. Postfix method calls (morel#346): `x.f a` desugars to
      the qualified call the receiver's type names (`5.abs ()` is
      `Int.abs 5`, `[1].hd ()` is `List.hd [1]`). A
      `pull-passing.py` run then adds the postfix hunks, absent
      until now, across every structure file.
- [ ] 75. `Sys.plan`: emit java's compiled-plan string. A
      `pull-passing.py` run adds the plan hunks — the single
      largest remaining block — across every file at once.

The remaining skeletons stay out of this phase: `datalog`
(morel#323) and `pp` (morel#398 user surface) are post-endpoint
(`word` is now task 61); `relational` is the query engine
(48-50 and beyond); `ieee-real`, `int-inf`, and `interact` are
already at java's coverage (their java files are type-references
only).

### G. Relational, first slices (47-50)

Runs after phase F. The `Bag` structure is already complete
(task 55); its query *typing* — bags as unordered collections —
lands here with the collection work (morel#273).

- [x] 47. Typing for scan (`in`, `=`), `where`, `yield` over
      lists: a query's rows carry named fields, the element type
      is the sole field or a record of them, `yield` of a record
      literal exposes its fields. The list-scan `:t` hunks pass.
      (`Core.From` + `FromBuilder` land with the evaluator in
      task 48, rather than as an unused IR node now — no
      speculative infrastructure. The collection-kind constraint
      hook from task 21 is ready for bags.)
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
set-op steps; quantifiers; in-heap scott dataset (morel#255);
ordered/unordered queries + aggregate adaptation (morel#273,
#271, #282, #328); full `over`/`inst` overloading surface
(morel#237); polymorphic datatypes (morel#70, #205); cross-unit
inlining (morel#223, #330); surface conveniences — postfix
method calls,
`op` sections, unparser, tabular output, `-e`/`--eval`, `use`
(morel#346, #311, #293, #259, #333, #198); match-coverage analysis
(morel#55); predicate inversion (morel#217).
**Endpoint milestone: parity with morel-rust `e0779b86`.**

## Commit-history maintenance

Deferred history surgery: fold or re-order commits so the final
history reads as if each fact were known from the start. Do these
in a quiet moment, replaying with `git rebase -f -x 'fullMake
--no-clean'` so every rewritten commit stays green.

- [ ] H1. Fold the type-variable naming fix into its origin.
      Commit `26addc9` ("Name type variables beyond 'z as java
      does") corrected `varName` to number the 27th variable and
      beyond `'ba`, `'bb`, ..., `'zz`, `'baa`, matching java's
      `TypeVar.name`. Cleave the code change (the `varName` rewrite
      and its `types_test.go` cases) from its corpus regenerations
      and squash it into `4f64aef` ("Add interned type
      representations and the type-from-string bootstrap"), which
      first generated type-variable names, so the base-26 scheme is
      correct from the start. Re-regenerate any earlier corpus whose
      wide dumps this shifts.
- [ ] H2. Strip provenance and process from code comments. Sweep
      the Go source for comments that reference java ("as java
      does", "matching java's ...", "like java"), tasks, bug or
      issue numbers, or anything else ephemeral or about how the
      work was done, and reword them to describe only the behavior.
      Comments should read as if the code had always been this way.
      Prefer folding each edit into the commit that introduced the
      comment.

## After the endpoint

Fast-follows, in rough order: TCO (morel#151 — frames are already
shaped for it), `raise` (#364), PP (#398), outer joins
(#75), `yieldAll` (#257), `?.` (#378), file reader (#209), FBBT
(#373), Datalog (#323), shell polish (line editing, history,
highlighting, morel#413/#414). Calcite/hybrid remains deferred
indefinitely.
