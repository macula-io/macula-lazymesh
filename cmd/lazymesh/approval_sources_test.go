package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedApprovalCallers is the set of files allowed to answer an
// approval: main.go's run() wires the TUI's approval bus and the control
// socket's approve message, and nothing else may. The check is per file,
// not per call site — a second wiring point inside the file that already
// owns the wiring is not a new caller, while a new file is.
var allowedApprovalCallers = []string{"cmd/lazymesh/main.go"}

// TestOnlyTheOperatorCanAnswerAnApproval is threat model invariant 4
// (macula-io/macula-lazymesh#38) plus #40's two-frontend rule: an
// approval is answered only through sessionhost.AnswerApproval or its
// TryAnswerApproval variant, and only the TUI's approval bus and the
// control socket's approve message call either, both wired in this file's
// run. A new caller anywhere else (a room-message handler, a ring answer,
// a tool result) fails this test until someone shows it is an operator's
// own act.
func TestOnlyTheOperatorCanAnswerAnApproval(t *testing.T) {
	got, err := approvalCallers(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != strings.Join(allowedApprovalCallers, ",") {
		t.Fatalf("an approval is answered from %v; only the TUI approval bus and the control socket in cmd/lazymesh/main.go may answer one", got)
	}
}

// TestTheGuardCatchesANewApprovalCaller is the mutation check: the guard
// is only worth having if a new caller turns it red. It runs the same
// detector over a synthetic tree containing (a) a caller outside the
// allowed places, which must be reported, (b) a caller of the unqualified
// name, which must be reported too, and (c) the wrapper that defines
// AnswerApproval in terms of TryAnswerApproval, which must NOT be
// reported — otherwise every legitimate tree fails.
func TestTheGuardCatchesANewApprovalCaller(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("internal/rogue/rogue.go", `package rogue

import "github.com/macula-io/macula-lazymesh/internal/sessionhost"

func answerIt() { sessionhost.TryAnswerApproval(nil, "approve-1", true) }
`)
	write("internal/rogue/unqualified.go", `package rogue

func legacy() { AnswerApproval(nil, "approve-1", true) }
`)
	write("internal/sessionhost/session.go", `package sessionhost

func AnswerApproval(session any, id string, allow bool) { TryAnswerApproval(session, id, allow) }

func TryAnswerApproval(session any, id string, allow bool) bool { return false }
`)

	got, err := approvalCallers(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, ",")

	for _, want := range []string{"internal/rogue/rogue.go", "internal/rogue/unqualified.go"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the guard missed a new approval caller in %s; it reported %v", want, got)
		}
	}
	if strings.Contains(joined, "internal/sessionhost/session.go") {
		t.Fatalf("the guard flagged the wrapper that defines AnswerApproval; it reported %v", got)
	}
}

// approvalCallers walks root and returns the files that answer an
// approval, in walk order. Calls made inside the body of AnswerApproval
// itself are not counted: that body is the wrapper delegating to
// TryAnswerApproval, not a second operator path.
func approvalCallers(root string) ([]string, error) {
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
		reported := false
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				// The wrapper delegates; it is not a call site. Stop
				// before descending into it so its own TryAnswerApproval
				// call is not counted.
				if node.Name.Name == "AnswerApproval" && node.Recv == nil {
					return false
				}
			case *ast.CallExpr:
				if answersApproval(node.Fun) && !reported {
					rel, _ := filepath.Rel(root, path)
					callers = append(callers, filepath.ToSlash(rel))
					reported = true
				}
			}
			return true
		})
		return nil
	})
	return callers, err
}

// answersApproval matches a call to either way of answering an
// approval: AnswerApproval, and TryAnswerApproval, the control socket's
// variant (#41).
func answersApproval(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name == "AnswerApproval" || f.Sel.Name == "TryAnswerApproval"
	case *ast.Ident:
		return f.Name == "AnswerApproval" || f.Name == "TryAnswerApproval"
	}
	return false
}
