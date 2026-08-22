package stremio

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// reloadSwappedFields is every field Reload replaces while holding the write
// lock. Kept in sync with Reload by TestReloadSwapsExactlyTheRuntimeFields
// below, so adding a field there without adding it here fails rather than
// silently escaping the check.
var reloadSwappedFields = []string{
	"config", "baseURL", "indexer", "queryCache", "validator", "triageService",
	"availClient", "availReporter", "availNZBIndexerHosts", "tmdbClient",
	"tvdbClient", "streamManager",
}

// Reload swaps twelve fields under the write lock every time the config is
// saved. Reading any of them off the receiver without the lock is a data race,
// and reading two of them separately is worse than that: a request can end up
// built from half of one configuration and half of the next — a new indexer
// list against the old aggregator, or an AvailNZB mode that says "on" against
// the nil client that says "off".
//
// This walk found 131 such reads across 50 functions when it was written. They
// now go through s.runtime(), which returns all twelve together under one read
// lock. The rule is the same coarse one the API server's equivalent test uses
// (see pkg/server/api/config_lock_test.go): a function that mentions s.mu is
// assumed to know what it is doing, because anything sharper needs flow
// analysis.
func TestRuntimeFieldsAreOnlyReadThroughTheSnapshot(t *testing.T) {
	targets := make(map[string]bool, len(reloadSwappedFields))
	for _, f := range reloadSwappedFields {
		targets[f] = true
	}

	for _, fn := range parsePackageFuncs(t) {
		recv := receiverName(fn)
		if recv == "" {
			continue
		}
		var unguarded []token.Pos
		locks := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// Only reads through the receiver count. A local struct with a
			// field of the same name is not what this is about.
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != recv {
				return true
			}
			if sel.Sel.Name == "mu" {
				locks = true
			}
			if targets[sel.Sel.Name] {
				unguarded = append(unguarded, sel.Pos())
			}
			return true
		})
		if locks {
			continue
		}
		for _, pos := range unguarded {
			t.Errorf("%s: %s reads a reload-swapped field off the receiver without taking s.mu — take one s.runtime() snapshot instead",
				testFileSet.Position(pos), fn.Name.Name)
		}
	}
}

// The list above is only worth anything if it matches Reload. This reads the
// assignments out of Reload's body and requires the two to agree, so a field
// added to the reload set cannot quietly opt out of the check above.
func TestReloadSwapsExactlyTheRuntimeFields(t *testing.T) {
	var reload *ast.FuncDecl
	for _, fn := range parsePackageFuncs(t) {
		if fn.Name.Name == "Reload" && receiverName(fn) != "" {
			reload = fn
			break
		}
	}
	if reload == nil {
		t.Fatal("Reload not found")
	}
	recv := receiverName(reload)

	assigned := map[string]bool{}
	ast.Inspect(reload.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == recv {
				assigned[sel.Sel.Name] = true
			}
		}
		return true
	})

	listed := map[string]bool{}
	for _, f := range reloadSwappedFields {
		listed[f] = true
		if !assigned[f] {
			t.Errorf("reloadSwappedFields lists %q, but Reload does not assign it", f)
		}
	}
	for f := range assigned {
		if !listed[f] {
			t.Errorf("Reload assigns %q, but reloadSwappedFields does not list it — add it there and to serverRuntime", f)
		}
	}
}

var testFileSet = token.NewFileSet()

func parsePackageFuncs(t *testing.T) []*ast.FuncDecl {
	t.Helper()
	pkgs, err := parser.ParseDir(testFileSet, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	var out []*ast.FuncDecl
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
					out = append(out, fn)
				}
			}
		}
	}
	return out
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}
