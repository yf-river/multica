// 本文件是 metrics 包内部测试专用的辅助工具（仅测试引用，import "testing"，
// 生产代码路径不会用到）。放在普通 .go 而非 _test.go 里，方便跨包的测试文件复用。
package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// SumAllCounters 返回当前注册在接收者 m 上所有 counter 样本的总和。供
// business_pairing_test.go 的 lint 测试用：确认一个合成事件至少让一个计数器
// 前进——即 IncForEvent 分发确实覆盖到了这个分支。生产代码从不调用它。
//
// 故意排除直方图与 Gauge，否则预热过的桶（例如 failure_reason 的 0 计数）会让
// 任何事件都「平凡地通过」检查，测不出分发是否真的发生。
func SumAllCounters(m *BusinessMetrics) float64 {
	if m == nil {
		return 0
	}
	// 每次调用新建一个干净 Registry：MustRegister 遇到重复注册会 panic，
	// 复用一个共享注册表会互相污染。
	reg := prometheus.NewPedanticRegistry()
	for _, c := range m.Collectors() {
		reg.MustRegister(c)
	}
	families, err := reg.Gather()
	if err != nil {
		return 0
	}
	var total float64
	for _, fam := range families {
		if fam.GetType() != dto.MetricType_COUNTER {
			continue
		}
		for _, mtr := range fam.GetMetric() {
			if c := mtr.GetCounter(); c != nil {
				total += c.GetValue()
			}
		}
	}
	return total
}

// GatherForTest 把全部采集器注册到新 Registry 上，返回按指标名索引的结果。
// 仅测试用——生产环境的 /metrics 端点读的是 NewRegistry 里构建的共享注册表。
// 任何 Gather 错误都会通过 t.Fatalf 上报，所以调用方拿到返回值可以直接解引用，
// 不用再做 nil 判断。
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
