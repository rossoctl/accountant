package metrics

import "github.com/rossoctl/accountant"

// Observation is what a host hands to an [Observer] after it has computed one request: the request's
// facts, the usage the provider billed, and the result.
type Observation struct {
	Meta  accountant.RequestMeta
	Usage accountant.Usage
	// Output is the output tokens the provider billed for the request. 0 means the host does not give
	// it. A host with a SizedPricer sets it to get the re-rate of the output tier. Output never counts
	// toward the prompt size.
	Output int
	Result accountant.Result
	// TokensBefore is the prompt size, in tokens, of the body the client sent, by the host's own
	// measure. Above 0, the sink gives it to [accountant.Withhold] for the size bound, which then counts
	// every operation. 0 or less means the host does not give it, and the sink calls Withhold with no size:
	// the library's own measure of the client messages decides.
	//
	// Deprecated: the sink uses the library's own size bound when TokensBefore is 0 or less. A positive
	// value still selects the legacy host bound until the field is removed
	// (docs/metrics/metrics.md).
	TokensBefore int
}

// Observer receives each [Observation]. The host calls it after Compute, once for each request or
// ping. The accountant never calls an Observer itself, so pricing stays pure.
//
// An Observer must return fast. It must make no network call and take no blocking lock. It must not
// keep the slices of the Result: the host may reuse them.
//
// [Sink] is the OpenTelemetry Observer. A host with another consumer, such as a ledger, declares the
// same one-method interface itself: Go interfaces are structural, so it needs no import of this one.
type Observer interface {
	Observe(Observation)
}

// Multi returns an [Observer] that calls each observer in order. A nil observer is skipped. With none
// left, Multi returns nil. A panic in one observer does not stop the others: Multi recovers it and goes on.
func Multi(observers ...Observer) Observer {
	var live multi
	for _, o := range observers {
		if o != nil {
			live = append(live, o)
		}
	}
	if len(live) == 0 {
		return nil // a host that tests o == nil gets true
	}
	return live
}

type multi []Observer

func (m multi) Observe(o Observation) {
	for _, x := range m {
		observeSafely(x, o)
	}
}

func observeSafely(x Observer, o Observation) {
	defer func() { _ = recover() }()
	x.Observe(o)
}
