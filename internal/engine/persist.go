package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// snapshotVersion is the format of [Trackers.Snapshot]. Restore refuses any other value.
const snapshotVersion = 1

// wireSnapshot is the saved form of a set of Trackers. It holds hashes and sizes only (R2).
type wireSnapshot struct {
	Version int          `json:"version"`
	Threads []wireThread `json:"threads"`
}

type wireThread struct {
	Key       string      `json:"key"`
	Created   time.Time   `json:"created"`
	Active    []Operation `json:"active,omitempty"`
	PrevPrint *wirePrint  `json:"prevPrint,omitempty"`
	Cache     CacheState  `json:"cache"`
	Version   uint64      `json:"stateVersion"`
}

type wirePrint struct {
	Thread               string          `json:"thread,omitempty"`
	Model                string          `json:"model,omitempty"`
	At                   time.Time       `json:"at"`
	Client               Body            `json:"client"`
	Forwarded            Body            `json:"forwarded"`
	ClientBreakpoints    []Breakpoint    `json:"clientBreakpoints,omitempty"`
	ForwardedBreakpoints []Breakpoint    `json:"forwardedBreakpoints,omitempty"`
	Cached               Position        `json:"cached"`
	Splices              []MessageSplice `json:"splices,omitempty"`
}

// Snapshot returns the state of every thread as JSON. It holds hashes, sizes and positions only
// (R2). A host saves it before a restart and passes it to [Trackers.Restore] after. It does no I/O.
// The same state gives the same bytes (R26).
func (ts *Trackers) Snapshot() []byte {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	w := wireSnapshot{Version: snapshotVersion, Threads: make([]wireThread, 0, len(ts.m))}
	for k, t := range ts.m {
		s := t.snapshot()
		wt := wireThread{Key: k, Created: t.created, Active: s.Active, Cache: s.Cache, Version: s.Version}
		if p := s.PrevPrint; p != nil {
			wt.PrevPrint = &wirePrint{Thread: p.Thread, Model: p.Model, At: p.At, Client: p.Client,
				Forwarded: p.Forwarded, ClientBreakpoints: p.ClientBreakpoints,
				ForwardedBreakpoints: p.ForwardedBreakpoints, Cached: p.Cached, Splices: p.splices}
		}
		w.Threads = append(w.Threads, wt)
	}
	slices.SortFunc(w.Threads, func(a, b wireThread) int { return strings.Compare(a.Key, b.Key) })
	out, err := json.Marshal(w)
	if err != nil { // only plain data types are in w, so this cannot happen
		panic(fmt.Sprintf("accountant: snapshot: %v", err))
	}
	return out
}

// Restore replaces the state of every thread with the one in data, made by [Trackers.Snapshot]. On
// an error it changes nothing. The count of [Trackers.Trimmed] is not part of the snapshot.
func (ts *Trackers) Restore(data []byte) error {
	var w wireSnapshot
	if err := json.Unmarshal(data, &w); err != nil {
		return fmt.Errorf("accountant: restore: %w", err)
	}
	if w.Version != snapshotVersion {
		return fmt.Errorf("accountant: restore: snapshot version %d, want %d", w.Version, snapshotVersion)
	}
	m := make(map[string]*Tracker, len(w.Threads))
	for _, wt := range w.Threads {
		st := ThreadState{Active: wt.Active, Cache: wt.Cache, Version: wt.Version}
		if p := wt.PrevPrint; p != nil {
			st.PrevPrint = &Fingerprint{Thread: p.Thread, Model: p.Model, At: p.At, Client: p.Client,
				Forwarded: p.Forwarded, ClientBreakpoints: p.ClientBreakpoints,
				ForwardedBreakpoints: p.ForwardedBreakpoints, Cached: p.Cached, splices: p.Splices}
		}
		m[wt.Key] = &Tracker{state: st, created: wt.Created}
	}
	ts.mu.Lock()
	ts.m = m
	ts.mu.Unlock()
	return nil
}

// Trimmed returns how many threads the trim has dropped since the set was made. A host can export
// it as a metric. A dropped thread loses its active changes (design §6.1, Q13).
func (ts *Trackers) Trimmed() uint64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.trimmed
}
