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
// nil receivers, T!/!, structured errors, syntax sugar, struct shorthand, nilable pointers last).
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
		name:      "nil_receivers",
		commitMsg: "modernize: remove nil-receiver guards and optional ?. chains",
		enabled: func(c Config) bool {
			return c.RemoveNilReceiverGuards || c.OptionalMethodChains
		},
		stepConfig: func(base Config) Config {
			return Config{
				RemoveNilReceiverGuards: base.RemoveNilReceiverGuards,
				OptionalMethodChains:    base.OptionalMethodChains,
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
				ErrorsBaseEmbed:                base.ErrorsBaseEmbed,
				ErrorsBaseSetMsg:               base.ErrorsBaseSetMsg,
				ErrorsBasePositionalComposites: base.ErrorsBasePositionalComposites,
				ErrorsBaseMessageFieldRefs:     base.ErrorsBaseMessageFieldRefs,
				ErrorsBaseUsages:               base.ErrorsBaseUsages,
			}
		},
	},
	{
		name:      "for_in_syntax",
		commitMsg: "modernize: for-in loop syntax",
		enabled: func(c Config) bool {
			return c.ForInSyntax
		},
		stepConfig: func(base Config) Config {
			return Config{ForInSyntax: base.ForInSyntax}
		},
	},
	{
		name:      "shorthand_literals",
		commitMsg: "modernize: array, map, and set literal syntax",
		enabled: func(c Config) bool {
			return c.ShorthandLiterals
		},
		stepConfig: func(base Config) Config {
			return Config{ShorthandLiterals: base.ShorthandLiterals}
		},
	},
	{
		name:      "spread_call_syntax",
		commitMsg: "modernize: prefix spread in variadic calls",
		enabled: func(c Config) bool {
			return c.SpreadCallSyntax
		},
		stepConfig: func(base Config) Config {
			return Config{SpreadCallSyntax: base.SpreadCallSyntax}
		},
	},
	{
		name:      "negative_slice_indices",
		commitMsg: "modernize: negative slice index syntax",
		enabled: func(c Config) bool {
			return c.NegativeSliceIndices
		},
		stepConfig: func(base Config) Config {
			return Config{NegativeSliceIndices: base.NegativeSliceIndices}
		},
	},
	{
		name:      "interpolated_strings",
		commitMsg: "modernize: interpolated string syntax",
		enabled: func(c Config) bool {
			return c.InterpolatedStrings
		},
		stepConfig: func(base Config) Config {
			return Config{InterpolatedStrings: base.InterpolatedStrings}
		},
	},
	{
		name:      "interface_nil_eq",
		commitMsg: "modernize: label interface == nil comparisons for review",
		enabled: func(c Config) bool {
			return c.InterfaceNilEqComments
		},
		stepConfig: func(base Config) Config {
			return Config{InterfaceNilEqComments: base.InterfaceNilEqComments}
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
				OptionalMethodChains:      base.OptionalMethodChains,
			}
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
			changed, err = runFormattingPass(absRoot, vcsRoot, kind)
		} else {
			changed, err = runModernizePass(absRoot, stepCfg)
			if err == nil && step.name == "nil_receivers" && len(changed) > 0 {
				fmtChanged, fmtErr := runFormattingPass(absRoot, vcsRoot, kind)
				if fmtErr != nil {
					err = fmtErr
				} else if len(fmtChanged) > 0 {
					changed = mergeChangedPaths(changed, fmtChanged)
				}
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		if len(changed) == 0 {
			fmt.Fprintf(os.Stderr, "step %s: no changes\n", step.name)
			continue
		}
		fmt.Fprintf(os.Stderr, "step %s: %d file(s) changed\n", step.name, len(changed))
		committed, err := commitStep(vcsRoot, kind, step.commitMsg, changed)
		if err != nil {
			return fmt.Errorf("%s commit: %w", step.name, err)
		}
		if committed {
			fmt.Fprintf(os.Stderr, "step %s: committed\n", step.name)
		} else {
			fmt.Fprintf(os.Stderr, "step %s: nothing to commit\n", step.name)
		}
	}
	return nil
}

func runFormattingPass(root, vcsRoot string, kind vcsKind) ([]string, error) {
	modRoot, ok := findModuleRoot(root)
	if !ok {
		modRoot = root
	}
	cmd := exec.Command("go", "fmt", "./...")
	cmd.Dir = modRoot
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go fmt: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return vcsModifiedFiles(vcsRoot, kind)
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

func mergeChangedPaths(a, b []string) []string {
	seen := make(map[string]struct{}, len(a) + len(b))
	var out []string
	for _, path := range append(a, b...) {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func runModernizePass(absRoot string, cfg Config) ([]string, error) {
	summary, err := runModernize(absRoot, cfg)
	if err != nil {
		return nil, err
	}
	printSummary(summary)
	return summary.changedPaths, nil
}

func commitStep(vcsRoot string, kind vcsKind, message string, paths []string) (bool, error) {
	paths = filterVCSTracked(vcsRoot, kind, paths)
	if len(paths) == 0 {
		return false, nil
	}
	switch kind {
	case vcsGit:
		return gitCommit(vcsRoot, message, paths)
	case vcsHg:
		return hgCommit(vcsRoot, message, paths)
	default:
		return false, fmt.Errorf("unsupported vcs %q", kind)
	}
}

func gitCommit(vcsRoot, message string, paths []string) (bool, error) {
	for _, path := range paths {
		rel, err := filepath.Rel(vcsRoot, path)
		if err != nil {
			rel = path
		}
		cmd := exec.Command("git", "-C", vcsRoot, "add", "--", rel)
		if err := cmd.Run(); err != nil {
			continue
		}
	}
	diff := exec.Command("git", "-C", vcsRoot, "diff", "--cached", "--quiet")
	if err := diff.Run(); err == nil {
		return false, nil
	}
	cmd := exec.Command("git", "-C", vcsRoot, "commit", "-m", message)
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git commit: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

func hgCommit(vcsRoot, message string, paths []string) (bool, error) {
	for _, path := range paths {
		rel, err := filepath.Rel(vcsRoot, path)
		if err != nil {
			rel = path
		}
		cmd := exec.Command("hg", "add", rel)
		cmd.Dir = vcsRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			return false, fmt.Errorf("hg add %s: %v: %s", rel, err, strings.TrimSpace(string(out)))
		}
	}
	cmd := exec.Command("hg", "commit", "-m", message)
	cmd.Dir = vcsRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("hg commit: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

func vcsModifiedFiles(vcsRoot string, kind vcsKind) ([]string, error) {
	switch kind {
	case vcsGit:
		return gitModifiedFiles(vcsRoot)
	case vcsHg:
		return hgModifiedFiles(vcsRoot)
	default:
		return nil, nil
	}
}

func gitModifiedFiles(vcsRoot string) ([]string, error) {
	cmd := exec.Command("git", "-C", vcsRoot, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		if line[0] == '?' && line[1] == '?' {
			continue
		}
		if line[0] == ' ' && line[1] == ' ' {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx + 4:]
		}
		paths = append(paths, filepath.Join(vcsRoot, path))
	}
	return paths, nil
}

func hgModifiedFiles(vcsRoot string) ([]string, error) {
	cmd := exec.Command("hg", "status", "-mard")
	cmd.Dir = vcsRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 3 {
			continue
		}
		paths = append(paths, filepath.Join(vcsRoot, line[2:]))
	}
	return paths, nil
}

func filterVCSTracked(vcsRoot string, kind vcsKind, paths []string) []string {
	var tracked []string
	for _, path := range paths {
		if isVCSTracked(vcsRoot, kind, path) {
			tracked = append(tracked, path)
		}
	}
	return tracked
}

func isVCSTracked(vcsRoot string, kind vcsKind, path string) bool {
	rel, err := filepath.Rel(vcsRoot, path)
	if err != nil {
		rel = path
	}
	switch kind {
	case vcsGit:
		if isGitIgnored(vcsRoot, rel) {
			return false
		}
		cmd := exec.Command("git", "-C", vcsRoot, "ls-files", "--error-unmatch", "--", rel)
		return cmd.Run() == nil
	case vcsHg:
		cmd := exec.Command("hg", "status", "-n", rel)
		cmd.Dir = vcsRoot
		out, err := cmd.Output()
		if err != nil {
			return false
		}
		line := strings.TrimSpace(string(out))
		return line != "" && !strings.HasPrefix(line, "?")
	default:
		return true
	}
}

func isGitIgnored(vcsRoot, rel string) bool {
	cmd := exec.Command("git", "-C", vcsRoot, "check-ignore", "-q", "--", rel)
	return cmd.Run() == nil
}
