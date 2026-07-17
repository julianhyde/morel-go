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

# `Real.floor` etc. saturate on infinity and NaN instead of raising

This is an addendum to the earlier report that `Real.floor`,
`Real.ceil`, and `Real.round` gave wrong results for negative reals
(they used `Math.round`). That is now fixed. A related divergence
from the Standard ML Basis remains, for the same four functions.

## Summary

The int-valued `Real.floor`, `Real.ceil`, `Real.trunc`, and
`Real.round` convert an infinity or a NaN to a saturated integer.
The Standard ML Basis requires them to raise instead: `Overflow`
when the value is out of the `int` range (including an infinity),
and `Domain` for a NaN.

## Wrong results

Verified against SML/NJ. morel-go raises the same exceptions SML/NJ
does.

| Expression | morel-java | Correct |
| --- | --- | --- |
| `Real.floor Real.posInf` | `2147483647` | raises `Overflow` |
| `Real.ceil Real.negInf` | `~2147483648` | raises `Overflow` |
| `Real.trunc Real.posInf` | `2147483647` | raises `Overflow` |
| `Real.floor nan` | `0` | raises `Domain` |
| `Real.round nan` | `0` | raises `Domain` |

These appear in `src/test/resources/script/built-in/real.smli`, in
the block

```
fun f x = (Real.floor x, Real.ceil x, Real.trunc x, Real.round x);
```

whose expected output encodes the saturated values, e.g.

```
f Real.posInf;
> val it = (2147483647,2147483647,2147483647,2147483647) : int * int * int * int
f nan;
> val it = (0,0,0,0) : int * int * int * int
```

## Suggested fix

Raise `Overflow` when the rounded value falls outside `int`, and
`Domain` when the argument is a NaN, rather than returning the
saturated cast.
