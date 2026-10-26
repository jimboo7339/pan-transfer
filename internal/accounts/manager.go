// Package accounts 管理网盘账号到驱动实例的生命周期：
// 按账号缓存驱动实例、注入凭据、回写刷新后的 token/cookie，并提供扫码登录。
package accounts

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"pan-transfer/internal/driver"
	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
)

// Manager 持有账号驱动实例缓存。
type Manager struct {
	st *store.Store

	mu        sync.Mutex
	instances map[int64]driver.Driver

	qrMu      sync.Mutex
	qrDrivers map[string]driver.Driver // 扫码登录的临时实例：token -> driver
}

// New 构造 Manager。
func New(st *store.Store) *Manager {
	return &Manager{st: st, instances: map[int64]driver.Driver{}, qrDrivers: map[string]driver.Driver{}}
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

	acc, err := m.st.GetAccount(ctx, accountID)
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

// DropAccount 释放账号缓存实例（更新配置/删除账号后调用）。
func (m *Manager) DropAccount(ctx context.Context, accountID int64) {
	m.mu.Lock()
	drv, ok := m.instances[accountID]
	delete(m.instances, accountID)
	m.mu.Unlock()
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
func (m *Manager) mergeCredentials(ctx context.Context, acc *store.Account, creds domain.AuthCredentials) error {
	cfg := store.ParseAccountConfig(acc.Config)
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
	acc.Config = string(raw)
	return m.st.UpdateAccount(ctx, acc)
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
