# modernize — syntax change examples

Before/after examples for every transformation **modernize** applies today when run on a module tree.

Run with Bow as `GOROOT`. Output uses fork-only syntax (`T!`, `expr!`, `*T?`).

---

## Summary

| Category | Config flag | Active by default? |
|----------|-------------|----------------------|
| `go.mod` `nilable_pointers warnings` | `nilable_pointers_go_mod` | Yes |
| `*_gen.go` nilable disable directive | `nilable_pointers_gen_disable` | Yes |
| Nilable pointer type inference (`*T` / `*T?`) | `nilable_pointers_annotate` | Yes |
| `err!` propagation / error-return cleanup | `err_bang_body` | Yes |
| `(T, error)` → `T!` signature conversion | `err_bang_signatures` | Yes |
| `fmt.Errorf` → `errors.New` | `fmt_errorf_to_errors_new` | Yes |
| Custom errors → `errors.Base` | `errors_base_embed` (+ related flags) | Yes |

See **[config.md](config.md)** for the full flag list and how to disable individual passes.

---

## 1. Module setup

### `go.mod` — enable nilable pointers

**Before**

```go
module example.com/app

go 1.27
```

**After**

```go
module example.com/app

go 1.27

nilable_pointers warnings
```

### `*_gen.go` — disable nilable pointers

Prepends a file-level directive so generated code is not annotated.

**Before** (`foo_gen.go`)

```go
package p

type Gen struct {
    P *int
}
```

**After**

```go
//go:nilable_pointers disable
package p

type Gen struct {
    P *int
}
```

---

## 2. Nilable pointers (`*T` / `*T?`)

Controlled by `nilable_pointers_annotate` (default **on**). When enabled, modernize rewrites pointer types in source:

- `*T?` when nil can flow in (nil assignment, `return nil`, nil arguments, lookup helpers that may return nil)
- `*T` (strict) when no nil evidence is found, or when mixed non-nil returns dominate

`nilable_pointers_go_mod` and `nilable_pointers_gen_disable` run separately (module + generated-file setup).

**Before**

```go
func Find(id int) *User {
    return nil
}

func Use(u *User) {
    var p *User
    p = nil
    _ = u
    _ = p
}
```

**After** (when nilable inference runs)

```go
func Find(id int) *User? {
    return nil
}

func Use(u *User?) {
    var p *User?
    p = nil
    _ = u
    _ = p
}
```

Respects `//go:nilable_pointers disable` … `end` regions (types inside are unchanged).

---

## 2.5 `(T, error)` → `T!` signature conversion

Runs **first** in each file, before `err!` cleanup. Converts eligible `(T, error)` result types to `T!` on function declarations and interface methods, then rewrites common `if err != nil` patterns inside those bodies to `call()!`.

**Before**

```go
func Load() (*File, error) {
    if err := open(); err != nil {
        return nil, err
    }
    return f, nil
}

type Saver interface {
    Save() error
}
```

**After**

```go
func Load() *File! {
    open()!
    return f
}

type Saver interface {
    Save() error
}
```

Does not convert when the value result is a map or channel type, or when the function is not a simple `(T, error)` pair. Interface methods with a lone `error` result are unchanged.

---

## 3. Error propagation (`err!`)

Applies inside functions that return `T!` or `error` — including functions just converted from `(T, error)` in §2.5.

### 3.1 Drop redundant `nil` on success (`T!` functions)

**Before**

```go
func Load() *File! {
    f := open()
    return f, nil
}
```

**After**

```go
func Load() *File! {
    f := open()
    return f
}
```

### 3.2 `return zero, err` → `return err`

**Before**

```go
func Save() error! {
    if err := write(); err != nil {
        return nil, err
    }
    return nil
}
```

**After**

```go
func Save() error! {
    if err := write(); err != nil {
        return err
    }
    return nil
}
```

### 3.3 `if err := fn(); err != nil { return err }` → `fn()!`

**Before**

```go
func Run() error! {
    if err := step(); err != nil {
        return err
    }
    return nil
}
```

**After**

```go
func Run() error! {
    step()!
    return nil
}
```

### 3.4 `if err := fn(); err != nil { return zero, err }` → `fn()!` (`T!` functions)

**Before**

```go
func Read() []byte! {
    if err := open(); err != nil {
        return nil, err
    }
    return data, nil
}
```

**After**

```go
func Read() []byte! {
    open()!
    return data
}
```

### 3.5 `val, err := fn(); if err != nil { return zero, err }` → `val = fn()!`

**Before**

```go
func Parse() int! {
    n, err := strconv.Atoi(s)
    if err != nil {
        return 0, err
    }
    return n, nil
}
```

**After**

```go
func Parse() int! {
    n := strconv.Atoi(s)!
    return n
}
```

### 3.6 `err := fn(); err!` (standalone propagation)

**Before**

```go
func Run() error! {
    err := ping()
    err!
    return nil
}
```

**After**

```go
func Run() error! {
    ping()!
    return nil
}
```

### 3.7 Remove unused `var err error`

**Before**

```go
func f() error! {
    var err error
    return g()!
}
```

**After**

```go
func f() error! {
    return g()!
}
```

### Conditions (when `err!` is **not** applied)

- `err` is used again later in the same block
- `if` has an `else` branch
- `err` name is not the checked variable
- Inner `func` literals are not entered (patterns inside closures are skipped)

---

## 4. Structured errors

### 4.1 `fmt.Errorf` → `errors.New`

Only when the format is a **string literal** and contains **no `%w`**.

**Before**

```go
return fmt.Errorf("something failed")
return fmt.Errorf("bad %s", name)
return fmt.Errorf("wrap: %w", err)
```

**After**

```go
return errors.New("something failed")
return errors.New("bad %s", name)
return fmt.Errorf("wrap: %w", err) // unchanged — chains errors
```

Skipped when:

- Format is a variable (cannot see `%w` at compile time)
- File imports a third-party `…/errors` package and not std `errors`

Adds `import "errors"` when needed.

---

### 4.2 Custom error types — two paths

A struct is a custom error candidate when it:

1. Defines `Error() string`, and
2. Does not already embed `errors.Base`

#### Path A — **embed-only** (message-only types)

**Criteria (all required):**

| Rule | Detail |
|------|--------|
| `Error()` is trivial | Single `return` of `e.msg` or `fmt.Sprintf("…", e.msg)` |
| One message field | String field named `msg`, `message`, `text`, `description`, `detail`, or `reason` |
| No other fields | No `Code`, `cause`, `err error`, embeds, etc. |
| Field name | `err string` is **not** treated as a message field |

**Before**

```go
type AppError struct {
    msg string
}

func (e AppError) Error() string {
    return e.msg
}

func (e *AppError) setMsg(msg string) {
    e.msg = msg
}

func fail() error {
    return AppError{msg: "oops"}
}
```

**After**

```go
import "errors"

type AppError struct {
    errors.Base
}

func fail() error {
    return errors.NewCustom[AppError]("oops")
}

func describe(e AppError) string {
    return e.Base.Message
}
```

Removed: `msg` field, `Error()`, `setMsg`. Field reads on typed receivers/parameters (`e.msg`) become `e.Base.Message`.

**With `fmt.Sprintf` in `Error()`:**

**Before**

```go
type RemoteErr struct {
    msg string
}

func (e RemoteErr) Error() string {
    return fmt.Sprintf("remote: %s", e.msg)
}

func makeRemote(msg string) error {
    return RemoteErr{msg: msg}
}
```

**After**

```go
func makeRemote(msg string) error {
    return errors.NewCustom[RemoteErr]("remote: %s", msg)
}
```

#### Path B — **extra fields** (domain data stays)

**Before**

```go
type MyErr struct {
    Code int
}

func (e MyErr) Error() string {
    return fmt.Sprintf("code %d", e.Code)
}

func newMyErr(code int) MyErr {
    return MyErr{Code: code}
}
```

**After**

```go
type MyErr struct {
    errors.Base
    Code int
}

func (e MyErr) Error() string {
    return fmt.Sprintf("code %d", e.Code)
}

func newMyErr(code int) MyErr {
    e := MyErr{Code: code}
    errors.InitCustom(&e.Base, "%s", e.Error())
    return e
}
```

Kept: all domain fields, custom `Error()`. Added: `errors.Base`, `InitCustom` at constructor `return`.

#### Generic `setMsg` factory

**Before**

```go
func Error[T ErrorConfig, PT interface {
    *T
    setMsg(string)
}](format string, vals ...any) T {
    pt := PT(new(T))
    pt.setMsg(fmt.Sprintf(format, vals...))
    return *pt
}
```

**After**

```go
func Error[T ErrorConfig, PT interface {
    *T
}](format string, vals ...any) T {
    return *errors.NewCustom[T](format, vals...)
}
```

---

## 5. What modernize does **not** fix (broken references)

Modernize is **not** a full semantic refactor. Some uses of removed fields or old construction forms may still break the build.

### Rewritten (embed-only types)

| Pattern | Example | After |
|---------|---------|-------|
| Field reads (typed ident) | `e.msg` on `AppError` receiver/param | `e.Base.Message` |
| `return` composite | `return AppError{msg: "x"}` | `return errors.NewCustom[AppError]("x")` |
| Assign composite | `e := AppError{msg: "x"}` | `e := errors.NewCustom[AppError]("x")` |
| `var` init | `var e = AppError{msg: "x"}` | `var e = errors.NewCustom[AppError]("x")` |

Requires a **keyed** message field (`msg:`, `message:`, etc.). The ident must be a receiver, parameter, or short-var assignment of the embed-only type.

### Not rewritten

| Pattern | Example | Result after embed-only pass |
|---------|---------|------------------------------|
| Untyped field reads | `x.msg` when `x` is `interface{}` / unknown | **Compile error** — field gone |
| Positional composite literals | `ErrInvalidARN{s}` | **Broken** — `s` binds to `errors.Base`, not `ARN` |
| Helper methods using `msg` | `Msg()`, `setMsg()`, `Clone()` | **Broken** — `setMsg` removed; others not updated |
| Cross-package literals | `otherpkg.Err{msg: "x"}` | Skipped (selector type) |
| `err string` / `err error` fields | `type E struct { err error }` | Stays on **has-extra** path; field kept |

### Construction (has-extra types)

For **has-extra** types, only a top-level `return` of a composite literal (or `&composite`) in a function body is expanded to:

```go
e := MyErr{Code: code}
errors.InitCustom(&e.Base, "%s", e.Error())
return e
```

Inline `return` expressions used as sub-expressions, struct literals in assignments, and positional literals are not fixed.

---

## 6. Recommended workflow

1. Run modernize on a package or module.
2. **`go build`** — expect failures where:
   - Positional error literals need field names (`{ARN: s}` not `{s}`)
   - `msg` fields are still referenced on untyped or cross-package values
   - `err!` was applied where the non-error result is used afterward (compiler ICE in some cases)
   - Unused `fmt` imports remain (modernize does not prune imports)
3. Fix remaining sites by hand, or extend modernize (see gaps above).

See **[config.md](config.md)** to turn off passes you do not want (e.g. `"errors_base_embed": false`).
