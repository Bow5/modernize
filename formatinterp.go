package main

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// formatFuncRewrite describes a printf-style call that can become a non-f variant
// with an interpolated string argument.
type formatFuncRewrite struct {
	pkg         string // import name, e.g. "fmt", "log"; empty for any receiver
	method      string // e.g. "Printf"
	nonF        string // e.g. "Print"
	formatIndex int    // index of format literal in Args
	skipWrap    bool   // skip when format contains %w
}

var formatFuncRewrites = []formatFuncRewrite{
	{pkg: "fmt", method: "Printf", nonF: "Print", formatIndex: 0, skipWrap: true},
	{pkg: "fmt", method: "Fprintf", nonF: "Fprint", formatIndex: 1, skipWrap: true},
	{pkg: "fmt", method: "Fatalf", nonF: "Fatal", formatIndex: 0, skipWrap: true},
	{pkg: "log", method: "Printf", nonF: "Print", formatIndex: 0, skipWrap: true},
	{pkg: "log", method: "Fatalf", nonF: "Fatal", formatIndex: 0, skipWrap: true},
	{pkg: "", method: "Errorf", nonF: "Error", formatIndex: 0, skipWrap: true},
	{pkg: "", method: "Fatalf", nonF: "Fatal", formatIndex: 0, skipWrap: true},
}

func containsPrintfWrap(format string) bool {
	return strings.Contains(format, "%w")
}

func formatArgsToInterpSegments(format string, valueArgs []ast.Expr) ([]interpSegment, bool) {
	if containsPrintfWrap(format) {
		return nil, false
	}
	verbs, parts, ok := parsePrintfFormat(format)
	if !ok || len(verbs) != len(valueArgs) {
		return nil, false
	}
	var segs []interpSegment
	argIdx := 0
	for _, part := range parts {
		if part.verb {
			if argIdx >= len(valueArgs) {
				return nil, false
			}
			arg := valueArgs[argIdx]
			if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					return nil, false
				}
				if len(segs) > 0 && segs[len(segs) - 1].expr == nil {
					segs[len(segs) - 1].literal += s
				} else {
					segs = append(segs, interpSegment{literal: s})
				}
				argIdx++
				continue
			}
			segs = append(segs, interpSegment{
				expr:   arg,
				format: printfVerbToInterp(verbs[argIdx]),
			})
			argIdx++
			continue
		}
		segs = append(segs, interpSegment{literal: part.text})
	}
	if argIdx != len(valueArgs) {
		return nil, false
	}
	return segs, true
}

func formatCallArgsToInterpString(fset *token.FileSet, formatArg ast.Expr, valueArgs []ast.Expr) (string, bool) {
	lit, ok := formatArg.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING || !isDoubleQuoted(lit.Value) {
		return "", false
	}
	format, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	segs, ok := formatArgsToInterpSegments(format, valueArgs)
	if !ok {
		return "", false
	}
	return renderInterpolatedString(fset, segs)
}

func formatCallArgsToInterpLit(fset *token.FileSet, args []ast.Expr) (*ast.BasicLit, bool) {
	if len(args) == 0 {
		return nil, false
	}
	text, ok := formatCallArgsToInterpString(fset, args[0], args[1:])
	if !ok {
		return nil, false
	}
	return &ast.BasicLit{Kind: token.STRING, Value: text}, true
}

func matchFormatFuncRewrite(sel *ast.SelectorExpr) (formatFuncRewrite, bool) {
	for _, rw := range formatFuncRewrites {
		if sel.Sel.Name != rw.method {
			continue
		}
		if rw.pkg == "" {
			return rw, true
		}
		id, ok := ast.Unparen(sel.X).(*ast.Ident)
		if ok && id.Name == rw.pkg {
			return rw, true
		}
	}
	return formatFuncRewrite{}, false
}
