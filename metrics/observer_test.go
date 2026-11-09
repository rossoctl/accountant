package metrics

import "testing"

type countObserver struct{ n int }

func (c *countObserver) Observe(Observation) { c.n++ }

type panicObserver struct{}

func (panicObserver) Observe(Observation) { panic("boom") }

func TestMultiCallsEveryObserverInOrder(t *testing.T) {
	a, b := &countObserver{}, &countObserver{}
	Multi(a, nil, b).Observe(Observation{})
	if a.n != 1 || b.n != 1 {
		t.Fatalf("calls: a=%d b=%d, want 1 and 1", a.n, b.n)
	}
}

func TestMultiSurvivesAPanickingObserver(t *testing.T) {
	b := &countObserver{}
	Multi(panicObserver{}, b).Observe(Observation{})
	if b.n != 1 {
		t.Fatalf("the observer after a panic ran %d times, want 1", b.n)
	}
}

func TestMultiOfNothingIsNil(t *testing.T) {
	if Multi() != nil || Multi(nil, nil) != nil {
		t.Fatal("Multi with no live observer must return nil, so a host can test o == nil")
	}
}
