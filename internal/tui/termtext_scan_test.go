package tui

// The guards behind #95: one constructor for terminal text, and no chat
// entry whose text is a raw string. A raw STRING VARIABLE cannot reach
// either -- the field is a termText, so it does not compile -- but two
// things can still slip past the type: a conversion termText(x), which is
// legal Go, and a string constant, which is untyped and assigns to a
// termText silently. The first is what this scan forbids outright; the
// second can only be text an author wrote into lazymesh itself, never text
// a peer or the model sent, so it is allowed and named as such.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestTermText_OnlyOneConstructor: a termText is built by termTextOf and
// nothing else. A direct conversion would be a second door, and a second
// door is how the rule gets forgotten -- the reason the approval popup and
// the chat rendered the same bidi override two different ways until #95.
func TestTermText_OnlyOneConstructor(t *testing.T) {
	src := parseTUI(t)
	constructor := 0
	for _, f := range src.files {
		ast.Inspect(f.File, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "termText" {
				return true
			}
			if f.filepath == "termtext.go" {
				constructor++
				return true
			}
			t.Errorf("%s: a termText conversion outside termtext.go: use termTextOf", src.where(call))
			return true
		})
	}
	if constructor != 1 {
		t.Fatalf("termText is converted %d times in termtext.go, want exactly the constructor's one", constructor)
	}
}

// TestTermText_ChatEntriesTakeConstructedText: every chatEntry literal
// sets text, tool and detail from the constructor, from another entry's
// already-constructed field, or from a string literal this package wrote
// itself. A composite literal is the one place the compiler cannot help:
// its elements are ordinary expressions.
func TestTermText_ChatEntriesTakeConstructedText(t *testing.T) {
	src := parseTUI(t)
	checked := 0
	for _, f := range src.files {
		ast.Inspect(f.File, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isIdent(lit.Type, "chatEntry") {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || (key.Name != "text" && key.Name != "tool" && key.Name != "detail") {
					continue
				}
				checked++
				if !isConstructedText(kv.Value) {
					t.Errorf("%s: chatEntry.%s is not terminal text (%s): build it with termTextOf",
						src.where(kv.Value), key.Name, describe(kv.Value))
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("the scan found no chatEntry text fields: it has stopped looking at anything")
	}
}

// isConstructedText reports whether e is a value the compiler already
// holds to be terminal text: the constructor, another entry's constructed
// field, or a string literal (untyped, so assignable -- text lazymesh
// wrote itself, not text a peer sent).
func isConstructedText(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.CallExpr:
		return isIdent(v.Fun, "termTextOf")
	case *ast.SelectorExpr:
		return v.Sel.Name == "text" || v.Sel.Name == "tool" || v.Sel.Name == "detail"
	case *ast.BasicLit:
		return v.Kind == token.STRING
	}
	return false
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// describe is how a failed element reads in the failure message.
func describe(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.CallExpr:
		return describe(v.Fun) + "(...)"
	case *ast.SelectorExpr:
		return describe(v.X) + "." + v.Sel.Name
	case *ast.Ident:
		return v.Name
	case *ast.BasicLit:
		return v.Value
	}
	return "expression"
}

type parsedTUI struct {
	fset  *token.FileSet
	files []*parsedFile
}

type parsedFile struct {
	filepath string
	*ast.File
}

// where is file:line for a node, for a test failure.
func (p parsedTUI) where(n ast.Node) string {
	pos := p.fset.Position(n.Pos())
	return filepath.Base(pos.Filename) + ":" + strconv.Itoa(pos.Line)
}

// parseTUI parses every non-test .go file of this package.
func parseTUI(t *testing.T) parsedTUI {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := parsedTUI{fset: fset}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out.files = append(out.files, &parsedFile{filepath: p, File: f})
	}
	if len(out.files) < 10 {
		t.Fatalf("parsed only %d files: the scan is not looking at the package", len(out.files))
	}
	return out
}
