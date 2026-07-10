package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigStepCommitsTrue(t *testing.T) {
	if !DefaultConfig().StepCommits {
		t.Fatal("expected step_commits default true")
	}
}

func TestConfigForNilableStep(t *testing.T) {
	base := DefaultConfig()
	base.ErrBangSignatures = false
	base.ShorthandTypes = false
	step := modernizeSteps[1] // nilable_pointers
	if step.name != "nilable_pointers" {
		t.Fatalf("unexpected step: %s", step.name)
	}
	got := step.stepConfig(base)
	if !got.NilablePointersAnnotate || got.ErrBangSignatures || got.ShorthandTypes {
		t.Fatalf("unexpected step config: %+v", got)
	}
}

func TestFindVCSRootGit(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "pkg", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	root, kind := findVCSRoot(sub)
	if kind != vcsGit || root != dir {
		t.Fatalf("findVCSRoot(%q) = (%q, %q), want (%q, git)", sub, root, kind, dir)
	}
}

func TestModernizeStepOrder(t *testing.T) {
	want := []string{"formatting", "nilable_pointers", "err_bang", "structured_errors", "shorthand_types"}
	if len(modernizeSteps) != len(want) {
		t.Fatalf("got %d steps, want %d", len(modernizeSteps), len(want))
	}
	for i, name := range want {
		if modernizeSteps[i].name != name {
			t.Fatalf("step %d: got %q, want %q", i, modernizeSteps[i].name, name)
		}
	}
}
