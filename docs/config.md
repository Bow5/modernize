# modernize — configuration

Each rewrite pass can be turned on or off with `modernize.json`. **All flags default to `true`.** Omit a key to keep the default.

## Config file location

modernize loads the first file it finds, in order:

1. Path from `MODERNIZE_CONFIG` (if set)
2. `{target}/modernize.json` — e.g. `minio/modernize.json` when you run `./modernize ./minio`
3. `{repo}/modernize/modernize.json` — walks up from the current working directory (useful when developing the tool itself)

If no file is found, all passes run (same as the bundled `modernize/modernize.json`).

When a file is loaded, modernize prints `using config <path>` to stderr.

## Example

Copy the template from the modernize repo:

```json
{
  "nilable_pointers_go_mod": true,
  "nilable_pointers_gen_disable": true,
  "nilable_pointers_annotate": true,
  "err_bang_signatures": true,
  "err_bang_body": true,
  "fmt_errorf_to_errors_new": true,
  "errors_base_embed": true,
  "errors_base_setmsg": true,
  "errors_base_positional_composites": true,
  "errors_base_message_field_refs": true,
  "errors_base_usages": true,
  "shorthand_types": true,
  "step_commits": true,
  "remove_nil_receiver_guards": true,
  "optional_method_chains": true
}
```

To disable only `errors.Base` embedding:

```json
{
  "errors_base_embed": false
}
```

## Flags

| Flag | Default | What it does |
|------|---------|--------------|
| `nilable_pointers_go_mod` | `true` | Add `nilable_pointers enable` to `go.mod` when missing |
| `nilable_pointers_gen_disable` | `true` | Prepend `//go:nilable_pointers disable` to `*_gen.go` files |
| `nilable_pointers_annotate` | `true` | Rewrite pointer types to `*T` / `*T?` from nil-flow evidence (see [examples.md §2](examples.md#2-nilable-pointers-t--t)) |
| `err_bang_signatures` | `true` | Convert `(T, error)` → `T!` on functions and interface methods; rewrite matching `if err != nil` bodies in newly converted functions |
| `err_bang_body` | `true` | `expr!` propagation, drop `return v, nil`, fix error returns, remove unused `err` in `T!` / `error` functions |
| `fmt_errorf_to_errors_new` | `true` | Rewrite literal `fmt.Errorf` (no `%w`) to `errors.New` |
| `errors_base_embed` | `true` | Embed `errors.Base` on custom error types (remove message-only fields / `Error()` for embed-only types; prepend `Base` for has-extra types) |
| `errors_base_setmsg` | `true` | Rewrite generic factories constrained with `setMsg(string)` to `errors.NewCustom[T](...)` |
| `errors_base_positional_composites` | `true` | Turn positional composites like `&Err{s}` into keyed literals for has-extra error types |
| `errors_base_message_field_refs` | `true` | Rewrite `.msg` (etc.) field reads to `.Base.Message` after embed-only migration |
| `errors_base_usages` | `true` | Rewrite constructions (`NewCustom`, constructor returns, assign/`var` composites) |
| `shorthand_types` | `true` | Rewrite `type T struct` / `type I interface` to `struct T` / `interface I` |
| `step_commits` | `true` | When the target is in a git or hg repo, run each pass separately and commit its changes (formatting first, then nil receivers, nilable pointers, `T!` / `!`, structured errors, shorthand) |
| `remove_nil_receiver_guards` | `true` | Remove `if recv == nil { return … }` guards in pointer-receiver methods (unreachable in Bow — [nil receivers](../../go/doc/new_features/nil_receivers.md)) |
| `optional_method_chains` | `true` | Add `?.` only where a method had `if recv == nil { return nil / zero }` — behaviorally equivalent to the old guard |

## Step commits

With `step_commits` enabled (default), modernize runs passes in [Bow README](https://github.com/Bow5/Bow) migration order and creates one VCS commit per pass when the target tree is inside a **git** or **hg** repository:

1. `gofmt` formatting only
2. Nil receivers (remove `if recv == nil { … }` guards, optional `?.` method chains)
3. Nilable pointers (`go.mod`, `*_gen.go` directives, `*T` / `*T?` annotations)
4. `T!` / `!` error handling
5. Structured errors (`errors.Base`, `fmt.Errorf` → `errors.New`, …)
6. Struct and interface shorthand

If no git/hg root is found, modernize runs all enabled passes in one go without committing.

Disable step commits to restore the previous single-pass behavior:

```json
{
  "step_commits": false
}
```

## Typical combinations

**Err! and structured errors only** (no pointer annotations):

```json
{
  "nilable_pointers_annotate": false
}
```

**Nilable module setup only** (enable in `go.mod` + gen directives, no source rewrites):

```json
{
  "nilable_pointers_annotate": false,
  "err_bang_signatures": false,
  "err_bang_body": false,
  "fmt_errorf_to_errors_new": false,
  "errors_base_embed": false,
  "errors_base_setmsg": false,
  "errors_base_positional_composites": false,
  "errors_base_message_field_refs": false,
  "errors_base_usages": false
}
```

**Custom errors without Base embedding** (keep `fmt.Errorf` → `errors.New` and usage rewrites if desired):

```json
{
  "errors_base_embed": false,
  "errors_base_setmsg": false,
  "errors_base_message_field_refs": false
}
```

## Related docs

- [examples.md](examples.md) — before/after for each pass
- [../modernize.json](../modernize.json) — canonical template in the repo
- [../README.md](../README.md) — usage and requirements
