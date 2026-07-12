package main

import (
	"os"
	"os/exec"
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
	step := modernizeSteps[11] // nilable_pointers
	if step.name != "nilable_pointers" {
		t.Fatalf("unexpected step: %s", step.name)
	}
	got := step.stepConfig(base)
	if !got.NilablePointersAnnotate || got.ErrBangSignatures || got.ShorthandTypes || got.RemoveNilReceiverGuards {
		t.Fatalf("unexpected step config: %+v", got)
	}
	if got.NilCoalesceFallback {
		t.Fatalf("expected nil_coalesce_fallback false in default step config: %+v", got)
	}
}

func TestConfigForNilReceiverStep(t *testing.T) {
	base := DefaultConfig()
	step := modernizeSteps[1] // nil_receivers
	if step.name != "nil_receivers" {
		t.Fatalf("unexpected step: %s", step.name)
	}
	got := step.stepConfig(base)
	if !got.RemoveNilReceiverGuards || !got.OptionalMethodChains || got.NilablePointersAnnotate {
		t.Fatalf("unexpected step config: %+v", got)
	}
}

func TestFindVCSRootGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
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

func TestFindVCSRootHg(t *testing.T) {
	if _, err := exec.LookPath("hg"); err != nil {
		t.Skip("hg not on PATH")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".hg"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "pkg", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	root, kind := findVCSRoot(sub)
	if kind != vcsHg || root != dir {
		t.Fatalf("findVCSRoot(%q) = (%q, %q), want (%q, hg)", sub, root, kind, dir)
	}
}

func TestFindVCSRootSkipsRepoWithoutBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("git"); err == nil {
		t.Skip("git is on PATH; cannot test missing-binary case")
	}
	root, kind := findVCSRoot(dir)
	if kind != "" || root != "" {
		t.Fatalf("findVCSRoot(%q) = (%q, %q), want empty without git on PATH", dir, root, kind)
	}
}

func TestModernizeStepOrder(t *testing.T) {
	want := []string{"formatting", "nil_receivers", "err_bang", "structured_errors", "for_in_syntax", "shorthand_literals", "spread_call_syntax", "negative_slice_indices", "interpolated_strings", "interface_nil_eq", "shorthand_types", "nilable_pointers"}
	if len(modernizeSteps) != len(want) {
		t.Fatalf("got %d steps, want %d", len(modernizeSteps), len(want))
	}
	for i, name := range want {
		if modernizeSteps[i].name != name {
			t.Fatalf("step %d: got %q, want %q", i, modernizeSteps[i].name, name)
		}
	}
}
