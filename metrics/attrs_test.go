package metrics

import "go.opentelemetry.io/otel/attribute"

func kv(in []attribute.KeyValue) map[string]string {
	m := make(map[string]string, len(in))
	for _, a := range in {
		m[string(a.Key)] = a.Value.Emit()
	}
	return m
}
