package driver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"pan-transfer/internal/domain"
)

// 限流错误应指数退避重试；成功即返回。
func TestRetryOnRateLimit_Success(t *testing.T) {
	policy := RetryPolicy{Attempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, Jitter: 0}
	calls := 0
	err := retryOnRateLimit(context.Background(), policy, "test", func() error {
		calls++
		if calls < 3 {
			return domain.Errorf(domain.CodeRateLimited, "操作过于频繁")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

// 非限流错误不重试。
func TestRetryOnRateLimit_NonRetryable(t *testing.T) {
	policy := RetryPolicy{Attempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	calls := 0
	err := retryOnRateLimit(context.Background(), policy, "test", func() error {
		calls++
		return domain.Errorf(domain.CodeValidation, "参数错误")
	})
	if err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (非限流不重试)", calls)
	}
}

// 重试耗尽后错误保留限流错误码并标注次数。
func TestRetryOnRateLimit_Exhausted(t *testing.T) {
	policy := RetryPolicy{Attempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	calls := 0
	err := retryOnRateLimit(context.Background(), policy, "test", func() error {
		calls++
		return domain.Errorf(domain.CodeRateLimited, "操作过于频繁")
	})
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	var ae *domain.AppError
	if !errors.As(err, &ae) || ae.Code != domain.CodeRateLimited {
		t.Errorf("err = %v, want CodeRateLimited", err)
	}
	if got := err.Error(); !strings.Contains(got, "已重试 3 次") {
		t.Errorf("err message = %q, want 包含重试次数", got)
	}
}

// ctx 取消时立即返回。
func TestRetryOnRateLimit_Cancel(t *testing.T) {
	policy := RetryPolicy{Attempts: 5, BaseDelay: time.Hour, MaxDelay: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := retryOnRateLimit(ctx, policy, "test", func() error {
		return domain.Errorf(domain.CodeRateLimited, "操作过于频繁")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
