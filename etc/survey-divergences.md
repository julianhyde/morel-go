# Survey: morel-go vs morel-java `.smli` divergences

Method: each shared `.smli` file from morel-java was replayed through the current morel-go binary and compared statement by statement (`etc/../survey.py`, using pull-passing's segmenter). A statement "diverges" when morel-go's output differs from morel-java's — the statements the corpus currently drops.

Result: 1199 divergent statements across 52 shared files. The buckets below are grouped by root cause and classified as (a) blocked on an upcoming/planned feature, or (b) a small, already-in-scope fix that was missed.

## (b) Forgotten / in-scope fixes — recommended to fold back

These are the survey's actionable findings: features whose task is already marked complete, or trivially-scoped gaps, yet the behavior is missing.

### 1. Word literals as patterns

`case 0w1 of 0w1 => "one" | _ => "other"` and `fn 0w0 => "zero" | _ => ...` produce no output; the equivalent with int literals works. Word literals (task 64, complete) are accepted as expressions but not as patterns.

Evidence: simple.smli:1118, simple.smli:1120. This is the example that prompted the survey.

Fix scope: small — the pattern path needs a word-literal case alongside the existing int/char/string literal patterns.

### 2. Layered (`as`) patterns

`val w as (a, b) = (1, 2)` and `case (3,4) of z as (x, y) => x + y` produce no output. Task 14 lists "patterns I (wildcard, literal, id, tuple, record, `::`, layered `as`)" as complete, but `as` patterns are not handled past the parser (the `AsPat` AST node exists and unparses, but resolution/eval drops the statement).

Evidence: simple.smli (`val SOME (q as (1, i), SOME true) = ...`), idempotent.smli (`val it as (x, y) = (5, 6)`), and 8+ statements bucketed under `SILENT: val`.

Fix scope: small-medium — handle `AsPat` in the core resolver and the pattern matcher (bind both the whole value and the sub-pattern).

### 3. The `scott` global dataset

`scott.emps`, `scott.depts` are unbound in every file except scott.smli. In morel-java `scott` is a global dataset available to all scripts; morel-go only defines it locally inside scott.smli (`val scott = {...}`), and the test harness runs each file in a fresh kernel, so other files cannot see it.

Evidence: 135 statements — relational.smli (33), blog.smli (49), dual.smli (31), hybrid.smli (11), pretty.smli (4). relational.smli never writes `val scott`, so it relies on the global.

Fix scope: medium, high value — expose the `scott` record (emps/depts/…) as a built-in global binding, mirroring java's foreign dataset. Unblocks the single largest cascade in the corpus.

### 4. `val it = [] : int list` and similar wrong-output cases

A handful of statements where morel-go returns an empty (or otherwise wrong) collection instead of java's result — distinct from the "silent/unsupported" cases, so likely small bugs rather than missing features.

Evidence: 9 statements under `OUT: > val it = [] : int list` (relational.smli, built-in/relational.smli, e.g. `List.except`). Needs per-case investigation; flagged, not yet root-caused.

## (a) Blocked on upcoming / planned features — correctly deferred

These map to plan items not yet reached, or to features not in the current plan window.

- **`from` queries needing generators / `such that` / extents** (~178, minus the scott cascade): `from p where path p`, unbounded scans, `[1 .. 5]` range literals (17), `cs.contains` / continuous-set method access (18). → phase K, tasks 99-107 (extents, generators).
- **`Sys.planEx`** (43): → task 107.
- **`Sys.plan` output differences** (~115, incl. `apply(fnValue Sys.plan …)`): plan-format rendering still diverges for some shapes. → ongoing (tasks 94/107 territory); worth a targeted pass later.
- **`PP.render` / pretty-printer structure** (19): the `PP` structure is unimplemented. → not in the current plan window.
- **`Datalog.*`** (`validate`/`execute`/`translate`/`translate`, ~28): the Datalog structure is unimplemented. → not in the current plan window.
- **Exception handling** — `raise`, `handle`, and the exception constructors `Empty`/`Div`/`Overflow`/`Bind`/`Fail` used as values (~17): → language feature, not in the current plan window.
- **Datatype/type edge cases**: `unbound type constructor: my_list` (5, recursive/forward type alias), `val x : (int, int) = ...` (parenthesized-tuple annotation errors) — mostly deliberate error cases in datatype.smli/type.smli. → partly task 92/16 follow-ups, partly genuine java errors go should also raise.
- **Cascade failures**: `emps`, `states_within`, `words`, `neighbors`, `pivot`, `triples`, `is_adjacent`, `hcf`, `gcd` unbound (~60 combined) are all secondary — their defining statement was dropped for one of the reasons above, so the references fail too. They resolve automatically when the root feature lands.

## Recommendation

Fix the four (b) items in priority order: **scott global** (135 stmts), **as-patterns** (~12 stmts, and a claimed-complete feature), **word-literal patterns** (~3 stmts, the prompting example), then investigate the `val it = []` wrong-output cases. The (a) buckets need no action beyond their existing plan tasks.
