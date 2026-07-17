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

# `Real.floor`, `Real.ceil`, and `Real.round` give wrong results

## Summary

In morel-java, `Real.floor`, `Real.ceil`, and `Real.round` are
implemented with `Math.round`, which rounds a real to the *nearest*
integer (ties toward positive infinity). That is not what any of the
three should do:

- `Real.floor r` should be the largest integer `<= r`;
- `Real.ceil r` should be the smallest integer `>= r`;
- `Real.round r` should round to the nearest integer, ties to
  **even** (per the Standard ML Basis and IEEE 754).

As a result they return incorrect values for many arguments —
`Real.floor` and `Real.ceil` for negative reals with a fractional
part, and `Real.round` at every half-integer where round-to-even and
round-half-up disagree.

## Root cause

`src/main/java/net/hydromatic/morel/eval/Codes.java`:

```java
// REAL_CEIL
public Integer apply(Float f) {
  if (f >= 0) {
    return Math.round(f);      // should be (int) Math.ceil(f)
  } else {
    return -Math.round(-f);
  }
}

// REAL_FLOOR
public Integer apply(Float f) {
  if (f >= 0) {
    return -Math.round(-f);    // should be (int) Math.floor(f)
  } else {
    return Math.round(f);
  }
}

// REAL_ROUND
public Integer apply(Float f) {
  return Math.round(f);        // should round ties to even
}
```

`Math.round(x)` is `floor(x + 0.5)`, so none of these computes a true
floor, ceiling, or round-to-even.

## Wrong results

Verified against SML/NJ (the correct column). morel-rust and
morel-go both produce the correct column.

| Expression | morel-java | Correct |
| --- | --- | --- |
| `Real.floor ~2.25` | `~2` | `~3` |
| `Real.floor ~2.5`  | `~2` | `~3` |
| `Real.floor ~3.5`  | `~3` | `~4` |
| `Real.ceil ~1.75`  | `~2` | `~1` |
| `Real.ceil ~2.5`   | `~3` | `~2` |
| `Real.ceil ~3.5`   | `~4` | `~3` |
| `Real.round 0.5`   | `1`  | `0`  |
| `Real.round 2.5`   | `3`  | `2`  |
| `Real.round ~3.5`  | `~3` | `~4` |

These appear in `src/test/resources/script/built-in/real.smli`, in
the block

```
fun f x = (Real.floor x, Real.ceil x, Real.trunc x, Real.round x);
```

whose expected output encodes the wrong values, e.g.

```
f ~2.5;
> val it = (~2,~3,~2,~2) : int * int * int * int
```

where the correct result is `(~3,~2,~2,~2)`.

(The real-valued `Real.realFloor`, `Real.realCeil`, and
`Real.realRound` are correct — they use `Math.floor`, `Math.ceil`,
and `Math.rint` — so only the integer-valued three are affected.)

## Suggested fix

```java
// REAL_CEIL
return (int) Math.ceil(f);
// REAL_FLOOR
return (int) Math.floor(f);
// REAL_ROUND
return (int) Math.rint(f);   // rint rounds ties to even
```

with the existing overflow handling retained.
