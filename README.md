# modernize

A small source rewriter for [BetterGo](https://github.com/BetterGo3/BetterGo) that uses `T!` result types and `!` error propagation.

It walks a directory of Go files and updates common error-handling patterns to the shorter fork syntax. Files are rewritten in place.

## What it changes

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

The same rewrite applies to `err := fn(); if err != nil { return err }` and `_, err := fn(); …`.

It skips `vendor/`, `.git/`, nested function literals, and cases where transforming would break the code (for example, when `err` is still needed later).

## Requirements

Build and run with **this fork’s Go** as `GOROOT` — the output uses `T!` and `expr!`, which standard Go does not accept.

## Usage

```bash
export GOROOT=/path/to/go-fork

go build -o modernize .

# default root is "minio"; pass a path to scan another tree
./modernize ./path/to/package
```

Each modified file path is printed; a summary count is written to stderr.

## License

MIT — see [LICENSE](LICENSE).
