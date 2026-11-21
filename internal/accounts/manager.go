// Package accounts 管理网盘账号到驱动实例的生命周期：
// 按账号缓存驱动实例、注入凭据、回写刷新后的 token/cookie，并提供扫码登录。
package accounts

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"pan-transfer/internal/driver"
	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
)

// DefaultRequestIntervalMS 是账号级 API 请求的最小间隔（毫秒）。
// 与 CASX / quark-auto-save 的保守默认一致：逐文件遍历是触发平台风控的主因，
// 请求之间留出间隔能显著降低概率。可在「设置 → 转存策略」中调整。
const DefaultRequestIntervalMS = 300

// Manager 持有账号驱动实例缓存。
type Manager struct {
	// stMu 保护 st 的并发读。
	//
	// 写侧只有 Reload（还原备份时调用，从 HTTP handler goroutine）。
	// 读侧是每次请求：RequestInterval / instance / mergeCredentials。
	// 早先 Reload 在 m.mu 内写 st，而所有读点都不持锁 ——
	// 属于"写加锁、读不加锁"的经典错误，锁形同虚设。
	stMu sync.RWMutex
	st   *store.Store

	mu        sync.Mutex
	instances map[int64]driver.Driver

	// delays 是账号级请求间隔门（令牌桶）。驱动在发起平台 API 前调用 Wait，
	// 同一 accountID 的任意两个请求都不会短于设定间隔。
	delays *driver.DelayController

	// refreshMu 保护 refreshes；refreshes 提供 per-account 的刷新单飞与冷却。
	refreshMu sync.Mutex
	refreshes map[int64]*refreshEntry

	qrMu      sync.Mutex
	qrDrivers map[string]driver.Driver // 扫码登录的临时实例：token -> driver
}

// New 构造 Manager。
func New(st *store.Store) *Manager {
	return &Manager{
		st:        st,
		instances: map[int64]driver.Driver{},
		delays:    driver.NewDelayController(),
		refreshes: map[int64]*refreshEntry{},
		qrDrivers: map[string]driver.Driver{},
	}
}

// store 返回当前存储句柄（持读锁取快照）。
//
// 所有读库路径必须通过它，而不是直接读 m.st：
// 后者与 Reload 的写并发，且可能指向已 Close 的旧库。
func (m *Manager) store() *store.Store {
	m.stMu.RLock()
	st := m.st
	m.stMu.RUnlock()
	return st
}

// RequestInterval 返回当前配置的账号级请求间隔。
// 设置项 request_interval_ms 为空或非法时回落到 DefaultRequestIntervalMS。
func (m *Manager) RequestInterval(ctx context.Context) time.Duration {
	ms := DefaultRequestIntervalMS
	if st := m.store(); st != nil {
		if v, err := st.GetSetting(ctx, "request_interval_ms"); err == nil {
			if n, e := strconv.Atoi(strings.TrimSpace(v)); e == nil && n >= 0 {
				ms = n
			}
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// Executor 让 Manager 可直接作为 transfer.Service 的 Executor 使用。
func (m *Manager) Run(ctx context.Context, accountID int64, fn func(driver.Driver) error) error {
	drv, err := m.instance(ctx, accountID)
	if err != nil {
		return err
	}
	return fn(drv)
}

// instance 取（或创建并缓存）账号驱动实例。
func (m *Manager) instance(ctx context.Context, accountID int64) (driver.Driver, error) {
	m.mu.Lock()
	if drv, ok := m.instances[accountID]; ok {
		m.mu.Unlock()
		return drv, nil
	}
	m.mu.Unlock()

	st := m.store()
	if st == nil {
		return nil, domain.Errorf(domain.CodeInternal, "存储尚未初始化")
	}
	acc, err := st.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	drv, err := m.build(ctx, acc)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	// 双检：并发下可能已有人建好；保留先到者。
	if exist, ok := m.instances[accountID]; ok {
		m.mu.Unlock()
		_ = drv.Drop(ctx)
		return exist, nil
	}
	m.instances[accountID] = drv
	m.mu.Unlock()
	return drv, nil
}

// build 构造并初始化一个账号驱动实例（未入缓存）。
func (m *Manager) build(ctx context.Context, acc *store.Account) (driver.Driver, error) {
	drv, ok := driver.New(acc.DriverType)
	if !ok {
		return nil, domain.Errorf(domain.CodeValidation, "未知驱动类型：%s", acc.DriverType)
	}
	if err := applyConfigJSON(drv, acc.Config); err != nil {
		_ = drv.Drop(ctx)
		return nil, err
	}
	// 注入账号级请求间隔门：同一账号的所有平台请求串行且保持最小间隔，
	// 这是降低平台风控概率的第一道闸（逐文件遍历是风控主因）。
	if rc, ok := drv.(driver.RequestIntervalConsumer); ok {
		rc.SetRequestIntervalGate(m.delays.Gate(acc.ID))
	}
	// 注入认证刷新守卫：并发 401 只会触发一次真实刷新，其余等待复用结果。
	// 光鸭 refresh_token 是轮换的，缺少单飞会让旧响应覆盖新 token 导致账号莫名失效。
	if ac, ok := drv.(driver.AuthRefreshConsumer); ok {
		ac.SetAuthRefreshGuard(m.refreshGuard(acc.ID))
	}
	// 运行期 token/cookie 变化回写账号配置。
	if pc, ok := drv.(driver.AuthPersistConsumer); ok {
		pc.SetAuthPersister(func(ctx context.Context, creds domain.AuthCredentials) error {
			return m.mergeCredentials(ctx, acc, creds)
		})
	}
	if err := drv.Init(ctx); err != nil {
		_ = drv.Drop(ctx)
		return nil, err
	}
	return drv, nil
}

// Reload 在底层存储被替换后重置账号侧状态（用于从备份还原）。
//
// 还原会换掉 SQLite 文件并重开连接，旧 *store.Store 指向已关闭的库。
// 这里换掉引用并清空所有缓存的驱动实例——它们持有旧的账号快照与凭据，
// 留着会让任务继续用还原前的凭据打网盘，行为完全不可预期。
func (m *Manager) Reload(st *store.Store) {
	m.mu.Lock()
	old := m.instances
	m.instances = map[int64]driver.Driver{}
	m.mu.Unlock()

	// st 的写必须在 stMu 内，不能用 m.mu 代替：
	// 读侧（RequestInterval / instance / mergeCredentials）持的是 stMu，
	// 写侧若换成另一把锁，两边就不互斥了。
	m.stMu.Lock()
	m.st = st
	m.stMu.Unlock()

	ctx := context.Background()
	for id, drv := range old {
		m.delays.DropAccount(id)
		m.dropRefreshState(id)
		_ = drv.Drop(ctx)
	}
}

// DropAccount 释放账号缓存实例（更新配置/删除账号后调用）。
func (m *Manager) DropAccount(ctx context.Context, accountID int64) {
	m.mu.Lock()
	drv, ok := m.instances[accountID]
	delete(m.instances, accountID)
	m.mu.Unlock()
	// 同步清理间隔门与刷新守卫的按账号状态，避免 map 无界增长。
	m.delays.DropAccount(accountID)
	m.dropRefreshState(accountID)
	if ok {
		_ = drv.Drop(ctx)
	}
}

// Probe 检查账号连通性。
func (m *Manager) Probe(ctx context.Context, accountID int64) error {
	return m.Run(ctx, accountID, func(drv driver.Driver) error {
		return drv.Ping(ctx)
	})
}

// mergeCredentials 把驱动回传的凭据合并进账号配置 JSON 并落库。
//
// 并发说明：acc 是 build 时从 GetAccount 拿到的共享对象，
// 同一账号的驱动实例会被并发使用（运行中的 job + 同时的 Probe / 文件列表）。
// 若两个 token 刷新同时 read-modify-write acc.Config，会丢失更新——
// 一个刷新的新 token 被另一个覆盖，正是 refresh_guard 想避免的
// "账号莫名失效"。因此这里在锁内完成"读库→合并→写库"，
// 保证同一账号的凭据回写串行化。
func (m *Manager) mergeCredentials(ctx context.Context, acc *store.Account, creds domain.AuthCredentials) error {
	m.stMu.Lock()
	defer m.stMu.Unlock()

	st := m.st
	if st == nil {
		return domain.Errorf(domain.CodeInternal, "存储尚未初始化")
	}
	// 从库里重新读当前配置，而不是用传进来的 acc.Config：
	// 后者可能是几分钟前 build 时的快照，中间可能已被别的刷新写过。
	latest, err := st.GetAccount(ctx, acc.ID)
	if err != nil {
		return err
	}
	cfg := store.ParseAccountConfig(latest.Config)
	if c := strings.TrimSpace(creds.Cookie); c != "" {
		cfg["cookie"] = c
	}
	if t := strings.TrimSpace(creds.AccessToken); t != "" {
		cfg["access_token"] = t
	}
	if t := strings.TrimSpace(creds.RefreshToken); t != "" {
		cfg["refresh_token"] = t
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	latest.Config = string(raw)
	return st.UpdateAccount(ctx, latest)
}

// DriverInfoOut 是前端驱动选择所需的元信息（含 Addition 表单 schema）。
type DriverInfoOut struct {
	Name        string               `json:"name"`
	DisplayName string               `json:"display_name"`
	Description string               `json:"description"`
	CardTags    []string             `json:"card_tags"`
	CardColor   string               `json:"card_color"`
	AuthLabel   string               `json:"auth_label"`
	AuthType    string               `json:"auth_type"`
	Fields      []driver.FieldSchema `json:"fields"`
	QRLogin     bool                 `json:"qr_login"`
}

// ListDriverTypes 列出注册的驱动类型。
func (m *Manager) ListDriverTypes() []DriverInfoOut {
	infos := driver.List()
	out := make([]DriverInfoOut, 0, len(infos))
	for _, in := range infos {
		out = append(out, DriverInfoOut{
			Name:        in.Name,
			DisplayName: in.DisplayName,
			Description: in.Description,
			CardTags:    in.CardTags,
			CardColor:   in.CardColor,
			AuthLabel:   in.AuthLabel,
			AuthType:    in.AuthType,
			Fields:      in.Fields,
			QRLogin:     in.SupportsQRLogin,
		})
	}
	return out
}

// StartQRLogin 开始扫码登录：driverType 必填；configJSON 可带非认证字段（如 client_id）。
// 返回不透明轮询 token。
func (m *Manager) StartQRLogin(ctx context.Context, driverType, configJSON string) (*driver.QRStartResult, error) {
	drv, release, err := driver.OpenEphemeral(ctx, driverType, configJSON, driver.EphemeralConfig{})
	if err != nil {
		return nil, err
	}
	qp, ok := drv.(driver.QRLoginProvider)
	if !ok {
		release(ctx)
		return nil, domain.Errorf(domain.CodeNotImplement, "驱动 %s 不支持扫码登录", driverType)
	}
	res, err := qp.StartQRLogin(ctx)
	if err != nil {
		release(ctx)
		return nil, err
	}
	m.qrMu.Lock()
	m.qrDrivers[res.Token] = drv
	m.qrMu.Unlock()
	return res, nil
}

// PollQRPollResult 是对外的扫码轮询结果：成功时附带合并后的账号配置 JSON。
type PollQRResult struct {
	Status     driver.QRStatus `json:"status"`
	Message    string          `json:"message"`
	ConfigJSON string          `json:"config_json,omitempty"`
}

// PollQRLogin 轮询扫码登录结果；Success 时返回可直接保存的账号配置。
func (m *Manager) PollQRLogin(ctx context.Context, token string) (*PollQRResult, error) {
	m.qrMu.Lock()
	drv, ok := m.qrDrivers[token]
	m.qrMu.Unlock()
	if !ok {
		return nil, domain.Errorf(domain.CodeNotFound, "扫码会话不存在或已结束")
	}
	qp, ok := drv.(driver.QRLoginProvider)
	if !ok {
		return nil, domain.Errorf(domain.CodeNotImplement, "驱动不支持扫码登录")
	}
	res, err := qp.PollQRLogin(ctx, token)
	if err != nil {
		return nil, err
	}
	out := &PollQRResult{Status: res.Status, Message: res.Message}
	switch res.Status {
	case driver.QRSuccess, driver.QRFailed, driver.QRExpired:
		// 会话结束：取出实例并清理。
		m.qrMu.Lock()
		delete(m.qrDrivers, token)
		m.qrMu.Unlock()
		defer func() { _ = drv.Drop(ctx) }()
	}
	if res.Status == driver.QRSuccess {
		cfg := map[string]string{}
		// 把 Fields（驱动要求回填的 Addition 字段）合并进配置。
		for k, v := range res.Fields {
			cfg[k] = v
		}
		// Addition 本身可能已带字段（如 client_id / device_id）。
		if raw, err := json.Marshal(drv.GetAddition()); err == nil {
			extra := map[string]any{}
			if json.Unmarshal(raw, &extra) == nil {
				for k, v := range extra {
					if s, ok := v.(string); ok && s != "" {
						cfg[k] = s
					}
				}
			}
		}
		// 注入运行凭据（cookie / token）。
		if c := strings.TrimSpace(res.Credentials.Cookie); c != "" {
			cfg["cookie"] = c
		}
		if t := strings.TrimSpace(res.Credentials.AccessToken); t != "" {
			cfg["access_token"] = t
		}
		if t := strings.TrimSpace(res.Credentials.RefreshToken); t != "" {
			cfg["refresh_token"] = t
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return nil, err
		}
		out.ConfigJSON = string(raw)
	}
	return out, nil
}

// applyConfigJSON 把账号配置 JSON 反序列化进驱动 Addition。
func applyConfigJSON(drv driver.Driver, configJSON string) error {
	raw := strings.TrimSpace(configJSON)
	if raw == "" || raw == "{}" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), drv.GetAddition()); err != nil {
		return domain.Errorf(domain.CodeValidation, "驱动配置解析失败：%v", err)
	}
	return nil
}
