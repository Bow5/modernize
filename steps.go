package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type vcsKind string

const (
	vcsGit vcsKind = "git"
	vcsHg  vcsKind = "hg"
)

type modernizeStep struct {
	name       string
	commitMsg  string
	enabled    func(Config) bool
	stepConfig func(Config) Config
}

// modernizeSteps follows go/README.md migration order (formatting first, then
// nilable pointers, T!/!, structured errors, struct/interface shorthand).
var modernizeSteps = []modernizeStep{
	{
		name:      "formatting",
		commitMsg: "modernize: apply gofmt formatting",
		enabled:   func(Config) bool { return true },
		stepConfig: func(base Config) Config {
			return base
		},
	},
	{
		name:      "nilable_pointers",
		commitMsg: "modernize: nilable pointer annotations",
		enabled: func(c Config) bool {
			return c.NilablePointersGoMod || c.NilablePointersGenDisable || c.NilablePointersAnnotate
		},
		stepConfig: func(base Config) Config {
			return Config{
				NilablePointersGoMod:      base.NilablePointersGoMod,
				NilablePointersGenDisable: base.NilablePointersGenDisable,
				NilablePointersAnnotate:   base.NilablePointersAnnotate,
			}
		},
	},
	{
		name:      "err_bang",
		commitMsg: "modernize: T! result types and ! error propagation",
		enabled: func(c Config) bool {
			return c.ErrBangSignatures || c.ErrBangBody
		},
		stepConfig: func(base Config) Config {
			return Config{
				ErrBangSignatures: base.ErrBangSignatures,
				ErrBangBody:       base.ErrBangBody,
			}
		},
	},
	{
		name:      "structured_errors",
		commitMsg: "modernize: structured errors (errors.Base)",
		enabled:   func(c Config) bool { return c.anyStructuredErrors() },
		stepConfig: func(base Config) Config {
			return Config{
				FmtErrorfToErrorsNew:           base.FmtErrorfToErrorsNew,
				ErrorsBaseEmbed:                  base.ErrorsBaseEmbed,
				ErrorsBaseSetMsg:                 base.ErrorsBaseSetMsg,
				ErrorsBasePositionalComposites:   base.ErrorsBasePositionalComposites,
				ErrorsBaseMessageFieldRefs:       base.ErrorsBaseMessageFieldRefs,
				ErrorsBaseUsages:                 base.ErrorsBaseUsages,
			}
		},
	},
	{
		name:      "shorthand_types",
		commitMsg: "modernize: struct and interface shorthand",
		enabled: func(c Config) bool {
			return c.ShorthandTypes
		},
		stepConfig: func(base Config) Config {
			return Config{ShorthandTypes: base.ShorthandTypes}
		},
	},
}

func findVCSRoot(start string) (root string, kind vcsKind) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", ""
	}
	for {
		if isGitRepo(dir) {
			return dir, vcsGit
		}
		if isHgRepo(dir) {
			return dir, vcsHg
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func isHgRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".hg"))
	return err == nil
}

func runStepCommits(absRoot string, baseCfg Config, vcsRoot string, kind vcsKind) error {
	for _, step := range modernizeSteps {
		if !step.enabled(baseCfg) {
			continue
		}
		stepCfg := step.stepConfig(baseCfg)
		var changed []string
		var err error
		if step.name == "formatting" {
			changed, err = runFormattingPass(absRoot)
		} else {
			changed, err = runModernizePass(absRoot, stepCfg)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		if len(changed) == 0 {
			fmt.Fprintf(os.Stderr, "step %s: no changes\n", step.name)
			continue
		}
		fmt.Fprintf(os.Stderr, "step %s: %d file(s) changed\n", step.name, len(changed))
		if err := commitStep(vcsRoot, kind, step.commitMsg, changed); err != nil {
			return fmt.Errorf("%s commit: %w", step.name, err)
		}
		fmt.Fprintf(os.Stderr, "step %s: committed\n", step.name)
	}
	return nil
}

func runFormattingPass(root string) ([]string, error) {
	paths, err := collectGoSourceFiles(root)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	gofmtPath := "gofmt"
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		gofmtPath = filepath.Join(goroot, "bin", "gofmt")
	}
	before, err := gofmtList(gofmtPath, paths)
	if err != nil {
		return nil, err
	}
	if len(before) == 0 {
		return nil, nil
	}
	cmd := exec.Command(gofmtPath, append([]string{"-w"}, before...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("gofmt: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return before, nil
}

func gofmtList(gofmtPath string, paths []string) ([]string, error) {
	cmd := exec.Command(gofmtPath, append([]string{"-l"}, paths...)...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 0 {
			// gofmt -l returns 0 even when listing files
		} else {
			return nil, fmt.Errorf("gofmt -l: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	var changed []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			changed = append(changed, line)
		}
	}
	return changed, nil
}

func collectGoSourceFiles(root string) ([]string, error) {
	var paths []string
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
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	return paths, err
}

type passSummary struct {
	changedFiles int
	changedPaths []string
	counts       rewriteCounts
}

func runModernizePass(absRoot string, cfg Config) ([]string, error) {
	summary, err := runModernize(absRoot, cfg)
	if err != nil {
		return nil, err
	}
	printSummary(summary)
	return summary.changedPaths, nil
}

func commitStep(vcsRoot string, kind vcsKind, message string, paths []string) error {
	switch kind {
	case vcsGit:
		return gitCommit(vcsRoot, message, paths)
	case vcsHg:
		return hgCommit(vcsRoot, message, paths)
	default:
		return fmt.Errorf("unsupported vcs %q", kind)
	}
}

func gitCommit(vcsRoot, message string, paths []string) error {
	args := []string{"-C", vcsRoot, "add", "--"}
	args = append(args, paths...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	diff := exec.Command("git", "-C", vcsRoot, "diff", "--cached", "--quiet")
	if err := diff.Run(); err == nil {
		return nil
	}
	cmd := exec.Command("git", "-C", vcsRoot, "commit", "-m", message)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func hgCommit(vcsRoot, message string, paths []string) error {
	for _, path := range paths {
		rel, err := filepath.Rel(vcsRoot, path)
		if err != nil {
			rel = path
		}
		cmd := exec.Command("hg", "add", rel)
		cmd.Dir = vcsRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("hg add %s: %v: %s", rel, err, strings.TrimSpace(string(out)))
		}
	}
	cmd := exec.Command("hg", "commit", "-m", message)
	cmd.Dir = vcsRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("hg commit: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
