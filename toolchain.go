package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const bowParserProbe = `package bowprobe
func _() {
	for x in []int{1} {
		_ = x
	}
}
func f(args ...int) {}
func g() { f(...[]int{1}) }
`

func goRoot() string {
	if g := os.Getenv("GOROOT"); g != "" {
		return g
	}
	return runtime.GOROOT()
}

func goTool(name string) string {
	return filepath.Join(goRoot(), "bin", name)
}

func bowEnv() []string {
	root := goRoot()
	env := os.Environ()
	out := make([]string, 0, len(env)+2)
	seenGOROOT := false
	seenPATH := false
	prefix := filepath.Join(root, "bin")
	for _, e := range env {
		if strings.HasPrefix(e, "GOROOT=") {
			out = append(out, "GOROOT="+root)
			seenGOROOT = true
			continue
		}
		if strings.HasPrefix(e, "PATH=") {
			out = append(out, "PATH="+prefix+string(os.PathListSeparator)+strings.TrimPrefix(e, "PATH="))
			seenPATH = true
			continue
		}
		out = append(out, e)
	}
	if !seenGOROOT {
		out = append(out, "GOROOT="+root)
	}
	if !seenPATH {
		out = append(out, "PATH="+prefix)
	}
	return out
}

func verifyBowParser() error {
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "bowprobe.go", bowParserProbe, 0); err != nil {
		return fmt.Errorf("Bow parser probe failed (GOROOT=%s): %w\nRebuild modernize with the Bow Go toolchain (GOROOT=%s)", goRoot(), err, goRoot())
	}
	if _, err := os.Stat(goTool("gofmt")); err != nil {
		return fmt.Errorf("Bow gofmt not found at %s: %w", goTool("gofmt"), err)
	}
	return nil
}

func runGoFmt(args ...string) ([]byte, error) {
	cmd := exec.Command(goTool("gofmt"), args...)
	cmd.Env = bowEnv()
	return cmd.CombinedOutput()
}

func parseModernizeFile(fset *token.FileSet, path string) (*ast.File, error) {
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	f.NilablePointersRegions = buildNilablePointersRegions(collectNilablePointersDirectives(f))
	return f, nil
}

func reparseModernizeFile(fset *token.FileSet, path string, src []byte) (*ast.File, error) {
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	f.NilablePointersRegions = buildNilablePointersRegions(collectNilablePointersDirectives(f))
	return f, nil
}
