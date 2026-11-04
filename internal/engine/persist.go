package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// snapshotVersion is the format of [Trackers.Snapshot]. Restore refuses any other value.
const snapshotVersion = 1

// wireSnapshot is the saved form of a set of Trackers. It holds no content (R2): no message text.
// It holds hashes, sizes, positions, and the IDs and names the state needs (session and thread keys,
// operator names, ChangeIDs, model names, setting values, times and TTL strings).
type wireSnapshot struct {
	Version int           `json:"version"`
	Threads *[]wireThread `json:"threads"`
}

type wireThread struct {
	Key       string      `json:"key"`
	Created   time.Time   `json:"created"`
	Active    []Operation `json:"active,omitempty"`
	PrevPrint *wirePrint  `json:"prevPrint,omitempty"`
	Cache     CacheState  `json:"cache"`
	Version   uint64      `json:"stateVersion"`
}

// wireBody is a Body whose 64-bit hashes are decimal strings, so a JavaScript reader does not round
// them.
type wireBody struct {
	Start [3]int    `json:"start"`
	Pos   []wirePos `json:"pos"`
}

type wirePos struct {
	Hash  string `json:"hash"`
	Bytes int32  `json:"bytes"`
	Media int32  `json:"media,omitempty"`
}

func toWireBody(b Body) wireBody {
	w := wireBody{Start: b.Start, Pos: make([]wirePos, len(b.Pos))}
	for i, p := range b.Pos {
		w.Pos[i] = wirePos{Hash: strconv.FormatUint(p.Hash, 10), Bytes: p.Bytes, Media: p.Media}
	}
	return w
}

func fromWireBody(w wireBody) (Body, error) {
	b := Body{Start: w.Start}
	if len(w.Pos) > 0 {
		b.Pos = make([]PosHash, len(w.Pos))
	}
	for i, p := range w.Pos {
		h, err := strconv.ParseUint(p.Hash, 10, 64)
		if err != nil {
			return Body{}, err
		}
		b.Pos[i] = PosHash{Hash: h, Bytes: p.Bytes, Media: p.Media}
	}
	return b, nil
}

type wirePrint struct {
	Thread               string          `json:"thread,omitempty"`
	Model                string          `json:"model,omitempty"`
	At                   time.Time       `json:"at"`
	Client               wireBody        `json:"client"`
	Forwarded            wireBody        `json:"forwarded"`
	ClientBreakpoints    []Breakpoint    `json:"clientBreakpoints,omitempty"`
	ForwardedBreakpoints []Breakpoint    `json:"forwardedBreakpoints,omitempty"`
	Cached               Position        `json:"cached"`
	Splices              []MessageSplice `json:"splices,omitempty"`
}

// Snapshot returns the state of every thread as JSON. It holds no content (R2): no message text. It
// holds hashes, sizes, positions, and the IDs and names the state needs (session and thread keys,
// operator names, ChangeIDs, model names, setting values, times and TTL strings). A host saves it
// before a restart and passes it to [Trackers.Restore] after. It does no I/O. The same state gives
// the same bytes. It returns an error when the state cannot be encoded, for example a request time
// outside the years 0 to 9999 (Compute prices such a request without error).
func (ts *Trackers) Snapshot() ([]byte, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	threads := make([]wireThread, 0, len(ts.m))
	for k, t := range ts.m {
		s := t.snapshot()
		wt := wireThread{Key: k, Created: t.created, Active: s.Active, Cache: s.Cache, Version: s.Version}
		if p := s.PrevPrint; p != nil {
			wt.PrevPrint = &wirePrint{Thread: p.Thread, Model: p.Model, At: p.At, Client: toWireBody(p.Client),
				Forwarded: toWireBody(p.Forwarded), ClientBreakpoints: p.ClientBreakpoints,
				ForwardedBreakpoints: p.ForwardedBreakpoints, Cached: p.Cached, Splices: p.splices}
		}
		threads = append(threads, wt)
	}
	slices.SortFunc(threads, func(a, b wireThread) int { return strings.Compare(a.Key, b.Key) })
	out, err := json.Marshal(wireSnapshot{Version: snapshotVersion, Threads: &threads})
	if err != nil {
		return nil, fmt.Errorf("accountant: snapshot: %w", err)
	}
	return out, nil
}

// Restore replaces the state of every thread with the one in data, made by [Trackers.Snapshot]. On
// an error it changes nothing. It refuses a snapshot with no threads field, an empty thread key, or
// a key twice. An EndRequest that runs during Restore can be lost: stop the traffic first. The count
// of [Trackers.Trimmed] is not part of the snapshot.
func (ts *Trackers) Restore(data []byte) error {
	var w wireSnapshot
	if err := json.Unmarshal(data, &w); err != nil {
		return fmt.Errorf("accountant: restore: %w", err)
	}
	if w.Version != snapshotVersion {
		return fmt.Errorf("accountant: restore: snapshot version %d, want %d", w.Version, snapshotVersion)
	}
	if w.Threads == nil {
		return fmt.Errorf("accountant: restore: no threads field")
	}
	m := make(map[string]*Tracker, len(*w.Threads))
	for _, wt := range *w.Threads {
		if wt.Key == "" {
			return fmt.Errorf("accountant: restore: empty thread key")
		}
		if _, dup := m[wt.Key]; dup {
			return fmt.Errorf("accountant: restore: thread key %q twice", wt.Key)
		}
		st := ThreadState{Active: wt.Active, Cache: wt.Cache, Version: wt.Version}
		if p := wt.PrevPrint; p != nil {
			c, err := fromWireBody(p.Client)
			if err != nil {
				return fmt.Errorf("accountant: restore: %w", err)
			}
			f, err := fromWireBody(p.Forwarded)
			if err != nil {
				return fmt.Errorf("accountant: restore: %w", err)
			}
			st.PrevPrint = &Fingerprint{Thread: p.Thread, Model: p.Model, At: p.At, Client: c, Forwarded: f,
				ClientBreakpoints: p.ClientBreakpoints, ForwardedBreakpoints: p.ForwardedBreakpoints,
				Cached: p.Cached, splices: p.Splices}
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
