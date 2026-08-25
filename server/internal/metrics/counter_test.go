package metrics_test

import (
	"github.com/multica-ai/multica/server/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func sumAllCounters(m *metrics.BusinessMetrics) float64 {
	if m == nil {
		return 0
	}
	reg := prometheus.NewPedanticRegistry()
	for _, collector := range m.Collectors() {
		reg.MustRegister(collector)
	}
	families, err := reg.Gather()
	if err != nil {
		return 0
	}
	var total float64
	for _, family := range families {
		if family.GetType() != dto.MetricType_COUNTER {
			continue
		}
		for _, metric := range family.GetMetric() {
			if counter := metric.GetCounter(); counter != nil {
				total += counter.GetValue()
			}
		}
	}
	return total
}
