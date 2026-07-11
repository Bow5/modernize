package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestRemoveNilReceiverGuard(t *testing.T) {
	const src = `package p
type Connection struct{}
type Subroute struct{ Connection *Connection }

func (c *Connection) Subroute(s string) *Subroute {
	if c == nil {
		return nil
	}
	return &Subroute{Connection: c}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := removeNilReceiverGuards(f)
	if n != 1 {
		t.Fatalf("removed %d guards, want 1", n)
	}
	fn := f.Decls[2].(*ast.FuncDecl)
	if len(fn.Body.List) != 1 {
		t.Fatalf("body len %d, want 1 after guard removal", len(fn.Body.List))
	}
	var buf strings.Builder
	if err := format.Node(&buf, fset, f); err != nil {
		t.Fatal(err)
	}
	formatted := collapseBlankLineAfterOpeningBrace([]byte(buf.String()))
	if strings.Contains(string(formatted), "func (c *Connection) Subroute(s string) *Subroute {\n\n\treturn") {
		t.Fatalf("extra blank line after guard removal:\n%s", formatted)
	}
}

func TestRemoveNilReceiverGuardSkipsLoggingBody(t *testing.T) {
	const src = `package p
type T struct{}
func (t *T) M() {
	if t == nil {
		log.Println("bad")
		return
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if removeNilReceiverGuards(f) != 0 {
		t.Fatal("expected guard with extra logging to be kept")
	}
}

func TestOptionalMethodChainsWithNilGuard(t *testing.T) {
	const src = `package p
type M struct{}
func Connection(host string) *M { return nil }
func (m *M) Subroute(path string) *M {
	if m == nil {
		return nil
	}
	return m
}

func f(host, path string) *M {
	return Connection(host).Subroute(path)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := []*ast.File{f}
	guardIdx := buildNilReceiverGuardIndex(files)
	if n := optionalMethodChains(f, files, guardIdx); n != 1 {
		t.Fatalf("rewrote %d chains, want 1", n)
	}
	ret := f.Decls[3].(*ast.FuncDecl).Body.List[0].(*ast.ReturnStmt)
	call := ret.Results[0].(*ast.CallExpr)
	sel := call.Fun.(*ast.SelectorExpr)
	if _, ok := sel.X.(*ast.NullCondExpr); !ok {
		t.Fatalf("expected NullCondExpr on selector, got %T", sel.X)
	}
}

func TestOptionalMethodChainsSkipsWithoutNilGuard(t *testing.T) {
	const src = `package p
type M struct{}
func Connection(host string) *M { return nil }
func (m *M) Subroute(path string) *M {
	return m
}

func f(host, path string) *M {
	return Connection(host).Subroute(path)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := []*ast.File{f}
	guardIdx := buildNilReceiverGuardIndex(files)
	if n := optionalMethodChains(f, files, guardIdx); n != 0 {
		t.Fatalf("rewrote %d chains, want 0 without nil receiver guard", n)
	}
}

func TestOptionalMethodChainsDoubleChain(t *testing.T) {
	const src = `package p
type M struct{}
func Connection() *M { return nil }
func (m *M) MethodA() *M {
	if m == nil {
		return nil
	}
	return m
}
func (m *M) MethodB() *M {
	if m == nil {
		return nil
	}
	return m
}

func f() *M {
	return Connection().MethodA().MethodB()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := []*ast.File{f}
	guardIdx := buildNilReceiverGuardIndex(files)
	if n := optionalMethodChains(f, files, guardIdx); n != 2 {
		t.Fatalf("rewrote %d chains, want 2", n)
	}
	ret := f.Decls[4].(*ast.FuncDecl).Body.List[0].(*ast.ReturnStmt)
	outer := ret.Results[0].(*ast.CallExpr).Fun.(*ast.SelectorExpr)
	if _, ok := outer.X.(*ast.NullCondExpr); !ok {
		t.Fatalf("outer expected NullCondExpr, got %T", outer.X)
	}
	innerCall := outer.X.(*ast.NullCondExpr).X.(*ast.CallExpr)
	inner := innerCall.Fun.(*ast.SelectorExpr)
	if _, ok := inner.X.(*ast.NullCondExpr); !ok {
		t.Fatalf("inner expected NullCondExpr, got %T", inner.X)
	}
}

func TestOptionalMethodChainsOnlyInnerGuard(t *testing.T) {
	const src = `package p
type M struct{}
func Connection() *M { return nil }
func (m *M) MethodA() *M {
	return m
}
func (m *M) MethodB() *M {
	if m == nil {
		return nil
	}
	return m
}

func f() *M {
	return Connection().MethodA().MethodB()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := []*ast.File{f}
	guardIdx := buildNilReceiverGuardIndex(files)
	if n := optionalMethodChains(f, files, guardIdx); n != 1 {
		t.Fatalf("rewrote %d chains, want 1 (MethodB only)", n)
	}
}

func TestNilablePointerChainsOnCallResult(t *testing.T) {
	const src = `package logger

type ReqInfo struct{ BucketName string }

func GetReqInfo() *ReqInfo {
	return nil
}

func (r *ReqInfo) SetTags(k, v string) {}

func Use() {
	GetReqInfo().SetTags("k", "v")
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	retFn := f.Decls[1].(*ast.FuncDecl)
	retFn.Type.Results.List[0].Type = &ast.NilableTypeExpr{X: retFn.Type.Results.List[0].Type, QPos: retFn.Type.Results.List[0].Type.End()}

	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	if n := nilablePointerChains(f, files, returns, nil); n != 0 {
		t.Fatalf("nilablePointerChains rewrote %d, want 0 for method calls", n)
	}
	if n := nilableMethodGuards(f, files, returns, nil); n != 0 {
		t.Fatalf("nilableMethodGuards rewrote %d, want 0 (guards disabled)", n)
	}
}

func TestRangeMethodGuardOnRecvField(t *testing.T) {
	const src = `package p

type Client struct{}
func (c *Client) Alive() <-chan int { return nil }

type Sys struct {
	hc *Client
}

func (sys *Sys) beat() {
	for range sys.hc.Alive() {
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := f.Decls[2].(*ast.GenDecl).Specs[0].(*ast.TypeSpec).Type.(*ast.StructType).Fields.List[0]
	st.Type = &ast.NilableTypeExpr{X: st.Type, QPos: st.Type.End()}

	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	if n := nilableMethodGuards(f, files, returns, nil); n != 0 {
		t.Fatalf("nilableMethodGuards rewrote %d, want 0 for range (guards disabled)", n)
	}
}

func TestNilablePointerChainsOnLocalVar(t *testing.T) {
	const src = `package p

type ReqInfo struct{ BucketName string }

func GetReqInfo() *ReqInfo {
	return nil
}

func f() string {
	reqInfo := GetReqInfo()
	return reqInfo.BucketName
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	retFn := f.Decls[1].(*ast.FuncDecl)
	retFn.Type.Results.List[0].Type = &ast.NilableTypeExpr{X: retFn.Type.Results.List[0].Type, QPos: retFn.Type.Results.List[0].Type.End()}

	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	if n := nilablePointerChains(f, files, returns, nil); n != 1 {
		t.Fatalf("rewrote %d chains, want 1", n)
	}
	fn := f.Decls[2].(*ast.FuncDecl)
	ret := fn.Body.List[1].(*ast.ReturnStmt)
	sel := ret.Results[0].(*ast.SelectorExpr)
	if _, ok := sel.X.(*ast.NullCondExpr); !ok {
		t.Fatalf("expected ?. on reqInfo, got %T", sel.X)
	}
}

func TestNilablePointerChainsSkipsAfterNilCheck(t *testing.T) {
	const src = `package p

type ReqInfo struct{ BucketName string }

func GetReqInfo() *ReqInfo {
	return nil
}

func f() string {
	reqInfo := GetReqInfo()
	if reqInfo == nil {
		return ""
	}
	return reqInfo.BucketName
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	retFn := f.Decls[1].(*ast.FuncDecl)
	retFn.Type.Results.List[0].Type = &ast.NilableTypeExpr{X: retFn.Type.Results.List[0].Type, QPos: retFn.Type.Results.List[0].Type.End()}

	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	if n := nilablePointerChains(f, files, returns, nil); n != 0 {
		t.Fatalf("rewrote %d chains, want 0 after nil check", n)
	}
}

func TestNilablePointerChainsSkipsAfterNilAssign(t *testing.T) {
	const src = `package p

type ReqInfo struct {
	API string
	tags []string
}

func GetReqInfo() *ReqInfo {
	return nil
}

func f() string {
	req := GetReqInfo()
	if req == nil {
		req = &ReqInfo{API: "SYSTEM"}
	}
	return req.API
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	retFn := f.Decls[1].(*ast.FuncDecl)
	retFn.Type.Results.List[0].Type = &ast.NilableTypeExpr{X: retFn.Type.Results.List[0].Type, QPos: retFn.Type.Results.List[0].Type.End()}

	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	if n := nilablePointerChains(f, files, returns, nil); n != 0 {
		t.Fatalf("rewrote %d chains, want 0 after nil assign fallback", n)
	}
}

func TestNilablePointerChainsSkipsInsideNotNilBranch(t *testing.T) {
	const src = `package p

type Checksum struct{ Type int }

func f(res *Checksum) int {
	if res != nil {
		return res.Type
	}
	return 0
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := f.Decls[1].(*ast.FuncDecl)
	fn.Type.Params.List[0].Type = &ast.NilableTypeExpr{X: fn.Type.Params.List[0].Type, QPos: fn.Type.Params.List[0].Type.End()}

	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	if n := nilablePointerChains(f, files, returns, nil); n != 0 {
		t.Fatalf("rewrote %d chains, want 0 inside if res != nil", n)
	}
}

func TestResolveCallResultTypeIgnoresLocalNewForImport(t *testing.T) {
	const src = `package p

import "crypto/sha1"

func New() *T { return nil }

type T struct{}

func f() {
	h := sha1.New()
	h.Write(nil)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	local := buildReturnTypeIndex([]*ast.File{f})
	call := f.Decls[3].(*ast.FuncDecl).Body.List[0].(*ast.AssignStmt).Rhs[0].(*ast.CallExpr)
	if got := resolveCallResultType(local, nil, f, call); got != nil {
		t.Fatalf("expected sha1.New() not to resolve to local New(), got %T", got)
	}
}

func TestNilablePointerChainsOnMultiReturnAssign(t *testing.T) {
	const src = `package p

type Target struct{ Client int; Bucket string }

func head() (tgt *Target, ok bool) { return nil, false }

func use() {
	tgt, ok := head()
	if !ok {
		return
	}
	_ = Target{Client: tgt.Client, Bucket: tgt.Bucket}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	headFn := f.Decls[1].(*ast.FuncDecl)
	headFn.Type.Results.List[0].Type = &ast.NilableTypeExpr{X: headFn.Type.Results.List[0].Type, QPos: headFn.Type.Results.List[0].Type.End()}

	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	if n := nilablePointerChains(f, files, returns, nil); n != 2 {
		t.Fatalf("nilablePointerChains rewrote %d, want 2 field chains", n)
	}
}

func TestNilableMethodGuardInFuncLitAfterOuterNilCheck(t *testing.T) {
	const src = `package p

type Target struct{}
func (t *Target) StatObject() error { return nil }

func work(tgt *Target) {
	if tgt == nil {
		return
	}
	go func() {
		_ = tgt.StatObject()
	}()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := f.Decls[2].(*ast.FuncDecl)
	fn.Type.Params.List[0].Type = &ast.NilableTypeExpr{X: fn.Type.Params.List[0].Type, QPos: fn.Type.Params.List[0].Type.End()}

	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	varIdx := buildFuncVarIndex(files, returns, nil)
	if !isNilablePointerType(varIdx.byFunc[fn]["tgt"]) {
		t.Fatal("expected nilable tgt param")
	}
	// Outer nil check must not suppress guards inside the func lit.
	if n := nilableMethodGuards(f, files, returns, nil); n != 0 {
		t.Fatalf("nilableMethodGuards rewrote %d, want 0 in func lit (guards disabled)", n)
	}
}

func TestNilableMethodGuardPreservesVariadicSpread(t *testing.T) {
	const src = `package p

type Client struct{}
func (c *Client) Alive(servers ...int) <-chan int { return nil }

type Sys struct{ hc *Client }

func (sys *Sys) beat(eps []int) {
	for range sys.hc.Alive(eps...) {
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := f.Decls[2].(*ast.GenDecl).Specs[0].(*ast.TypeSpec).Type.(*ast.StructType).Fields.List[0]
	st.Type = &ast.NilableTypeExpr{X: st.Type, QPos: st.Type.End()}

	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	if n := nilableMethodGuards(f, files, returns, nil); n != 0 {
		t.Fatalf("nilableMethodGuards rewrote %d, want 0 (guards disabled)", n)
	}
	fn := f.Decls[3].(*ast.FuncDecl)
	rangeStmt := fn.Body.List[0].(*ast.RangeStmt)
	call := rangeStmt.X.(*ast.CallExpr)
	if call.Ellipsis == 0 {
		t.Fatal("expected variadic ellipsis preserved")
	}
}

func TestBuildFuncVarsTracksNilableMethodResult(t *testing.T) {
	const src = `package p
type Entry struct{}
type Cache struct{}
func (c *Cache) size() *Entry { return nil }
func (c *Cache) scan() { flat := c.size(); _ = flat }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	sizeFn := f.Decls[2].(*ast.FuncDecl)
	sizeFn.Type.Results.List[0].Type = &ast.NilableTypeExpr{X: sizeFn.Type.Results.List[0].Type, QPos: sizeFn.Type.Results.List[0].Type.End()}
	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	varIdx := buildFuncVarIndex(files, returns, nil)
	scanFn := f.Decls[3].(*ast.FuncDecl)
	if !isNilablePointerType(varIdx.byFunc[scanFn]["flat"]) {
		t.Fatalf("expected flat to be nilable, got %#v", varIdx.byFunc[scanFn]["flat"])
	}
}

func TestNilableStarDerefGuard(t *testing.T) {
	const src = `package p

type Entry struct{ Size int }

type Cache struct{}
func (c *Cache) size() *Entry { return nil }
func (c *Cache) replace(e Entry) {}

func (c *Cache) scan() {
	flat := c.size()
	c.replace(*flat)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	retFn := f.Decls[2].(*ast.FuncDecl)
	retFn.Type.Results.List[0].Type = &ast.NilableTypeExpr{X: retFn.Type.Results.List[0].Type, QPos: retFn.Type.Results.List[0].Type.End()}

	files := []*ast.File{f}
	returns := buildReturnTypeIndex(files)
	if n := nilableMethodGuards(f, files, returns, nil); n != 0 {
		t.Fatalf("nilableMethodGuards star deref rewrote %d, want 0 (guards disabled)", n)
	}
}
