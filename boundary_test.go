package cost

import (
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/rossoctl/cost"

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
