package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// identityFields are the parts of a Stream that UpdateStreamConfig must not
// touch. They are not settings: the name and token are how a client
// authenticates, the password hash never round-trips through the config
// endpoint, and the order is owned by the list.
var identityFields = map[string]bool{
	"Username":     true,
	"Token":        true,
	"PasswordHash": true,
	"Order":        true,
}

// UpdateStreamConfig copies field by field, so a field added to Stream and
// wired all the way through the API still silently fails to save until it is
// named here too. That is not a hypothetical: simkl_scrobble and
// mdblist_scrobble reached the handler, the JSON and the config struct, and
// were dropped at this one assignment — the switch moved in the UI and was
// gone on the next load.
func TestUpdateStreamConfigCopiesEverySetting(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "stream.go", nil, 0)
	if err != nil {
		t.Fatalf("parse stream.go: %v", err)
	}

	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "UpdateStreamConfig" {
			body = fn.Body
			return false
		}
		return true
	})
	if body == nil {
		t.Fatal("UpdateStreamConfig not found in stream.go")
	}

	// Every field the function assigns to on the stored stream.
	assigned := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "stream" {
				assigned[sel.Sel.Name] = true
			}
		}
		return true
	})

	streamType := reflect.TypeOf(Stream{})
	var missing []string
	for i := 0; i < streamType.NumField(); i++ {
		name := streamType.Field(i).Name
		if identityFields[name] || assigned[name] {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		t.Fatalf("UpdateStreamConfig never assigns %s — those settings cannot be saved. "+
			"Copy them there, or add them to identityFields if they are deliberately not settings.",
			strings.Join(missing, ", "))
	}
}
