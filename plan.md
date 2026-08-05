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
# Release Morel Go v0.8.0 — plan

Plan for issue
[#3](https://github.com/hydromatic/morel-go/issues/3),
the first release of Morel Go.

## 0. Status

Branch `3-release`, five commits, each `fullMake`-green:

1. `ea09476` Rename `HISTORY.md` to `CHANGELOG.md`
2. `caf4ad7` Add `docs/howto.md`
3. `9920b84` Expand `README.md`
4. `b66ed38` Shorten the shell banner
5. `19186b7` Release v0.8.0 (#3)

Commit 5 carries the full commit log: the output of
`~/dev/share.0/tools/relNotes` over the whole history (180 commits),
grouped into Morel Java's `HISTORY.md` sections. There is no
'Component upgrades' section; the module has no dependencies.

`relNotes` itself was changed, in `~/dev/share.0` (uncommitted
there):

* Linkify `hydromatic/morel#420` as `morel#420`,
  `hydromatic/morel-rust#123` as `rust#123`, and
  `hydromatic/morel-go#7` as `#7`.
* Linkify a bare `#35` against the repository `relNotes` is run in,
  read from `remote.origin.url`. In morel-java this reproduces the
  `([#317](...))` form its `HISTORY.md` already uses.
* Add a `WIDTH` environment variable. `relNotes` folded at 81, and
  then indents continuation lines by 2, so a bullet could reach 83
  columns; this project's lint allows 80. Run `WIDTH=78 relNotes`.

The linkify rules run in a second `sed`, after the existing
`s!\(http.*\), !...!` rule, which would otherwise chew up the URLs
they generate. The bare-`#` rule runs in a third, and matches only a
`#` preceded by a space or `(`, so it does not re-match the `#` in a
`[java#420]` label.

Commit 4 was not in the original plan: the banner read `morel-go
version 0.1.0 (go version go1.26.5, ...)`, which says "go" twice
because `runtime.Version()` already carries it, and did not mark the
version with Go's 'v'. It now reads `morel-go v0.8.0 (go1.26.5,
darwin/arm64)`. `productVersion` itself keeps the bare `0.8.0`,
because it also backs the `productVersion` property; the banner
supplies the 'v'.

Remaining: tag `v0.8.0`, push, warm the proxy, create the GitHub
release, run the manual verification in `docs/howto.md`, close #3.
See §7.6.

Follow-up, not part of this release: `corpus_test.go` has
`maxParseGap = 415`, but the corpus now has only 241 parse failures,
232 of which are `:t` harness directives. The constant should come
down.

## 0.1. Where we were

* `internal/shell/sys.go` has `productVersion = "0.1.0"`.
* `README` (the plain in-distribution file) says "release 0.1.0" and
  refers to a "Building" section of `README.md` that does not exist
  (the section is called "Download and build").
* `HISTORY.md` has a stub `0.1.0 / 2025-xx-xx  Initial release`, and
  its release-tag link points at `hydromatic/morel`, not
  `hydromatic/morel-go`.
* `README.md` is minimal: badge, title, four-line build recipe, "More
  information". No requirements, no install, no shell transcript, no
  documentation links, no status.
* There is no `docs/` directory, so no `howto.md` recording the
  release procedure (Java and Rust both have one).
* No release workflow; `.github/workflows/go.yml` builds and tests
  only.
* No test asserts the version string: `testdata/script/built-in/sys.smli`
  checks only that the `productVersion` and `banner` properties exist.
  So bumping the version does not churn reference output.

## 1. Version numbering

Java tags releases `morel-0.8.0`; Rust tags `morel-rust-0.2.0`. Go
cannot follow that pattern: for a module rooted at the repository
root, the module proxy only recognises tags of the exact form
`vX.Y.Z`. So:

* the git tag is **`v0.8.0`**, not `morel-go-0.8.0`;
* the GitHub release title can still read "Morel Go 0.8.0";
* `productVersion` stays `"0.8.0"` (no `v`), so the banner reads
  `morel-go version 0.8.0 (go version go1.25.x, darwin/arm64)`,
  parallel to Java and Rust.

Jumping 0.1.0 → 0.8.0 aligns the number with Morel Java 0.8.0, which
is what the issue asks for and what the ~85%-compatibility claim is
measured against. Say so explicitly in the release notes, so the jump
does not read as a mistake.

Go has no `-SNAPSHOT` convention. Two options for what `productVersion`
holds between releases:

1. **Keep the constant** and bump it in the release commit, as Java and
   Rust do. Simple and deterministic; a build from `main` claims to be
   the last release.
2. **Derive it from `runtime/debug.ReadBuildInfo()`**, falling back to
   the constant. Correct for `go install ...@v0.8.0`, but a plain
   `go build` and every `go test` binary reports `(devel)`, which makes
   the banner non-deterministic.

Recommend option 1 for this release; revisit if we start shipping
binaries where provenance matters.

## 2. `README.md` — expand

Model on Morel Rust's `README.md`, which is the closest sibling in
maturity, plus the Go-specific parts. Sections, in order:

1. **Badges.** Keep the build badge; add
   [pkg.go.dev](https://pkg.go.dev/badge/github.com/hydromatic/morel-go.svg)
   and, optionally, Go Report Card. (Java has Maven Central and
   javadoc badges; these are the Go equivalents.)
2. **Title and tagline.** "A functional query language, implemented in
   Go" — Java and Rust both name the implementation language.
3. **Requirements.** Go 1.25 or higher (from `go.mod`).
4. **Get Morel.** Three routes:
   * `go install github.com/hydromatic/morel-go/cmd/morel@latest` —
     the idiomatic Go install, and the reason `cmd/morel` must stay
     outside `internal/`;
   * download a binary from the
     [releases page](https://github.com/hydromatic/morel-go/releases)
     (only if we do §6);
   * clone and `go build ./cmd/morel`, as today.
5. **Run the shell.** A transcript with the real banner, `"Hello,
   world!";`, and ctrl-D to exit; then `-e` examples copied in spirit
   from Rust's README:
   ```
   $ ./morel -e "1 + 2"
   $ ./morel -e "from i in [1,2,3] where i > 1"
   ```
   and `use "script.sml";` from Java's README. Verify each transcript
   by running it.
6. **Documentation.** Go has no `docs/` of its own, so link out:
   Morel Java's [language reference](https://github.com/hydromatic/morel/blob/main/docs/reference.md)
   and [query reference](https://github.com/hydromatic/morel/blob/main/docs/query.md),
   the change log, and `testdata/script` (suggest `built-in.smli` and
   `relational.smli`, as Rust and Java each suggest a script).
7. **Status.** Short, honest, and the part that carries the release:
   ~85% compatible with Morel Java and Morel Rust, linked to the
   [blog post](http://blog.hydromatic.net/2026/08/01/morel-on-go.html);
   a bulleted list of what works and what does not. Do *not* copy
   Java's exhaustive built-in inventory — it is long and would rot.
   Derive the gaps from the corpus: which scripts in `testdata/script`
   are skipped or partially disabled, and which of `lib/*.sig` are
   unimplemented.
8. **More information.** Keep as-is; the existing list is right.

Deliberately *not* copying Java's "Extensions" section (postfix
labels, relational extensions, ~200 lines). It belongs in a language
reference, and Go's README should link to Java's rather than fork it.

## 3. Change log

Two decisions:

* **Rename `HISTORY.md` to `CHANGELOG.md`**, as Rust did in its 0.2.0
  release commit. `CHANGELOG.md` is the near-universal convention in
  the Go ecosystem, and it makes Go consistent with Rust. Update the
  links in `README.md` and `README`.
* **Fix the tag URLs.** The template and the stub entry both point at
  `hydromatic/morel`; they must point at `hydromatic/morel-go`, and
  the tag path is `/releases/tag/v0.8.0`.

Content: replace the `0.1.0` stub with a `0.8.0` entry in the shape of
Rust's 0.2.0 entry — prose, not a change-by-change list, since there is
no previous release to diff against:

* one paragraph on what Morel Go is and the compatibility strategy
  (copy `.smli` scripts from Morel Java and get them to pass);
* the ~85% figure, and the version-number alignment with Morel Java
  0.8.0;
* key features: parser, Hindley-Milner type resolution, evaluation,
  relational `from`, Datalog, the built-in library (count the
  structures in `lib/`), the shell (`-e`, scripts, `.smli` idempotent
  mode);
* known gaps: no line editing or history in the REPL (see §5), plus
  whatever the corpus survey in §2.7 turns up.

Keep the commented-out `0.x.0` template at the top for next time.

## 4. `README` (plain distribution file)

* 0.1.0 → 0.8.0;
* `HISTORY.md` → `CHANGELOG.md`;
* fix the dangling "Building" section reference to name the real
  section.

## 5. `docs/howto.md` — new

Java and Rust each have one; Go has none. Create `docs/howto.md` with
two sections.

**How to make a release (for committers)** — the Go equivalent of
Java's Maven dance, which is much shorter:

```bash
go mod tidy
fullMake            # build, lint, tests, all platforms green in CI
git clean -nx       # sandbox is clean
git tag v0.8.0
git push origin v0.8.0
GOPROXY=proxy.golang.org go list -m github.com/hydromatic/morel-go@v0.8.0
```

with notes that: version numbers live in `internal/shell/sys.go`,
`README` and `README.md`, and the copyright year in `NOTICE`; a
published tag must never be moved, because the checksum database pins
it (publish `v0.8.1` instead); and the GitHub release is created from
the tag with the `CHANGELOG.md` text.

Also a "cleaning up after a failed release attempt" note: deleting a
tag is safe *only* before anything has fetched it through the proxy;
afterwards, use `retract` in `go.mod`.

**Manually verify a release (for committers)** — adapt Java's and
Rust's checklist. Their checklists are largely about `~/.morel/history`
and up-arrow recall, which Go does not have, so ours is shorter: the
banner reports the release version, a statement evaluates, `-e` works,
a script file runs, and ctrl-D exits cleanly.

## 6. Release automation (optional, decide before starting)

Go's convention for a CLI is to attach cross-compiled binaries to the
GitHub release. GoReleaser plus a tag-triggered workflow is the
standard way, and gives darwin/linux/windows × amd64/arm64 archives
with a checksum file for roughly 40 lines of config.

Against: it is new machinery on release day, and `go install` already
covers anyone who has Go. Recommend **deferring** binaries to 0.9.0
and shipping 0.8.0 as a tag plus `go install`, unless you want the
release to be usable without a Go toolchain.

If we do it: `.goreleaser.yaml` + `.github/workflows/release.yml`
triggered on `v*` tags, `builds.main: ./cmd/morel`, ldflags stamping
`internal/shell.productVersion` — which would settle §1 in favour of a
`var` rather than a `const`.

## 7. Order of work

Each step is its own commit, each green under `fullMake`.

1. Survey the corpus and `lib/*.sig` for the status section and the
   known-gaps list. Output: notes, no commit.
2. Rename `HISTORY.md` → `CHANGELOG.md`; fix the `hydromatic/morel`
   URLs to `hydromatic/morel-go`. Mechanical, separable, and worth
   landing on its own.
3. Expand `README.md` (§2). Verify every transcript by running it.
4. Add `docs/howto.md` (§5).
5. Release commit: `productVersion` → `0.8.0`, `README` → 0.8.0,
   `NOTICE` copyright year, `CHANGELOG.md` 0.8.0 entry dated.
6. Tag `v0.8.0`, push, warm the proxy, create the GitHub release,
   run the manual verification from §5, close #3.

Steps 2–4 are independent of the release itself and can land now;
step 5 is the one that must be last.

## 8. Decisions

Settled 2026-08-04:

* **Rename `HISTORY.md` to `CHANGELOG.md`.** Yes, as §3.
* **Binaries.** Defer to a later release, as §6. So 0.8.0 ships as a
  tag plus `go install`; no `.goreleaser.yaml`, no release workflow,
  and `productVersion` stays a `const` (§1, option 1).
* **REPL line editing and history.** Not a blocker. Record it in
  `CHANGELOG.md` as a known gap, and keep the §5 verification
  checklist short accordingly.
