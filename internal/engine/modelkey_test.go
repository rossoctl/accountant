package engine

import (
	"testing"
	"time"
)

// accountant#44 item 6: two spellings of one model id are not a model change.

// modelRun runs the second request of ex2 (nothing read) after a first one made with model1/key1.
func modelRun(t *testing.T, ts *Trackers, model, key string, first bool) Result {
	t.Helper()
	rs := ex2Requests()
	q := rs[0]
	if !first {
		q = rs[2]
		q.at = t0.Add(10 * time.Second)
		q.usage = Usage{CacheWrite: 31500} // nothing read: the entry is lost
	}
	r := ts.BeginRequest(RequestMeta{RequestID: "r", Session: "s1", Model: model, ModelKey: key, At: q.at}, q.client, q.clientBPs)
	fwd := q.fwd
	if fwd.Pos == nil {
		fwd = q.client
	}
	r.EndRequest(Thread{ID: "main"}, fwd, q.clientBPs, q.facts)
	return r.Compute(q.usage)
}

func TestAModelKeyHidesAChangeOfSpelling(t *testing.T) {
	for _, c := range []struct {
		name         string
		m1, k1       string
		m2, k2       string
		wantModelChg bool
	}{
		{"no key, ids differ: the old rule", "claude-sonnet-4-5", "", "claude-sonnet-4-5-20250929", "", true},
		{"same key, ids differ", "claude-sonnet-4-5", "sonnet-4-5", "aws/claude-sonnet-4-5-20250929", "sonnet-4-5", false},
		{"keys differ", "claude-sonnet-4-5", "sonnet-4-5", "claude-opus-4-5", "opus-4-5", true},
		{"key on one side only: the key is compared with the id", "m", "k", "m", "", true},
	} {
		ts := NewTrackers()
		modelRun(t, ts, c.m1, c.k1, true)
		br := effect(t, modelRun(t, ts, c.m2, c.k2, false), EffectBreak)
		if got := br.Cause == CauseModelChanged; got != c.wantModelChg {
			t.Errorf("%s: Cause = %q, want model_changed = %v", c.name, br.Cause, c.wantModelChg)
		}
	}
}

// The key is part of the stored fingerprint: a restart does not lose it.
func TestAModelKeySurvivesSnapshotAndRestore(t *testing.T) {
	ts := NewTrackers()
	modelRun(t, ts, "claude-sonnet-4-5", "sonnet-4-5", true)
	restored := NewTrackers()
	if err := restored.Restore(mustSnap(t, ts)); err != nil {
		t.Fatal(err)
	}
	br := effect(t, modelRun(t, restored, "claude-sonnet-4-5-20250929", "sonnet-4-5", false), EffectBreak)
	if br.Cause == CauseModelChanged {
		t.Fatalf("Cause = %q after restore, want none: the key was lost", br.Cause)
	}
}
