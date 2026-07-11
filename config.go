package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config toggles each modernization pass. All flags default to true.
type Config struct {
	NilablePointersGoMod             bool `json:"nilable_pointers_go_mod"`
	NilablePointersGenDisable        bool `json:"nilable_pointers_gen_disable"`
	NilablePointersAnnotate          bool `json:"nilable_pointers_annotate"`
	ErrBangSignatures                bool `json:"err_bang_signatures"`
	ErrBangBody                      bool `json:"err_bang_body"`
	FmtErrorfToErrorsNew             bool `json:"fmt_errorf_to_errors_new"`
	ErrorsBaseEmbed                  bool `json:"errors_base_embed"`
	ErrorsBaseSetMsg                 bool `json:"errors_base_setmsg"`
	ErrorsBasePositionalComposites   bool `json:"errors_base_positional_composites"`
	ErrorsBaseMessageFieldRefs       bool `json:"errors_base_message_field_refs"`
	ErrorsBaseUsages                 bool `json:"errors_base_usages"`
	ShorthandTypes                   bool `json:"shorthand_types"`
	ForInSyntax                      bool `json:"for_in_syntax"`
	ShorthandLiterals                bool `json:"shorthand_literals"`
	SpreadCallSyntax                 bool `json:"spread_call_syntax"`
	NegativeSliceIndices             bool `json:"negative_slice_indices"`
	InterpolatedStrings              bool `json:"interpolated_strings"`
	StepCommits                      bool `json:"step_commits"`
	RemoveNilReceiverGuards          bool `json:"remove_nil_receiver_guards"`
	OptionalMethodChains             bool `json:"optional_method_chains"`
}

func DefaultConfig() Config {
	return Config{
		NilablePointersGoMod:           true,
		NilablePointersGenDisable:        true,
		NilablePointersAnnotate:          true,
		ErrBangSignatures:                true,
		ErrBangBody:                      true,
		FmtErrorfToErrorsNew:             true,
		ErrorsBaseEmbed:                  true,
		ErrorsBaseSetMsg:                 true,
		ErrorsBasePositionalComposites:   true,
		ErrorsBaseMessageFieldRefs:       true,
		ErrorsBaseUsages:                 true,
		ShorthandTypes:                   true,
		ForInSyntax:                      true,
		ShorthandLiterals:                true,
		SpreadCallSyntax:                 true,
		NegativeSliceIndices:             true,
		InterpolatedStrings:              true,
		StepCommits:                      true,
		RemoveNilReceiverGuards:          true,
		OptionalMethodChains:             true,
	}
}

func loadConfig(targetRoot string) (Config, string, error) {
	cfg := DefaultConfig()
	var paths []string
	if env := os.Getenv("MODERNIZE_CONFIG"); env != "" {
		paths = append(paths, env)
	}
	paths = append(paths, filepath.Join(targetRoot, "modernize.json"))
	if repo := findRepoConfig(); repo != "" {
		paths = append(paths, repo)
	}
	seen := map[string]bool{}
	for _, path := range paths {
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		loaded, err := readConfigFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return cfg, "", err
		}
		cfg = loaded
		return cfg, path, nil
	}
	return cfg, "", nil
}

func (c Config) anyStructuredErrors() bool {
	return c.FmtErrorfToErrorsNew || c.ErrorsBaseEmbed || c.ErrorsBaseSetMsg ||
		c.ErrorsBasePositionalComposites || c.ErrorsBaseMessageFieldRefs || c.ErrorsBaseUsages
}

func readConfigFile(path string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func findRepoConfig() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, "modernize", "modernize.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
