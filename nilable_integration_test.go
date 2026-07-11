package main

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPtrAnnotatorNilEvidence(t *testing.T) {
	const src = `package p

type Node struct {
	Next *Node
}

func Find(id int) *Node {
	return nil
}

func Must(x *Node) *Node {
	if x == nil {
		panic("nil")
	}
	return x
}

func Use(n *Node) {
	var p *Node
	p = nil
	_ = n
	_ = p
}

func Call() {
	Use(nil)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()

	strict := func(kind, owner, name string, index int) {
		key := ptrSiteKey{kind: kind, owner: owner, name: name, index: index}
		if ann.nilable[key] {
			t.Errorf("%v should be strict", key)
		}
	}
	markNilable := func(kind, owner, name string, index int) {
		key := ptrSiteKey{kind: kind, owner: owner, name: name, index: index}
		if !ann.nilable[key] {
			t.Errorf("%v should be nilable", key)
		}
	}

	strict("field", "Node", "Next", -1)
	markNilable("result", "Find", "", 0)
	strict("result", "Must", "", 0)
	strict("param", "Must", "x", 0)
	markNilable("param", "Use", "n", 0)
	markNilable("var", "Use", "p", -1)

	rewriteFilePointerTypes(fset, f, ann)
	var buf strings.Builder
	if err := format.Node(&buf, fset, f); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Find(id int) *Node?") {
		t.Fatalf("expected nilable result:\n%s", out)
	}
	if strings.Contains(out, "Next *Node?") {
		t.Fatalf("Next should stay strict:\n%s", out)
	}
	if !strings.Contains(out, "Must(x *Node) *Node") {
		t.Fatalf("Must should keep strict pointers:\n%s", out)
	}

	if got := ann.countVerifiedNonNilPointers(); got != 3 {
		t.Fatalf("verified non-nil pointers = %d, want 3", got)
	}
}

func TestPtrAnnotatorLookupTripleReturnStrict(t *testing.T) {
	const src = `package p

type Result struct{}

func (c *Config) LookupUserDN(username string) (*Result, []string, error) {
	return nil, nil, errX
}

type Config struct{}
var errX error
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	key := ptrSiteKey{kind: "result", owner: "LookupUserDN", index: 0}
	if ann.nilable[key] {
		t.Fatalf("triple-return lookup should keep strict pointer result")
	}
	rewriteFilePointerTypes(fset, f, ann)
	out := formatTestFile(fset, f)
	if strings.Contains(out, "*Result?") {
		t.Fatalf("expected strict pointer:\n%s", out)
	}
}

func TestPtrAnnotatorSyncsFieldFromNilableParam(t *testing.T) {
	const src = `package p

type Cycle struct{}

type Metrics struct {
	cycle *Cycle
}

func (p *Metrics) setCycle(c *Cycle) {
	p.cycle = c
}

func use(m *Metrics) {
	m.setCycle(nil)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	fieldKey := ptrSiteKey{kind: "field", owner: "Metrics", name: "cycle", index: -1}
	if !ann.nilable[fieldKey] {
		t.Fatal("field assigned from nilable param should be nilable")
	}
	rewriteFilePointerTypes(fset, f, ann)
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "cycle *Cycle?") {
		t.Fatalf("expected nilable field:\n%s", out)
	}
}

func TestPtrAnnotatorVarStrictWhenAlsoAssigned(t *testing.T) {
	const src = `package p

type Policy struct {
	Version string
}

func Parse() (*Policy, error) {
	return &Policy{}, nil
}

func f() {
	var sp *Policy
	sp, err := Parse()
	if err != nil {
		return
	}
	_ = sp.Version
	sp = nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	key := ptrSiteKey{kind: "var", owner: "f", name: "sp", index: -1}
	if ann.nilable[key] {
		t.Fatal("var assigned non-nil should stay strict")
	}
}

func TestPtrAnnotatorNonErrorPairStrict(t *testing.T) {
	const src = `package p

type RemoteErr struct{}
type Config struct{}

func Verify() (*Config, *RemoteErr) {
	return &Config{}, nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	key := ptrSiteKey{kind: "result", owner: "Verify", index: 1}
	if ann.nilable[key] {
		t.Fatalf("non-error pair should keep strict second result")
	}
}

func TestPtrAnnotatorSkipsNilReceiverGuardReturn(t *testing.T) {
	const src = `package p

type tracer struct{ Subroute string }

func (c *tracer) subroute(s string) *tracer {
	if c == nil {
		return nil
	}
	c2 := *c
	c2.Subroute = s
	return &c2
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	key := ptrSiteKey{kind: "result", owner: "subroute", index: 0}
	if ann.nilable[key] {
		t.Fatal("nil-receiver guard return nil should not mark result nilable")
	}
	changed, _ := applyPtrAnnotations(fset, []string{"p.go"}, []*ast.File{f})
	if changed[0] {
		t.Fatal("expected no pointer type rewrite")
	}
}

func TestPtrAnnotatorNilReturnWithNonNilPath(t *testing.T) {
	const src = `package p

type Client struct{}

func newClient() *Client {
	c, err := open()
	if err != nil {
		return nil
	}
	return c
}

func open() (*Client, error) {
	return nil, nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	changed, _ := applyPtrAnnotations(fset, []string{"p.go"}, []*ast.File{f})
	if !changed[0] {
		t.Fatal("expected pointer annotation changes")
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "newClient() *Client?") {
		t.Fatalf("expected nilable return type:\n%s", out)
	}
	if strings.Contains(out, "return new(") {
		t.Fatalf("must not replace return nil with return new:\n%s", out)
	}
	if !strings.Contains(out, "return nil") {
		t.Fatalf("expected return nil preserved:\n%s", out)
	}
}

func TestPtrAnnotatorPropagatesNilableFromCalleeReturn(t *testing.T) {
	const src = `package p

type Checksum struct{ Encoded string }

func NewChecksumWithType(value string) *Checksum {
	if value == "" {
		return nil
	}
	return &Checksum{Encoded: value}
}

func NewChecksumString(value string) *Checksum {
	return NewChecksumWithType(value)
}

type Reader struct {
	Result *Checksum
}

func (r *Reader) read() {
	r.Result = NewChecksumWithType("x")
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	changed, _ := applyPtrAnnotations(fset, []string{"p.go"}, []*ast.File{f})
	if !changed[0] {
		t.Fatal("expected pointer annotation changes")
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "NewChecksumString(value string) *Checksum?") {
		t.Fatalf("expected passthrough return to be nilable:\n%s", out)
	}
	if !strings.Contains(out, "Result *Checksum?") {
		t.Fatalf("expected field assigned from nilable callee to be nilable:\n%s", out)
	}
}

func TestPtrAnnotatorPropagatesNilableToParam(t *testing.T) {
	const src = `package p

type HTTPRangeSpec struct{}

func partNumberToRangeSpec(part int) *HTTPRangeSpec? {
	return nil
}

func setHeaders(rs *HTTPRangeSpec) {
	rs = partNumberToRangeSpec(1)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	changed, _ := applyPtrAnnotations(fset, []string{"p.go"}, []*ast.File{f})
	if !changed[0] {
		t.Fatal("expected pointer annotation changes")
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "setHeaders(rs *HTTPRangeSpec?)") {
		t.Fatalf("expected param reassigned from nilable callee to be nilable:\n%s", out)
	}
}

func TestPtrAnnotatorSliceAndMapNilEvidence(t *testing.T) {
	const src = `package p

func findSlice() []string {
	return nil
}

func findMap() map[string]int {
	return nil
}

func findChan() chan int {
	return nil
}

func use() {
	var s []string
	s = nil
	var m map[string]int
	m = nil
	var ch chan int
	ch = nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()

	markNilable := func(kind, owner, name string, index int) {
		key := ptrSiteKey{kind: kind, owner: owner, name: name, index: index}
		if !ann.nilable[key] {
			t.Errorf("%v should be nilable", key)
		}
	}
	markNilable("result", "findSlice", "", 0)
	markNilable("result", "findMap", "", 0)
	markNilable("result", "findChan", "", 0)
	markNilable("var", "use", "s", -1)
	markNilable("var", "use", "m", -1)
	markNilable("var", "use", "ch", -1)

	rewriteFilePointerTypes(fset, f, ann)
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "[]string?") {
		t.Fatalf("expected nilable slice:\n%s", out)
	}
	if !strings.Contains(out, "map[string]int?") {
		t.Fatalf("expected nilable map:\n%s", out)
	}
	if !strings.Contains(out, "chan int?") {
		t.Fatalf("expected nilable channel:\n%s", out)
	}
}

func TestPtrAnnotatorStructFieldNilAssign(t *testing.T) {
	const src = `package p

type DEK struct {
	Plaintext []byte
}

func (d *DEK) clear() {
	d.Plaintext = nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	key := ptrSiteKey{kind: "field", owner: "DEK", name: "Plaintext", index: -1}
	if !ann.nilable[key] {
		t.Fatal("Plaintext should be nilable")
	}
}

func TestPtrAnnotatorStructDeclShorthand(t *testing.T) {
	const src = `package p

struct queueItem {
	dst chan [][]string
}

func send(in *queueItem) {
	in.dst <- nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	key := ptrSiteKey{kind: "field", owner: "queueItem", name: "dst", index: -1}
	if !ann.chanElemNilable[key] {
		t.Fatal("expected chanElemNilable on shorthand struct field")
	}
	changed := false
	if rewriteChanNilSends(f, ann) {
		changed = true
	}
	if !changed {
		t.Fatal("expected nil send rewrite")
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "dst <- []") {
		t.Fatalf("expected empty slice send:\n%s", out)
	}
}

func TestPtrAnnotatorDropNilableParamIfaceCallDoesNotMatchConcrete(t *testing.T) {
	const src = `package p

import "hash"

type UUIDHash struct{}

func (u UUIDHash) Sum(b []byte) []byte { return b }
func (u UUIDHash) Write(p []byte) (int, error) { return len(p), nil }
func (u UUIDHash) Reset() {}
func (u UUIDHash) Size() int { return 0 }
func (u UUIDHash) BlockSize() int { return 0 }

var _ hash.Hash = &UUIDHash{}

type Reader struct {
	md5 hash.Hash
}

func (r *Reader) read() {
	r.md5.Sum(nil)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	key := ptrSiteKey{kind: "param", owner: "Sum", name: "b", index: 0}
	if ann.nilable[key] {
		t.Fatal("concrete Sum param should not be nilable from interface call")
	}
}

func TestRewriteNilableSliceFieldArgs(t *testing.T) {
	moduleNilableSliceFields = map[string]ast.Expr{
		"DEK.Plaintext": &ast.ArrayType{Elt: &ast.Ident{Name: "byte"}},
	}
	const src = `package config

import "github.com/minio/minio/internal/kms"

func f(k *kms.KMS) {
	key := k.GenerateKey(nil, nil)!
	_ = stream(key.Plaintext)
}

func stream(b []byte) {}
`
	path := t.TempDir() + "/p.go"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	if !rewriteNilableSliceFieldArgs(f, fset, ann, path) {
		t.Fatal("expected rewrite")
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "??") {
		t.Fatalf("expected coalesce:\n%s", out)
	}
}

func TestDropNilableSourcesForMultiValueReturnUse(t *testing.T) {
	const src = `package p

func getHosts() ([]string, []byte?, error) {
	var remain []byte?
	remain = append(remain, 1)
	return nil, remain, nil
}

func use() {
	_, remain, _ := getHosts()
	consume(remain)
}

func consume(b []byte) {}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	key := ptrSiteKey{kind: "result", owner: "getHosts", name: "remain", index: 1}
	if ann.nilable[key] {
		t.Fatal("getHosts remain result should not stay nilable when passed to consume")
	}
}

func TestDropNilableResultsAssignedToStrictSliceVar(t *testing.T) {
	const src = `package p

func lookup() (*int, []string, error) {
	return nil, nil, nil
}

func use() {
	var groups []string
	_, groups, _ = lookup()
	_ = groups
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	key := ptrSiteKey{kind: "result", owner: "lookup", index: 1}
	if ann.nilable[key] {
		t.Fatal("lookup []string result should not stay nilable when assigned to []string var")
	}
}

func TestModuleNilableDEKPlaintext(t *testing.T) {
	const kmsSrc = `package kms

struct DEK {
	Plaintext []byte
}

func (d *DEK) UnmarshalText(text []byte) error {
	d.Plaintext = nil
	return nil
}
`
	const configSrc = `package config

import "kms"

func f(k *kms.KMS) {
	key := k.GenerateKey(nil, nil)!
	_ = consume(key.Plaintext)
}

func consume(b []byte) {}
`
	// stub KMS type for GenerateKey
	const kmsExtra = `package kms

type KMS struct{}
func (k *KMS) GenerateKey(a, b any) DEK! { return DEK{} }
`
	dir := t.TempDir()
	kmsDir := filepath.Join(dir, "kms")
	configDir := filepath.Join(dir, "config")
	os.MkdirAll(kmsDir, 0o755)
	os.MkdirAll(configDir, 0o755)
	write := func(path, src string) {
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(kmsDir, "conn.go"), kmsSrc)
	write(filepath.Join(kmsDir, "extra.go"), kmsExtra)
	write(filepath.Join(configDir, "crypto.go"), configSrc)

	pkgs := []pkgFiles{
		{dir: kmsDir, paths: []string{filepath.Join(kmsDir, "conn.go"), filepath.Join(kmsDir, "extra.go")}},
		{dir: configDir, paths: []string{filepath.Join(configDir, "crypto.go")}},
	}
	moduleNilableSliceFields = buildModuleNilableSliceFields(pkgs)
	if _, ok := moduleNilableSliceFields["DEK.Plaintext"]; !ok {
		t.Fatalf("expected DEK.Plaintext in module map: %#v", moduleNilableSliceFields)
	}
}

func TestCoalesceModuleSliceFieldCallArgsMinioCrypto(t *testing.T) {
	if _, err := os.Stat("/root/GolangProject/minio/internal/kms/conn.go"); err != nil {
		t.Skip("minio not present")
	}
	pkgs, err := collectPackages("/root/GolangProject/minio")
	if err != nil {
		t.Fatal(err)
	}
	moduleNilableSliceFields = buildModuleNilableSliceFields(pkgs)
	if _, ok := moduleNilableSliceFields["DEK.Plaintext"]; !ok {
		t.Fatalf("missing DEK.Plaintext in %#v", moduleNilableSliceFields)
	}
	const src = `package config

import "github.com/minio/minio/internal/kms"

func f(k *kms.KMS) {
	key := k.GenerateKey(nil, nil)!
	_ = consume(key.Plaintext)
}

func consume(b []byte) {}
`
	path := t.TempDir() + "/crypto.go"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !coalesceModuleSliceFieldCallArgs(f, fset, path) {
		t.Fatal("expected coalesce")
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "??") {
		t.Fatalf("expected ??: %s", out)
	}
}

func TestLookupFuncSkipsNonPointerNilableReturns(t *testing.T) {
	const src = `package p

func lookupUser() (*int, []string, error) {
	return nil, nil, nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ann := newPtrAnnotator(fset, []*ast.File{f})
	ann.analyze()
	key := ptrSiteKey{kind: "result", owner: "lookupUser", index: 1}
	if ann.nilable[key] {
		t.Fatal("lookup func should not nilable-annotate slice results")
	}
}
