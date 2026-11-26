package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"pan-transfer/internal/domain"
)

// TestWriteErrUsesCanonicalStatus 验证错误码 → HTTP 状态码走 AppError.HTTPStatus()
// 单一事实源。早先 writeErr 另写一份 switch，把 PERMISSION_DENIED 映射成 401
// （应 403）、DRIVER_ERROR 落入默认 500（应 502），两套映射分叉。
func TestWriteErrUsesCanonicalStatus(t *testing.T) {
	cases := []struct {
		code domain.ErrorCode
		want int
	}{
		{domain.CodeValidation, http.StatusBadRequest},
		{domain.CodeNotFound, http.StatusNotFound},
		{domain.CodeAuthExpired, http.StatusUnauthorized},
		{domain.CodeAdminAuthRequired, http.StatusUnauthorized},
		{domain.CodeRateLimited, http.StatusTooManyRequests},
		{domain.CodePermissionDenied, http.StatusForbidden},
		{domain.CodeDriverError, http.StatusBadGateway},
		{domain.CodeNotImplement, http.StatusNotImplemented},
		{domain.CodeInternal, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeErr(rec, domain.Errf(tc.code))
			if rec.Code != tc.want {
				t.Fatalf("%s 期望 %d，得到 %d（响应: %s）", tc.code, tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRestoreGateRejectsRequests 验证还原换库窗口内所有 API 请求被门控成 503，
// 而不是拿到已 Close 的库产生 500 或鉴权 fail-open。
func TestRestoreGateRejectsRequests(t *testing.T) {
	srv, _ := newTestServer(t)
	var restoring atomic.Bool
	srv.deps.Restoring = &restoring

	restoring.Store(true)
	rec, _ := doJSON(t, srv, http.MethodGet, "/api/tasks", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("还原期间应返回 503，得到 %d（响应: %s）", rec.Code, rec.Body.String())
	}

	restoring.Store(false)
	rec, _ = doJSON(t, srv, http.MethodGet, "/api/tasks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("还原结束后应恢复 200，得到 %d（响应: %s）", rec.Code, rec.Body.String())
	}
}

// TestAuthEnabledFailClosed 验证读库失败时按"需要鉴权"处理（fail-closed）。
// 早先吞掉错误返回 false，库故障/还原窗口会放行未鉴权请求。
func TestAuthEnabledFailClosed(t *testing.T) {
	srv, st := newTestServer(t)
	// 关闭库后 GetSetting 必然失败。
	_ = st.Close()

	if !srv.authEnabled(t.Context()) {
		t.Fatal("读库失败时应按需要鉴权处理，实际返回 false（fail-open）")
	}
}

// TestOriginAllowedIgnoresSpoofedForwardedHost 验证未配置可信代理时不采信
// 客户端伪造的 X-Forwarded-Host，避免绕过同源校验。
func TestOriginAllowedIgnoresSpoofedForwardedHost(t *testing.T) {
	srv, _ := newTestServer(t)
	t.Setenv("PAN_TRANSFER_TRUST_PROXY", "")

	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8935/api/tasks", nil)
	r.Host = "127.0.0.1:8935"
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("X-Forwarded-Host", "evil.example")
	if srv.originAllowed(r) {
		t.Fatal("伪造的 X-Forwarded-Host 让跨站 Origin 被判为同源")
	}

	// 自身 Origin 仍应放行。
	r.Header.Set("Origin", "http://127.0.0.1:8935")
	r.Header.Set("X-Forwarded-Host", "")
	if !srv.originAllowed(r) {
		t.Fatal("同源 Origin 被拒绝")
	}
}

// TestHealthReadyWithNilStore 验证 Store 未注入/还原失败置 nil 时
// /api/health/ready 返回 503 而不是 panic（该端点不套 wrap）。
func TestHealthReadyWithNilStore(t *testing.T) {
	srv, _ := newTestServer(t)
	slot := srv.deps.Store
	*slot = nil

	req := httptest.NewRequest(http.MethodGet, "/api/health/ready", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("无 Store 时应返回 503，得到 %d（响应: %s）", rec.Code, rec.Body.String())
	}
}
