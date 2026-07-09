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
	mod := &fileModernizer{fset: fset, file: f}
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
	mod := &fileModernizer{fset: fset, file: f}
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
	mod := &fileModernizer{fset: fset, file: f}
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
	_, _, changed, err := modernizeParsedFile(fset, f, filepath.Join(t.TempDir(), "p.go"), false, nil)
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
	mod := &fileModernizer{fset: fset, file: f, pkgEmbed: pkgEmbed}
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
	mod := &fileModernizer{fset: fset, file: f}
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
