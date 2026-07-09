# Modernizer agent notes

## Do not remove syntax features to work around compiler bugs

The modernizer exists to rewrite Go source to use fork syntax (`T!`, `err!`, `errors.Base`, nilable pointers, etc.). When a modernized program fails to compile:

1. **Fix the compiler or type checker** in `go/` — that is the right place for bugs in `ForceExpr`, `ResultType`, escape analysis, or lowering.
2. **Fix the modernizer rewrite** if it emits invalid AST or applies a transform incorrectly.
3. **Do not** disable or gate off a syntax feature (e.g. turning off `err!` body rewrites) just to make builds pass. Temporary flags that hide rewrites are not acceptable workarounds.

If minio or another large tree exposes a compiler ICE, reproduce with a small case, fix `go/src/cmd/compile`, rebuild the toolchain (`./make.bash` in `go/src`), then rerun modernize and verify the build.

## Workflow

- Run tests: `go test .` from `modernize/`
- Rebuild tool: `go build -o ../go/bin/modernize .`
- Typical target: reset tree, `modernize <path>`, `go build ./...` with `GOROOT` pointing at the fork

## Major passes

- `(T, error)` → `T!` signatures; `if err != nil` → `call()!` / `err!` in bodies
- `fmt.Errorf` → `errors.New`; custom errors → `errors.Base` / `errors.NewCustom`
- Unused `fmt` import pruning after error rewrites
