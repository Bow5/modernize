package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func formatTestFile(fset *token.FileSet, f *ast.File) string {
	var out bytes.Buffer
	if err := format.Node(&out, fset, f); err != nil {
		return err.Error()
	}
	return out.String()
}

func TestModernizeFmtErrorfToErrorsNew(t *testing.T) {
	const src = `package p

import "fmt"

func f() error {
	return fmt.Errorf("something failed")
}

func g() error {
	return fmt.Errorf("value %s", "x")
}

func h(err error) error {
	return fmt.Errorf("wrap: %w", err)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	mod := &fileModernizer{fset: fset, file: f, cfg: DefaultConfig()}
	n, _ := mod.modernizeStructuredErrors()
	if n != 2 {
		t.Fatalf("expected 2 fmt.Errorf rewrites, got %d", n)
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, `fmt.Errorf("something failed")`) {
		t.Fatalf("fmt.Errorf not rewritten:\n%s", out)
	}
	if !strings.Contains(out, `errors.New("something failed")`) {
		t.Fatalf("missing errors.New:\n%s", out)
	}
	if !strings.Contains(out, `errors.New("value %s", "x")`) {
		t.Fatalf("missing formatted errors.New:\n%s", out)
	}
	if !strings.Contains(out, `fmt.Errorf("wrap: %w", err)`) {
		t.Fatalf("should keep wrapped fmt.Errorf:\n%s", out)
	}
}

func TestModernizeEmbedOnlyCustomError(t *testing.T) {
	const src = `package p

type AppError struct {
	msg string
}

func (e AppError) Error() string {
	return e.msg
}

func fail() error {
	return AppError{msg: "oops"}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	mod := &fileModernizer{fset: fset, file: f, cfg: DefaultConfig()}
	_, custom := mod.modernizeStructuredErrors()
	if custom < 2 {
		t.Fatalf("expected custom error rewrites, got %d", custom)
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "errors.Base") {
		t.Fatalf("missing errors.Base embed:\n%s", out)
	}
	if strings.Contains(out, "msg string") {
		t.Fatalf("msg field should be removed:\n%s", out)
	}
	if !strings.Contains(out, "errors.NewCustom[AppError]") {
		t.Fatalf("missing NewCustom:\n%s", out)
	}
}

func TestModernizeExtraFieldCustomError(t *testing.T) {
	const src = `package p

type MyErr struct {
	Code int
}

func (e MyErr) Error() string {
	return "code"
}

func newMyErr(code int) MyErr {
	return MyErr{Code: code}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	mod := &fileModernizer{fset: fset, file: f, cfg: DefaultConfig()}
	_, custom := mod.modernizeStructuredErrors()
	if custom < 2 {
		t.Fatalf("expected custom error rewrites, got %d", custom)
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "errors.Base") {
		t.Fatalf("missing errors.Base embed:\n%s", out)
	}
	if !strings.Contains(out, "errors.InitCustom") {
		t.Fatalf("missing InitCustom in constructor:\n%s", out)
	}
}

func TestModernizeResultTypeConversion(t *testing.T) {
	const src = `package p

func Load() (*File, error) {
	if err := open(); err != nil {
		return nil, err
	}
	return f, nil
}

type Saver interface {
	Save() error
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changes")
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "func Load() *File!") {
		t.Fatalf("missing T! result type:\n%s", out)
	}
	if !strings.Contains(out, "open()!") {
		t.Fatalf("missing try/bang in converted body:\n%s", out)
	}
	if strings.Contains(out, ", error)") {
		t.Fatalf("Load should not keep (T, error) pair:\n%s", out)
	}
}

func TestModernizeMessageFieldRefsToBaseMessage(t *testing.T) {
	const src = `package p

type AppError struct {
	msg string
}

func (e AppError) Error() string {
	return e.msg
}

func describe(e AppError) string {
	return e.msg
}

func fail() error {
	return AppError{msg: "oops"}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkgEmbed := collectPackageEmbedOnlyTypes([]*ast.File{f})
	mod := &fileModernizer{fset: fset, file: f, pkgEmbed: pkgEmbed, cfg: DefaultConfig()}
	_, custom := mod.modernizeStructuredErrors()
	if custom < 3 {
		t.Fatalf("expected custom error rewrites, got %d", custom)
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, "e.msg") {
		t.Fatalf("msg field refs should be rewritten:\n%s", out)
	}
	if !strings.Contains(out, "e.Base.Message") {
		t.Fatalf("missing Base.Message rewrite:\n%s", out)
	}
}

func TestModernizeAssignCompositeToNewCustom(t *testing.T) {
	const src = `package p

type AppError struct {
	msg string
}

func (e AppError) Error() string {
	return e.msg
}

func makeErr() error {
	e := AppError{msg: "x"}
	return e
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	mod := &fileModernizer{fset: fset, file: f, cfg: DefaultConfig()}
	_, custom := mod.modernizeStructuredErrors()
	if custom < 2 {
		t.Fatalf("expected custom error rewrites, got %d", custom)
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, `AppError{msg:`) {
		t.Fatalf("assign composite should become NewCustom:\n%s", out)
	}
	if !strings.Contains(out, `errors.NewCustom[AppError]("x")`) {
		t.Fatalf("missing NewCustom in assign:\n%s", out)
	}
}

func TestModernizePositionalHasExtraComposite(t *testing.T) {
	const src = `package p

type ErrInvalidARN struct {
	ARN string
}

func (e ErrInvalidARN) Error() string {
	return e.ARN
}

func parse(s string) error {
	return &ErrInvalidARN{s}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkgExtra := collectPackageHasExtraErrorTypes([]*ast.File{f})
	mod := &fileModernizer{fset: fset, file: f, pkgExtraFields: pkgExtra, cfg: DefaultConfig()}
	_, custom := mod.modernizeStructuredErrors()
	if custom < 2 {
		t.Fatalf("expected custom error rewrites, got %d", custom)
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, "&ErrInvalidARN{s}") {
		t.Fatalf("positional composite should be keyed:\n%s", out)
	}
	if !strings.Contains(out, "ARN: s") {
		t.Fatalf("missing keyed field:\n%s", out)
	}
}

func TestModernizePruneUnusedFmtImport(t *testing.T) {
	const src = `package p

import "fmt"

func f() error {
	return fmt.Errorf("oops")
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	mod := &fileModernizer{fset: fset, file: f, cfg: DefaultConfig()}
	mod.modernizeStructuredErrors()
	out := formatTestFile(fset, f)
	if strings.Contains(out, `"fmt"`) {
		t.Fatalf("fmt import should be removed:\n%s", out)
	}
}

func TestModernizeSkipErrBangWhenErrReused(t *testing.T) {
	const src = `package p

import "os"

func lockedOpen() (*os.File, error) {
	f, err := os.Open("x")
	if err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	return f, nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected signature conversion")
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, `os.Open("x")!`) {
		t.Fatalf("should not bang Open when err is reused:\n%s", out)
	}
	if !strings.Contains(out, "f, err := os.Open") {
		t.Fatalf("expected to keep err binding:\n%s", out)
	}
}

func TestModernizeSkipInitCustomOnNonConstructor(t *testing.T) {
	const src = `package p

type Err struct {
	msg    string
	detail string
}

func (e Err) Error() string {
	if e.detail != "" {
		return e.detail
	}
	return e.msg
}

func ErrorToErr(err error) Err {
	return Err{msg: err.Error()}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	mod := &fileModernizer{fset: fset, file: f, cfg: DefaultConfig()}
	_, custom := mod.modernizeStructuredErrors()
	if custom < 1 {
		t.Fatalf("expected Base embed, got %d", custom)
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, "InitCustom") {
		t.Fatalf("should not InitCustom non-constructor returns:\n%s", out)
	}
	if !strings.Contains(out, "return Err{msg: err.Error()}") {
		t.Fatalf("expected plain return:\n%s", out)
	}
}

func TestModernizeAssignErrBangWhenErrReused(t *testing.T) {
	const src = `package p

import (
	"os"
	"syscall"
)

func lockedOpen(path string) (*os.File, error) {
	f, err := os.OpenFile(path, syscall.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	return f, nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected signature conversion")
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "f, err := os.OpenFile") {
		t.Fatalf("expected to keep err binding:\n%s", out)
	}
	if !strings.Contains(out, "err!") {
		t.Fatalf("expected err! when err is reused later:\n%s", out)
	}
	if strings.Contains(out, "os.OpenFile(path, syscall.O_RDONLY, 0)!") {
		t.Fatalf("should not collapse assign when err is reused:\n%s", out)
	}
}

func TestModernizeErrBangInSwitchCase(t *testing.T) {
	const src = `package p

import "errors"

func helper(s string) ([]string, error) {
	if s == "" {
		return nil, errEmpty
	}
	return []string{s}, nil
}

var errEmpty = errors.New("empty")

func Load(raw string) (*Item, error) {
	switch {
	case raw != "":
		parts, err := helper(raw)
		if err != nil {
			return nil, err
		}
		return &Item{parts: parts}, nil
	default:
		return nil, errEmpty
	}
}

type Item struct{ parts []string }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changes")
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, "parts, err := helper") {
		t.Fatalf("expected helper()! in switch case:\n%s", out)
	}
	if !strings.Contains(out, "helper(raw)!") {
		t.Fatalf("missing helper()! call:\n%s", out)
	}
}

func TestModernizeErrBangErrorsNewReturn(t *testing.T) {
	const src = `package p

import (
	"errors"
	"strings"
)

func parse(endpoint string) (string, error) {
	return endpoint, nil
}

func expand(s string) ([]string, error) {
	var endpoints []string
	for endpoint := range strings.SplitSeq(s, ",") {
		pattern, err := parse(endpoint)
		if err != nil {
			return nil, errors.New("invalid endpoint '%s': %v", endpoint, err)
		}
		endpoints = append(endpoints, pattern)
	}
	return endpoints, nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changes")
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "func expand(s string) ([]string, error)") {
		t.Fatalf("expected expand to keep (T, error) with range-over-Seq loop:\n%s", out)
	}
	if strings.Contains(out, "[]string!") {
		t.Fatalf("should not convert expand to T! inside SplitSeq loop:\n%s", out)
	}
}
func TestModernizeErrBangSequentialErrChecks(t *testing.T) {
	const src = `package p

func helper(s string) ([]string, error) {
	return []string{s}, nil
}

func other() (int, error) {
	return 1, nil
}

func Load(raw string) (*Item, error) {
	parts, err := helper(raw)
	if err != nil {
		return nil, err
	}
	n, err := other()
	if err != nil {
		return nil, err
	}
	return &Item{parts: parts, n: n}, nil
}

type Item struct {
	parts []string
	n     int
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changes")
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, "parts, err := helper") || strings.Contains(out, "n, err := other") {
		t.Fatalf("expected bang calls for sequential err checks:\n%s", out)
	}
	if !strings.Contains(out, "helper(raw)!") || !strings.Contains(out, "other()!") {
		t.Fatalf("missing bang calls:\n%s", out)
	}
}

func TestModernizeSetMsgGenericFactory(t *testing.T) {
	const src = `package config

import "fmt"

type ErrorConfig interface {
	ErrConfigGeneric
}

type ErrConfigGeneric struct {
	msg string
}

func (ge *ErrConfigGeneric) setMsg(msg string) {
	ge.msg = msg
}

func (ge ErrConfigGeneric) Error() string {
	return ge.msg
}

func Error[T ErrorConfig, PT interface {
	*T
	setMsg(string)
}](format string, vals ...any) T {
	pt := PT(new(T))
	pt.setMsg(fmt.Sprintf(format, vals...))
	return *pt
}

func Errorf(format string, vals ...any) ErrConfigGeneric {
	return Error[ErrConfigGeneric](format, vals...)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "config.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "config.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, "errors.Base") {
		t.Fatalf("should skip Base embed when generic setMsg factory is present:\n%s", out)
	}
	if !strings.Contains(out, "pt.setMsg") {
		t.Fatalf("Error factory should be unchanged:\n%s", out)
	}
}

func TestModernizeErrBangSkipsParamShadow(t *testing.T) {
	const src = `package p

type Key struct {
	Plaintext []byte
}

type KMS struct{}

func (k *KMS) GenerateKey() (Key, error) {
	return Key{}, nil
}

var GlobalKMS *KMS

func newEncrypt(key []byte) error {
	key, err := GlobalKMS.GenerateKey()
	if err != nil {
		return err
	}
	_ = key.Plaintext
	return nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changes")
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, "GenerateKey()!") {
		t.Fatalf("should not bang when LHS shadows param:\n%s", out)
	}
	if !strings.Contains(out, "key, err := GlobalKMS.GenerateKey()") {
		t.Fatalf("expected original assign:\n%s", out)
	}
}

func TestModernizeErrBangInErrorReturnFunc(t *testing.T) {
	const src = `package p

import "strconv"

func run() error {
	workerSize, err := strconv.Atoi("42")
	if err != nil {
		return err
	}
	_ = workerSize

	x, err := strconv.Atoi("1")
	if err != nil {
		return err
	}
	_ = x

	if err := fail(); err != nil {
		return err
	}
	return nil
}

func fail() error {
	return nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changes")
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, "workerSize, err :=") || strings.Contains(out, "if err != nil") {
		t.Fatalf("expected err! rewrites in error-returning func:\n%s", out)
	}
	if !strings.Contains(out, `workerSize := strconv.Atoi("42")!`) {
		t.Fatalf("missing assign bang:\n%s", out)
	}
	if !strings.Contains(out, `x := strconv.Atoi("1")!`) {
		t.Fatalf("missing second assign bang:\n%s", out)
	}
	if !strings.Contains(out, "fail()!") {
		t.Fatalf("missing if-init bang:\n%s", out)
	}
}

func TestModernizeAssignKeepErrWhenReused(t *testing.T) {
	const src = `package p

type G struct{}

func (g *G) SetMatchETag(string) error {
	return nil
}

func run(g *G) error {
	if err := g.SetMatchETag("x"); err != nil {
		return err
	}
	hr, err := fake()
	if err != nil {
		return err
	}
	_ = hr
	_, err = other()
	return err
}

func fake() (int, error) {
	return 0, nil
}

func other() error {
	return nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changes")
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, `g.SetMatchETag("x")!`) {
		t.Fatalf("missing if-init bang:\n%s", out)
	}
	if !strings.Contains(out, "hr, err := fake()") || !strings.Contains(out, "err!") {
		t.Fatalf("expected assign kept with err!:\n%s", out)
	}
	if strings.Contains(out, "fake()!") {
		t.Fatalf("should not collapse assign when err is reused:\n%s", out)
	}
}

func TestModernizeAssignTripleReturnKeepErr(t *testing.T) {
	const src = `package p

type Opts struct{}
type Info struct{}

func batchReplicationOpts() (Opts, bool, error) {
	return Opts{}, false, nil
}

func run() error {
	putOpts, isMP, err := batchReplicationOpts()
	if err != nil {
		return err
	}
	_ = putOpts
	if isMP {
		_, err = other()
		return err
	}
	return nil
}

func other() error {
	return nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changes")
	}
	out := formatTestFile(fset, f)
	if !strings.Contains(out, "putOpts, isMP, err := batchReplicationOpts()") {
		t.Fatalf("expected triple assign kept:\n%s", out)
	}
	if strings.Contains(out, "if err != nil") {
		t.Fatalf("expected err! instead of if err check:\n%s", out)
	}
	if !strings.Contains(out, "err!") {
		t.Fatalf("missing err!:\n%s", out)
	}
}

func TestModernizeAssignReturnErrWithGap(t *testing.T) {
	const src = `package p

type RI struct {
	mu int
}

func (ri *RI) MarshalMsg([]byte) ([]byte, error) {
	return nil, nil
}

func (ri *RI) persist() error {
	var data []byte
	buf, err := ri.MarshalMsg(data)
	ri.mu = 0
	if err != nil {
		return err
	}
	_ = buf
	return nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changes")
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, "if err != nil") {
		t.Fatalf("expected err! after gap:\n%s", out)
	}
	if !strings.Contains(out, "buf, err := ri.MarshalMsg(data)") || !strings.Contains(out, "ri.mu = 0") || !strings.Contains(out, "err!") {
		t.Fatalf("expected assign, gap stmt, err!:\n%s", out)
	}
}

func TestModernizeAssignTripleReturnWithBlank(t *testing.T) {
	const src = `package p

type Result struct{}
type Info struct{}

func LookupUserDN() (*Result, []string, error) {
	return nil, nil, nil
}

func run() error {
	_, ldapGroups, err := LookupUserDN()
	if err != nil {
		return err
	}
	_ = ldapGroups
	return nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changes")
	}
	out := formatTestFile(fset, f)
	if strings.Contains(out, "LookupUserDN()!") {
		t.Fatalf("should not collapse triple return:\n%s", out)
	}
	if !strings.Contains(out, "_, ldapGroups, err := LookupUserDN()") || !strings.Contains(out, "err!") {
		t.Fatalf("expected assign + err!:\n%s", out)
	}
}

func TestModernizeSkipErrNotLastInTripleAssign(t *testing.T) {
	const src = `package p

func Do() (any, error, bool) {
	return nil, nil, false
}

func run() error {
	val, err, _ := Do()
	if err != nil {
		return err
	}
	_ = val
	return nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("should not rewrite when err is not last lhs:\n%s", formatTestFile(fset, f))
	}
}

func TestModernizeRewriteCounts(t *testing.T) {
	const src = `package p

import "strconv"

func run() error {
	x := strconv.Atoi("1")!

	if err := fail(); err != nil {
		return err
	}

	hr, err := fake()
	if err != nil {
		return err
	}
	_ = hr
	return nil
}

func fail() error {
	return nil
}

func fake() (int, error) {
	return 0, nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, counts, _, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if counts.callBang != 2 {
		t.Fatalf("expected 2 call()!, got %d", counts.callBang)
	}
	if counts.errBang != 0 {
		t.Fatalf("expected 0 err!, got %d", counts.errBang)
	}
}

func TestModernizeRewriteCountsErrBang(t *testing.T) {
	const src = `package p

func run() error {
	hr, err := fake()
	if err != nil {
		return err
	}
	_ = hr
	_, err = other()
	return err
}

func fake() (int, error) {
	return 0, nil
}

func other() error {
	return nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, counts, _, err := modernizeParsedFile(fset, []*ast.File{f}, f, filepath.Join(t.TempDir(), "p.go"), false, nil, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if counts.callBang != 0 {
		t.Fatalf("expected 0 call()!, got %d", counts.callBang)
	}
	if counts.errBang != 1 {
		t.Fatalf("expected 1 err!, got %d", counts.errBang)
	}
}
