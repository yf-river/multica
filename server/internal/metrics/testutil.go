package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// GatherForTest registers every collector on a fresh registry and returns
// the resulting metric families keyed by name. Test-only — the production
// /metrics endpoint reads from the shared registry constructed in
// NewRegistry. Any Gather error is reported via t.Fatalf so callers can
// dereference the result without nil checks.
func GatherForTest(t *testing.T, m *BusinessMetrics) map[string]*dto.MetricFamily {
	t.Helper()
	if m == nil {
		t.Fatalf("GatherForTest: nil BusinessMetrics")
	}
	reg := prometheus.NewPedanticRegistry()
	for _, c := range m.Collectors() {
		reg.MustRegister(c)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("GatherForTest: gather failed: %v", err)
	}
	out := make(map[string]*dto.MetricFamily, len(families))
	for _, fam := range families {
		out[fam.GetName()] = fam
	}
	return out
}
