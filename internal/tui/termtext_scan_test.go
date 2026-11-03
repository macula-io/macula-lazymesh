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

// TestTermText_DTOStringsReachOnlyTheSinksThroughTheConstructor is the
// class guard Pluto asked for at r=1 (#95): the realm join panel printed
// the realm server's message, link, QR, handle and timestamps straight
// into a strings.Builder, which no per-field review was going to catch.
//
// The rule: a STRING field of a struct that arrives from outside --
// this package's JSON DTOs, and internal/realmjoin's and
// internal/frontend's exported structs -- may not be passed to a text
// sink (WriteString, a lipgloss Render, fmt.Sprintf, strings.Join,
// lipgloss.Join*, a table.Row cell) unless it is inside a termTextOf
// call. Numeric and slice fields are not text and are not checked, so
// "%d agents" and len(...) stay legal.
func TestTermText_DTOStringsReachOnlyTheSinksThroughTheConstructor(t *testing.T) {
	src := parseTUI(t)
	fields := dtoStringFields(t, src)
	sinks := 0
	for _, f := range src.files {
		// Everything inside a termTextOf call is already terminal text;
		// the rule is about text that reaches a sink WITHOUT one.
		safe := constructedNodes(f.File)
		ast.Inspect(f.File, func(n ast.Node) bool {
			if safe[n] {
				return true
			}
			if lit, ok := n.(*ast.CompositeLit); ok && isSelector(lit.Type, "table", "Row") {
				for _, elt := range lit.Elts {
					if e, ok := elt.(ast.Expr); ok {
						sinks++
						if raw := unwrappedDTOString(e, fields, safe); raw != "" {
							t.Errorf("%s: %s reaches a table row without termTextOf", src.where(e), raw)
						}
					}
				}
			}
			for _, arg := range sinkArgs(n) {
				sinks++
				if raw := unwrappedDTOString(arg, fields, safe); raw != "" {
					t.Errorf("%s: %s reaches a text sink without termTextOf", src.where(arg), raw)
				}
			}
			return true
		})
	}
	if sinks < 10 {
		t.Fatalf("the scan found only %d text sinks: it is not looking at the package", sinks)
	}
}

// constructedNodes is every node inside a termTextOf call: text that has
// already been through the constructor.
func constructedNodes(f *ast.File) map[ast.Node]bool {
	safe := map[ast.Node]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isIdent(call.Fun, "termTextOf") {
			return true
		}
		for _, a := range call.Args {
			ast.Inspect(a, func(n ast.Node) bool {
				if n != nil {
					safe[n] = true
				}
				return true
			})
		}
		return true
	})
	return safe
}

// sinkArgs returns the expressions a node passes to a text sink.
func sinkArgs(n ast.Node) []ast.Expr {
	if lit, ok := n.(*ast.CompositeLit); ok {
		if !isSelector(lit.Type, "table", "Row") {
			return nil
		}
	}
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil
	}
	if !isTextSink(call.Fun) {
		return nil
	}
	return call.Args
}

func isTextSink(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "WriteString", "Render", "Sprintf", "Join", "JoinHorizontal", "JoinVertical", "NewStyle":
	}
	switch sel.Sel.Name {
	case "WriteString", "Render", "Sprintf", "Join", "JoinHorizontal", "JoinVertical":
		return true
	}
	id, ok := fun.(*ast.Ident)
	return ok && id.Name == "fmt" // never: fmt is always a selector
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// unwrappedDTOString reports the first DTO string field used inside e
// without a termTextOf around it, or "".
func unwrappedDTOString(e ast.Expr, fields map[string]bool, safe map[ast.Node]bool) string {
	found := ""
	ast.Inspect(e, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && found == "" && fields[sel.Sel.Name] && !safe[n] {
			found = describe(sel)
			return false
		}
		return found == ""
	})
	return found
}

// dtoStringFields collects the names of string fields on structs that
// arrive from outside: any struct in this package with a json tag, and
// the exported structs of internal/realmjoin and internal/frontend.
func dtoStringFields(t *testing.T, src parsedTUI) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	collect := func(f *ast.File, requireJSON bool) {
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			exported := ast.IsExported(spec.Name.Name)
			if requireJSON && !exported {
				// Local DTOs may be unexported (joinedRoom, pendingRing):
				// they are told apart by their json tags, one level below.
			}
			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					continue
				}
				id, ok := field.Type.(*ast.Ident)
				if !ok || id.Name != "string" {
					continue
				}
				if requireJSON {
					if !hasJSONTag(field) {
						continue
					}
				} else if !ast.IsExported(field.Names[0].Name) || !exported {
					continue
				}
				out[field.Names[0].Name] = true
			}
			return true
		})
	}
	for _, f := range src.files {
		collect(f.File, true)
	}
	for _, dir := range []string{"../realmjoin", "../frontend"} {
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		parsed := 0
		for _, p := range paths {
			if strings.HasSuffix(p, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(src.fset, p, nil, 0)
			if err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			collect(f, false)
			parsed++
		}
		if parsed == 0 {
			t.Fatalf("no sources parsed in %s: the class guard is not looking at it", dir)
		}
	}
	if len(out) < 10 {
		t.Fatalf("collected only %d DTO string fields: the class guard is not looking at anything", len(out))
	}
	return out
}

func hasJSONTag(field *ast.Field) bool {
	return field.Tag != nil && strings.Contains(field.Tag.Value, `json:"`)
}
