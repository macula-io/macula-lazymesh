package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyTheOperatorCanAnswerAnApproval is threat model invariant 4
// (macula-io/macula-lazymesh#38): an approval is answered only through
// sessionhost.AnswerApproval, and only the TUI's approval bus and the
// control socket's approve message call it, both wired in this file's
// run. A new caller anywhere else (a room-message handler, a ring
// answer, a tool result) fails this test until someone shows it is an
// operator's own act.
func TestOnlyTheOperatorCanAnswerAnApproval(t *testing.T) {
	root := filepath.Join("..", "..")
	var callers []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "dist") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if answersApproval(call.Fun) {
				rel, _ := filepath.Rel(root, path)
				callers = append(callers, filepath.ToSlash(rel))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cmd/lazymesh/main.go", "cmd/lazymesh/main.go"}
	if strings.Join(callers, ",") != strings.Join(want, ",") {
		t.Fatalf("AnswerApproval is called from %v; only the TUI approval bus and the control socket in cmd/lazymesh/main.go may answer an approval", callers)
	}
}

func answersApproval(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name == "AnswerApproval"
	case *ast.Ident:
		return f.Name == "AnswerApproval"
	}
	return false
}
