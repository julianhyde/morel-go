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
# Development notes for Claude

## Before every commit

Run `fullMake --no-clean` from the repository root and confirm it
passes before you commit. For morel-go, `fullMake` builds
(`go build`), lints (`go vet`), checks formatting (`gofmt`), and
runs the tests (`go test`). Never commit if it fails; fix the code
until it is green.

## Repositories

Morel has implementations in Go, Rust, and Java. The repositories
are [morel-go](https://github.com/hydromatic/morel-go),
[morel-rust](https://github.com/hydromatic/morel-rust), and
[morel-java](https://github.com/hydromatic/morel).

morel-java is the reference implementation; morel-go and morel-rust
are ports of it. A common task is to propagate a change from one
repository into another — usually from morel-java (or morel-rust)
into morel-go.

> morel-go is early-stage, so some paths referenced below (the
> shared `.smli` script tests, `etc/check-convergence.py`) mirror
> morel-rust and will appear here as the port matures. Follow the
> same process and layout when you add them.

## Propagation process

### Reading the source change

Read the commit message, the code changes, and especially the test
changes in `src/test/resources/script/*.smli`. Those `.smli` tests
are shared between the projects — the morel-go test files use the
same `.smli` format and often the same content as morel-java and
morel-rust.

### Implementing the feature

Implement the feature in Go. Then enable any disabled test regions
by either:

- Removing the surrounding `(* ... *)` block-comment delimiters, or
- Replacing `set("mode","validate")` / `set("mode","evaluate")`
  brackets with ordinary enabled code.

A propagation must move every changed `.smli` section literally,
adding any that do not yet exist — do not adapt or skip a section
because the implementation is hard.

### Verifying

Run `fullMake --no-clean` and confirm it passes. Gate convergence
with `etc/check-convergence.py HEAD`, which fails if any `.smli`
file diverged further from morel-java. Both it and `fullMake` must
pass before committing.

### Commit message

Use the original morel-java commit summary as the first line of the
commit message. Append a blank line and then a propagation line
that cites the morel-java issue and commit SHA:

```
Join (hydromatic/morel#72)

Add clauses to `from` to support inner joins. We continue to
allow comma joins, but only up until the first step (`where`,
`join`, `group`, `yield` or `order` keyword). After that,
commas would introduce ambiguity when combined with the
commas in `group` or `compute`.

We will add outer joins (`left`, `right`, `full` keywords)
in a later commit.

Propagates hydromatic/morel#72 commit ab102172
```

If a morel-java commit uses the old `[MOREL-NNN]` format, convert
it to the new format `hydromatic/morel#NNN`. For example,
`[MOREL-72] Join` becomes `Join (hydromatic/morel#72)`.

## Regular development

Regular features (originating in morel-go) use a commit message
that references the morel-go issue:

```
Add `banner`, `productName`, `productVersion` properties (#30)

Add three new read-only properties to the Sys structure.

Fixes #30
```

## Quick experiments

To run a single Morel expression from the shell, pass `-e` (or
`--eval`, or `--eval=EXPR`) to the binary; the result is printed
and the process exits. Useful when reproducing a bug from a
one-liner without needing a script file:

```
$ go build ./cmd/morel
$ ./morel -e '1 + 2'
val it = 3 : int
$ ./morel --eval='from x in [1,2,3] yield x * 2'
val it = [2,4,6] : int list
```
