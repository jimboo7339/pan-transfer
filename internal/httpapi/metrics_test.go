package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMetricsEndpointFormat 验证 /metrics 输出 Prometheus 文本格式。
//
// 这是自托管圈的通用语言：有了它，PanTransfer 才能接进 Grafana，
// 而"成功率 P95 耗时"这些是日志回答不了的问题。
func TestMetricsEndpointFormat(t *testing.T) {
	srv, _ := newTestServer(t)

	// 先制造一些请求，让计数器非零。
	_, _ = doJSON(t, srv, http.MethodGet, "/api/settings", nil)
	_, _ = doJSON(t, srv, http.MethodGet, "/api/health", nil)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type 应为 text/plain，得到 %q", ct)
	}

	body := rec.Body.String()
	mustContain := []string{
		"# HELP pantransfer_http_requests_total",
		"# TYPE pantransfer_http_requests_total counter",
		"pantransfer_http_request_duration_seconds_bucket",
		"pantransfer_http_request_duration_seconds_sum",
		"pantransfer_api_errors_total",
		"pantransfer_scheduler_sweeps_total",
		"pantransfer_uptime_seconds",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("指标输出缺少 %q", want)
		}
	}
}

// TestMetricsLabelsAreLowCardinality 验证指标标签使用路由模板而非真实路径。
//
// 高基数是 Prometheus 的头号杀手：如果 /api/tasks/{id}/runs 用真实路径做 label，
// 每个任务 ID 都会生成一条独立时间序列，几十个任务就能让 Prometheus 变慢。
func TestMetricsLabelsAreLowCardinality(t *testing.T) {
	srv, _ := newTestServer(t)

	// 请求几个不同的具体路径（对应同一个路由模板）。
	for _, id := range []string{"1", "2", "3", "4", "5"} {
		_, _ = doJSON(t, srv, http.MethodGet, "/api/jobs/"+id, nil)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `route="/api/jobs/{id}"`) {
		t.Errorf("应使用归一化的路由模板，输出片段：\n%s", firstLines(body, 40))
	}
	// 不应出现任何一个具体的 jobs/1 之类的标签。
	for _, id := range []string{"1", "2", "3", "4", "5"} {
		if strings.Contains(body, `route="/api/jobs/`+id+`"`) {
			t.Errorf("标签中不应包含具体 ID %s（高基数）", id)
		}
	}
}

// TestMetricsCountsRequests 验证请求计数确实在累加。
func TestMetricsCountsRequests(t *testing.T) {
	srv, _ := newTestServer(t)

	// 先打一次目标请求（第一次调用 metricsCount 时该路由还不存在，返回 -1，
	// 所以这里以"打过一次之后"的读数作为基线）。
	_, _ = doJSON(t, srv, http.MethodGet, "/api/settings", nil)
	before := metricsCount(t, srv, "/api/settings")
	_, _ = doJSON(t, srv, http.MethodGet, "/api/settings", nil)
	after := metricsCount(t, srv, "/api/settings")

	if after != before+1 {
		t.Errorf("计数应 +1：before=%d after=%d", before, after)
	}
}

// TestMetricsNormalizesRoute 验证路径归一化覆盖主要端点。
func TestMetricsNormalizesRoute(t *testing.T) {
	cases := map[string]string{
		"/api/tasks/42/runs":             "/api/tasks/{id}/runs",
		"/api/tasks/42/saved-names":      "/api/tasks/{id}/saved-names",
		"/api/tasks/42/saved-names/count": "/api/tasks/{id}/saved-names/count",
		"/api/tasks/42/status":           "/api/tasks/{id}/status",
		"/api/tasks/42/run":              "/api/tasks/{id}/run",
		"/api/tasks/42":                  "/api/tasks/{id}",
		"/api/accounts/7/probe":          "/api/accounts/{id}/probe",
		"/api/accounts/7/profile":        "/api/accounts/{id}/profile",
		"/api/accounts/7":                "/api/accounts/{id}",
		"/api/categories/3/test":         "/api/categories/{id}/test",
		"/api/categories/3/callback-logs": "/api/categories/{id}/callback-logs",
		"/api/jobs/9/cancel":             "/api/jobs/{id}/cancel",
		"/api/jobs/9":                    "/api/jobs/{id}",
		"/api/settings":                  "/api/settings",
		"/api/health":                    "/api/health",
	}
	for in, want := range cases {
		if got := normalizeRoute(in); got != want {
			t.Errorf("normalizeRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHTTPInstrumentationRecordsSlowRequests 验证耗时直方图对慢请求也生效。
func TestHTTPInstrumentationRecordsSlowRequests(t *testing.T) {
	m := newMetrics()
	h := httpInstrumentation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}), m, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	m.mu.Lock()
	count := m.totalRequestsLocked()
	sum := m.requestSum
	m.mu.Unlock()
	if count != 1 {
		t.Errorf("应记录 1 次请求，得到 %d", count)
	}
	if sum < 0.02 {
		t.Errorf("耗时应 >= 20ms，得到 %g 秒", sum)
	}
}

// TestHTTPInstrumentationSkipsStatic 验证静态资源不进指标（避免噪声）。
func TestHTTPInstrumentationSkipsStatic(t *testing.T) {
	m := newMetrics()
	h := httpInstrumentation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), m, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/index-abc.js", nil))

	m.mu.Lock()
	count := m.totalRequestsLocked()
	m.mu.Unlock()
	if count != 0 {
		t.Errorf("静态资源不应计入指标，得到 %d", count)
	}
}

// TestStatusRecorderCapturesCode 验证状态码记录器能抓到写入的状态。
func TestStatusRecorderCapturesCode(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	sr.WriteHeader(http.StatusTeapot)
	if sr.status != http.StatusTeapot {
		t.Errorf("status = %d, want %d", sr.status, http.StatusTeapot)
	}
}

// metricsCount 从指标输出里读出某个 route+status 的计数。
func metricsCount(t *testing.T, srv *Server, route string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "pantransfer_http_requests_total{") &&
			strings.Contains(line, `route="`+route+`"`) {
			// 取行尾数字
			idx := strings.LastIndex(line, " ")
			if idx < 0 {
				continue
			}
			var n int
			_, _ = fmtSscan(line[idx+1:], &n)
			return n
		}
	}
	return -1
}

// firstLines 取前 n 行，用于失败时输出可读片段。
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// fmtSscan 是 fmt.Sscanf 的小包装，避免在测试里直接引 fmt 造成困惑。
func fmtSscan(s string, out *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	*out = n
	return 1, nil
}