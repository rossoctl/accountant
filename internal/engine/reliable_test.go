package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Every check name has a decided class. A new check added to result.go fails here until its class
// is chosen, so it cannot gate nothing by accident.
func TestEveryCheckHasAFatalClass(t *testing.T) {
	want := map[string]bool{
		CheckSum: true, CheckUniqueChange: true, CheckSpans: true,
		CheckConservation: false, CheckSharedWarm: false, CheckBadUsage: false, CheckRatioRange: false,
		CheckBreakpoints: false, CheckMediaTokens: false, CheckSizeTokens: false, CheckSizeRange: false,
	}
	// The map above must cover every Check constant of result.go, so a new check cannot be non-fatal
	// by default: the test fails until its class is chosen here.
	f, err := parser.ParseFile(token.NewFileSet(), "result.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for name := range want {
		covered[name] = true
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, sp := range g.Specs {
			v := sp.(*ast.ValueSpec)
			for i, n := range v.Names {
				if !strings.HasPrefix(n.Name, "Check") || n.Name == "CheckFailure" || i >= len(v.Values) {
					continue
				}
				val := ""
				switch x := v.Values[i].(type) {
				case *ast.BasicLit:
					val = strings.Trim(x.Value, `"`)
				case *ast.Ident: // CheckBadUsage = ReasonBadUsage: the value is the reason's
					val = map[string]string{"ReasonBadUsage": CheckBadUsage, "ReasonRatioRange": CheckRatioRange,
						"ReasonBreakpoints": CheckBreakpoints, "ReasonMediaTokens": CheckMediaTokens,
						"ReasonSizeRange": CheckSizeRange}[x.Name]
				}
				if val == "" || !covered[val] {
					t.Errorf("%s (%q) has no class in the table above: choose fatal or not", n.Name, val)
				}
			}
		}
	}
	for name, fatal := range want {
		if got := (CheckFailure{Check: name}).Fatal(); got != fatal {
			t.Errorf("%s: Fatal = %v, want %v", name, got, fatal)
		}
	}
	if (CheckFailure{Check: "order"}).Fatal() {
		t.Error("an unknown check name must not be fatal")
	}
}

func TestResultReliable(t *testing.T) {
	if !(Result{}).Reliable() {
		t.Error("no checks: want reliable")
	}
	if !(Result{Checks: []CheckFailure{{Check: CheckConservation}}}).Reliable() {
		t.Error("an informational check must not make a Result unreliable")
	}
	if (Result{Checks: []CheckFailure{{Check: CheckConservation}, {Check: CheckSum}}}).Reliable() {
		t.Error("a sum failure: want unreliable")
	}
}

func TestTierTokensArithmetic(t *testing.T) {
	a := TierTokens{Input: 1, CacheWrite5m: 2, CacheWrite1h: 3, CacheRead: 4}
	b := TierTokens{Input: 10, CacheWrite5m: 20, CacheWrite1h: 30, CacheRead: 40}
	if got, want := a.Add(b), (TierTokens{Input: 11, CacheWrite5m: 22, CacheWrite1h: 33, CacheRead: 44}); got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
	if got, want := a.Sub(b), (TierTokens{Input: -9, CacheWrite5m: -18, CacheWrite1h: -27, CacheRead: -36}); got != want {
		t.Errorf("Sub = %+v, want %+v", got, want)
	}
	if !(TierTokens{}).IsZero() || a.IsZero() {
		t.Error("IsZero wrong")
	}
	// A tier added to TierTokens must be handled by Add and Sub: set every field to 1 and expect
	// every field to be 2 after Add. The test fails on a field the arithmetic forgets.
	one := TierTokens{Input: 1, CacheWrite5m: 1, CacheWrite1h: 1, CacheRead: 1}
	if got := one.Add(one); got != (TierTokens{Input: 2, CacheWrite5m: 2, CacheWrite1h: 2, CacheRead: 2}) {
		t.Errorf("Add dropped a tier: %+v", got)
	}
}

func TestCacheEffectNetIsSavedMinusCaused(t *testing.T) {
	e := CacheEffect{Saved: TierTokens{CacheRead: 100}, Caused: TierTokens{CacheWrite5m: 100}}
	if got, want := e.Net(), (TierTokens{CacheRead: 100, CacheWrite5m: -100}); got != want {
		t.Errorf("Net = %+v, want %+v", got, want)
	}
}
