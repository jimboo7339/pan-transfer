package accounts

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"pan-transfer/internal/driver"
)

// 认证刷新守卫：解决两个真实问题。
//
//  1. 并发 401 会各自触发一次真实刷新。光鸭的 refresh_token 是轮换的
//     （drivers/guangya/auth.go 刷新成功后回写新 token），多个刷新并行时
//     后完成的旧响应可能覆盖新 token，表现为「账号莫名失效、需要重新扫码」。
//     → 用 per-account 单飞把并发折叠成一次。
//
//  2. 刷新失败（网络抖动 / 上游 5xx）后立刻重试会继续打上游，放大风控面。
//     → 失败 outcome 决定冷却时长：retryable 短暂退避，fatal 长时间不再尝试。

// refreshCooldowns 是各刷新结局对应的冷却时长。
var refreshCooldowns = map[driver.RefreshOutcome]time.Duration{
	driver.RefreshRetryable: 30 * time.Second,
	driver.RefreshFatal:     10 * time.Minute,
}

type refreshEntry struct {
	mu     sync.Mutex // 单飞：同一账号同时只有一个刷新在跑
	coolAt time.Time  // 冷却截止时间，之前不再发起新刷新
}

// refreshGuard 返回绑定到指定账号的刷新守卫，供驱动 SetAuthRefreshGuard 注入。
func (m *Manager) refreshGuard(accountID int64) driver.AuthRefreshGuard {
	return func(ctx context.Context, fn func(context.Context) (driver.RefreshOutcome, error)) error {
		m.refreshMu.Lock()
		ent, ok := m.refreshes[accountID]
		if !ok {
			ent = &refreshEntry{}
			m.refreshes[accountID] = ent
		}
		m.refreshMu.Unlock()

		ent.mu.Lock()
		defer ent.mu.Unlock()

		if time.Now().Before(ent.coolAt) {
			// 仍在冷却窗口内：直接放行，让调用方沿用当前凭据去撞一次请求。
			// 不返回错误，避免把「暂时不该刷新」升级成一次任务失败。
			slog.Debug("认证刷新处于冷却窗口，跳过", "account", accountID, "cool_until", ent.coolAt.Format(time.RFC3339))
			return nil
		}

		outcome, err := fn(ctx)
		if err != nil {
			// err 已由驱动分类（RefreshOutcome）；冷却只在拿到结局后设置。
			if d, ok := refreshCooldowns[outcome]; ok {
				ent.coolAt = time.Now().Add(d)
				slog.Warn("认证刷新失败，进入冷却", "account", accountID, "outcome", outcome.String(), "cooldown", d.String(), "err", err.Error())
			}
			return err
		}
		ent.coolAt = time.Time{}
		return nil
	}
}

// dropRefreshState 清理账号删除后的刷新状态。
func (m *Manager) dropRefreshState(accountID int64) {
	m.refreshMu.Lock()
	delete(m.refreshes, accountID)
	m.refreshMu.Unlock()
}
