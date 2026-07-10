# modernize

A source rewriter for [Bow](https://github.com/Bow5/Bow) that migrates code to fork syntax: `T!` result types, `expr!` error propagation, nilable pointer types (`*T` / `*T?`), and struct/interface shorthand (`struct T { … }`, `interface I { … }`).

It walks a module or directory of Go files and rewrites them in place. Files are processed per package so nilable-pointer inference can use call sites within the package.

See **[docs/examples.md](docs/examples.md)** for a full before/after catalog, including what reference rewrites are *not* performed.

See **[docs/config.md](docs/config.md)** to enable or disable individual rewrite passes via `modernize.json`.

## What it changes

### Nilable pointers

Adds `nilable_pointers enable` to `go.mod` when missing, then annotates pointer types:

- `*T?` when nil can flow in (nil assignment, `return nil`, `nil` arguments, zero-init `var p *T`, `json:",omitempty"` pointer fields)
- `*T` (strict) when no nil evidence is found in the package — preferred wherever inference allows

Respects `//go:nilable_pointers disable` … `end` regions (those types are left unchanged).

**Example:**

```go
func Find(id int) *User {
    return nil
}
func Conn() *DB {
    if db == nil {
        panic("uninitialized")
    }
    return db
}
```

becomes:

```go
func Find(id int) *User? {
    return nil
}
func Conn() *DB {
    if db == nil {
        panic("uninitialized")
    }
    return db
}
```

(`db` would be `*DB?` if it is zero-initialized or assigned `nil` elsewhere.)

### Error propagation (`T!` / `expr!`)

**Drop redundant `nil` on success** (in `T!` functions):

```go
return value, nil  →  return value
```

**Propagate errors with `!`** (in `error` or `T!` functions, when `err` is not used again in the block):

```go
if err := fn(); err != nil {
    return err
}
```
→
```go
fn()!
```

Skips `vendor/`, `.git/`, `testdata/`, and `_test.go` files.

### Struct and interface shorthand

Rewrites named struct and interface type declarations:

```go
type Person struct { Name string }     →  struct Person { Name string }
type Stringer interface { String() string }  →  interface Stringer { String() string }
```

Skips parenthesized `type ( … )` groups, type aliases, generics, and non-struct/interface types. Also rewrites local `type` declarations inside function bodies.

### Nil receivers

Removes unreachable `if recv == nil { … }` guards in pointer-receiver methods (always unreachable in Bow — see [nil receivers](https://github.com/Bow5/Bow/blob/master/doc/new_features/nil_receivers.md)). Adds `?.` on call chains only when the callee had such a guard (equivalent short-circuit), never merely because a return type is nullable.

### Structured errors (`errors.Base`)

**`fmt.Errorf` → `errors.New`** when the format string is a literal with no `%w` (no error chaining):

```go
return fmt.Errorf("something failed")       →  return errors.New("something failed")
return fmt.Errorf("bad %s", name)           →  return errors.New("bad %s", name)
return fmt.Errorf("wrap: %w", err)          →  unchanged
```

**Custom error types** that define `Error() string` get an `errors.Base` embed:

- **Message-only types** (single `msg` / `message` field, `Error()` returns it): field and `Error()` are removed; constructions become `errors.NewCustom[YourError](...)`.
- **Types with extra domain fields**: `errors.Base` is added; constructor returns are rewritten to call `errors.InitCustom(&e.Base, "%s", e.Error())`.

```go
type AppError struct { msg string }
func (e AppError) Error() string { return e.msg }
func fail() error { return AppError{msg: "oops"} }
```
→
```go
type AppError struct { errors.Base }
func fail() error { return errors.NewCustom[AppError]("oops") }
```

## Requirements

Build and run with **Bow** as `GOROOT` — the output uses `T!`, `expr!`, and `*T?`, which standard Go does not accept.

## Usage

```bash
export GOROOT=/path/to/go-fork
export PATH=$GOROOT/bin:$PATH

go build -o modernize .

# default root is "."; pass a path to scan another tree
./modernize ./path/to/module

# optional: per-target config at ./path/to/module/modernize.json
# or MODERNIZE_CONFIG=/path/to/modernize.json
```

With `step_commits` enabled (default), each rewrite pass is applied and committed separately when the target is a git or hg repository. See [docs/config.md](docs/config.md#step-commits).

Each modified file path is printed; a summary count is written to stderr.

## License

MIT — see [LICENSE](LICENSE).
