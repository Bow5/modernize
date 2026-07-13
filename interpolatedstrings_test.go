package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestEscapeNonInterpBraces(t *testing.T) {
	got, changed := rewriteLiteralBraces(`"a {} brace"`)
	if !changed {
		t.Fatal("expected change")
	}
	want := "`a {} brace`"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMuxTemplateToRawString(t *testing.T) {
	got, changed := rewriteLiteralBraces(`"{key:.*}"`)
	if !changed {
		t.Fatal("expected change")
	}
	want := "`{key:.*}`"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestHealPathToBacktick(t *testing.T) {
	got, changed := rewriteLiteralBraces(`"/heal/{bucket}/{prefix:.*}"`)
	if !changed {
		t.Fatal("expected change")
	}
	want := "`/heal/{bucket}/{prefix:.*}`"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEscapeLiteralBracesOnRouterCall(t *testing.T) {
	const src = `package cmd
func f() {
	Queries("key", "{key:.*}")
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "router.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	edits, n := escapeLiteralBraces(fset, f, []byte(src), nil)
	if n != 1 {
		t.Fatalf("escapeLiteralBraces rewrote %d, want 1", n)
	}
	if !strings.Contains(string(edits[0].text), "`{key:.*}`") {
		t.Fatalf("got %q", edits[0].text)
	}
}

func TestMuxTemplateWithBacktickFallsBackToEscape(t *testing.T) {
	got, changed := rewriteLiteralBraces(`"prefix ` + "`" + ` suffix {key:.*}"`)
	if !changed {
		t.Fatal("expected change")
	}
	want := `"prefix ` + "`" + ` suffix \\{key:.*\\}"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMuxTemplateInInterpStringEscaped(t *testing.T) {
	got, changed := rewriteLiteralBraces(`"route {key:.*} price {price:.2f}"`)
	if !changed {
		t.Fatal("expected change")
	}
	want := `"route \\{key:.*\\} price {price:.2f}"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEscapeSkipsInterpolation(t *testing.T) {
	got, changed := escapeNonInterpBraces(`"price {price:.2f}"`)
	if changed {
		t.Fatalf("unexpected change: %q", got)
	}
}

func TestSprintfToInterp(t *testing.T) {
	const src = `package p
import "fmt"
func f(n int) string {
	return fmt.Sprintf("part.%d", n)
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	edits, n := rewriteSprintfToInterp(fset, f, []byte(src), nil, nil)
	if n != 1 {
		t.Fatalf("rewrote %d, want 1", n)
	}
	if len(edits) != 1 {
		t.Fatal("expected one edit")
	}
	if string(edits[0].text) != `"part.{n:d}"` {
		t.Fatalf("got %q", edits[0].text)
	}
}

func TestConcatToInterp(t *testing.T) {
	const src = `package p
func f(name string) string {
	return "hello " + name + "!"
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	edits, n := rewriteConcatToInterp(fset, f, []byte(src), nil, nil)
	if n != 1 {
		t.Fatalf("rewrote %d, want 1", n)
	}
	if string(edits[0].text) != `"hello {name:v}!"` {
		t.Fatalf("got %q", edits[0].text)
	}
}

func TestConcatSkipsIntegerAdd(t *testing.T) {
	const src = `package p
func f(i int) int {
	return i + 1
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, n := rewriteConcatToInterp(fset, f, nil, nil, nil)
	if n != 0 {
		t.Fatalf("rewrote %d, want 0 for integer add", n)
	}
}

func TestPrintfToNonF(t *testing.T) {
	const src = `package p
import "fmt"
func f(name string) {
	fmt.Printf("hello %s", name)
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	edits, n := rewriteFormatFuncsToNonF(fset, f, []byte(src), nil, nil)
	if n != 1 {
		t.Fatalf("rewrote %d, want 1", n)
	}
	if !strings.Contains(string(edits[0].text), `fmt.Print("hello {name:s}")`) {
		t.Fatalf("got %q", edits[0].text)
	}
}

func TestErrorsNewToInterp(t *testing.T) {
	const src = `package p
import "errors"
func f(endpoint string, err error) error {
	return errors.New("invalid %s: %v", endpoint, err)
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	edits, n := rewriteErrorsNewToInterp(fset, f, []byte(src), nil)
	if n != 1 {
		t.Fatalf("rewrote %d, want 1", n)
	}
	got := string(edits[0].text)
	if !strings.Contains(got, `errors.New("invalid {endpoint:s}: {err:v}")`) {
		t.Fatalf("got %q", got)
	}
}

func TestEscapeLiteralBracesSkipsErrorsNew(t *testing.T) {
	const src = `package p
import "errors"
func f(str string) error {
	return errors.New("ParseBool: parsing '{str}': {strconv.ErrSyntax}")
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	edits, n := escapeLiteralBraces(fset, f, []byte(src), nil)
	if n != 0 {
		t.Fatalf("rewrote %d edits, want 0; got %v", n, edits)
	}
}

func TestConfigErrorfToError(t *testing.T) {
	const src = `package p

import "github.com/example/config"

func f(name string) error {
	return config.Errorf("bad %s", name)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	edits, n := rewriteFormatFuncsToNonF(fset, f, []byte(src), nil, nil)
	if n != 1 {
		t.Fatalf("rewrote %d, want 1", n)
	}
	got := string(edits[0].text)
	want := `config.Error[ErrConfigGeneric]("bad {name:s}")`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
