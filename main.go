// modernize rewrites (T, error) functions and if err != nil early returns
// to T! and expr! syntax for the Go language fork.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cfg, cfgPath, err := loadConfig(absRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if cfgPath != "" {
		fmt.Fprintf(os.Stderr, "using config %s\n", cfgPath)
	}

	if cfg.StepCommits {
		if vcsRoot, kind := findVCSRoot(absRoot); kind != "" {
			if err := runStepCommits(absRoot, cfg, vcsRoot, kind); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "step_commits enabled but no git/hg repo found; running without commits\n")
	}

	summary, err := runModernize(absRoot, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	printSummary(summary)
}

func printSummary(summary passSummary) {
	fmt.Fprintf(os.Stderr, "modernized %d files (%d nilable, %d verified *T, %d call()!, %d err!, %d fmt.Errorf→errors.New, %d custom errors, %d shorthand types, %d for-in loops, %d nil receiver guards removed, %d optional method chains)\n",
		summary.changedFiles, summary.counts.nilable, summary.counts.verifiedNonNil, summary.counts.callBang,
		summary.counts.errBang, summary.counts.fmtErrorf, summary.counts.customErr, summary.counts.shorthand, summary.counts.forIn,
		summary.counts.nilRecvGuards, summary.counts.optionalChains)
}

func runModernize(absRoot string, cfg Config) (passSummary, error) {
	var summary passSummary

	if modRoot, ok := findModuleRoot(absRoot); ok {
		if cfg.NilablePointersGoMod {
			if changed, err := ensureNilablePointers(modRoot); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Join(modRoot, "go.mod"), err)
			} else if changed {
				modPath := filepath.Join(modRoot, "go.mod")
				fmt.Println(modPath)
				summary.changedPaths = append(summary.changedPaths, modPath)
			}
		}
		if cfg.NilablePointersGenDisable {
			paths, err := disableNilablePointersOnGenFiles(absRoot)
			if err != nil {
				fmt.Fprintf(os.Stderr, "gen files: %v\n", err)
			} else if len(paths) > 0 {
				fmt.Fprintf(os.Stderr, "marked %d *_gen.go files nilable_pointers disable\n", len(paths))
				summary.changedPaths = append(summary.changedPaths, paths...)
			}
		}
	}

	pkgs, err := collectPackages(absRoot)
	if err != nil {
		return summary, err
	}

	modIdx, err := buildModuleFuncIndex(absRoot, pkgs)
	if err != nil {
		return summary, err
	}

	for _, pkg := range pkgs {
		paths, n, e := modernizePackage(pkg, cfg, modIdx)
		if e != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", pkg.dir, e)
			continue
		}
		summary.changedFiles += len(paths)
		summary.changedPaths = append(summary.changedPaths, paths...)
		summary.counts.callBang += n.callBang
		summary.counts.errBang += n.errBang
		summary.counts.nilable += n.nilable
		summary.counts.verifiedNonNil += n.verifiedNonNil
		summary.counts.fmtErrorf += n.fmtErrorf
		summary.counts.customErr += n.customErr
		summary.counts.shorthand += n.shorthand
		summary.counts.nilRecvGuards += n.nilRecvGuards
		summary.counts.optionalChains += n.optionalChains
	}
	return summary, nil
}

type pkgFiles struct {
	dir   string
	paths []string
}

func collectPackages(root string) ([]pkgFiles, error) {
	byDir := map[string][]string{}
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
		dir := filepath.Dir(path)
		byDir[dir] = append(byDir[dir], path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []pkgFiles
	for dir, paths := range byDir {
		sort.Strings(paths)
		out = append(out, pkgFiles{dir: dir, paths: paths})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dir < out[j].dir })
	return out, nil
}

type rewriteCounts struct {
	callBang       int
	errBang        int
	nilable        int
	verifiedNonNil int
	fmtErrorf      int
	customErr      int
	shorthand      int
	forIn          int
	nilRecvGuards  int
	optionalChains int
}

func modernizePackage(pkg pkgFiles, cfg Config, modIdx *moduleFuncIndex) (changedPaths []string, counts rewriteCounts, err error) {
	fset := token.NewFileSet()
	files := make([]*ast.File, len(pkg.paths))
	for i, path := range pkg.paths {
		f, e := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if e != nil {
			return nil, counts, e
		}
		f.NilablePointersRegions = buildNilablePointersRegions(collectNilablePointersDirectives(f))
		files[i] = f
	}

	nilableChanged := make([]bool, len(files))
	var verifiedNonNil int
	if cfg.NilablePointersAnnotate {
		nilableChanged, verifiedNonNil = applyPtrAnnotations(fset, files)
	}
	pkgEmbed := collectPackageEmbedOnlyTypes(files)
	pkgExtraFields := collectPackageHasExtraErrorTypes(files)

	for i, path := range pkg.paths {
		_, countsPart, fileChanged, e := modernizeParsedFile(fset, files, files[i], path, nilableChanged[i], pkgEmbed, pkgExtraFields, cfg, modIdx)
		if e != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, e)
			continue
		}
		if fileChanged {
			changedPaths = append(changedPaths, path)
			fmt.Println(path)
		}
		counts.callBang += countsPart.callBang
		counts.errBang += countsPart.errBang
		counts.fmtErrorf += countsPart.fmtErrorf
		counts.customErr += countsPart.customErr
		counts.shorthand += countsPart.shorthand
		counts.forIn += countsPart.forIn
		counts.nilRecvGuards += countsPart.nilRecvGuards
		counts.optionalChains += countsPart.optionalChains
		if nilableChanged[i] && fileChanged {
			counts.nilable++
		}
	}
	counts.verifiedNonNil = verifiedNonNil
	return changedPaths, counts, nil
}

func modernizeParsedFile(fset *token.FileSet, pkgFiles []*ast.File, f *ast.File, path string, forceWrite bool, pkgEmbed map[string]string, pkgExtraFields map[string][]string, cfg Config, modIdx *moduleFuncIndex) (nilableChanged bool, counts rewriteCounts, changed bool, err error) {
	mod := &fileModernizer{fset: fset, file: f, pkgEmbed: pkgEmbed, pkgExtraFields: pkgExtraFields, cfg: cfg}

	if cfg.ErrBangSignatures {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				mod.modernizeFunc(n)
			case *ast.InterfaceType:
				mod.modernizeInterface(n)
			}
			return true
		})
	}

	if cfg.ErrBangBody {
		ast.Inspect(f, func(n ast.Node) bool {
			if fn, ok := n.(*ast.FuncDecl); ok {
				mod.simplifyNilReturnsInFunc(fn)
				mod.fixErrorReturns(fn)
				mod.propagateReturnErrInFunc(fn)
				mod.removeUnusedErrVarInFunc(fn)
			}
			return true
		})
	}
	if cfg.anyStructuredErrors() {
		counts.fmtErrorf, counts.customErr = mod.modernizeStructuredErrors()
	}
	if cfg.ShorthandTypes {
		counts.shorthand = mod.modernizeShorthandTypes()
	}
	if cfg.ForInSyntax {
		counts.forIn = mod.modernizeForIn()
	}
	if cfg.RemoveNilReceiverGuards || cfg.OptionalMethodChains {
		guards, chains := modernizeNilReceivers(f, pkgFiles, cfg, modIdx)
		counts.nilRecvGuards = guards
		counts.optionalChains = chains
		if guards > 0 || chains > 0 {
			mod.mark()
		}
	}
	counts.callBang = mod.callBangCount
	counts.errBang = mod.errBangStmtCount

	if !mod.changed && !forceWrite {
		return false, counts, false, nil
	}

	if err := writeFormattedFile(path, fset, f); err != nil {
		return false, counts, false, err
	}
	return forceWrite, counts, true, nil
}

func modernizeFile(path string) (bool, int, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return false, 0, err
	}
	f.NilablePointersRegions = buildNilablePointersRegions(collectNilablePointersDirectives(f))
	changedFlags, _ := applyPtrAnnotations(fset, []*ast.File{f})
	_, countsPart, changed, err := modernizeParsedFile(fset, []*ast.File{f}, f, path, changedFlags[0], nil, nil, DefaultConfig(), nil)
	return changed, countsPart.callBang + countsPart.errBang, err
}

type fileModernizer struct {
	fset           *token.FileSet
	file           *ast.File
	pkgEmbed       map[string]string   // embed-only error type → removed message field (package scope)
	pkgExtraFields map[string][]string // has-extra error type → domain field names (package scope)
	cfg            Config
	changed        bool
	callBangCount  int
	errBangStmtCount int
}

func (m *fileModernizer) modernizeForIn() int {
	n := modernizeForIn(m.file)
	if n > 0 {
		m.mark()
	}
	return n
}

func (m *fileModernizer) modernizeShorthandTypes() int {
	n := modernizeShorthandTypes(m.file)
	if n > 0 {
		m.mark()
	}
	return n
}

func (m *fileModernizer) simplifyNilReturnsInFunc(fn *ast.FuncDecl) {
	if fn.Type == nil || fn.Body == nil || !hasResultTypeBang(fn.Type) {
		return
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 2 || !isNilIdent(ret.Results[1]) {
			return true
		}
		ret.Results = []ast.Expr{ret.Results[0]}
		m.mark()
		return true
	})
}

// fixErrorReturns rewrites `return zero, err` to `return err` in (T, error) and T! functions.
func (m *fileModernizer) fixErrorReturns(fn *ast.FuncDecl) {
	if fn.Type == nil || fn.Body == nil || !canFixErrorReturns(fn.Type) {
		return
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 2 {
			return true
		}
		if isNilExpr(ret.Results[1]) {
			return true
		}
		if !isNilExpr(ret.Results[0]) && !isZeroReturnValue(ret.Results[0]) {
			return true
		}
		ret.Results = []ast.Expr{ret.Results[1]}
		m.mark()
		return true
	})
}

func isZeroReturnValue(e ast.Expr) bool {
	e = ast.Unparen(e)
	switch z := e.(type) {
	case *ast.BasicLit:
		return z.Value == "0" || z.Value == "0.0" || z.Value == "false" || z.Value == "\"\""
	case *ast.Ident:
		return z.Name == "nil"
	default:
		return false
	}
}

func canFixErrorReturns(ft *ast.FuncType) bool {
	return hasResultTypeBang(ft)
}

func hasResultTypeBang(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	_, ok := ft.Results.List[0].Type.(*ast.ResultTypeExpr)
	return ok
}

func canPropagateWithBang(ft *ast.FuncType) bool {
	if hasResultTypeBang(ft) {
		return true
	}
	if ft.Results != nil && len(ft.Results.List) == 1 {
		return isErrorType(ft.Results.List[0].Type)
	}
	return false
}

func (m *fileModernizer) removeUnusedErrVarInFunc(fn *ast.FuncDecl) {
	if fn.Body == nil {
		return
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if b, ok := n.(*ast.BlockStmt); ok {
			m.removeUnusedErrVarInBody(b)
		}
		return true
	})
}

func (m *fileModernizer) removeUnusedErrVarInBody(body *ast.BlockStmt) {
	for i := 0; i < len(body.List); i++ {
		decl, ok := body.List[i].(*ast.DeclStmt)
		if !ok {
			continue
		}
		gen, ok := decl.Decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		if !m.cleanUnusedErrInVarDecl(gen, body, i) {
			continue
		}
		if len(gen.Specs) == 0 {
			body.List = append(body.List[:i], body.List[i+1:]...)
			i--
		}
		m.mark()
	}
}

func (m *fileModernizer) cleanUnusedErrInVarDecl(gen *ast.GenDecl, body *ast.BlockStmt, declIdx int) bool {
	var newSpecs []ast.Spec
	changed := false
	for _, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "err" || len(vs.Values) != 0 || !isErrorType(vs.Type) {
			newSpecs = append(newSpecs, spec)
			continue
		}
		if errVarHasUsesInBlock(body, declIdx) {
			newSpecs = append(newSpecs, spec)
			continue
		}
		changed = true
	}
	if changed {
		gen.Specs = newSpecs
	}
	return changed
}

func isVarErrDecl(decl *ast.DeclStmt) bool {
	gen, ok := decl.Decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.VAR || len(gen.Specs) != 1 {
		return false
	}
	spec, ok := gen.Specs[0].(*ast.ValueSpec)
	if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "err" {
		return false
	}
	return len(spec.Values) == 0 && isErrorType(spec.Type)
}

func errVarHasUsesInBlock(body *ast.BlockStmt, declIdx int) bool {
	for j := declIdx + 1; j < len(body.List); j++ {
		if stmtUsesOuterErr(body.List[j], "err") {
			return true
		}
	}
	return false
}

func stmtUsesOuterErr(stmt ast.Stmt, name string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok && assign.Tok == token.DEFINE {
			for _, lhs := range assign.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
					for _, rhs := range assign.Rhs {
						ast.Inspect(rhs, func(nn ast.Node) bool {
							if id, ok := nn.(*ast.Ident); ok && id.Name == name {
								found = true
								return false
							}
							return true
						})
					}
					return false
				}
			}
		}
		if ifStmt, ok := n.(*ast.IfStmt); ok {
			if assign, ok := ifStmt.Init.(*ast.AssignStmt); ok && assign.Tok == token.DEFINE {
				for _, lhs := range assign.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
						return false
					}
				}
			}
		}
		if b, ok := n.(*ast.BlockStmt); ok && n != stmt {
			if blockDeclaresErr(b, name) {
				return false
			}
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func blockDeclaresErr(body *ast.BlockStmt, name string) bool {
	for _, s := range body.List {
		if assign, ok := s.(*ast.AssignStmt); ok && assign.Tok == token.DEFINE {
			for _, lhs := range assign.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
					return true
				}
			}
		}
		if decl, ok := s.(*ast.DeclStmt); ok && isVarErrDecl(decl) {
			return true
		}
	}
	return false
}

func (m *fileModernizer) propagateReturnErrInFunc(fn *ast.FuncDecl) {
	if fn.Type == nil || fn.Body == nil || !canPropagateWithBang(fn.Type) {
		return
	}
	var vt ast.Expr
	var params []string
	if fn.Type.Results != nil && len(fn.Type.Results.List) == 1 {
		if rt, ok := fn.Type.Results.List[0].Type.(*ast.ResultTypeExpr); ok {
			vt = rt.X
		}
	}
	if fn.Type != nil {
		params = paramNames(fn.Type)
	}
	m.walkFuncStmtLists(fn.Body, func(list []ast.Stmt) []ast.Stmt {
		for {
			changed := m.propagateReturnErrStmtList(&list, vt, params)
			if !changed {
				break
			}
		}
		return list
	})
}

func paramNames(ft *ast.FuncType) []string {
	if ft.Params == nil {
		return nil
	}
	var names []string
	for _, f := range ft.Params.List {
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

func (m *fileModernizer) walkFuncStmtLists(body *ast.BlockStmt, transform func([]ast.Stmt) []ast.Stmt) {
	if body == nil {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		switch x := n.(type) {
		case *ast.BlockStmt:
			x.List = transform(x.List)
		case *ast.CaseClause:
			x.Body = transform(x.Body)
		case *ast.CommClause:
			x.Body = transform(x.Body)
		}
		return true
	})
}

func (m *fileModernizer) propagateReturnErrStmtList(list *[]ast.Stmt, vt ast.Expr, params []string) bool {
	if list == nil || *list == nil {
		return false
	}
	var newList []ast.Stmt
	bodyChanged := false
	for i := 0; i < len(*list); i++ {
		stmt := (*list)[i]
		if tryStmt, ok := m.matchIfInitReturnErr(stmt, *list, i, vt, params); ok {
			newList = append(newList, tryStmt)
			m.countBangStmt(tryStmt)
			bodyChanged = true
			m.mark()
			continue
		}
		if tryStmts, ok := m.matchAssignReturnErr(stmt, *list, i, vt, params); ok {
			newList = append(newList, tryStmts...)
			m.countBangStmts(tryStmts)
			i += 1
			bodyChanged = true
			m.mark()
			continue
		}
		if tryStmt, ok := m.matchAssignErrBang(stmt, *list, i, params); ok {
			newList = append(newList, tryStmt)
			m.countBangStmt(tryStmt)
			i += 1
			bodyChanged = true
			m.mark()
			continue
		}
		if tryStmts, consumed, ok := m.matchAssignReturnErrGapped(stmt, *list, i, vt, params); ok {
			newList = append(newList, tryStmts...)
			m.countBangStmts(tryStmts)
			i += consumed - 1
			bodyChanged = true
			m.mark()
			continue
		}
		if tryStmt, ok := m.matchStandaloneErrBang(stmt, *list, i, vt, params); ok {
			newList = append(newList, tryStmt)
			m.countBangStmt(tryStmt)
			bodyChanged = true
			m.mark()
			continue
		}
		newList = append(newList, stmt)
	}
	*list = newList
	return bodyChanged
}

// if err != nil { return err } or if err != nil { return zero, err } → err!
func (m *fileModernizer) matchStandaloneErrBang(stmt ast.Stmt, list []ast.Stmt, i int, vt ast.Expr, params []string) (ast.Stmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil {
		return nil, false
	}
	errName, ok := errIdentFromCond(ifStmt.Cond)
	if !ok || errName != "err" {
		return nil, false
	}
	if !isReturnErrIf(ifStmt, errName, vt) {
		return nil, false
	}
	if !errBoundBeforeInStmts(list, i, errName, params) {
		return nil, false
	}
	if !lastErrBindIsValid(list, i, errName) {
		return nil, false
	}
	if stmtsBetweenTouchErr(list, lastErrBindBefore(list, i, errName)+1, i, errName) {
		return nil, false
	}
	return errBangStmt(errName), true
}

func isReturnErrIf(ifStmt *ast.IfStmt, errName string, vt ast.Expr) bool {
	if ifStmt == nil || ifStmt.Init != nil || ifStmt.Else != nil {
		return false
	}
	name, ok := errIdentFromCond(ifStmt.Cond)
	if !ok || name != errName {
		return false
	}
	if len(ifStmt.Body.List) != 1 {
		return false
	}
	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok {
		return false
	}
	return isReturnErrOnly(ret, errName) || isErrorOnlyReturn(ret, errName) || (vt != nil && isReturnWithErr(ret, errName))
}

func errBoundBeforeInStmts(list []ast.Stmt, before int, name string, params []string) bool {
	for _, p := range params {
		if p == name {
			return true
		}
	}
	for j := 0; j < before; j++ {
		if stmtDeclaresIdent(list[j], name) || stmtAssignsToIdent(list[j], name) {
			return true
		}
	}
	return false
}

func lastErrBindBefore(list []ast.Stmt, before int, name string) int {
	last := -1
	for j := 0; j < before; j++ {
		if stmtDeclaresIdent(list[j], name) || stmtAssignsToIdent(list[j], name) {
			last = j
		}
	}
	return last
}

func lastErrBindIsValid(list []ast.Stmt, before int, name string) bool {
	idx := lastErrBindBefore(list, before, name)
	if idx < 0 {
		return false
	}
	assign, ok := list[idx].(*ast.AssignStmt)
	if !ok {
		return true
	}
	if len(assign.Lhs) < 3 {
		return true
	}
	last, ok := assign.Lhs[len(assign.Lhs)-1].(*ast.Ident)
	return ok && last.Name == name
}

func stmtsBetweenTouchErr(list []ast.Stmt, start, end int, name string) bool {
	for j := start; j < end; j++ {
		if stmtTouchesErr(list[j], name) {
			return true
		}
	}
	return false
}

func stmtTouchesErr(stmt ast.Stmt, name string) bool {
	return stmtUsesIdent(stmt, name) || stmtAssignsToIdent(stmt, name)
}

// val, err := call(); ...; if err != nil { return err } → val, err := call(); ...; err!
// Lower priority than consecutive assign+call()! rewrites.
func (m *fileModernizer) matchAssignReturnErrGapped(asg ast.Stmt, list []ast.Stmt, i int, vt ast.Expr, params []string) ([]ast.Stmt, int, bool) {
	_, _, errLHS, ok := parseErrAssign(asg)
	if !ok {
		return nil, 0, false
	}
	errName := errLHS.Name
	for j := i + 1; j < len(list); j++ {
		if ifStmt, ok := list[j].(*ast.IfStmt); ok && isReturnErrIf(ifStmt, errName, vt) {
			if j == i+1 {
				return nil, 0, false
			}
			if stmtsBetweenTouchErr(list, i+1, j, errName) {
				return nil, 0, false
			}
			out := append([]ast.Stmt{}, list[i:j]...)
			out = append(out, errBangStmt(errName))
			return out, j - i + 1, true
		}
		if stmtTouchesErr(list[j], errName) {
			return nil, 0, false
		}
	}
	return nil, 0, false
}

func errBangStmt(errName string) ast.Stmt {
	return &ast.ExprStmt{X: &ast.ForceExpr{X: &ast.Ident{Name: errName}}}
}

func bangExpr(x ast.Expr) ast.Expr {
	return &ast.ForceExpr{X: x}
}

func bangExprX(e ast.Expr) (ast.Expr, bool) {
	switch x := e.(type) {
	case *ast.ForceExpr:
		return x.X, true
	case *ast.TryExpr:
		return x.X, true
	default:
		return nil, false
	}
}

func stmtUsesIdent(stmt ast.Stmt, name string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func errUsedInStmts(list []ast.Stmt, start int, errName string) bool {
	for j := start; j < len(list); j++ {
		if stmtUsesOuterErr(list[j], errName) {
			return true
		}
	}
	return false
}

func lhsShadowsParam(lhs ast.Expr, params []string) bool {
	id, ok := ast.Unparen(lhs).(*ast.Ident)
	if !ok {
		return false
	}
	for _, p := range params {
		if p == id.Name {
			return true
		}
	}
	return false
}

func bangStmtFromErrAssign(assign *ast.AssignStmt, call *ast.CallExpr, errLHS *ast.Ident, list []ast.Stmt, before int, params []string) (ast.Stmt, bool) {
	if len(assign.Lhs) == 1 {
		return &ast.ExprStmt{X: bangExpr(call)}, true
	}
	var nonErrLHS []ast.Expr
	for _, lhs := range assign.Lhs {
		if id, ok := lhs.(*ast.Ident); ok && id.Name == errLHS.Name {
			continue
		}
		if isBlank(lhs) {
			continue
		}
		nonErrLHS = append(nonErrLHS, lhs)
	}
	if len(nonErrLHS) == 0 {
		return &ast.ExprStmt{X: bangExpr(call)}, true
	}
	if len(assign.Lhs) >= 3 || len(nonErrLHS) > 1 {
		return nil, false
	}
	if lhsShadowsParam(nonErrLHS[0], params) {
		return nil, false
	}
	tok := assign.Tok
	if tok == token.DEFINE {
		for _, lhs := range nonErrLHS {
			if id, ok := lhs.(*ast.Ident); ok && identDeclaredInStmts(list, before, id.Name, params) {
				tok = token.ASSIGN
				break
			}
		}
	}
	return &ast.AssignStmt{
		Tok: tok,
		Lhs: nonErrLHS,
		Rhs: []ast.Expr{bangExpr(call)},
	}, true
}

func identDeclaredInStmts(list []ast.Stmt, before int, name string, params []string) bool {
	for _, p := range params {
		if p == name {
			return true
		}
	}
	for j := 0; j < before; j++ {
		if stmtDeclaresIdent(list[j], name) {
			return true
		}
	}
	return false
}

func stmtDeclaresIdent(stmt ast.Stmt, name string) bool {
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		if s.Tok != token.DEFINE {
			return false
		}
		for _, lhs := range s.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
				return true
			}
		}
	case *ast.DeclStmt:
		gen, ok := s.Decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			return false
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				if n.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// if err := call(); err != nil { return err }
func (m *fileModernizer) matchIfInitReturnErr(stmt ast.Stmt, list []ast.Stmt, i int, vt ast.Expr, params []string) (ast.Stmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Else != nil || ifStmt.Init == nil {
		return nil, false
	}
	assign, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE {
		return nil, false
	}
	call, errLHS, ok := parseErrAssignCall(assign)
	if !ok {
		return nil, false
	}
	errName, ok := errIdentFromCond(ifStmt.Cond)
	if !ok || errName != errLHS.Name {
		return nil, false
	}
	if len(ifStmt.Body.List) != 1 {
		return nil, false
	}
	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok {
		return nil, false
	}
	if !isReturnErrOnly(ret, errName) && !isErrorOnlyReturn(ret, errName) && (vt == nil || !isReturnWithErr(ret, errName)) {
		return nil, false
	}
	return bangStmtFromErrAssign(assign, call, errLHS, list, i, params)
}

func isReturnErrOnly(ret *ast.ReturnStmt, errName string) bool {
	return len(ret.Results) == 1 && isErrIdent(ret.Results[0], errName)
}

func isErrorConstructorCall(e ast.Expr) bool {
	e = ast.Unparen(e)
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch pkg.Name + "." + sel.Sel.Name {
	case "errors.New", "fmt.Errorf":
		return true
	default:
		return false
	}
}

func isErrorOnlyReturn(ret *ast.ReturnStmt, errName string) bool {
	if isReturnErrOnly(ret, errName) {
		return true
	}
	return len(ret.Results) == 1 && isErrorConstructorCall(ret.Results[0])
}

// err := call(); err! or val, err := call(); err! → call()! or val = call()!
func (m *fileModernizer) matchAssignErrBang(asg ast.Stmt, list []ast.Stmt, i int, params []string) (ast.Stmt, bool) {
	assign, call, errLHS, ok := parseErrAssign(asg)
	if !ok {
		return nil, false
	}
	if i+1 >= len(list) {
		return nil, false
	}
	exprStmt, ok := list[i+1].(*ast.ExprStmt)
	if !ok {
		return nil, false
	}
	try, ok := bangExprX(exprStmt.X)
	if !ok {
		return nil, false
	}
	id, ok := try.(*ast.Ident)
	if !ok || id.Name != errLHS.Name {
		return nil, false
	}
	if errUsedInStmts(list, i+2, errLHS.Name) {
		return nil, false
	}
	return bangStmtFromErrAssign(assign, call, errLHS, list, i, params)
}

func parseErrAssignCall(assign *ast.AssignStmt) (*ast.CallExpr, *ast.Ident, bool) {
	if len(assign.Rhs) != 1 {
		return nil, nil, false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return nil, nil, false
	}
	var errLHS *ast.Ident
	switch len(assign.Lhs) {
	case 1:
		errLHS, ok = assign.Lhs[0].(*ast.Ident)
		if !ok || errLHS.Name != "err" {
			return nil, nil, false
		}
	case 2:
		if id, ok := assign.Lhs[1].(*ast.Ident); ok && id.Name == "err" {
			errLHS = id
		} else if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Name == "err" {
			errLHS = id
		} else {
			return nil, nil, false
		}
	default:
		last, ok := assign.Lhs[len(assign.Lhs)-1].(*ast.Ident)
		if !ok || last.Name != "err" {
			return nil, nil, false
		}
		errLHS = last
	}
	return call, errLHS, true
}

func parseErrAssign(asg ast.Stmt) (*ast.AssignStmt, *ast.CallExpr, *ast.Ident, bool) {
	assign, ok := asg.(*ast.AssignStmt)
	if !ok || (assign.Tok != token.DEFINE && assign.Tok != token.ASSIGN) {
		return nil, nil, nil, false
	}
	call, errLHS, ok := parseErrAssignCall(assign)
	if !ok {
		return nil, nil, nil, false
	}
	return assign, call, errLHS, true
}

// val, err := call(); if err != nil { return err } → val := call()! or val, err := call(); err!
func (m *fileModernizer) matchAssignReturnErr(asg ast.Stmt, list []ast.Stmt, i int, vt ast.Expr, params []string) ([]ast.Stmt, bool) {
	assign, call, errLHS, ok := parseErrAssign(asg)
	if !ok {
		return nil, false
	}
	if i+1 >= len(list) {
		return nil, false
	}
	ifStmt, ok := list[i+1].(*ast.IfStmt)
	if !ok || !isReturnErrIf(ifStmt, errLHS.Name, vt) {
		return nil, false
	}
	errName := errLHS.Name
	if errUsedInStmts(list, i+2, errName) {
		return []ast.Stmt{assign, errBangStmt(errName)}, true
	}
	if stmt, ok := bangStmtFromErrAssign(assign, call, errLHS, list, i, params); ok {
		return []ast.Stmt{stmt}, true
	}
	if len(assign.Lhs) >= 3 {
		return []ast.Stmt{assign, errBangStmt(errName)}, true
	}
	return nil, false
}

func (m *fileModernizer) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.FuncDecl:
		m.modernizeFunc(n)
		return nil
	case *ast.InterfaceType:
		m.modernizeInterface(n)
		return nil
	}
	return m
}

func (m *fileModernizer) mark() {
	m.changed = true
}

func (m *fileModernizer) countBangStmts(stmts []ast.Stmt) {
	for _, stmt := range stmts {
		m.countBangStmt(stmt)
	}
}

func (m *fileModernizer) countBangStmt(stmt ast.Stmt) {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		m.countBangExpr(s.X)
	case *ast.AssignStmt:
		for _, rhs := range s.Rhs {
			m.countBangExpr(rhs)
		}
	}
}

func (m *fileModernizer) countBangExpr(e ast.Expr) {
	fe, ok := ast.Unparen(e).(*ast.ForceExpr)
	if !ok {
		return
	}
	switch x := ast.Unparen(fe.X).(type) {
	case *ast.CallExpr:
		m.callBangCount++
	case *ast.Ident:
		if x.Name == "err" {
			m.errBangStmtCount++
		}
	}
}

func isErrorType(t ast.Expr) bool {
	ident, ok := t.(*ast.Ident)
	return ok && ident.Name == "error"
}

func resultPair(ft *ast.FuncType) (valueType ast.Expr, ok bool) {
	if ft.Results == nil || len(ft.Results.List) != 2 {
		return nil, false
	}
	first := ft.Results.List[0]
	second := ft.Results.List[1]
	if len(first.Names) > 0 || len(second.Names) > 0 {
		return nil, false
	}
	if !isErrorType(second.Type) {
		return nil, false
	}
	if !isSupportedResultType(first.Type) {
		return nil, false
	}
	return first.Type, true
}

func isSupportedResultType(t ast.Expr) bool {
	switch ast.Unparen(t).(type) {
	case *ast.MapType, *ast.ChanType:
		return false
	}
	return true
}

func (m *fileModernizer) convertResultType(ft *ast.FuncType) bool {
	vt, ok := resultPair(ft)
	if !ok {
		return false
	}
	vt = unwrapNilableTypeExpr(vt)
	ft.Results = &ast.FieldList{
		List: []*ast.Field{{
			Type: &ast.ResultTypeExpr{X: astutilClone(vt)},
		}},
	}
	m.mark()
	return true
}

func unwrapNilableTypeExpr(t ast.Expr) ast.Expr {
	if ne, ok := ast.Unparen(t).(*ast.NilableTypeExpr); ok {
		return ne.X
	}
	return t
}

func containsRangeOverSeqLoop(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		if isRangeOverFuncIterator(rs.X) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isRangeOverFuncIterator(x ast.Expr) bool {
	call, ok := x.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	name := sel.Sel.Name
	return strings.HasSuffix(name, "Seq") || strings.HasSuffix(name, "Seq2")
}

func (m *fileModernizer) modernizeFunc(fn *ast.FuncDecl) {
	if fn.Type == nil {
		return
	}
	if fn.Body != nil && containsRangeOverSeqLoop(fn.Body) {
		return
	}
	if !m.convertResultType(fn.Type) {
		return
	}
	vt, ok := m.valueResultType(fn.Type)
	if !ok {
		return
	}
	if fn.Body != nil && m.cfg.ErrBangSignatures && m.cfg.ErrBangBody {
		var params []string
		if fn.Type != nil {
			params = paramNames(fn.Type)
		}
		m.walkFuncStmtLists(fn.Body, func(list []ast.Stmt) []ast.Stmt {
			return m.modernizeStmtList(list, vt, params)
		})
	}
}

func (m *fileModernizer) valueResultType(ft *ast.FuncType) (ast.Expr, bool) {
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return nil, false
	}
	if len(ft.Results.List) == 1 {
		if rt, ok := ft.Results.List[0].Type.(*ast.ResultTypeExpr); ok {
			return rt.X, true
		}
	}
	return resultPair(ft)
}

func (m *fileModernizer) modernizeInterface(it *ast.InterfaceType) {
	if it.Methods == nil {
		return
	}
	for _, field := range it.Methods.List {
		ft, ok := field.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		m.convertResultType(ft)
	}
}

func (m *fileModernizer) modernizeStmtList(list []ast.Stmt, vt ast.Expr, params []string) []ast.Stmt {
	if list == nil {
		return list
	}
	var newList []ast.Stmt
	for i := 0; i < len(list); i++ {
		stmt := list[i]
		if asg, ok := m.matchAssignErrCheck(stmt, list, i, vt, params); ok {
			newList = append(newList, asg)
			m.countBangStmt(asg)
			i += 1
			m.mark()
			continue
		}
		if tryStmt, ok := m.matchIfInitErr(stmt, list, i, vt, params); ok {
			newList = append(newList, tryStmt)
			m.countBangStmt(tryStmt)
			m.mark()
			continue
		}
		if tryStmt, ok := m.matchIfAssignErr(stmt, list, i, vt, params); ok {
			newList = append(newList, tryStmt)
			m.countBangStmt(tryStmt)
			m.mark()
			continue
		}
		newList = append(newList, stmt)
	}
	return newList
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

func errIdentFromCond(cond ast.Expr) (string, bool) {
	be, ok := cond.(*ast.BinaryExpr)
	if !ok {
		return "", false
	}
	if be.Op != token.NEQ {
		return "", false
	}
	if id, ok := be.X.(*ast.Ident); ok && isNilIdent(be.Y) {
		return id.Name, true
	}
	if id, ok := be.Y.(*ast.Ident); ok && isNilIdent(be.X) {
		return id.Name, true
	}
	return "", false
}

func isReturnWithErr(ret *ast.ReturnStmt, errName string) bool {
	return len(ret.Results) == 2 && isErrIdent(ret.Results[1], errName)
}

func isZeroReturn(ret *ast.ReturnStmt, vt ast.Expr, errName string) bool {
	if len(ret.Results) != 2 {
		return false
	}
	if !isErrIdent(ret.Results[1], errName) {
		return false
	}
	return zeroMatches(ret.Results[0], vt)
}

func isErrIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func zeroMatches(z, vt ast.Expr) bool {
	z = ast.Unparen(z)
	switch z := z.(type) {
	case *ast.BasicLit:
		return z.Value == "0" || z.Value == "0.0" || z.Value == "false" || z.Value == "nil" || z.Value == "\"\""
	case *ast.Ident:
		if z.Name == "nil" {
			return true
		}
		if id, ok := vt.(*ast.Ident); ok {
			return z.Name == id.Name+"{}"
		}
		return false
	case *ast.CompositeLit:
		return true
	case *ast.CallExpr:
		if sel, ok := z.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "make" {
			return true
		}
		return false
	default:
		return false
	}
}

func errDeclBeforeInStmts(list []ast.Stmt, before int, name string, params []string) bool {
	for j := 0; j < before; j++ {
		if stmtDeclaresIdent(list[j], name) {
			return true
		}
	}
	for _, p := range params {
		if p == name {
			return true
		}
	}
	return false
}

// if err := call(); err != nil { return zero, err }
func (m *fileModernizer) matchIfInitErr(stmt ast.Stmt, list []ast.Stmt, i int, vt ast.Expr, params []string) (ast.Stmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Else != nil || ifStmt.Init == nil {
		return nil, false
	}
	assign, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE {
		return nil, false
	}
	if len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
		errLHS, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return nil, false
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return nil, false
		}
		errName, ok := errIdentFromCond(ifStmt.Cond)
		if !ok || errName != errLHS.Name {
			return nil, false
		}
		if len(ifStmt.Body.List) != 1 {
			return nil, false
		}
		ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
		if !ok || !isZeroReturn(ret, vt, errName) {
			return nil, false
		}
		if errUsedInStmts(list, i+1, errName) {
			return nil, false
		}
		return &ast.ExprStmt{X: bangExpr(call)}, true
	}
	if len(assign.Lhs) == 2 && len(assign.Rhs) == 1 {
		errLHS, ok := assign.Lhs[1].(*ast.Ident)
		if !ok {
			return nil, false
		}
		valLHS := assign.Lhs[0]
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return nil, false
		}
		errName, ok := errIdentFromCond(ifStmt.Cond)
		if !ok || errName != errLHS.Name {
			return nil, false
		}
		if len(ifStmt.Body.List) != 1 {
			return nil, false
		}
		ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
		if !ok || !isZeroReturn(ret, vt, errName) {
			return nil, false
		}
		if errUsedInStmts(list, i+1, errName) {
			return nil, false
		}
		if isBlank(valLHS) {
			return &ast.ExprStmt{X: bangExpr(call)}, true
		}
		if lhsShadowsParam(valLHS, params) {
			return nil, false
		}
		tok := token.DEFINE
		if id, ok := valLHS.(*ast.Ident); ok && identDeclaredInStmts(list, i, id.Name, params) {
			tok = token.ASSIGN
		}
		return &ast.AssignStmt{
			Tok: tok,
			Lhs: []ast.Expr{astutilClone(valLHS)},
			Rhs: []ast.Expr{bangExpr(call)},
		}, true
	}
	return nil, false
}

// if err = call(); err != nil { return zero, err } — error-only call
func (m *fileModernizer) matchIfAssignErr(stmt ast.Stmt, list []ast.Stmt, i int, vt ast.Expr, params []string) (ast.Stmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Else != nil || ifStmt.Init == nil {
		return nil, false
	}
	assign, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return nil, false
	}
	errLHS, ok := assign.Lhs[0].(*ast.Ident)
	if !ok {
		return nil, false
	}
	if errDeclBeforeInStmts(list, i, errLHS.Name, params) {
		return nil, false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	errName, ok := errIdentFromCond(ifStmt.Cond)
	if !ok || errName != errLHS.Name {
		return nil, false
	}
	if len(ifStmt.Body.List) != 1 {
		return nil, false
	}
	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || !isZeroReturn(ret, vt, errName) {
		return nil, false
	}
	return &ast.ExprStmt{X: bangExpr(call)}, true
}

func (m *fileModernizer) matchAssignErrCheck(asg ast.Stmt, list []ast.Stmt, i int, vt ast.Expr, params []string) (ast.Stmt, bool) {
	assign, ok := asg.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
		return nil, false
	}
	errLHS, ok := assign.Lhs[1].(*ast.Ident)
	if !ok {
		return nil, false
	}
	valLHS := assign.Lhs[0]
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	if i+1 >= len(list) {
		return nil, false
	}
	ifStmt, ok := list[i+1].(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil {
		return nil, false
	}
	errName, ok := errIdentFromCond(ifStmt.Cond)
	if !ok || errName != errLHS.Name {
		return nil, false
	}
	if len(ifStmt.Body.List) != 1 {
		return nil, false
	}
	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || !isZeroReturn(ret, vt, errName) {
		return nil, false
	}
	if errUsedInStmts(list, i+2, errName) {
		return nil, false
	}
	if isBlank(valLHS) {
		return &ast.ExprStmt{X: bangExpr(call)}, true
	}
	if lhsShadowsParam(valLHS, params) {
		return nil, false
	}
	tok := token.DEFINE
	if id, ok := valLHS.(*ast.Ident); ok && identDeclaredInStmts(list, i, id.Name, params) {
		tok = token.ASSIGN
	}
	return &ast.AssignStmt{
		Tok: tok,
		Lhs: []ast.Expr{astutilClone(valLHS)},
		Rhs: []ast.Expr{bangExpr(call)},
	}, true
}

func errAssignedLater(list []ast.Stmt, start int, errName string) bool {
	for j := start; j < len(list); j++ {
		if stmtAssignsToIdent(list[j], errName) {
			return true
		}
	}
	return false
}

func stmtAssignsToIdent(stmt ast.Stmt, name string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		var assign *ast.AssignStmt
		switch node := n.(type) {
		case *ast.AssignStmt:
			assign = node
		case *ast.IfStmt:
			if node.Init != nil {
				assign, _ = node.Init.(*ast.AssignStmt)
			}
		}
		if assign == nil {
			return true
		}
		for _, lhs := range assign.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func isBlank(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "_"
}

func astutilClone(e ast.Expr) ast.Expr {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		return &ast.Ident{Name: e.Name}
	case *ast.StarExpr:
		return &ast.StarExpr{X: astutilClone(e.X)}
	case *ast.ArrayType:
		return &ast.ArrayType{Len: astutilClone(e.Len), Elt: astutilClone(e.Elt)}
	case *ast.MapType:
		return &ast.MapType{Key: astutilClone(e.Key), Value: astutilClone(e.Value)}
	case *ast.SelectorExpr:
		return &ast.SelectorExpr{X: astutilClone(e.X), Sel: e.Sel}
	case *ast.IndexExpr:
		return &ast.IndexExpr{X: astutilClone(e.X), Index: astutilClone(e.Index)}
	case *ast.IndexListExpr:
		indices := make([]ast.Expr, len(e.Indices))
		for i, ix := range e.Indices {
			indices[i] = astutilClone(ix)
		}
		return &ast.IndexListExpr{X: astutilClone(e.X), Indices: indices}
	case *ast.InterfaceType:
		return &ast.InterfaceType{Methods: e.Methods}
	case *ast.StructType:
		return &ast.StructType{Fields: e.Fields}
	case *ast.ChanType:
		return &ast.ChanType{Dir: e.Dir, Value: astutilClone(e.Value)}
	case *ast.FuncType:
		return &ast.FuncType{Func: e.Func, Params: e.Params, Results: e.Results}
	case *ast.BasicLit:
		return &ast.BasicLit{Kind: e.Kind, Value: e.Value}
	case *ast.Ellipsis:
		return &ast.Ellipsis{Elt: astutilClone(e.Elt)}
	default:
		return e
	}
}
