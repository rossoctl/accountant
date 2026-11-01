package cost

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// hidden are exported names of internal/engine that a host never needs. Every other exported name
// of the engine must be re-exported by cost.go, or a host cannot name a type it reads.
var hidden = map[string]bool{
	"Tracker": true, "ThreadState": true, "ThreadKey": true,
	"Fingerprint": true, "PosHash": true, "CacheState": true,
}

func exported(t *testing.T, dir string, skipTests bool) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			if skipTests && len(name) > 8 && name[len(name)-8:] == "_test.go" {
				continue
			}
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil && d.Name.IsExported() {
						out[d.Name.Name] = true
					}
				case *ast.GenDecl:
					for _, s := range d.Specs {
						switch s := s.(type) {
						case *ast.TypeSpec:
							if s.Name.IsExported() {
								out[s.Name.Name] = true
							}
						case *ast.ValueSpec:
							for _, n := range s.Names {
								if n.IsExported() {
									out[n.Name] = true
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

// TestEveryEngineNameIsReExportedOrHidden keeps cost.go in step with the engine: a new exported
// name must be added there, or listed in hidden on purpose.
func TestEveryEngineNameIsReExportedOrHidden(t *testing.T) {
	engine := exported(t, "internal/engine", true)
	root := exported(t, ".", true)
	for name := range engine {
		if !root[name] && !hidden[name] {
			t.Errorf("engine.%s is not re-exported by cost.go; add it, or list it in hidden", name)
		}
	}
	for name := range hidden {
		if root[name] {
			t.Errorf("%s is listed as hidden but cost.go exports it", name)
		}
		if !engine[name] {
			t.Errorf("%s is listed as hidden but the engine has no such name", name)
		}
	}
}
