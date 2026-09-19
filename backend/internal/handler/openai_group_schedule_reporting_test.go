package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenAIRequestHandlersReportScheduleResultsWithAPIKeyGroup(t *testing.T) {
	t.Parallel()

	files := map[string]int{
		"openai_alpha_search.go":     3,
		"openai_chat_completions.go": 4,
		"openai_embeddings.go":       3,
		"openai_gateway_handler.go":  12,
		"openai_images.go":           6,
	}

	for filename, wantScopedCalls := range files {
		filename := filename
		wantScopedCalls := wantScopedCalls
		t.Run(filename, func(t *testing.T) {
			t.Parallel()

			file, fileSet := parseHandlerSourceFile(t, filename)
			scopedCalls := 0
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch selector.Sel.Name {
				case "ReportOpenAIAccountScheduleResult":
					t.Errorf("legacy unscoped schedule reporting call remains at %s", fileSet.Position(call.Pos()).String())
				case "ReportOpenAIAccountScheduleResultForGroup":
					scopedCalls++
					if len(call.Args) == 0 || !isAPIKeyGroupID(call.Args[0]) {
						t.Errorf("scoped schedule reporting must pass apiKey.GroupID first at %s", fileSet.Position(call.Pos()).String())
					}
				}
				return true
			})

			if scopedCalls != wantScopedCalls {
				t.Fatalf("scoped schedule reporting calls = %d, want %d", scopedCalls, wantScopedCalls)
			}
		})
	}
}

func TestGrokMediaKeepsUnscopedOpenAIScheduleReporting(t *testing.T) {
	t.Parallel()

	file, fileSet := parseHandlerSourceFile(t, "grok_media.go")
	legacyCalls := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch selector.Sel.Name {
		case "ReportOpenAIAccountScheduleResult":
			legacyCalls++
		case "ReportOpenAIAccountScheduleResultForGroup":
			t.Errorf("Grok media must not report into group-scoped OpenAI TTFT state at %s", fileSet.Position(call.Pos()).String())
		}
		return true
	})

	if legacyCalls == 0 {
		t.Fatal("expected Grok media to retain unscoped OpenAI schedule reporting")
	}
}

func parseHandlerSourceFile(t *testing.T, filename string) (*ast.File, *token.FileSet) {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	path := filepath.Join(filepath.Dir(currentFile), filename)
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return file, fileSet
}

func isAPIKeyGroupID(expr ast.Expr) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "GroupID" {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == "apiKey"
}
