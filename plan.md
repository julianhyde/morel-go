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
4. `etc/check-convergence.py` is the progress metric: per-file
   divergence from morel-java must never increase, and trends
   monotonically to zero. Its per-file report is the project
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
| Test disablement | None — a section enters a `.smli` file only when it passes; absence is visible in the divergence report |
| New tests | Written against morel-java first (upstream), then propagated back; go-only `.smli` files are a temporary escape hatch, flagged by the report tool |
| Convergence gate | Per-file divergence from morel-java (source of truth) may never increase, and trends to zero; informational diff against morel-rust (which carries ~6.4k divergent lines of its own) |

## Phases and tasks

### Phase 0 — Infrastructure

- [x] 0.1 Repo skeleton: license files, CI, lint, `cmd/morel`.
- [ ] 0.2 Project lint test: license headers, trailing whitespace,
      `.smli` hygiene (port of java `LintTest` / rust
      `tests/lint.rs`).
- [ ] 0.3 Lexer: tokens, positions, nested comments, string and
      char literals, backtick-quoted identifiers.
- [ ] 0.4 `StatementSplitter`, driven by the lexer.
- [ ] 0.5 `Kernel` (execute(stmt) -> output; owns `Config`,
      environment, session), `ScriptRunner` (line loop, echo,
      prompts), batch `main`.
- [ ] 0.6 Minimal idempotent script harness (`> `-prefixed
      expected output; file must reproduce itself); test list
      generated from the directory. `.sig` files, `data/` CSVs,
      and `.smli` hunks are pulled as components need them.
- [ ] 0.7 Port `etc/check-convergence.py`: per-file divergence
      from java may never increase; per-file report is the
      project dashboard. Propagation ledger.

### Phase 1 — Parse

- [ ] 1.1 AST (spans, node ids) and the full grammar.
- [ ] 1.2 `Sys.parseTree` (java 49c48703) plus the minimal
      evaluation path needed to call a builtin from a script
      (string literal -> curried builtin application -> string
      result). Parser correctness is then verified by pulled
      parse-tree hunks, not by Go unit tests; new parse tests are
      added to morel-java first and propagated back.
      **Milestone: full grammar; pulled parse-tree hunks pass.**

### Phase 2 — Types

- [ ] 2.1 `TypeSystem`, type hierarchy, type-from-string bootstrap
      for builtin signatures.
- [ ] 2.2 Unifier (Martelli-Montanari port) with constraint hooks.
- [ ] 2.3 `TypeResolver`: Ast -> Core (typed IR from the start).
- [ ] 2.4 `.sig`-driven builtin registry (types only — `:t` needs
      no implementations) + signature checker.
- [ ] 2.5 `:t` support in the harness, and type printing in java's
      exact format; pull stable `:t` hunks of
      `type-inference.smli` and `type.smli` as inference grows.
      **Milestone: pulled `:t` hunks pass — type inference tested
      without evaluation.**

### Phase 3 — Evaluator core

- [ ] 3.1 `Val` representation; type-directed comparators.
- [ ] 3.2 Span-carrying errors; uniformly curried builtins.
- [ ] 3.3 `Code`/`Compiler`; slot-indexed frames, capture analysis,
      link table, let-rec — designed as one unit, frames shaped for
      trampolining.
- [ ] 3.4 Wadler-Leijen printing engine (morel#398) — pulled
      forward deliberately, so output matches java's present-day
      format exactly (wrapping, real formatting, SML-NJ span
      format) and pulled hunks never need re-blessing.
- [ ] 3.5 Output matcher + `matchStrict` (morel#334).
      **Milestone: hunks of `simple`, `closure`, `match`,
      `datatype`, `exception` pulled and passing.**

### Phase 4 — Standard library (one structure per commit)

Most functions in most structures are not load-bearing: implement
the ones later hunks depend on first, and fill in breadth at any
time — early, late, or opportunistically. The grouping below is a
default order, not a commitment.

- [ ] 4.1 General, Bool, Order, Option.
- [ ] 4.2 Int, Math, Real.
- [ ] 4.3 String, Char.
- [ ] 4.4 List, Bag, ListPair, Vector, Either.
- [ ] 4.5 Sys, Fn; environment compaction.
      **Milestone: load-bearing functions in place; pulled
      `built-in.smli` hunks pass.**

### Phase 5 — Relational

- [ ] 5.1 `FromBuilder`; scan/where/yield.
- [ ] 5.2 `RowSink` push pipeline, java's binding model verbatim.
- [ ] 5.3 Joins (incl. comma joins); group/compute (final syntax).
- [ ] 5.4 Ordered/unordered queries; aggregate adaptation
      (morel#273, #271, #282, #328).
- [ ] 5.5 Set-op steps, distinct, take/skip, into/through,
      quantifiers (exists/forall/require).
- [ ] 5.6 In-heap scott dataset (morel#255).
- [ ] 5.7 **Milestone: `relational.smli`, `bag.smli` largely
      green.**

### Phase 6 — Maturity features

- [ ] 6.1 Full `over`/`inst` overloading surface (morel#237);
      polymorphic datatypes (morel#70, #205).
- [ ] 6.2 Cross-statement recursion; cross-unit inlining
      (morel#223, #330).
- [ ] 6.3 Range, Variant, Time, Date structures; now/timeZone
      properties (morel#338, #324, #351, #278, #352).
- [ ] 6.4 Surface conveniences: postfix method calls, `op`
      sections, unparser, tabular output, `-e`/`--eval`, `use`
      (morel#346, #311, #293, #259, #333, #198).
- [ ] 6.5 Predicate inversion (morel#217): `such-that.smli`,
      `fixed-point.smli`.
      **Endpoint milestone: parity with morel-rust `e0779b86`.**

## After the endpoint

Fast-follows, in rough order: TCO (morel#151 — frames are already
shaped for it), `raise` (#364), Word (#396), PP (#398), outer joins
(#75), `yieldAll` (#257), `?.` (#378), file reader (#209), FBBT
(#373), Datalog (#323), interactive shell (line editing, history,
highlighting). Calcite/hybrid remains deferred indefinitely.
