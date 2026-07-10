package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const nilableDisableDirective = "//go:nilable_pointers disable\n"

func disableNilablePointersOnGenFiles(root string) ([]string, error) {
	var changed []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if isSkippedDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		if strings.Contains(content, "nilable_pointers disable") {
			return nil
		}
		updated := nilableDisableDirective + content
		if err := os.WriteFile(path, []byte(updated), 0); err != nil {
			return err
		}
		changed = append(changed, path)
		fmt.Println(path)
		return nil
	})
	return changed, err
}
