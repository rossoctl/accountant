package engine

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestRequestRecordsTheCallsInOrder(t *testing.T) {
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{RequestID: "r1", Session: "s1", At: time.Unix(0, 0)},
		NewBody(nil, [][]byte{[]byte("sys")}, [][]byte{[]byte("hello")}), nil)
	r.Add(Operation{ID: "a", Kind: KindRewrite, Operator: "mask"})
	r.Add(Operation{ID: "b", Kind: KindSetting, Operator: "router"})
	r.EndRequest(Thread{ID: "main"}, Body{}, []Breakpoint{{TTL: "5m"}}, EntryFacts{})

	if len(r.ops) != 2 || r.ops[0].ID != "a" || r.ops[1].ID != "b" {
		t.Fatalf("Ops = %+v, want a then b", r.ops)
	}
	if !r.ended || len(r.breakpoints) != 1 || r.thread.ID != "main" {
		t.Fatalf("EndRequest did not record: %+v", r)
	}
}

func TestComputeWithNoUsagePricesNothing(t *testing.T) {
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{Session: "s1"}, Body{}, nil)
	r.Add(Operation{ID: "a", Kind: KindRewrite})
	r.EndRequest(Thread{ID: "main"}, Body{}, nil, EntryFacts{})

	res := r.Compute(Usage{})
	if len(res.Ops) != 1 || !hasUnpriced(res, ReasonNoUsage) {
		t.Fatalf("result = %+v, want the op unpriced with no_usage", res)
	}
}

func TestComputeWithoutEndRequestPricesNothing(t *testing.T) {
	ts := NewTrackers()
	r := ts.BeginRequest(RequestMeta{Session: "s1"}, Body{}, nil)
	r.Add(Operation{ID: "a", Kind: KindRewrite})
	res := r.Compute(Usage{Input: 10})
	if !hasUnpriced(res, ReasonEndRequestMissing) {
		t.Fatalf("result = %+v, want end_request_missing", res)
	}
}

func TestEndRequestCopiesTheState(t *testing.T) {
	ts := NewTrackers()
	tr := ts.tracker(ThreadKey("s1", Thread{ID: "main"}), time.Time{})
	tr.state.Active = []Operation{{ID: "x"}}
	r := ts.BeginRequest(RequestMeta{Session: "s1"}, Body{}, nil)
	r.EndRequest(Thread{ID: "main"}, Body{}, nil, EntryFacts{})
	r.start.Active[0].ID = "changed"
	if tr.state.Active[0].ID != "x" {
		t.Fatal("the record shares the Tracker's Active slice")
	}
}

func TestNewBodyHashesThePrefix(t *testing.T) {
	a := NewBody(nil, [][]byte{[]byte("s")}, [][]byte{[]byte("m0"), []byte("m1")})
	b := NewBody(nil, [][]byte{[]byte("s")}, [][]byte{[]byte("m0"), []byte("mX")})
	if a.Pos[0] != b.Pos[0] || a.Pos[1] != b.Pos[1] {
		t.Fatal("equal prefixes must have equal hashes")
	}
	if a.Pos[2].Hash == b.Pos[2].Hash {
		t.Fatal("a different position must change the hash")
	}
	if a.Start != [3]int{0, 0, 1} {
		t.Fatalf("Start = %v", a.Start)
	}
}

// The machine-stable reasons are part of the contract with a host that switches on them (design
// §6.3, Unpriced): each one must be in the design as written.
func TestEveryUnpricedReasonIsInTheDesign(t *testing.T) {
	doc, err := os.ReadFile("../../docs/design.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{ReasonEndRequestMissing, ReasonPingResultUnknown, ReasonUnknownKind,
		ReasonNegativeSize, ReasonPenaltyUnknown, ReasonNoUsage, ReasonNotSpend, ReasonBadUsage, ReasonRatioRange, ReasonMediaTokens, ReasonBreakpoints, ReasonSizeRange} {
		if !strings.Contains(string(doc), `"`+reason+`"`) && !strings.Contains(string(doc), "`"+reason+"`") {
			t.Errorf("reason %q is not named in docs/design.md", reason)
		}
	}
}

// The check names are machine-stable strings that a host switches on (design §6.5). Each constant
// has the literal value here, and the design names it as written.
func TestEveryCheckNameIsPinnedAndInTheDesign(t *testing.T) {
	doc, err := os.ReadFile("../../docs/design.md")
	if err != nil {
		t.Fatal(err)
	}
	for got, want := range map[string]string{
		CheckSum: "sum", CheckUniqueChange: "unique_change", CheckSpans: "spans", CheckConservation: "conservation",
		CheckSharedWarm: "shared_warm", CheckBadUsage: "bad_usage", CheckRatioRange: "ratio_range",
		CheckBreakpoints: "breakpoints", CheckMediaTokens: "media_tokens", CheckSizeTokens: "size_tokens",
		CheckSizeRange: "size_range",
	} {
		if got != want {
			t.Errorf("check constant = %q, want %q", got, want)
		}
		if !strings.Contains(string(doc), `"`+want+`"`) && !strings.Contains(string(doc), "`"+want+"`") {
			t.Errorf("check %q is not named in docs/design.md", want)
		}
	}
}
