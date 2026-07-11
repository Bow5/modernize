# Interpolated strings (`interpolated_strings`)

Double-quoted strings are **interpolated**; backtick raw strings are not.

## Syntax

```go
price := 12.5
msg := "Price is {price:.2f}"

literal := "use \\{braces\\} for literal braces"
raw := `not {interpolated}`
```

- `{name}` inserts the value of `name` (default format `%v`).
- `{name:.2f}` uses printf-style formatting (`%.2f`).
- `\{` and `\}` produce literal `{` and `}`.

## Modernizer rewrites

1. **Escape literal braces** in `"..."` strings that are not interpolations.
2. **`fmt.Sprintf`** with a string-literal format → interpolated string.
3. **String concatenation** with `+` → single interpolated string when straightforward.

| Before | After |
| ------ | ----- |
| `fmt.Sprintf("part.%d", n)` | `"part.{n:d}"` |
| `"hello " + name + "!"` | `"hello {name}!"` |
| `"json: {}"` | `"json: \\{\\}"` |

This pass runs as its own step commit (`interpolated_strings`).

See [syntax.md](../../go/doc/new_features/syntax.md#interpolated-strings) in the Go fork docs.

[`go fix`](https://pkg.go.dev/golang.org/x/tools/cmd/fix) applies the same rewrites via the `interpolatedstrings` analyzer.
