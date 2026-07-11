# Shorthand literals (`shorthand_literals`)

Rewrites long-form composite literals to shorthand syntax when the explicit type prefix matches what shorthand inference would pick.

## When a rewrite happens

| Long form | Shorthand | Rewritten? |
| --------- | --------- | ---------- |
| `[]int{1, 2, 3}` | `[1, 2, 3]` | Yes — untyped integer literals default to `int` |
| `[]int64{1, 2, 3}` | (unchanged) | No — literals infer `int`, not `int64` |
| `[]User{User{"bob"}}` | `[User{"bob"}]` | Yes — element type matches |
| `[]IUser{User{"bob"}}` | (unchanged) | No — `IUser` is an interface, not the default for `User{"bob"}` |
| `map[string]int{"a": 1}` | `{"a": 1}` | Yes |
| `map[string]int64{"a": 1}` | (unchanged) | No |
| `set.Of("a", "b")` | `{"a", "b"}` | Yes |
| `set.Of[int64](1, 2)` | (unchanged) | No |

The same rule applies to maps and sets. Empty literals (`[]int{}`, `map[string]int{}`, `set.Of()`) are always rewritten.

See also [syntax.md](../../go/doc/new_features/syntax.md#array-map-and-set-literals) in the Go fork docs.

[`go fix`](https://pkg.go.dev/golang.org/x/tools/cmd/fix) applies the same rules via the `shorthandliterals` analyzer.
