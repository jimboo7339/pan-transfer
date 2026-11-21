package httpapi

import "pan-transfer/internal/transfer"

// metricsSink 把 transfer.MetricsSink 的三个方法桥到本包的 metrics 上。
//
// 放在 httpapi 而不是 transfer：指标的具体存储与渲染是 HTTP/运维关注点，
// 领域层只声明「发生了什么事件」（transfer.MetricsSink）。
// httpapi 已经 import transfer（它构造 API 响应），依赖方向不变。
type metricsSink struct {
	m *metrics
}

// newMetricsSink 构造一个桥接器。
func newMetricsSink(m *metrics) transfer.MetricsSink { return &metricsSink{m: m} }

// SweepCompleted 实现 transfer.MetricsSink。
func (s *metricsSink) SweepCompleted() { s.m.incSweep() }

// Transferred 实现 transfer.MetricsSink。
func (s *metricsSink) Transferred(n int) {
	if n > 0 {
		s.m.addTransfers(n)
	}
}

// RateLimited 实现 transfer.MetricsSink。
func (s *metricsSink) RateLimited() { s.m.incRateLimited() }
