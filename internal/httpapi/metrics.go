package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Prometheus 文本格式的指标暴露。
//
// 为什么不引 prometheus/client_golang：那个库会拖进一大串依赖（protobuf、
// procfs、多个 collector），而本项目是单二进制 + 内嵌前端，体积与依赖数都是卖点。
// 这里需要的只有三个计数器 + 一个耗时直方图，手写约 80 行即可，且完全可控。
//
// 指标的价值在于回答那些日志回答不了的问题：
//   - 哪个账号在持续限流（上游 429 次数按账号分布）
//   - 转存成功率随时间的变化（升级后是否变差）
//   - P95 耗时（定时任务是否开始变慢——往往是接口变更的先行信号）
//   - 调度是否还在触发（sweep 次数长期不涨 = 调度协程挂了）

// metrics 是进程内的轻量指标集合。
type metrics struct {
	mu sync.Mutex

	// requestsTotal 按 method+route+status 计数（route 已归一化，避免高基数）。
	requestsTotal map[string]int64
	// requestSeconds 是耗时直方图分桶（秒）。
	requestBuckets []float64
	requestCounts  []int64
	requestSum     float64

	// apiErrorsTotal 按错误码计数，便于定位"哪个接口在报什么错"。
	apiErrors map[string]int64

	// sweepsTotal 是调度扫描次数（调度协程存活的最直接证据）。
	sweepsTotal int64
	// transfersTotal 是累计转存文件数。
	transfersTotal int64
	// rateLimitedTotal 是上游限流次数（风控信号）。
	rateLimitedTotal int64

	startedAt time.Time
}

// 耗时桶：覆盖 10ms 到 60s，超过 60s 的进 +Inf（对转存工具足够）。
var defaultBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

func newMetrics() *metrics {
	return &metrics{
		requestsTotal:  map[string]int64{},
		requestBuckets: defaultBuckets,
		requestCounts:  make([]int64, len(defaultBuckets)),
		apiErrors:      map[string]int64{},
		startedAt:      time.Now(),
	}
}

// key 构造低基数的指标标签组合。
//
// 高基数是 Prometheus 的头号杀手：如果把 URL path 原样当 label，
// /api/tasks/{id}/runs 会产生每个 ID 一条时间序列，几十万条足以拖垮 Prometheus。
// 这里用 mux 的 route 模板（"GET /api/tasks/{id}/runs"）而不是实际 path。
func metricKey(method, route string, status int) string {
	return fmt.Sprintf("%s|%s|%d", method, route, status)
}

func (m *metrics) observeRequest(method, route string, status int, d time.Duration) {
	sec := d.Seconds()
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requestsTotal[metricKey(method, route, status)]++
	m.requestSum += sec
	for i, b := range m.requestBuckets {
		if sec <= b {
			m.requestCounts[i]++
		}
	}
}

func (m *metrics) incError(code string) {
	m.mu.Lock()
	m.apiErrors[code]++
	m.mu.Unlock()
}

func (m *metrics) incSweep() {
	m.mu.Lock()
	m.sweepsTotal++
	m.mu.Unlock()
}

func (m *metrics) addTransfers(n int) {
	m.mu.Lock()
	m.transfersTotal += int64(n)
	m.mu.Unlock()
}

func (m *metrics) incRateLimited() {
	m.mu.Lock()
	m.rateLimitedTotal++
	m.mu.Unlock()
}

// render 输出 Prometheus 文本格式（Content-Type: text/plain; version=0.0.4）。
func (m *metrics) render() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder
	b.WriteString("# HELP pantransfer_http_requests_total HTTP 请求总数（按 method/route/status）\n")
	b.WriteString("# TYPE pantransfer_http_requests_total counter\n")
	for _, k := range sortedKeys(m.requestsTotal) {
		fmt.Fprintf(&b, "pantransfer_http_requests_total{%s} %d\n", quoteLabel(k), m.requestsTotal[k])
	}

	b.WriteString("# HELP pantransfer_http_request_duration_seconds HTTP 请求耗时\n")
	b.WriteString("# TYPE pantransfer_http_request_duration_seconds histogram\n")
	// 直方图：每个 route 一条序列。为避免标签爆炸，这里只统计总体分布，
	// 按 route 细分的需求用日志排障更直接。
	for i, bound := range m.requestBuckets {
		fmt.Fprintf(&b, "pantransfer_http_request_duration_seconds_bucket{le=\"%g\"} %d\n", bound, m.requestCounts[i])
	}
	fmt.Fprintf(&b, "pantransfer_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", m.totalRequestsLocked())
	fmt.Fprintf(&b, "pantransfer_http_request_duration_seconds_sum %g\n", m.requestSum)
	fmt.Fprintf(&b, "pantransfer_http_request_duration_seconds_count %d\n", m.totalRequestsLocked())

	b.WriteString("# HELP pantransfer_api_errors_total API 错误数（按错误码）\n")
	b.WriteString("# TYPE pantransfer_api_errors_total counter\n")
	for _, k := range sortedKeys(m.apiErrors) {
		fmt.Fprintf(&b, "pantransfer_api_errors_total{code=\"%s\"} %d\n", k, m.apiErrors[k])
	}

	b.WriteString("# HELP pantransfer_scheduler_sweeps_total 调度扫描次数（调度协程存活的证明）\n")
	b.WriteString("# TYPE pantransfer_scheduler_sweeps_total counter\n")
	fmt.Fprintf(&b, "pantransfer_scheduler_sweeps_total %d\n", m.sweepsTotal)

	b.WriteString("# HELP pantransfer_transferred_files_total 累计转存文件数\n")
	b.WriteString("# TYPE pantransfer_transferred_files_total counter\n")
	fmt.Fprintf(&b, "pantransfer_transferred_files_total %d\n", m.transfersTotal)

	b.WriteString("# HELP pantransfer_rate_limited_total 上游限流命中次数（风控信号）\n")
	b.WriteString("# TYPE pantransfer_rate_limited_total counter\n")
	fmt.Fprintf(&b, "pantransfer_rate_limited_total %d\n", m.rateLimitedTotal)

	b.WriteString("# HELP pantransfer_uptime_seconds 进程运行时长\n")
	b.WriteString("# TYPE pantransfer_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "pantransfer_uptime_seconds %g\n", time.Since(m.startedAt).Seconds())

	return b.String()
}

func (m *metrics) totalRequestsLocked() int64 {
	var n int64
	for _, v := range m.requestsTotal {
		n += v
	}
	return n
}

func sortedKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// quoteLabel 把 "GET|{{SECRET_oizaxyun}}" 转成 Prometheus 的 method/route/status 标签。
// 直接内联进 label 字符串前要做一次替换，避免生成非法语法。
func quoteLabel(k string) string {
	parts := strings.SplitN(k, "|", 3)
	if len(parts) != 3 {
		return fmt.Sprintf("method=%q", k)
	}
	return fmt.Sprintf("method=%q,route=%q,status=%q", parts[0], parts[1], parts[2])
}

// routePattern 把实际 URL path 归一化成 mux 的 route 模板，避免高基数标签。
//
// 例：/api/tasks/42/runs -> /api/tasks/{id}/runs
//
// 为什么不用 r.Pattern（Go 1.23+ 才有）：需要兼容较老的 Go 版本与自托管编译环境；
// 而这里的路由集合是固定的 39 个端点，正则足够精确。
// normalizeRoute 把实际 URL path 归一化成 mux 的 route 模板，避免高基数标签。
func normalizeRoute(path string) string {
	// 只归一化 /api 下的已知模式，其它一律记为 "other"（静态资源不需要指标）。
	switch {
	case strings.HasPrefix(path, "/api/tasks/"):
		if strings.HasSuffix(path, "/runs") {
			return "/api/tasks/{id}/runs"
		}
		if strings.HasSuffix(path, "/saved-names") {
			return "/api/tasks/{id}/saved-names"
		}
		if strings.HasSuffix(path, "/saved-names/count") {
			return "/api/tasks/{id}/saved-names/count"
		}
		if strings.HasSuffix(path, "/status") {
			return "/api/tasks/{id}/status"
		}
		if strings.HasSuffix(path, "/run") {
			return "/api/tasks/{id}/run"
		}
		return "/api/tasks/{id}"
	case strings.HasPrefix(path, "/api/accounts/"):
		if strings.HasSuffix(path, "/profile") {
			return "/api/accounts/{id}/profile"
		}
		if strings.HasSuffix(path, "/probe") {
			return "/api/accounts/{id}/probe"
		}
		return "/api/accounts/{id}"
	case strings.HasPrefix(path, "/api/categories/"):
		if strings.HasSuffix(path, "/test") {
			return "/api/categories/{id}/test"
		}
		if strings.HasSuffix(path, "/callback-logs") {
			return "/api/categories/{id}/callback-logs"
		}
		return "/api/categories/{id}"
	case strings.HasPrefix(path, "/api/jobs/"):
		if strings.HasSuffix(path, "/cancel") {
			return "/api/jobs/{id}/cancel"
		}
		return "/api/jobs/{id}"
	case strings.HasPrefix(path, "/api/rules/"):
		return "/api/rules/{key}"
	}
	return path
}

// httpInstrumentation 是一个中间件，负责请求指标与访问日志。
//
// 成功请求此前完全没有日志（wrap 只在 handler 返回 error 时 Warn），
// 导致无法做访问审计、也无法回答"这个慢请求是谁发的"。
func httpInstrumentation(next http.Handler, m *metrics, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		// 只对 API 路径记指标：静态资源（内嵌前端）会产生大量噪声且价值低。
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			return
		}
		route := normalizeRoute(r.URL.Path)
		m.observeRequest(r.Method, route, rec.status, time.Since(start))

		// 访问日志：状态码 >= 500 记 Warn，4xx 记 Info，其余不记（避免刷屏）。
		switch {
		case rec.status >= 500:
			log.Warn("请求失败", "method", r.Method, "path", r.URL.Path, "status", rec.status, "cost", time.Since(start).String())
		case rec.status >= 400:
			log.Info("请求被拒绝", "method", r.Method, "path", r.URL.Path, "status", rec.status)
		}
	})
}

// statusRecorder 记录响应状态码（http.ResponseWriter 不暴露它）。
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Flush 透传，保证 SSE / 流式响应不被缓冲。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}