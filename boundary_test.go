package accountant

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const module = "github.com/rossoctl/accountant"

// TestImportsStandardLibraryOnly pins R3: the module must not import a host or any
// framework, so a third party can use it.
func TestImportsStandardLibraryOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-test", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if pkg == module || pkg == module+"_test" || strings.HasPrefix(pkg, module+".") || strings.HasPrefix(pkg, module+"/") ||
			strings.HasPrefix(pkg, "["+module) {
			continue // the module's own packages and test variants
		}
		t.Errorf("imports %q: the module may import only the standard library (R3)", pkg)
	}
}

// hostNames lists the host names that a Go file must not contain. CLAUDE.md: "Do not add host code, host
// names or host paths to Go files." A non-test file may not name a host anywhere. A test file may name a host
// in a string literal, because operator names are test data (R17), but not in a comment.
var hostNames = []string{"context-guru", "cortex", "bifrost"}

// hostPaths lists the path prefixes of host code. A comment in any Go file must not point at one. Names alone
// do not catch a path to a host file, so the paths have their own list.
var hostPaths = []string{"proxy", "proxyapp", "dash", "apply", "adapters", "cmd"}

// hostPathRe matches a host path prefix at the start of a word, as in a pointer to a host file.
var hostPathRe = regexp.MustCompile(`(^|[^\w./-])(` + strings.Join(hostPaths, "|") + `)/[\w.-]`)

// hostNameAllowed lists the Go files that may name a host outside the rule above, each with its reason. It is empty.
var hostNameAllowed = map[string]string{}

// goFiles returns the Go files of the module: the files that git tracks, or, when git is not available,
// every .go file outside the directories whose name starts with a dot.
func goFiles(t *testing.T) []string {
	t.Helper()
	if out, err := exec.Command("git", "ls-files", "*.go").Output(); err == nil && len(out) > 0 {
		return strings.Fields(string(out))
	}
	var files []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return files
}

// TestNoHostNamesInGoFiles fails when a Go file names a host or points at a host path. Hosts belong in docs/.
func TestNoHostNamesInGoFiles(t *testing.T) {
	for _, path := range goFiles(t) {
		if _, ok := hostNameAllowed[path]; ok {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		isTest := strings.HasSuffix(path, "_test.go")
		fset := token.NewFileSet()
		file := fset.AddFile(path, fset.Base(), len(src))
		var sc scanner.Scanner
		sc.Init(file, src, nil, scanner.ScanComments)
		for {
			pos, tok, lit := sc.Scan()
			if tok == token.EOF {
				break
			}
			if tok == token.COMMENT && hostPathRe.MatchString(lit) {
				t.Errorf("%s:%d points at a host path: cite the docs instead", path, fset.Position(pos).Line)
			}
			if isTest && tok != token.COMMENT {
				continue // test data: operator names stay (R17)
			}
			low := strings.ToLower(lit)
			if tok != token.COMMENT && tok != token.STRING && tok != token.IDENT {
				continue
			}
			for _, name := range hostNames {
				if strings.Contains(low, name) {
					t.Errorf("%s:%d names the host %q: say \"a host\" and cite the docs instead", path, fset.Position(pos).Line, name)
				}
			}
		}
	}
}
