// 限流重试：对齐 CASX drama_runtime_retry_*（3 次，1s→2s 指数退避 + 抖动，上限 8s）。
// 只对「限流」类错误重试，其余错误立即返回，避免对参数/权限错误做无意义重试。
package driver

import (
	"context"
	"log/slog"
	"math/rand"
	"strconv"
	"time"

	"pan-transfer/internal/domain"
)

// RetryPolicy 指数退避重试策略。
type RetryPolicy struct {
	Attempts  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
	Jitter    float64 // 0.2 = ±20%
}

// DefaultRateLimitRetry 与 CASX 默认值一致：3 次尝试，1s 起翻倍，上限 8s，20% 抖动。
var DefaultRateLimitRetry = RetryPolicy{
	Attempts:  3,
	BaseDelay: time.Second,
	MaxDelay:  8 * time.Second,
	Jitter:    0.2,
}

// RetryOnRateLimit 对限流类错误（CodeRateLimited）做指数退避重试。
// action 仅用于日志定位（如 "guangya list_share"）。
func RetryOnRateLimit(ctx context.Context, action string, fn func() error) error {
	return retryOnRateLimit(ctx, DefaultRateLimitRetry, action, fn)
}

func retryOnRateLimit(ctx context.Context, p RetryPolicy, action string, fn func() error) error {
	if p.Attempts <= 0 {
		p.Attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= p.Attempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		ae, ok := domain.AsAppError(err)
		if !ok || ae.Code != domain.CodeRateLimited {
			return err // 非限流错误不重试
		}
		if attempt == p.Attempts {
			break
		}
		delay := p.BaseDelay << (attempt - 1)
		if delay > p.MaxDelay || delay <= 0 {
			delay = p.MaxDelay
		}
		if p.Jitter > 0 {
			delay += time.Duration(float64(delay) * p.Jitter * (rand.Float64()*2 - 1))
		}
		slog.Warn("网盘限流，退避重试", "action", action, "attempt", attempt, "delay", delay.String(), "err", err.Error())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return &retryExhaustedError{err: lastErr, attempts: p.Attempts}
}

// retryExhaustedError 保留原错误码（限流），并在消息里标注已重试次数。
type retryExhaustedError struct {
	err      error
	attempts int
}

func (e *retryExhaustedError) Error() string {
	return e.err.Error() + "（已重试 " + strconv.Itoa(e.attempts) + " 次仍失败）"
}

func (e *retryExhaustedError) Unwrap() error { return e.err }
