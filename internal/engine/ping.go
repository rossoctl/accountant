package engine

import "time"

// Ping is a keep-alive ping in the library's own terms, so the library does not import a host
// type. The host maps its own report onto this (wiring document, W4.6).
type Ping struct {
	Session, ThreadID, Model, Host string
	Usage                          CallUsage
	At                             time.Time // when the ping started
	Window                         time.Time // the start of the idle gap the ping served
}

// PingFacts are what the thread's next real request needs in EntryFacts: LastPing, PingWindow and
// PingUsage. The host keeps the last one for each thread, passes it at that request's EndRequest, then
// clears it. With PingUsage set and PingRead nil, the library derives the extent the ping read
// (EntryFacts.PingUsage).
type PingFacts struct {
	LastPing   time.Time
	PingWindow string
	PingUsage  Usage
}

// Apply copies the facts into f. A ping that billed nothing leaves PingUsage nil.
func (p PingFacts) Apply(f *EntryFacts) {
	f.LastPing, f.PingWindow, f.PingUsage = p.LastPing, p.PingWindow, nil
	if p.PingUsage != (Usage{}) {
		u := p.PingUsage
		f.PingUsage = &u
	}
}

// pingThread gives the thread a ping is recorded on. The thread "" is the primary thread and
// becomes "main".
func pingThread(thread string) string {
	if thread == "" {
		return "main"
	}
	return thread
}

// PingWindowID gives the id of the keep-alive window that starts at start on thread: "ka:" + thread
// + ":" + start (RFC 3339, UTC). The thread "" becomes "main". This is the one rule for the id.
// PingOps uses it for the ChangeID and PingFacts.PingWindow, and a host uses it for a ping it does
// not send (for example a refused ping), so every id for one window is the same string.
func PingWindowID(thread string, start time.Time) string {
	return "ka:" + pingThread(thread) + ":" + start.UTC().Format(time.RFC3339)
}

// PingOps turns a ping into a RequestOps with one spend, and returns the facts for the thread's
// next real request. Price the RequestOps with ComputeStandalone. The thread "" is the primary
// thread and becomes "main". The ChangeID is PingWindowID(thread, window start), and
// PingFacts.PingWindow is the same string, so the ping's cost and the rescue it earns land on one
// change (design §4.4).
func PingOps(p Ping, b Billing) (RequestOps, PingFacts) {
	id := pingThread(p.ThreadID)
	change := PingWindowID(p.ThreadID, p.Window)
	return RequestOps{
			Version: 1,
			Session: p.Session,
			Thread:  Thread{ID: id},
			Model:   p.Model,
			At:      p.At,
			Billing: b,
			Ops: []Operation{{
				Kind:     KindSpend,
				Operator: "keeper",
				ChangeID: change,
				Call:     &ModelCall{Host: p.Host, Model: p.Model, Usage: p.Usage},
			}},
		},
		PingFacts{LastPing: p.At, PingWindow: change, PingUsage: p.Usage.Usage}
}
