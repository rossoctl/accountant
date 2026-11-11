package engine

import "testing"

func swBody(tools, system, messages int) Body {
	mk := func(prefix string, n int) [][]byte {
		var out [][]byte
		for i := range n {
			out = append(out, []byte(prefix+string(rune('a'+i))))
		}
		return out
	}
	return NewBody(mk("t", tools), mk("s", system), mk("m", messages))
}

func swBreak(sec Section, idx int) Breakpoint {
	return Breakpoint{Pos: Position{Section: sec, Index: idx}, TTL: "5m"}
}

func TestSharedWarmFromStopsAtTheSiblingsLastBreakpointInTheSharedPrefix(t *testing.T) {
	sib := swBody(1, 2, 4)
	// This request shares tools, system and messages 0..1, then differs.
	fwd := NewBody([][]byte{[]byte("ta")}, [][]byte{[]byte("sa"), []byte("sb")},
		[][]byte{[]byte("ma"), []byte("mb"), []byte("other"), []byte("x"), []byte("y")})
	got := SharedWarmFrom(fwd, []Sibling{{Body: sib, Breakpoints: []Breakpoint{
		swBreak(SectionSystem, 1), swBreak(SectionMessages, 1), swBreak(SectionMessages, 3)}}})
	want := Position{Section: SectionMessages, Index: 1}
	if got == nil || *got != want {
		t.Fatalf("SharedWarmFrom = %v, want %v", got, want)
	}
}

// A sibling that sent no breakpoint (automatic prefix caching) kept the whole shared prefix warm:
// with no breakpoint every position is one (design §5.1, §6.2).
func TestSharedWarmFromTakesTheWholeSharedPrefixOfASiblingWithoutBreakpoints(t *testing.T) {
	sib := swBody(0, 1, 3)
	fwd := NewBody(nil, [][]byte{[]byte("sa")}, [][]byte{[]byte("ma"), []byte("mb"), []byte("other"), []byte("x")})
	got := SharedWarmFrom(fwd, []Sibling{{Body: sib}})
	want := Position{Section: SectionMessages, Index: 1}
	if got == nil || *got != want {
		t.Fatalf("SharedWarmFrom = %v, want %v", got, want)
	}
	// A breakpoint list that holds none inside the sibling's body counts as no breakpoint too.
	got = SharedWarmFrom(fwd, []Sibling{{Body: sib, Breakpoints: []Breakpoint{swBreak(SectionMessages, 9)}}})
	if got == nil || *got != want {
		t.Fatalf("breakpoint outside the body: SharedWarmFrom = %v, want %v", got, want)
	}
	// Nothing shared: nil.
	if got := SharedWarmFrom(NewBody(nil, [][]byte{[]byte("zz")}, nil), []Sibling{{Body: sib}}); got != nil {
		t.Fatalf("nothing shared: SharedWarmFrom = %v, want nil", got)
	}
}

func TestSharedWarmFromIsNilWithoutABreakpointInTheSharedPrefix(t *testing.T) {
	sib := swBody(0, 1, 4)
	fwd := swBody(0, 1, 6)
	_ = fwd
	diverged := NewBody(nil, [][]byte{[]byte("sa")}, [][]byte{[]byte("zz"), []byte("mb")})
	if got := SharedWarmFrom(diverged, []Sibling{{Body: sib, Breakpoints: []Breakpoint{swBreak(SectionMessages, 2)}}}); got != nil {
		t.Fatalf("breakpoint after the first diff: SharedWarmFrom = %v, want nil", got)
	}
	if got := SharedWarmFrom(fwd, nil); got != nil {
		t.Fatalf("no siblings: SharedWarmFrom = %v, want nil", got)
	}
}

func TestSharedWarmFromTakesTheSiblingThatReachesFurthest(t *testing.T) {
	fwd := swBody(0, 1, 6)
	near, far := swBody(0, 1, 3), swBody(0, 1, 5)
	got := SharedWarmFrom(fwd, []Sibling{
		{Body: near, Breakpoints: []Breakpoint{swBreak(SectionMessages, 2)}},
		{Body: far, Breakpoints: []Breakpoint{swBreak(SectionMessages, 4)}},
	})
	want := Position{Section: SectionMessages, Index: 4}
	if got == nil || *got != want {
		t.Fatalf("SharedWarmFrom = %v, want %v", got, want)
	}
}

// The position is in fwd's own sections, even when the sibling's sections have other lengths.
func TestSharedWarmFromMapsThePositionIntoTheForwardedBody(t *testing.T) {
	sib := swBody(0, 1, 3)
	fwd := swBody(0, 1, 3)
	got := SharedWarmFrom(fwd, []Sibling{{Body: sib, Breakpoints: []Breakpoint{swBreak(SectionSystem, 0)}}})
	want := Position{Section: SectionSystem, Index: 0}
	if got == nil || *got != want {
		t.Fatalf("SharedWarmFrom = %v, want %v", got, want)
	}
}
