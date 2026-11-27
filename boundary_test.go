package accountant

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/rossoctl/accountant"

// TestImportsStandardLibraryOnly pins R3: the module must not import context-guru, Cortex or any
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

// hostNames lists the host names that a non-test Go file must not contain. CLAUDE.md: "Do not add host
// code, host names or host paths to Go files." Test files may use operator names as data (R17).
var hostNames = []string{"context-guru", "cortex", "bifrost"}

// hostNameAllowed lists the non-test Go files that may name a host, each with its reason. It is empty.
var hostNameAllowed = map[string]string{}

// TestNoHostNamesInGoFiles fails when a non-test Go file names a host. Hosts belong in docs/.
func TestNoHostNamesInGoFiles(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".claude" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if _, ok := hostNameAllowed[filepath.ToSlash(path)]; ok {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			low := strings.ToLower(line)
			for _, name := range hostNames {
				if strings.Contains(low, name) {
					t.Errorf("%s:%d names the host %q: say \"a host\" and cite the docs instead", path, i+1, name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
