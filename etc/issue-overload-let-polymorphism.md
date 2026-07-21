# Let-polymorphism and qualified types for overloaded identifiers

## Summary

An overloaded identifier (declared with `over` and extended with `val inst`) cannot yet be used at an abstract type, nor abstracted over: a function that applies an overloaded name to one of its parameters should acquire a *qualified type* recording the overload constraint, in the style of "A Second Look at Overloading" (Odersky, Wadler, Wehr, 1995), but instead the constraint is either lost or resolved too eagerly.

This is the feature that the commented-out `demo` example in `src/test/resources/script/overload.smli` is waiting on, and it is the natural completion of the `over`/`inst` mechanism.

## Background

Morel's overloading follows "A Second Look at Overloading". An `over foo` declaration introduces an overloaded name, and each `val inst foo = e` adds an instance whose type must be a function type differing from the others in its argument type. At an application `foo a`, the instance is selected by the type of `a`.

The paper's central contribution is that overload resolution need not happen at the application site. If the argument's type is not yet known — because it is a type variable bound by an enclosing lambda or `let` — the constraint "there is an instance of `foo` accepting this type" is *recorded in the type* as a qualified (predicated) type, exactly as a Haskell type class constraint is. Resolution is deferred until the type variable is instantiated, possibly at a distant call site, or propagated outward into the enclosing declaration's type.

Without this, an overloaded name can only be used where its argument type is already concrete, which excludes the most useful case: writing a polymorphic function in terms of overloaded operations.

## Current behavior

Applying an overloaded name to an argument of concrete type works, and it works even when the same name is applied at several concrete types in one scope, because each application is resolved independently.

Abstracting over an overloaded application does not work. Consider:

```
let
  over foo
  val inst foo = fn i: int => i + 1
  val inst foo = fn b: bool => not b
in
  fn r => foo r
end;
```

The function `fn r => foo r` should have a qualified type, informally `{foo : 'a -> 'b} => 'a -> 'b` — "for any `'a` and `'b` such that `foo` has an instance of type `'a -> 'b`, this is a function from `'a` to `'b`". Instead the constraint is dropped and the parameter type is left unconstrained, so the function is inferred as an ordinary polymorphic `'a -> 'b` (which is unsound: it claims to accept any argument, though only `int` and `bool` have instances).

The `demo` example from the paper, currently commented out in `overload.smli`, is the same limitation with two overloaded names:

```
over first;
over second;
val inst first = fn (x, y) => x;
val inst second = fn (x, y) => y;
val inst first = fn (x, y, z) => x;
val inst second = fn (x, y, z) => y;
val demo = fn r => (second r, first r);
```

The desired type of `demo` is a qualified type over `first` and `second`, something like `(first : 'a -> 'b, second : 'a -> 'c) => 'a -> ('c, 'b)`, so that `demo (3, "four", 5)` and `demo (3, "four")` both resolve at their call sites. Today `demo` cannot be given a type.

## Desired behavior

An overloaded application whose argument type is not yet determined should introduce a deferred constraint rather than failing or resolving to an unconstrained type.

A declaration whose inferred type carries unresolved overload constraints should generalize over them, yielding a qualified type, and should report that qualified type when echoed.

At a later application where the type variable becomes concrete, the recorded constraint should be discharged by selecting the matching instance; if no instance matches, that is a type error at the application site.

An overload constraint that can never be satisfied (no instance shares the required shape) should be reported as an error at the point where the type is generalized, not silently dropped.

## Test cases

The following should type-check and produce the indicated types (`:t` shows the type without evaluating).

```
(*) An overloaded name used at an abstract type yields a qualified type.
:t
let
  over foo
  val inst foo = fn i: int => i + 1
  val inst foo = fn b: bool => not b
in
  fn r => foo r
end;
> val it : {foo : 'a -> 'b} => 'a -> 'b
```

```
(*) The paper's demo: two constraints, resolved at each call site.
let
  over first
  over second
  val inst first = fn (x, y) => x
  val inst second = fn (x, y) => y
  val inst first = fn (x, y, z) => x
  val inst second = fn (x, y, z) => y
  val demo = fn r => (second r, first r)
in
  (demo (3, "four", 5), demo (3, "four"))
end;
> val it = (("four", 3), ("four", 3)) : (string * int) * (string * int)
```

```
(*) No matching instance at a concrete call site is an error.
let
  over foo
  val inst foo = fn i: int => i
in
  foo "x"
end;
> stdIn:... Error: no instance of 'foo' matches argument type 'string'
```

## Notes

The already-working case — an overloaded name applied only at concrete types — is a strict subset of this feature; implementing qualified types should preserve it.

A companion note for the Go port (morel-go): the port currently supports `over`/`inst` only within a single `let` and only where the argument type is concrete, and it lacks let-polymorphism generally, so this feature depends there on first adding generalization of `let`-bound values.
