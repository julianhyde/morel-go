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
# Attributes and doc comments — plan

Propagating one morel-java commit:

* `ed919a2b` Add attributes and doc comments (morel#369)

It was developed on `julianhyde/369-attribute`, whose eighteen
commits are the better guide to the order the work goes in; the
commit on `main` is their squash, and `attribute.smli` has grown
since under morel#368 and morel#399.

## 0. Where we are

* `attribute.smli` is one of two java files morel-go has never
  pulled: 300 lines, of which `pull-passing` reproduces
  **10/55 statements (18%)** — everything that happens not to
  contain an attribute.
* `internal/parse/lexer.go` **discards** comments. `skipTrivia`
  loops over whitespace, `(*)` line comments and `(* ... *)` block
  comments and returns only when the next token starts. A doc
  comment has to survive that.
* `[` lexes as `token.LBracket` with no lookahead, so `[@` is a
  new longest-match case.
* `Sys.parseTree` (`ast.Dump`) is the only consumer of any of
  this, and its output strings must match java exactly. They are
  taken from the corpus, not invented.
* A signature's dump is its *unparsed source*
  (`UnparseSignatureDecl`), so attributes on specs are an
  unparser change, not a dumper change.

## 1. What an attribute is

OCaml's notation, where the `@`-count is the scope:

| form | attaches to | AST |
|---|---|---|
| `[@a]` | the preceding *atom* | `AttributedExp` |
| `[@@a]` | a declaration, before or after | `AttributedDecl` |
| `[@@@a]` | nothing; stands alone | `FloatingAttrDecl` |
| `[@a]` after a type atom | that type | `AttributedType` |

A name may be dotted (`[@foo.bar]`). A payload is an expression,
or a type after `:` (`[@a: int list]`). `(** text *)` immediately
above a declaration desugars to `[@@doc "text"]`.

Attributes are **inert**: recorded in the AST, surfaced by
`Sys.parseTree`, and consumed by nothing. Typing and evaluation
see through them.

Attachment is at *atom* level, which the corpus pins:

```
1 + 2 [@a]    (plus (int_literal 1) (attributedExp (int_literal 2) …))
(1 + 2) [@a]  (attributedExp (plus …) (attribute @a))
f x [@a]      (apply (id f) (attributedExp (id x) …))
```

## 2. The hard part is the lexer

Everything else is routine. Two problems, both in lexing:

**Doc comments must survive.** java gets this from javacc: `(**`
opens a lexical state, the whole comment becomes a `DOC_COMMENT`
special token, and the parser walks `getToken(1).specialToken` to
find the ones preceding a declaration. Nested `(*` and `(*)` push
further states that deliberately emit *no* token on close, so
their text accumulates into the enclosing doc comment verbatim —
`(** outer (* one (* two *) *) end *)` keeps its inner markers.

morel-go has no trivia channel. The plan: `skipTrivia` recognizes
`(**`, captures the comment verbatim (tracking nesting, and
treating `(*)` inside as running to end-of-line), and appends it
to a `pendingDocs` field on the lexer. The parser drains that
field when it begins a declaration. A doc comment that precedes
anything else is dropped, as java's is.

Then ocamldoc's `-stars` rule on the captured text: strip
`^[ \t]*\*` from each line, keeping any whitespace *after* the
star.

**`(**)` and `(***)` are ordinary comments**, not doc comments —
longest-match special cases that must beat the `(**` opener. The
corpus pins both, including `"a" ^ (**) "b"` evaluating to `"ab"`.

## 3. Steps

Each step is a commit that passes `fullMake` on its own. Only the
last carries the `Propagates` line, because only it brings the
corpus to java's.

1. **Lexer tokens and expression attributes.** `[@`, `[@@`,
   `[@@@` (longest first). `Attribute` and `AttributedExp` nodes,
   their ops, and their dump forms. Absorb `[@…]*` after an
   expression atom. Dotted names; expression payloads.
2. **Declarations and floating attributes.** `[@@a]` before and
   after a declaration, merged in source order;
   `[@@@a]` as a declaration of its own. Inert in the compiler:
   `AttributedDecl` types and evaluates as its inner declaration,
   `FloatingAttrDecl` binds nothing and echoes nothing.
3. **Doc comments.** `(** … *)` → `[@@doc "…"]`, with nesting,
   verbatim capture, star-stripping, and the `(**)`/`(***)`
   special cases.
4. **Types and signature specs.** `[@a]` at type-atom level and
   `:` type payloads; attributes on `val`/`type`/`exception`
   specs and floating attributes inside `sig … end`, which means
   teaching the unparser to write them.
5. **Pull `attribute.smli`.**

## 4. Decisions and risks

* **`[@` versus `[` then `@`.** `@` is the append operator, and
  no expression begins with it, so a longest-match `[@` token is
  unambiguous. A list of the operator is written `[op @]`, which
  still lexes.
* **Where the doc comment attaches.** java collects the doc
  comments preceding the *next token* at the point a declaration
  starts. Draining a lexer field at the same point gives the same
  answer, but the two differ if a doc comment sits between the
  attributes and the declaration; the corpus does not test that,
  so follow java and drain at declaration start.
* **`dummy.smli`** (22 lines, unrelated) stays unpulled.
* **`Sys.parseTree` output is the whole contract.** Every dump
  string comes from `attribute.smli`; where the corpus is silent,
  prefer whatever java's `AstDumper` does over inventing a shape.
