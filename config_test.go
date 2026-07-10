package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigAllTrue(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.NilablePointersGoMod || !cfg.NilablePointersAnnotate || !cfg.ErrBangSignatures ||
		!cfg.ErrBangBody || !cfg.FmtErrorfToErrorsNew || !cfg.ErrorsBaseEmbed ||
		!cfg.ErrorsBaseSetMsg || !cfg.ErrorsBasePositionalComposites ||
		!cfg.ErrorsBaseMessageFieldRefs || !cfg.ErrorsBaseUsages || !cfg.ShorthandTypes ||
		!cfg.StepCommits || !cfg.RemoveNilReceiverGuards || !cfg.OptionalMethodChains {
		t.Fatalf("expected all defaults true: %+v", cfg)
	}
}

func TestLoadConfigPartialOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modernize.json")
	if err := os.WriteFile(path, []byte(`{"errors_base_embed": false, "err_bang_body": false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, loaded, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != path {
		t.Fatalf("loaded from %q, want %q", loaded, path)
	}
	if cfg.ErrorsBaseEmbed || cfg.ErrBangBody {
		t.Fatalf("expected overrides false: %+v", cfg)
	}
	if !cfg.ErrorsBaseUsages || !cfg.ErrBangSignatures || !cfg.NilablePointersAnnotate {
		t.Fatalf("expected other flags to stay true: %+v", cfg)
	}
}
