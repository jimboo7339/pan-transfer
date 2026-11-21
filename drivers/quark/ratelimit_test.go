package quark

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/driver"
)

// TestRateLimitMapsToRateLimitedError 验证夸克 429 被映射成 CodeRateLimited。
//
// 这是本次修复的核心：夸克此前把 HTTP 429 归入 `>= 400` 的 CodeDriverError
// 分支，既不识别为限流也不重试（RetryOnRateLimit 只在光鸭里调用），
// 导致一次限流就让整条转存任务失败。
func TestRateLimitMapsToRateLimitedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":429,"message":"操作过于频繁"}`))
	}))
	defer srv.Close()

	d := &Driver{}
	d.InitForTest(srv.URL)

	_, err := d.apiRequestTo(context.Background(), d.apiBase(), http.MethodGet, pathList, nil, nil, nil)
	if err == nil {
		t.Fatal("429 应返回错误")
	}
	ae, ok := domain.AsAppError(err)
	if !ok {
		t.Fatalf("应为 *domain.AppError，得到 %T: %v", err, err)
	}
	if ae.Code != domain.CodeRateLimited {
		t.Errorf("错误码应为 CodeRateLimited，得到 %v（错误：%v）", ae.Code, err)
	}
}

// TestBusinessRateLimitMapsToRateLimited 验证 HTTP 200 但业务码 429 也算限流。
//
// 夸克有些接口用 200 + status=429 表达"操作过于频繁"，
// 只看 HTTP 状态码会漏掉这种情况。
func TestBusinessRateLimitMapsToRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":429,"code":429,"message":"操作过于频繁","data":null}`))
	}))
	defer srv.Close()

	d := &Driver{}
	d.InitForTest(srv.URL)

	_, err := d.apiRequestTo(context.Background(), d.apiBase(), http.MethodGet, pathList, nil, nil, nil)
	if err == nil {
		t.Fatal("业务码 429 应返回错误")
	}
	ae, ok := domain.AsAppError(err)
	if !ok {
		t.Fatalf("应为 *domain.AppError，得到 %T: %v", err, err)
	}
	if ae.Code != domain.CodeRateLimited {
		t.Errorf("错误码应为 CodeRateLimited，得到 %v（错误：%v）", ae.Code, err)
	}
}

// TestApiRequestRetriesOnRateLimit 验证 apiRequest 会对限流做退避重试。
//
// 第一次 429、第二次成功时，整体应成功——这正是用户能感知到的差别
// （以前是第一次 429 整条任务就失败了）。
func TestApiRequestRetriesOnRateLimit(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":429,"message":"操作过于频繁"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"list":[]}}`))
	}))
	defer srv.Close()

	d := &Driver{}
	d.InitForTest(srv.URL)

	if _, err := d.apiRequest(context.Background(), http.MethodGet, pathList, nil, nil, nil); err != nil {
		t.Fatalf("限流后重试应成功，得到 %v", err)
	}
	if calls < 2 {
		t.Errorf("应至少请求 2 次（1 次限流 + 1 次重试），实际 %d", calls)
	}
}

// TestRetryExhaustionKeepsRateLimitCode 验证重试用尽后错误码仍是限流。
//
// 上层需要靠这个码决定"是否降低频率 / 熔断账号"，
// 如果降级成普通驱动错误，调度器会当成一次性失败继续硬撞。
func TestRetryExhaustionKeepsRateLimitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":429,"message":"操作过于频繁"}`))
	}))
	defer srv.Close()

	d := &Driver{}
	d.InitForTest(srv.URL)

	_, err := d.apiRequest(context.Background(), http.MethodGet, pathList, nil, nil, nil)
	if err == nil {
		t.Fatal("持续 429 应最终返回错误")
	}
	ae, ok := domain.AsAppError(err)
	if !ok {
		t.Fatalf("应为 *domain.AppError，得到 %T: %v", err, err)
	}
	if ae.Code != domain.CodeRateLimited {
		t.Errorf("重试用尽后错误码应仍为 CodeRateLimited，得到 %v", ae.Code)
	}
	// 错误信息应说明已重试，便于用户在日志里判断"是一直被限流还是偶发"。
	if got := ae.Message; got == "" {
		t.Error("错误信息不应为空")
	}
}

// TestRequestIntervalGateIsApplied 验证驱动会调用注入的间隔门。
//
// 这是 README 承诺"每账号请求间隔 300ms"的代码级验证：
// 之前 accounts.Manager 从未注入 gate，WaitRequestInterval 直接 return nil，
// 等于零节流——而请求间隔是降低账号风控概率的第一道闸。
func TestRequestIntervalGateIsApplied(t *testing.T) {
	var waits int
	gate := countingGate{waits: &waits}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"list":[]}}`))
	}))
	defer srv.Close()

	d := &Driver{}
	d.InitForTest(srv.URL)
	d.SetRequestIntervalGate(gate)

	if _, err := d.apiRequest(context.Background(), http.MethodGet, pathList, nil, nil, nil); err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if waits == 0 {
		t.Error("注入了 gate 后，驱动应调用 Wait 做间隔控制")
	}
}

// countingGate 记录 Wait 调用次数；interval 传 0 让测试不真的等待。
type countingGate struct{ waits *int }

func (g countingGate) Wait(ctx context.Context, interval time.Duration) error {
	*g.waits++
	return nil
}

var _ driver.RequestIntervalGate = countingGate{}
