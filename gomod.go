package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func findModuleRoot(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// ensureNilablePointers adds `nilable_pointers enable` to go.mod when missing.
func ensureNilablePointers(modRoot string) (bool, error) {
	modPath := filepath.Join(modRoot, "go.mod")
	data, err := os.ReadFile(modPath)
	if err != nil {
		return false, err
	}
	content := string(data)
	if hasNilablePointersDirective(content) {
		return false, nil
	}
	updated := insertNilablePointersDirective(content)
	if updated == content {
		return false, nil
	}
	return true, os.WriteFile(modPath, []byte(updated), 0)
}

func hasNilablePointersDirective(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nilable_pointers ") {
			return true
		}
	}
	return false
}

func insertNilablePointersDirective(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inserted := false
	for i, line := range lines {
		out = append(out, line)
		if inserted {
			continue
		}
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "go 1.") {
			out = append(out, "", "nilable_pointers enable")
			inserted = true
			continue
		}
		// go directive missing: insert before first require/replace/blank or at end of header.
		if i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if trim != "" && !strings.HasPrefix(trim, "module ") && !strings.HasPrefix(trim, "go 1.") &&
				(strings.HasPrefix(next, "require") || next == "" || strings.HasPrefix(next, "replace")) {
				out = append(out, "", "nilable_pointers enable")
				inserted = true
			}
		}
	}
	if !inserted {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, "nilable_pointers enable")
	}
	// Normalize trailing newline.
	result := strings.Join(out, "\n")
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return result
}

func isSkippedDir(name string) bool {
	switch name {
	case ".git", "vendor", "testdata":
		return true
	default:
		return strings.HasPrefix(name, ".")
	}
}

func writeFormattedFile(path string, fset *token.FileSet, f *ast.File) error {
	var out bytes.Buffer
	if err := format.Node(&out, fset, f); err != nil {
		return err
	}
	if err := os.WriteFile(path, out.Bytes(), 0); err != nil {
		return err
	}
	gofmtPath := "gofmt"
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		gofmtPath = filepath.Join(goroot, "bin", "gofmt")
	}
	cmd := exec.Command(gofmtPath, "-w", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gofmt %s: %v: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}
