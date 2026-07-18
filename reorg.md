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
# Reorganizing 1-bootstrap into right-sized commits

The ledger for rewriting the 200-commit `1-bootstrap` branch into
a clean history for `main`. This file is the planning medium: it
is reviewed and edited until the target history reads well, then
a replay script executes it. Like `plan.md`, it lives only on the
work branch and never enters the new history.

Old SHAs below refer to `1-bootstrap` at `15c0f6b`. The new
branch is built as `main-candidate` from `main` (`53a60b2`);
`1-bootstrap` is kept untouched as the reference.

## Shape of the problem

- 200 commits; **104 touch only `plan.md`** and 0 mix plan with
  code (the separate-commits discipline paid off). Dropping
  `plan.md` from history silently removes all planning and
  "Mark task N complete" commits. 96 code commits remain.
- Most of the 96 are already right-sized (one task, one commit):
  the infra/parser/type/eval eras keep their order and content,
  needing only message cleanup.
- ~20 commits are fixes that belong in an earlier commit
  (fold map below); 3 commits implement several structures at
  once and split; the structure era gets resequenced so each
  structure is one commit including its completion.

## Method

Two stages, split so that all risk lands in the first and the
second is provably tree-preserving.

1. **Review this ledger** until the target history reads well.
2. **Stage 1 — `1-bootstrap-reordered`** (moves and splits
   only). Branch from `1-bootstrap`; rebase with a generated
   todo that *keeps every commit* — plan.md commits, current
   messages, all of them — and only:
   - relocates each fix to sit **adjacent to** (not yet squashed
     into) its target from the fold map;
   - performs the three splits, with provisional messages
     ("Implement Int, Real, and Math [2/3: Real]");
   - resequences the structure era.
   Corpus rule (what makes reordering tractable): on any `.smli`
   conflict, do not resolve the patch — discard it and run
   `etc/pull-passing.py --apply <file>` at that tree; the file
   regenerates as exactly what that commit's code supports.
   This stage is experimental by design: try a move, verify,
   back it off if it fails; `1-bootstrap` is never touched.
   **Verify here** (the expensive pass): `git rebase -f main -x
   'fullMake --no-clean && python3 etc/check-convergence.py
   HEAD'` — every commit green, per-file divergence never
   increasing. **Audit**: `git range-diff 1-bootstrap
   1-bootstrap-reordered` shows every move; `git diff
   1-bootstrap 1-bootstrap-reordered` must be empty.
3. **Stage 2 — `main-candidate`** (drops, squashes, rewords
   only). From the stage-1 branch: drop the plan-only commits
   (tree change: plan.md only), squash each now-adjacent fold
   group and split remnant into its final commit (no tree
   change at surviving commits), and apply the message policy
   (no tree change). Because no surviving commit's tree changes
   except by plan.md/reorg.md, stage 1's verification carries
   over; a final spot check suffices. Trivially scriptable from
   this ledger.
4. **Comment sweep, last** (see below): one reviewed edit at the
   tip of `main-candidate`, distributed to birth commits with
   `git absorb` (`brew install git-absorb`) + autosquash rebase,
   then one more `fullMake` replay for safety.
5. **Final audit**: `git log --reverse --format='%h %s'` against
   this ledger; `git diff 1-bootstrap main-candidate` empty
   except `plan.md`, `reorg.md`, and the comment sweep.

## Policies

- **Drop**: all `plan.md` changes (automatic — no mixed
  commits); `issue.md` in all four commits that touch it
  (born 2fbe066, grown efa08dc, retired e74bbc4/01da4de — nets
  to nothing; the surviving `.smli` parts of e74bbc4/01da4de
  move to the `Real` structure commit).
- **Keep unchanged** (subject only to message policy): every
  code commit not named in the fold map, splits, or
  resequencing below.
- **Messages**: single line, and **no issue references
  anywhere** — subject or body, neither `#1` nor
  `hydromatic/morel#NNN` (the merge PR carries the linkage).
  Structure commits are "`` `X` `` structure". A body only
  where the design is not obvious from the diff — the unifier,
  value representation, frames/recursion, printing engine,
  builtin registry, statement splitter, script harness, postfix
  dispatch — and then it describes components and choices, not
  process or provenance ("Propagates", task numbers, java
  comparisons, issue numbers all gone).
- **agents.md**: squash all four commits (725d1b5, f3338b3,
  abb1800, bf51141) into one "Add development notes" with the
  final content, positioned first.
- **etc/ tooling**: squash d90c1b6 + b3981b4 into one "Add
  convergence and corpus tooling"; the scripts' java references
  are exempt from the comment sweep (comparing to java is their
  purpose).
- **CI**: fold 15c0f6b (macOS/Windows matrix) into 166cd41
  (lint config / Go 1.25), so CI is born multi-platform.

## Fold map (fix -> birth commit)

Code moves to the birth commit; a fold's `.smli` hunks move to
whichever commit owns that file's growth (usually the structure
commit), via the corpus rule.

| Fix | Folds into |
| --- | --- |
| df3c696 nan via `abs()` in real corpus | e9671a3 before its split (then Real/Math parts separate) |
| dbba9d4 `:t` errors in java's format | 006fdc9 error reporting |
| d3ea485 partial-application error span | 006fdc9 error reporting |
| 3cadbd2 parallel `val ... and ...` | f7ad0a0 val patterns / sequential decls |
| 559699a type-annotated patterns in val | f7ad0a0 val patterns |
| 52c77aa NaN ordering comparisons false | 2e52f08 equality and comparison |
| bc72280 strings as byte sequences | birth of `parse/string.go` (lexer 2bacca0 or printing a61f86a — check blame) |
| ecda769 backtick / non-canonical labels | 3f99c35 parser records |
| aaaa320 tyvar names beyond 'z (plan H1) | 4f64aef type representations (code+test only; corpus via rule) |
| 8ee993a Config.Directory prep | 91510f1 CLI parsing |
| c936db1 global `vector` alias | 8007e80 Vector |
| 91c9877 Char.toCString/fromCString | Char structure commit |
| 4566d38 Int.fmt, Int.abs overflow | Int structure commit |
| 18da1c0 Real.fmt, NaN/subnormal edges | Real structure commit |
| 5932f2b Real.fmt FIX small values | Real structure commit |
| fde0c8c floor/ceil of NaN raise Domain | Real structure commit |
| e74bbc4 / 01da4de java-fix corpus pulls | Real structure commit (`issue.md` parts dropped) |
| c822550 wrap long types (task 51) | a61f86a printing engine (decision D5) |
| 5425fbb checkNoUnresolvedFieldRefs | keep standalone, but move to just after 9c29395 (selectors); retitle "Check field references against record types" |
| a23166e checkNumericOperators | already in place after 45c1360; keep standalone |

Left where they are (features, not fixes): 94cf2f3 tabular
output, 32efad8 op sections, c6b80f7 postfix, db9935f from
typing, 98ae2a7 / c807959 corpus and stress commits.

## Splits

Each is a manual `git add -p` session; shared files
(`eval/builtin.go` table rows, `built-in.smli`) split by hunk;
per-structure corpus files regenerate via the corpus rule.

1. **65ed679** "Implement General, Bool, and Option" ->
   "`General` structure", "`Bool` structure", "`Order`
   structure", "`Option` structure".
2. **e9671a3** "Implement Int, Real, and Math" (+ df3c696
   folded) -> "`Int` structure", "`Real` structure", "`Math`
   structure" — after absorbing the completion folds above, so
   each structure appears exactly once.
3. **2b5401a** "Implement String and Char" -> "`String`
   structure", "`Char` structure" (+ 91c9877). Apply plan H3
   here: `isASCIIChar` -> `isAsciiChar` in the birth commit.

## Target narrative (68 commits)

The full target sequence. Sources are old SHAs; `+` marks a
fold or squash; splits are marked. Where no source is listed
in a chapter preamble, the commit is a straight keep+reword.

### A. Foundations (1-8)

1. Add development notes
   (725d1b5+f3338b3+abb1800+bf51141, final agents.md)
2. Configure lint and CI (166cd41+15c0f6b)
3. Add project lint test (75f1314)
4. Add lexer (2bacca0+dea12c9)
5. Add statement splitter (ee99558)
6. Add kernel, script runner, and command (fa71834)
7. Add idempotent script harness (ab4d780)
8. Add convergence and corpus tooling (d90c1b6+b3981b4)

### B. Parser (9-16)

9. Add AST and parse-tree dump (5e9cba9)
10. Add Sys.parseTree and the first expression parser
    (7b84ea5+cc7a802 — the parse.smli scaffold rides along;
    see D1)
11. Parse literals, tuples, lists, records, selectors
    (3f99c35+ecda769)
12. Parse operators (d5c729c)
13. Parse if, fn, case, and patterns (d06140f)
14. Parse declarations, types, and datatypes (d3e8d4b+1af0b2e)
15. Parse queries (854b96a+8c477ce — large but one story)
16. Parser hardening: error messages and spans (f9f841b)

### C. Type inference (17-25)

17. Add interned type representations
    (4f64aef+aaaa320's code — plan H1)
18. Add the unifier (6e986aa)
19. Add the Core IR and the type resolver (124975c)
20. Add ':t' statements: type queries without evaluation
    (a3b50d2)
21. Type tuples, records, and selectors; check field
    references (9c29395+5425fbb)
22. Type case, fun, and val rec (2becda7)
23. Type datatypes and constructor patterns (07bc031)
24. Load built-in signatures; check numeric operators
    (45c1360+a23166e)
25. Type annotations (2f0f116)

### D. Evaluator (26-37)

26. Add the runtime value representation (597f6ec)
27. Add Code, Frame, and the compiler; statements evaluate
    (b40dd68)
28. Add the printing engine
    (a61f86a+c822550 [D5]+bc72280 [check blame])
29. Evaluate case, if, and negation (81887a0)
30. Evaluate functions and closures (068f718)
31. Evaluate recursive functions (a3b6419)
32. Evaluate val patterns, currying, and sequential
    declarations (f7ad0a0+3cadbd2+559699a)
33. Evaluate list and string operators, equality, comparison
    (2e52f08+52c77aa)
34. Report compile and runtime errors
    (006fdc9+dbba9d4+d3ea485)
35. Evaluate datatype values (d5dd000)
36. Pull the first evaluation corpus (98ae2a7)
37. Stress-test closures and recursion across statements
    (c807959)

### E. Shell (38-42)

38. Add the built-in registry (250f2d2)
39. Add structure test scripts and their lint check (7f318ae)
40. Parse the command line (91510f1+8ee993a)
41. Run .smli scripts through the shared runner (809be0d)
42. Add the interactive shell (6d0eb63)

### F. Standard library (43-67)

One commit per structure — basics + completion + its
`built-in/<name>.smli` fully grown at that tree. `Sys` leads
(D6 resolved: yes) so every structure file is born with its
`Sys.set` preamble; `StringCvt` precedes the numerics because
`fmt` needs the radix.

43. `op` sections (32efad8)
44. `Sys` structure (754ad47)
45. Tabular output mode (94cf2f3)
46. `General` structure (split of 65ed679)
47. `Bool` structure (split of 65ed679)
48. `Order` structure (split of 65ed679; may prove to belong
    to the registry commit — verify at split time)
49. `Option` structure (split of 65ed679)
50. `StringCvt` structure (48028e1)
51. `Int` structure (split of e9671a3, +4566d38)
52. `Real` structure (split of e9671a3, +df3c696+18da1c0
    +5932f2b+fde0c8c+e74bbc4+01da4de)
53. `Math` structure (split of e9671a3, +a9f397f)
54. `Word` structure (624ece5)
55. `String` structure (split of 2b5401a; plan H3 rename here)
56. `Char` structure (split of 2b5401a, +91c9877)
57. `List` structure (aeee2ec+a179e37)
58. `Bag` structure (53a686c)
59. `Vector` structure (8007e80+c936db1)
60. `ListPair` structure (1f1f567)
61. `Either` structure (a98bb4c)
62. `Fn` structure (b5500e2)
63. `Range` structure (46d43b6)
64. `Variant` structure (1475068)
65. `Time` structure (077883a)
66. `Date` structure (a408d00)
67. Postfix method calls (c6b80f7 — its corpus regeneration
    adds the postfix hunks across every structure file)

### G. Queries (68)

68. Type 'from' queries over lists (db9935f) — the history
    ends where phase G of the plan begins.

Notes on achievability:
- Corpus files regenerate at each commit (the corpus rule), so
  preambles and cross-structure lines appear exactly when their
  support exists; per-commit corpus content will differ from
  the old history's, and the convergence gate in the verify
  pass confirms monotone convergence.
- Riskiest moves, to test first in stage 1: `Sys` ahead of the
  numerics (44), `StringCvt` ahead of `Int`/`Real` (50), the
  type-wrapping fold (28/D5), and the NaN-comparison fold (33).
- Chapters B-E keep the original commit order; only messages
  and the listed folds change there.

## Comment sweep (plan H2)

Inventory at `15c0f6b`: 64 comment lines mentioning java or rust
across ~15 source files (`shell/sys.go` 6, `shell/kernel.go` 6,
`compile/typeresolver.go` 4, `shell/args.go` 3, `parse/string.go`
3, `ast/unparse.go` 3, rest 1-2 each) and 9 test files; zero
"task N" / "phase N" references in code. Rule: reword to state
the behavior ("ints are 32-bit"), never the source ("as java
does"). Exempt: `etc/*.py`, `agents.md` (their subject *is* the
java comparison).

Execute after the replay: edit at tip, review the diff as one
unit, then `git absorb --base <first new commit>` + `git rebase
--autosquash` to distribute hunks to birth commits; hunks absorb
refuses fall forward into the nearest later commit touching the
file.

## Open decisions

- D1. `parse.smli`: still present and still the parser era's
  only test vehicle. Keep it through history (recommended), or
  add a tip commit deleting it once corpus files cover the
  parser?
- D2. `(#1)` suffixes: drop everywhere (recommended; the PR
  links the issue) or keep?
  [Resolved: no issue references anywhere in messages —
  subjects or bodies, `#1` or `hydromatic/morel#NNN`.]
- D3. agents.md on main: keep as "Add development notes"
  (recommended) or leave on the branch like plan.md?
- D4. Lexer: squash 2bacca0 + dea12c9 into one "Add lexer"
  (recommended), or keep the two slices?
- D5. c822550 type wrapping: fold into the printing engine
  (recommended — printing is born java-identical, per the day-1
  rule) at the cost of corpus regeneration in the commits
  between; or keep standalone.
- D6. `Sys` position: the resequencing keeps it mid-block; it
  could move to the head of the structure era so every
  `built-in` file carries its `Sys.set` preamble from birth
  (more regeneration churn, slightly nicer files).
  [Resolved in the target narrative: `Sys` leads the era.]

## Runbook

0. **Preliminaries**: settle D1-D6 (narrative assumes the
   recommendations); commit pending plan.md/reorg.md edits to
   `1-bootstrap`; `git tag bootstrap-orig 1-bootstrap`;
   `brew install git-absorb`.
1. **Probe the risky moves** on throwaway branches, each a
   short range-rebase + fullMake: Sys-before-numerics,
   StringCvt-before-Int/Real, the type-wrapping fold (D5), the
   NaN-comparison fold. A failed probe demotes that ledger
   entry to a standalone commit.
2. **Stage 1** (`1-bootstrap-reordered`): generate the todo
   from the target narrative — all 200 commits, reordered;
   splits marked `edit`; the 104 plan-only commits moved to
   the end as one block in original internal order (conflict-
   free, and stage 2's drop becomes "delete the tail"). Run
   via GIT_SEQUENCE_EDITOR. Conflicts: corpus rule for
   `.smli`; `issue.md` resolves as deleted. Then the tip
   check (`git diff 1-bootstrap 1-bootstrap-reordered`), the
   one expensive verification (`git rebase -f main -x
   'fullMake --no-clean && python3 etc/check-convergence.py
   HEAD'`), and the review artifact (`git range-diff
   bootstrap-orig 1-bootstrap-reordered`).
3. **Stage 2** (`main-candidate`): generated todo — drop the
   plan-only tail, `fixup` the adjacent fold groups and split
   remnants; then one message-rewrite pass from a
   subject-map table (reviewed first). Tree-preserving, so
   only a tip fullMake + `git diff` against stage 1.
4. **Comment sweep**: one reviewed edit at tip (64 lines);
   `git absorb --base <first new commit>` + autosquash;
   leftovers assigned by hand; one more `rebase -x fullMake`
   replay (comments can break lint).
5. **Final audit**: log vs the 68 ledger entries;
   `git diff bootstrap-orig main-candidate` empty except
   plan.md, reorg.md, issue.md, comment sweep. Repoint the
   PR; keep `bootstrap-orig`.

Review points for a human: the three `add -p` splits, the
comment-sweep diff, and the message map. Everything else is
scripted.
