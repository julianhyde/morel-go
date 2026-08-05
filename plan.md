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
# Shell editing, history and highlighting — plan

Propagating two morel-java commits:

* `b5aba63c` Shell command history (morel#414)
* `ee0b2a51` Syntax highlighting in the shell (morel#413)

Morel Rust did the same pair as `4209c1e6` (rustyline) and
`8a438100` (highlighting), and its commit messages are the closest
guide to what a port has to decide.

## 0. Where we are

* `internal/shell/repl.go` is 104 lines: a `bufio.Scanner` loop that
  accumulates lines until `Split` yields a complete statement. No
  editing, no history, no color. Arrow keys emit raw escape
  sequences.
* `lib/sys.sig` already declares `Sys.colorSchemes` and
  `Sys.deduceColorScheme`; both are "not implemented" placeholders,
  pinned by `built-in/sys.smli`.
* `sysProps` already accepts `colorScheme` and `terminalBackground`;
  both are unset and nothing reads them.
* `args.go` has no `--color-scheme` flag.
* `go.mod` has **no dependencies at all**. Either feature ends that;
  see §1.
* `built-in/sys.smli` is go's largest single divergence from java
  (166 lines against java's 537). Some of that gap is these two
  commits.

So the signatures and the property names are already in place. What
is missing is a terminal front end to hang them on.

## 1. The library question

Morel Java gets all of this from JLine; Morel Rust from rustyline.
Go has no equivalent in the standard library — `golang.org/x/term`
has a `Terminal` type with basic editing and in-memory history, but
no hook to color the line as you type, so it cannot do morel#413.

Measured on 2026-08-04 by resolving each module against
`proxy.golang.org` and counting what `go get` adds to a bare
`go.mod`:

| library | latest | released | modules added | highlighting | multiline | history file |
|---|---|---|---|---|---|---|
| `reeflective/readline` | v1.3.0 | 2026-07-05 | 3 | `SyntaxHighlighter` | `AcceptMultiline` | `NewHistoryFromFile` |
| `elk-language/go-prompt` | v1.4.0 | 2026-05-12 | 9 | lexer-based | yes | yes |
| `knz/bubbline` | untagged | 2025-12-01 | 27 | yes | yes | yes |
| `ergochat/readline` | v0.1.3 | 2024-09-09 | 3 | `Painter` | no | `HistoryFile` |
| `chzyer/readline` | v1.5.1 | 2022-07-15 | 2 | `Painter` | no | yes |
| `peterh/liner` | v1.2.2 | 2022-01-14 | 2 | no | no | yes |
| `golang.org/x/term` | v0.45.0 | 2026-07-08 | 2 | no | no | in-memory |

Notes on the ones that lose:

* `chzyer/readline` is the name everyone reaches for first, and it
  has been untouched since 2022. `ergochat/readline` is the
  maintained fork, but its last release is also two years old, and
  its `Painter` rewrites the buffer rather than styling it, which is
  awkward for per-token color.
* `knz/bubbline` is Bubbletea-based and does everything, but 27
  modules for a REPL prompt is a poor trade in a project that has
  none today.
* `elk-language/go-prompt` is the maintained fork of the archived
  `c-bata/go-prompt`. It is viable; it costs three times the
  dependencies and renders the prompt differently from a classic
  readline, which would show up as scrollback differences against
  Morel Java and Morel Rust.

**Recommendation: `github.com/reeflective/readline` v1.3.0.**

It is the only candidate that is both actively maintained (last
release a month ago) and cheap (3 modules: itself,
`github.com/rivo/uniseg`, `golang.org/x/sys`). More to the point,
its two callbacks line up with code morel-go already has:

```go
// SyntaxHighlighter is a helper function to provide syntax highlighting.
SyntaxHighlighter func(line []rune) string

// AcceptMultiline ... returns true if the line is deemed complete.
AcceptMultiline func(line []rune) (accept bool)
```

`AcceptMultiline` can be `Split` plus `Blank` — exactly the
completeness test `repl.go` already runs. That is better than
Morel Rust's arrangement: rustyline reads one line at a time and
Morel Rust accumulates statements itself, then pushes the whole
statement into history as one entry. With `AcceptMultiline` the
library does the accumulating, so a multi-line statement is one
history entry *and stays editable as a unit* — you can go back up
into an earlier line of the statement you are typing.

It also reads `.inputrc`, so Emacs and Vi bindings come free.

## 2. Terminal background (needed by morel#413 only)

Both siblings query the terminal with OSC 11 and fall back to
`COLORFGBG`, then to dark. Morel Rust used the `termbg` crate.

Use `github.com/muesli/termenv` (v0.16.0, 6 modules) for the query,
which is what Morel Rust did with `termbg`.

But do the luma conversion by hand, again as Morel Rust did: it "is
a three-line weighted sum matching Morel-Java's exact Rec. 601
formula and 0.5 threshold, so it is not worth a color crate that
would diverge". `termenv.BackgroundColor()` returns a
`color.Color`; format it into the exact `rgb:RRRR/GGGG/BBBB` string
the other two implementations put in `terminalBackground`, rather
than taking `HasDarkBackground()` and losing the string.

Fallbacks, in order, as Morel Java: the OSC 11 answer, then
`COLORFGBG`, then dark. (Morel Rust stops after the first and turns
highlighting off; follow Java, since we have the property anyway.)

## 3. Phase 1 — command history and editing (morel#414)

New package `internal/shell/terminal`, mirroring Morel Rust's
`shell::terminal::Shell`. Do **not** put readline inside
`Repl`: `Repl` must stay a plain `io.Reader`/`io.Writer` loop,
because `TestReplBanner` drives it with a `strings.Reader`, and
`--terminal=dumb` needs it.

* `cmd/morel/main.go` already branches on `isTerminal(os.Stdin)`.
  Extend that branch: a tty and not `--terminal=dumb` goes to the
  new package; everything else keeps today's path.
* Prompts `- ` and `= `, as now.
* History file `~/.morel/history-go`. It must **not** be
  `~/.morel/history`: that is JLine's, and Morel Rust already took
  `history-rust` for the same reason — the formats are not
  compatible.
* Create `~/.morel` if absent; warn rather than fail if it cannot be
  created. (Both siblings are explicit about this.)
* Ctrl-C abandons the statement in progress; Ctrl-D ends the
  session.
* A blank line keeps the `- ` prompt.

Deliverables: `go.mod`/`go.sum` gain their first entries;
`docs/howto.md`'s "Manually verify a release" section grows the
history checks that Morel Java's and Morel Rust's have and ours
currently says we do not need; `README.md`'s status section loses
the "no command-line editing and no history" bullet.

## 4. Phase 2 — syntax highlighting (morel#413)

### The scanner: a separate one, as both siblings have

Neither sibling highlights with its parser's lexer.

* **Java** highlights with `util/MorelHighlighter`, which predates
  #413 and exists to render Morel as HTML for the docs. #413
  extended it (+26 lines) and added `ShellHighlighter` (161 lines)
  to adapt it to JLine's `Highlighter` interface.
* **Rust** wrote `src/shell/highlight.rs`, 430 lines with its own
  `tokenize()`. It borrows exactly one thing from the parser,
  `syntax::parser::is_reserved_word`, and its doc comment calls
  itself "a simplification of Morel-Java's `MorelHighlighter`, which
  additionally distinguishes bound variables and function names that
  all render unstyled here anyway".

So: **a new `internal/shell/highlight.go`**, with its own scanner,
borrowing `token.Lookup` for the keyword set — the exact analogue of
Rust borrowing `is_reserved_word`. Do not touch
`internal/parse/lexer.go`.

This also disposes of what I had thought was the hard part. A
highlighting scanner must emit comments as tokens (the parser's
`skipTrivia` discards them) and must never fail on incomplete input,
because it runs on every keystroke. Retrofitting both onto the
parser's lexer would be invasive; writing a scanner that is tolerant
from the first line is not.

**Fold in java `dd3065d1` (morel#415) from the start.** It fixes a
crash found a few days after #413: the highlighter runs on every
keystroke, so the buffer passes through the state `"\` the moment
someone types a string escape, and `scanString` skipped the escape
with `i += 2` with no bounds check. Rust has not propagated it.
Write the bounds check in, and take its regression tests for the
intermediate states of typing an escaped string.

### Then

* Token categories: keyword, symbol, numeric, constant, string,
  comment, type variable; plain identifiers keep the terminal
  default.
* Built-in schemes "dark", "light", "none". Java loads them from
  `.properties` resources; go should embed them the way `lib/` is
  embedded (`lib.FS`).
* Implement `Sys.colorSchemes ()` and `Sys.deduceColorScheme ()`,
  replacing the placeholders, and unpin them in
  `built-in/sys.smli`.
* Add the `--color-scheme` flag to `args.go` and its usage text.
* Wire `SyntaxHighlighter`, reading the scheme on each keystroke so
  `Sys.set ("colorScheme", ...)` takes effect immediately.
* Java's commit also reclassified word, real and scientific literals
  as numeric; check go's lexer agrees.

## 5. Order of commits

Each its own commit, each `fullMake`-green.

1. `internal/shell/highlight.go`: the scanner and its categories,
   tolerant of incomplete input (including `"\`), with tests. Pure
   addition, no dependency, no color yet.
2. `internal/shell/terminal` on reeflective/readline: editing,
   prompts, Ctrl-C/Ctrl-D. First dependency.
3. History file `~/.morel/history-go`, plus the `docs/howto.md`
   verification steps.
4. Color schemes: embed dark/light/none, `Sys.colorSchemes`,
   `Sys.deduceColorScheme`, unpin `built-in/sys.smli`.
5. Terminal background: `termenv` OSC 11 query, `COLORFGBG`
   fallback, `terminalBackground` property.
6. Wire the highlighter; `--color-scheme` flag; `README.md` status.

Commit 1 is independent of everything; commits 2–3 are phase 1 and
can land and ship on their own, without any of 4–6.

## 6. Commit-message convention

`etc/check-convergence.py` reads a propagation ledger from the git
log, matching `[Pp]ropagates\s+\S*\s*commit\s+([0-9a-f]{7,40})`.

Morel Rust has 130 such lines, and its form matches that regex
exactly:

```
Propagates hydromatic/morel#413 commit ee0b2a51
```

with 16 of the 130 omitting the issue number, for a java commit that
had none:

```
Propagates hydromatic/morel commit <sha>
```

Use that form. morel-go's regex is right; what is wrong is its two
existing lines, which read `Propagate: hydromatic/morel#420 commit
d6b04acd` — "Propagate:" with a colon, where the regex wants
"Propagates". `--ledger` therefore reports "(0 propagation
commits)". Reword those two lines (they are history, so this means
leaving them and accepting two missing ledger entries, or a rebase
that is not worth it) and use Rust's wording from here on.

## 7. Decisions

Settled 2026-08-05:

* **Dependencies are allowed.** So: `reeflective/readline` for the
  line editor (3 modules) and `muesli/termenv` for the OSC 11 query
  (6 modules). morel-go goes from 0 to 8 modules, and `go.sum`
  appears. The next release notes get a 'Component upgrades'
  section.
* **A separate scanner**, `internal/shell/highlight.go`, as both
  siblings have. `internal/parse/lexer.go` is not touched.
* **`Propagates hydromatic/morel#NNN commit <sha>`**, as Morel Rust
  writes it.

Still open:

* Tab completion. Morel Rust explicitly deferred it ("the
  `Completer`/`Highlighter` traits ... are left as follow-ups").
  Defer too.
