package engine

// Observation is what a host hands to a [Sink] after it has computed one request: the request's
// facts, the usage the provider billed, and the result.
type Observation struct {
	Meta   RequestMeta
	Usage  Usage
	Result Result
}

// Sink receives each [Observation]. The host calls it after Compute, once for each request or ping.
// The library never calls a Sink itself, so pricing stays pure.
//
// A Sink must return fast. It must make no network call and take no blocking lock. It must not keep
// the slices of the Result: the host may reuse them.
type Sink interface {
	Observe(Observation)
}

// Multi returns a Sink that calls each sink in order. A nil sink is skipped. A panic in one sink
// does not stop the others: Multi recovers it and goes on.
func Multi(sinks ...Sink) Sink {
	var live multi
	for _, s := range sinks {
		if s != nil {
			live = append(live, s)
		}
	}
	return live
}

type multi []Sink

func (m multi) Observe(o Observation) {
	for _, s := range m {
		observeSafely(s, o)
	}
}

func observeSafely(s Sink, o Observation) {
	defer func() { _ = recover() }()
	s.Observe(o)
}
