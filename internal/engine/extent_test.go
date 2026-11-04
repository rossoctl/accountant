package engine

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// extentLinear is the old extent: it tries every candidate. It is the oracle for the binary search.
func extentLinear(b Body, r ratio, tokens int, candidates []bp) int {
	if tokens <= 0 {
		return -1
	}
	limit := tokens + 128 + tokens/50
	best := -1
	try := func(i int) {
		if i > best && r.upTo(b, i) <= limit {
			best = i
		}
	}
	if len(candidates) == 0 {
		for i := range b.Pos {
			try(i)
		}
	}
	for _, c := range candidates {
		try(c.at)
	}
	return best
}

func randomBody(rng *rand.Rand) Body {
	n := rng.Intn(60)
	pos := make([][]byte, n)
	for i := range pos {
		size := rng.Intn(500)
		if rng.Intn(4) == 0 {
			pos[i] = []byte(`{"type":"image","source":{"type":"base64","data":"` + strings.Repeat("A", rng.Intn(5000)) + `"}}`)
		} else {
			pos[i] = []byte(strings.Repeat("x", size))
		}
	}
	s := rng.Intn(n + 1)
	return NewBody(pos[:s/2], pos[s/2:s], pos[s:])
}

func TestExtentMatchesLinearScan(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 3000; iter++ {
		b := randomBody(rng)
		last := len(b.Pos) - 1
		media := b.cumMedia(last)
		r := ratio{tokens: rng.Intn(3000), bytes: max(b.cumBytes(last)-media, 1)}
		if rng.Intn(10) == 0 {
			r.bytes = 0
		}
		var cands []bp
		for k := rng.Intn(4); k > 0 && len(b.Pos) > 0; k-- {
			cands = append(cands, bp{at: rng.Intn(len(b.Pos)), ttl: "5m"})
		}
		tokens := rng.Intn(4000) - 100
		if got, want := extent(b, r, tokens, cands), extentLinear(b, r, tokens, cands); got != want {
			t.Fatalf("iter %d: extent=%d, linear scan=%d (n=%d tokens=%d cands=%v ratio=%+v)", iter, got, want, len(b.Pos), tokens, cands, r)
		}
		// A Body without prefix sums (decoded from JSON) must give the same answer.
		bare := Body{Start: b.Start, Pos: b.Pos}
		if got, want := extent(bare, r, tokens, cands), extentLinear(b, r, tokens, cands); got != want {
			t.Fatalf("iter %d: bare body extent=%d, want %d", iter, got, want)
		}
	}
}

// A restored Body has the same prefix sums as the one NewBody built.
func TestRestoredBodyPrefixSums(t *testing.T) {
	img := `{"type":"image","source":{"type":"base64","data":"` + strings.Repeat("A", 3000) + `"}}`
	b := NewBody([][]byte{[]byte("t")}, nil, [][]byte{[]byte("hello"), []byte(img), []byte("tail")})
	got, err := fromWireBody(toWireBody(b))
	if err != nil {
		t.Fatal(err)
	}
	for i := -1; i <= len(b.Pos); i++ {
		if got.cumBytes(i) != b.cumBytes(i) || got.cumMedia(i) != b.cumMedia(i) {
			t.Fatalf("i=%d: restored (%d,%d), built (%d,%d)", i, got.cumBytes(i), got.cumMedia(i), b.cumBytes(i), b.cumMedia(i))
		}
	}
	if len(got.cumMed) != len(got.Pos)+1 {
		t.Fatalf("restored body has no media prefix array")
	}
}

// BenchmarkComputeNoBreakpoints is 3 Computes of a body of n positions of 400 bytes, with no
// breakpoint (OpenAI-style traffic).
func BenchmarkComputeNoBreakpoints(bm *testing.B) {
	for _, n := range []int{1000, 5000, 20000} {
		body := mk(nil, nil, msgs(n, 400))
		bm.Run(itoa(n), func(bm *testing.B) {
			for i := 0; i < bm.N; i++ {
				ts := NewTrackers()
				for k := 0; k < 3; k++ {
					r := ts.BeginRequest(RequestMeta{Session: "s", At: t0.Add(time.Duration(k) * time.Second)}, body, nil)
					r.EndRequest(Thread{}, body, nil, exact5m)
					r.Compute(Usage{CacheRead: n * 400 * 9 / 10, Input: n * 400 / 10})
				}
			}
		})
	}
}
