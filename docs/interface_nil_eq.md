# Interface == nil comments

Bow changes `iface == nil` so typed nil pointers in interfaces compare equal to nil. Code that relied on the old behavior may need manual review.

The `interface_nil_eq_comments` step (and `go fix -interfacenileq`) inserts a comment above each interface-typed `== nil` / `!= nil`:

```go
//FIXME: Make sure still works after interface == nil change.
if err != nil {
```

No automatic rewrite is applied.

Config key: `interface_nil_eq_comments` (default `true`).

See [interface_nil_eq.md](../../go/doc/new_features/interface_nil_eq.md) in the Go fork docs.
