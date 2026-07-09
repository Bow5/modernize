# modernize

A source rewriter for [Better](https://github.com/Better14/Better) that migrates code to fork syntax: `T!` result types, `expr!` error propagation, and nilable pointer types (`*T` / `*T?`).

It walks a module or directory of Go files and rewrites them in place. Files are processed per package so nilable-pointer inference can use call sites within the package.

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

## Requirements

Build and run with **Better** as `GOROOT` — the output uses `T!`, `expr!`, and `*T?`, which standard Go does not accept.

## Usage

```bash
export GOROOT=/path/to/go-fork
export PATH=$GOROOT/bin:$PATH

go build -o modernize .

# default root is "."; pass a path to scan another tree
./modernize ./path/to/module
```

Each modified file path is printed; a summary count is written to stderr.

## License

MIT — see [LICENSE](LICENSE).
