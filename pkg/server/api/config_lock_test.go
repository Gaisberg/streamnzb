package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// Server.config is swapped wholesale by applyConfigPatch under the write lock,
// and two of its fields (AdminPasswordHash, AdminMustChangePassword) are also
// written in place. Reading it without s.mu is therefore a data race, and one
// the CI race job would not report today because that job is non-blocking.
//
// This walks the package rather than trusting review: it failed on eighteen
// references across seven functions when it was written, several of them in the
// path that decides whether a request is the admin's. Handlers read the config
// through s.Config() or one of the adminX() accessors, all of which lock.
//
// The rule is deliberately coarse — a function that mentions s.mu anywhere is
// accepted — because a precise one would need flow analysis. So it catches the
// common shape (a handler reaching straight for the field with no lock in
// sight) and cannot catch a read placed after an Unlock in a function that does
// lock elsewhere. Those three existed too and were fixed by hand; nothing here
// will stop them coming back.
func TestConfigFieldIsOnlyReadUnderTheLock(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var unguarded []token.Pos
				locks := false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if _, ok := sel.X.(*ast.Ident); !ok {
						return true
					}
					switch sel.Sel.Name {
					case "config":
						unguarded = append(unguarded, sel.Pos())
					case "mu":
						locks = true
					}
					return true
				})
				if locks {
					continue
				}
				for _, pos := range unguarded {
					t.Errorf("%s: %s reads s.config without taking s.mu — use s.Config() or an adminX() accessor",
						fset.Position(pos), fn.Name.Name)
				}
			}
		}
	}
}
