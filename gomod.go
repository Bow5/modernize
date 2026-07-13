package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"os"
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

// ensureNilablePointers adds or upgrades `nilable_pointers warnings` in go.mod.
func ensureNilablePointers(modRoot string) (bool, error) {
	modPath := filepath.Join(modRoot, "go.mod")
	data, err := os.ReadFile(modPath)
	if err != nil {
		return false, err
	}
	content := string(data)
	if dir := goModNilablePointersDirective(content); dir != "" {
		if dir == "nilable_pointers warnings" {
			return false, nil
		}
		updated := strings.Replace(content, dir, "nilable_pointers warnings", 1)
		if updated == content {
			return false, nil
		}
		return true, os.WriteFile(modPath, []byte(updated), 0)
	}
	updated := insertNilablePointersDirective(content)
	if updated == content {
		return false, nil
	}
	return true, os.WriteFile(modPath, []byte(updated), 0)
}

func goModNilablePointersDirective(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nilable_pointers ") {
			return line
		}
	}
	return ""
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
			out = append(out, "", "nilable_pointers warnings")
			inserted = true
			continue
		}
		// go directive missing: insert before first require/replace/blank or at end of header.
		if i + 1 < len(lines) {
			next := strings.TrimSpace(lines[i + 1])
			if trim != "" && !strings.HasPrefix(trim, "module ") && !strings.HasPrefix(trim, "go 1.") &&
				(strings.HasPrefix(next, "require") || next == "" || strings.HasPrefix(next, "replace")) {
				out = append(out, "", "nilable_pointers warnings")
				inserted = true
			}
		}
	}
	if !inserted {
		if len(out) > 0 && out[len(out) - 1] != "" {
			out = append(out, "")
		}
		out = append(out, "nilable_pointers warnings")
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

func writeSourceFile(path string, src []byte) error {
	if err := os.WriteFile(path, src, 0); err != nil {
		return err
	}
	if out, err := runGoFmt("-w", path); err != nil {
		return fmt.Errorf("gofmt %s: %v: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeFormattedFile(path string, fset *token.FileSet, f *ast.File) error {
	var out bytes.Buffer
	if err := format.Node(&out, fset, f); err != nil {
		return err
	}
	data := collapseBlankLineAfterOpeningBrace(out.Bytes())
	if err := os.WriteFile(path, data, 0); err != nil {
		return err
	}
	if out, err := runGoFmt("-w", path); err != nil {
		return fmt.Errorf("gofmt %s: %v: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// collapseBlankLineAfterOpeningBrace removes a single blank line between an
// opening brace and the next indented statement — common after deleting a
// nil-receiver guard block.
func collapseBlankLineAfterOpeningBrace(src []byte) []byte {
	lines := strings.Split(string(src), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasSuffix(strings.TrimSpace(line), "{") && i + 2 < len(lines) && lines[i + 1] == "" && isIndentedStmtLine(lines[i + 2]) {
			out = append(out, line)
			i++
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

func isIndentedStmtLine(line string) bool {
	return strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ")
}
