// Package httpapi 提供 pan-transfer 的 REST API 与静态资源托管。
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"pan-transfer/internal/accounts"
	"pan-transfer/internal/domain"
	"pan-transfer/internal/driver"
	"pan-transfer/internal/notify"
	"pan-transfer/internal/secret"
	"pan-transfer/internal/store"
	"pan-transfer/internal/transfer"
)

// Deps 是 API 层依赖。
type Deps struct {
	// Store 是数据库句柄的**地址**，而不是句柄本身。
	//
	// 为什么是指针的指针：从备份还原时必须换掉整个 *store.Store（换文件 →
	// 关库 → 重开），如果 Deps 里存的是值，所有已注册的 handler 会继续用
	// 已关闭的旧句柄。存地址后，handler 每次请求都通过 currentStore() 取
	// 当前句柄，还原完成后无需重启即可继续服务。
	Store **store.Store

	Accounts *accounts.Manager
	Transfer *transfer.Service
	Notify   *notify.Sender
	Log      *slog.Logger
	Version  string

	// AllowedOrigins 是反向代理场景下的额外可信 Origin（逗号分隔）。
	// 为空时只接受与请求 Host 同源的 Origin。
	AllowedOrigins string

	// ForceSecureCookie 为 "1" 时无条件给会话 Cookie 加 Secure，
	// 用于「TLS 在反代终止、应用只看到 HTTP」的部署。
	ForceSecureCookie string

	// DataDir 是 SQLite 所在目录，备份文件落在 DataDir/backups。
	DataDir string

	// DBPath 是当前数据库的完整路径（还原时替换它）。
	DBPath string

	// Restore 从备份还原数据库，由 main 装配（需要管理 Store/Manager/调度器
	// 三者的生命周期）。为 nil 时还原接口返回「当前构建未提供还原能力」。
	Restore func(backupPath string) error
}

// currentStore 返回当前数据库句柄；Store 未注入时返回 nil。
func (s *Server) currentStore() *store.Store {
	if s.deps.Store == nil {
		return nil
	}
	return *s.deps.Store
}

// Server 是 HTTP API 服务。
type Server struct {
	deps Deps
	mux  *http.ServeMux

	secret     string // 会话签名密钥
	secretOnce sync.Once

	// versionMu 保护 sessionVer（会话签名里混入的版本号，用于吊销全部会话）。
	versionMu  sync.RWMutex
	sessionVer string

	// logins 对登录尝试做限流，防止短口令被暴力破解。
	logins *loginLimiter

	// metrics 是进程内指标，由 Handler() 中间件填充、/metrics 暴露。
	metrics *metrics

	// handlerOnce 保证 Handler() 只装配一次。
	// 不这样做的话，每次调用 Handler() 都会重复注册 /metrics 并 panic
	// （测试中每个请求都会调 Handler()，生产里 main 也会调一次）。
	handlerOnce sync.Once
	handler     http.Handler

	// depsMu 保护 deps（Transfer 在装配期回填，其余字段只读）。
	depsMu sync.RWMutex
}

// NewServer 构造 API 服务（含路由注册）。
func NewServer(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux(), logins: newLoginLimiter(), metrics: newMetrics()}
	s.routes()
	// /metrics 不需要鉴权（只暴露计数与耗时，不含业务数据或路径参数），
	// 与 /api/health 一样放在 wrap 之外。在这里注册而非 Handler()，
	// 使 Handler() 可以被重复调用而无需额外同步。
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	return s
}

// Handler 返回根 Handler。
//
// 包一层指标中间件：成功请求此前完全没有日志，也无法回答
// "这个慢请求是谁发的""哪个账号在持续限流"。
func (s *Server) Handler() http.Handler {
	s.handlerOnce.Do(func() {
		s.handler = httpInstrumentation(s.mux, s.metrics, s.deps.Log)
	})
	return s.handler
}

// SetTransfer 回填 Transfer 依赖。
//
// 为什么需要它：Service 的构造需要 Metrics（指标桥接器），
// 而桥接器住在 httpapi。于是装配顺序变成「先 API server（拿到 sink）→
// 再 Service → 回填」，这个 setter 就是回填的那一步。
// 在 SetTransfer 之前调 Transfer 相关接口会 panic，因此只在 main 装配期使用。
func (s *Server) SetTransfer(svc *transfer.Service) {
	s.depsMu.Lock()
	s.deps.Transfer = svc
	s.depsMu.Unlock()
}

// transfer 返回当前 Service（每次请求都读，避免还原后用到旧实例）。
func (s *Server) transfer() *transfer.Service {
	s.depsMu.Lock()
	svc := s.deps.Transfer
	s.depsMu.Unlock()
	return svc
}

// 存在的意义：transfer 是领域层，不能直接 import httpapi 拿 metrics；
// 通过 main 装配把 sink 交给它，依赖方向保持 httpapi → transfer 单向。
func (s *Server) MetricsSink() transfer.MetricsSink {
	return newMetricsSink(s.metrics)
}

// handleMetrics 以 Prometheus 文本格式输出指标。
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(s.metrics.render()))
}

func (s *Server) routes() {
	m := s.mux
	s.registerCategoryRoutes()

	// 认证
	//
	// login / logout 都不套 wrap（未登录时也要能访问），但各自内部做校验：
	//   - login：限流 + 密码校验（见 handleLogin）；
	//   - logout：必须校验 Origin。早先 handleLogout 完全裸奔，
	//     任何人（无需登录）POST 一次就会 bumpSessionVersion，
	//     把用户所有已签发 token 全部作废 —— 一个未鉴权的状态变更端点，
//     还能被任意网页通过 form POST 触发（CSRF）。
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/logout", s.handleLogout)
	m.HandleFunc("GET /api/auth/status", s.handleAuthStatus)

	// 驱动与账号
	m.HandleFunc("GET /api/drivers", s.wrap(s.handleDrivers))
	m.HandleFunc("GET /api/accounts", s.wrap(s.handleAccountList))
	m.HandleFunc("POST /api/accounts", s.wrap(s.handleAccountCreate))
	m.HandleFunc("PUT /api/accounts/{id}", s.wrap(s.handleAccountUpdate))
	m.HandleFunc("DELETE /api/accounts/{id}", s.wrap(s.handleAccountDelete))
	m.HandleFunc("POST /api/accounts/{id}/probe", s.wrap(s.handleAccountProbe))
	m.HandleFunc("GET /api/accounts/{id}/profile", s.wrap(s.handleAccountProfile))
	m.HandleFunc("POST /api/accounts/qr/start", s.wrap(s.handleQRStart))
	m.HandleFunc("POST /api/accounts/qr/poll", s.wrap(s.handleQRPoll))

	// 分享预览与文件浏览
	m.HandleFunc("POST /api/share/preview", s.wrap(s.handleSharePreview))
	m.HandleFunc("GET /api/files/list", s.wrap(s.handleFileList))
	m.HandleFunc("GET /api/files/resolve", s.wrap(s.handleFileResolve))
	m.HandleFunc("POST /api/files/mkdir", s.wrap(s.handleFileMkdir))

	// 任务
	m.HandleFunc("GET /api/tasks", s.wrap(s.handleTaskList))
	m.HandleFunc("POST /api/tasks", s.wrap(s.handleTaskCreate))
	m.HandleFunc("POST /api/tasks/batch", s.wrap(s.handleTaskBatch))
	m.HandleFunc("PUT /api/tasks/{id}", s.wrap(s.handleTaskUpdate))
	m.HandleFunc("DELETE /api/tasks/{id}", s.wrap(s.handleTaskDelete))
	m.HandleFunc("POST /api/tasks/{id}/status", s.wrap(s.handleTaskStatus))
	m.HandleFunc("POST /api/tasks/{id}/run", s.wrap(s.handleTaskRun))
	m.HandleFunc("GET /api/tasks/{id}/runs", s.wrap(s.handleTaskRuns))
	m.HandleFunc("GET /api/tasks/{id}/saved-names", s.wrap(s.handleTaskSavedNames))
	m.HandleFunc("GET /api/tasks/{id}/saved-names/count", s.wrap(s.handleTaskSavedCount))
	m.HandleFunc("DELETE /api/tasks/{id}/saved-names", s.wrap(s.handleTaskSavedClear))

	// 异步运行句柄（进度 / 取消）
	m.HandleFunc("GET /api/jobs", s.wrap(s.handleJobList))
	m.HandleFunc("GET /api/jobs/{id}", s.wrap(s.handleJobGet))
	m.HandleFunc("POST /api/jobs/{id}/cancel", s.wrap(s.handleJobCancel))
	m.HandleFunc("DELETE /api/jobs/{id}", s.wrap(s.handleJobDelete))
	// 批量查进度：任务列表页一次要显示多个 job 的状态，
	// 一个个 GET 会让浏览器发 N 个请求（SQLite 单连接下还会排队）。
	m.HandleFunc("POST /api/jobs/batch", s.wrap(s.handleJobsBatch))

	// 命名正则规则
	m.HandleFunc("GET /api/rules", s.wrap(s.handleRuleList))
	m.HandleFunc("POST /api/rules", s.wrap(s.handleRuleSave))
	m.HandleFunc("DELETE /api/rules/{key}", s.wrap(s.handleRuleDelete))
	m.HandleFunc("POST /api/rules/test", s.wrap(s.handleRuleTest))

	// 调度辅助
	m.HandleFunc("GET /api/cron/next", s.wrap(s.handleCronNext))

	// 仪表盘（聚合总览，一次请求回答"系统健康吗"）
	m.HandleFunc("GET /api/dashboard", s.wrap(s.handleDashboard))

	// 全局搜索（跨任务/账号/规则/分类/运行记录/转存文件）
	m.HandleFunc("GET /api/search", s.wrap(s.handleSearch))

	// 备份与还原
	s.registerBackupRoutes()

	// 配置导入 / 导出（不含凭据）
	m.HandleFunc("GET /api/config/export", s.wrap(s.handleConfigExport))
	m.HandleFunc("POST /api/config/import/preview", s.wrap(s.handleConfigImportPreview))
	m.HandleFunc("POST /api/config/import", s.wrap(s.handleConfigImport))

	// 设置与通知
	m.HandleFunc("GET /api/settings", s.wrap(s.handleSettingsGet))
	m.HandleFunc("PUT /api/settings", s.wrap(s.handleSettingsPut))
	m.HandleFunc("POST /api/notify/test", s.wrap(s.handleNotifyTest))

	// 服务信息
	m.HandleFunc("GET /api/health", s.handleHealth)
	m.HandleFunc("GET /api/health/ready", s.handleHealthReady)
}

// handleHealthReady 是深度健康检查。
//
// /api/health 只证明进程能响应（Docker HEALTHCHECK 用它），但进程活着不等于
// 功能可用：DB 锁死、调度协程挂掉、账号全部失效都会让"健康"的容器实际不干活。
// 这里逐项探测依赖，供反代/编排系统做就绪判断，也供用户在设置页自查。
func (s *Server) handleHealthReady(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	checks := map[string]string{} // name -> "ok" | "error"
	details := map[string]string{}
	ok := true

	// 1. 数据库可读写（一条最小查询就够，避免引入额外依赖）。
	if st := s.currentStore(); st == nil {
		checks["database"] = "error"
		details["database"] = "存储未初始化"
		ok = false
	} else if _, err := st.GetSetting(ctx, "app_secret"); err != nil {
		checks["database"] = "error"
		details["database"] = err.Error()
		ok = false
	} else {
		checks["database"] = "ok"
	}

	// 2. 调度器是否在跑（正在执行任务数 / 最近扫描时间）。
	if svc := s.transfer(); svc != nil {
		stats := svc.Stats(ctx)
		checks["scheduler"] = "ok"
		details["running_tasks"] = fmt.Sprintf("%d", len(stats.RunningTaskIDs))
		if stats.LastSweepAt != "" {
			details["last_sweep_at"] = stats.LastSweepAt
		} else {
			// 尚未完成第一次扫描（启动后 30 秒内）。这是正常状态，
			// 不能报成 disabled —— 否则用户会以为调度被关了。
			details["last_sweep_at"] = "等待首次扫描（启动后 30 秒内）"
		}
		if stats.SweepsCompleted && !stats.SchedulingEnabled {
			// 调度被用户显式关闭，不是故障，但要如实反映。
			checks["scheduler"] = "disabled"
		}
	} else {
		checks["scheduler"] = "error"
		details["scheduler"] = "调度器未初始化"
		ok = false
	}

	// 3. 账号是否可用（有账号全部失效时给出可操作的结论）。
	if s.deps.Accounts != nil {
		if list, err := s.currentStore().ListAccounts(ctx); err == nil {
			total, active := len(list), 0
			for _, a := range list {
				if a.IsActive {
					active++
				}
			}
			details["accounts"] = fmt.Sprintf("%d/%d", active, total)
			checks["accounts"] = "ok"
			if total > 0 && active == 0 {
				// 全部停用不算"服务故障"，但用户八成是忘了开。
				checks["accounts"] = "warn"
			}
		}
	}

	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{
		"ok":      ok,
		"checks":  checks,
		"details": details,
		"version": s.deps.Version,
		"time":    time.Now().Format(time.RFC3339),
	})
}

// ---------- 基础设施 ----------

type apiError struct {
	Code    domain.ErrorCode `json:"code"`
	Message string           `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 把错误映射成 HTTP 响应。
//
// 关键点：只有 *domain.AppError 的 Message 才是给用户看的文案。
// 其它 error（SQL 报错、上游 URL、文件路径）必须替换为通用文案并记日志——
// 早先的实现把 err.Error() 原文回给客户端，等于把库结构、上游接口地址和
// 服务器目录结构一起公开了。
func writeErr(w http.ResponseWriter, err error) {
	var ae *domain.AppError
	if errors.As(err, &ae) {
		status := http.StatusInternalServerError
		switch ae.Code {
		case domain.CodeValidation:
			status = http.StatusBadRequest
		case domain.CodeNotFound:
			status = http.StatusNotFound
		case domain.CodeAuthExpired, domain.CodePermissionDenied:
			status = http.StatusUnauthorized
		case domain.CodeRateLimited:
			status = http.StatusTooManyRequests
		}
		writeJSON(w, status, map[string]apiError{"error": {Code: ae.Code, Message: ae.Message}})
		return
	}
	// 非领域错误：细节只进日志，不进响应。
	// 必须记日志：否则"500 + 通用文案"会让排障变成猜谜。
	// 这里用 slog 的全局 logger 而不是 Server.deps：writeErr 是包级函数，
	// 登录失败等路径也会走到，而那些路径不持有 Server 实例。
	slog.Error("未预期的服务端错误", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]apiError{"error": {
		Code:    domain.CodeInternal,
		Message: "服务器内部错误，请查看运行日志获取详细信息",
	}})
}

// wrap 统一鉴权 + 错误处理 + 错误计数。handler 返回错误时写 JSON 错误响应。
func (s *Server) wrap(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.checkAuth(r); err != nil {
			s.countAPIError(err)
			writeErr(w, err)
			return
		}
		if err := fn(w, r); err != nil {
			s.deps.Log.Warn("API 错误", "path", r.URL.Path, "err", err)
			s.countAPIError(err)
			writeErr(w, err)
		}
	}
}

// countAPIError 按错误码累加指标，便于定位"哪个错误在涨"。
func (s *Server) countAPIError(err error) {
	var ae *domain.AppError
	if errors.As(err, &ae) {
		s.metrics.incError(string(ae.Code))
		return
	}
	s.metrics.incError(string(domain.CodeInternal))
}

type stdBody struct {
	Ok   bool `json:"ok"`
	Data any  `json:"data,omitempty"`
}

func respond(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, stdBody{Ok: true, Data: data})
}

// maxRequestBodyBytes 是单个请求体的上限。
//
// 为什么需要：decode() 直接 json.NewDecoder(r.Body).Decode(v)，
// 没有 MaxBytesReader 的话，POST /api/config/import、
// POST /api/tasks/batch（500 个 ID 只是数组长度，不是字节数）、
// POST /api/notify/test 都能读入任意大小的 body。
// 更要紧的是 /api/auth/login 也走 decode，而它**未鉴权**——
// 可以在密码校验之前就把内存打满。
const maxRequestBodyBytes = 4 << 20 // 4 MiB

// decode 解析 JSON 请求体，带大小上限。
func decode(r *http.Request, v any) error {
	defer func() { _ = r.Body.Close() }()
	// MaxBytesReader 在超过上限时让 Decode 返回错误，
	// 而不是默默读完全部内容。
	r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return domain.Errorf(domain.CodeValidation, "请求体过大（上限 %d MiB）", maxRequestBodyBytes>>20)
		}
		return domain.Errorf(domain.CodeValidation, "请求体解析失败：%v", err)
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.Errorf(domain.CodeValidation, "非法 ID：%s", raw)
	}
	return id, nil
}

// ---------- 认证 ----------

// secretFields 是账号配置里不得下发给浏览器的凭据字段。
// 这些值一旦进入前端内存 / 浏览器扩展 / 前端错误日志，等于账号被接管
// （夸克、光鸭都是逆向 Web API，cookie 就是完整会话）。
var secretFields = map[string]bool{
	"cookie":        true,
	"access_token":  true,
	"refresh_token": true,
	"password":      true,
}

// RedactedSecret 是凭据字段在 API 响应中的占位值。
// 前端把它原样回传即表示「不修改该凭据」，服务端会保留原值。
const RedactedSecret = "__PT_REDACTED__"

// redactAccount 返回可安全下发给浏览器的账号视图：凭据字段替换为占位符。
//
// 只有 secretFields 里的字段被替换；其余字段（root_folder_id、delete_mode、
// download_mode 等）必须原样下发，否则用户在编辑账号时改不动任何配置。
func redactAccount(a *store.Account) map[string]any {
	cfg := map[string]string{}
	if strings.TrimSpace(a.Config) != "" {
		_ = json.Unmarshal([]byte(a.Config), &cfg)
	}
	has := map[string]bool{}
	for k, v := range cfg {
		if secretFields[k] {
			if strings.TrimSpace(v) != "" {
				has[k] = true
			}
			cfg[k] = RedactedSecret
		}
	}
	return map[string]any{
		"id":          a.ID,
		"name":        a.Name,
		"driver_type": a.DriverType,
		"config":      marshalString(cfg),
		"has_secret":  has,
		"is_active":   a.IsActive,
		"sort_order":  a.SortOrder,
		"created_at":  a.CreatedAt,
		"updated_at":  a.UpdatedAt,
	}
}

// restoreSecrets 把前端回传的占位符替换回数据库中的真实凭据。
// 未携带占位符的字段按用户的新输入处理。
func restoreSecrets(incoming, stored string) string {
	cfg := map[string]string{}
	_ = json.Unmarshal([]byte(incoming), &cfg)
	old := map[string]string{}
	if strings.TrimSpace(stored) != "" {
		_ = json.Unmarshal([]byte(stored), &old)
	}
	changed := false
	for k, v := range cfg {
		if v != RedactedSecret {
			continue
		}
		if real, ok := old[k]; ok {
			cfg[k] = real
		} else {
			delete(cfg, k)
		}
		changed = true
	}
	if !changed {
		return incoming
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return incoming
	}
	return string(out)
}

func marshalString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (s *Server) secretKey() string {
	s.secretOnce.Do(func() {
		ctx := context.Background()
		v, _ := s.currentStore().GetSetting(ctx, "app_secret")
		if v == "" {
			buf := make([]byte, 32)
			_, _ = rand.Read(buf)
			v = hex.EncodeToString(buf)
			_ = s.currentStore().SetSetting(ctx, "app_secret", v)
		}
		s.secret = v
	})
	return s.secret
}

func (s *Server) authEnabled(ctx context.Context) bool {
	v, _ := s.currentStore().GetSetting(ctx, "auth_password")
	return strings.TrimSpace(v) != ""
}

// sessionCtx is declared in auth.go; signing there uses it.

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	required := s.authEnabled(ctx)
	token := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	} else if c, err := r.Cookie(cookieName); err == nil {
		token = c.Value
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_required": required,
		"authed":        !required || (token != "" && s.verifySession(ctx, token)),
		"version":       s.deps.Version,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	ctx := r.Context()
	stored, _ := s.currentStore().GetSetting(ctx, "auth_password")
	if strings.TrimSpace(stored) == "" {
		respond(w, map[string]string{"token": ""}) // 未启用鉴权
		return
	}

	// 暴力破解防护：NAS 上 4 位数字口令很常见，必须限流而不只是"慢一点"。
	now := time.Now()
	// clientKey 现在是 Server 的方法（要读 PAN_TRANSFER_TRUST_PROXY），
	// 不再是包级函数。
	key := s.clientKey(r)
	if !s.logins.allow(key, now) {
		w.Header().Set("Retry-After", "300")
		writeErr(w, domain.Errorf(domain.CodeRateLimited, "登录失败次数过多，请 5 分钟后再试"))
		return
	}
	if !verifyPassword(stored, in.Password) {
		s.logins.recordFailure(key, now)
		writeErr(w, domain.Errorf(domain.CodeAuthExpired, "密码错误"))
		return
	}
	s.logins.reset(key)

	// 旧的无盐 SHA-256 口令在登录成功后顺带升级成 Argon2id，用户无感。
	if secret.NeedsRehash(stored) {
		if h := s.hashPassword(in.Password); h != "" {
			if err := s.currentStore().SetSetting(ctx, "auth_password", h); err != nil {
				s.deps.Log.Warn("密码哈希升级失败", "err", err)
			}
		}
	}

	expiry := now.Add(sessionTTL).Unix()
	token := s.signSession(expiry)
	s.setSessionCookie(w, r, token)
	respond(w, map[string]string{"token": token})
}

// handleLogout revokes the caller's session.
//
// 会话是无状态的，单纯清 Cookie 会让已签发的 token 继续有效 30 天。
// 这里递增会话版本，作废此前签发的全部 token——单人面板可以接受，
// 而且这是让「怀疑密码泄露」真正可执行的唯一办法。
//
// 安全：这个端点不套 wrap（未登录也要能登出），但**必须校验 Origin**。
// 早先它完全裸奔：任何人（无需登录）POST 一次就能 bumpSessionVersion，
// 把用户所有已签发 token 全部作废，而且能被任意网页通过 form POST 触发。
// 校验 Origin 后，跨站请求会被拒绝（浏览器会自动带上 Origin，
// 而攻击者的页面无法伪造）。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		writeErr(w, domain.Errorf(domain.CodePermissionDenied, "跨站请求被拒绝"))
		return
	}
	s.clearSessionCookie(w)
	s.bumpSessionVersion(r.Context())
	respond(w, map[string]bool{"ok": true})
}

// ---------- 驱动与账号 ----------

func (s *Server) handleDrivers(w http.ResponseWriter, _ *http.Request) error {
	respond(w, s.deps.Accounts.ListDriverTypes())
	return nil
}

func (s *Server) handleAccountList(w http.ResponseWriter, r *http.Request) error {
	list, err := s.currentStore().ListAccounts(r.Context())
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, redactAccount(a))
	}
	respond(w, out)
	return nil
}

func (s *Server) handleAccountCreate(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name       string `json:"name"`
		DriverType string `json:"driver_type"`
		Config     string `json:"config"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.DriverType) == "" {
		return domain.Errorf(domain.CodeValidation, "账号名称与驱动类型不能为空")
	}
	acc := &store.Account{Name: in.Name, DriverType: in.DriverType, Config: in.Config, IsActive: true}
	id, err := s.currentStore().CreateAccount(r.Context(), acc)
	if err != nil {
		return err
	}
	// 立即构建实例验证配置；失败则回滚创建。
	ctx := r.Context()
	created, err := s.currentStore().GetAccount(ctx, id)
	if err == nil {
		if berr := s.deps.Accounts.Probe(ctx, id); berr != nil {
			_ = s.currentStore().DeleteAccount(ctx, id)
			return domain.Errorf(domain.CodeValidation, "账号验证失败：%v", berr)
		}
		_ = created
	}
	respond(w, map[string]int64{"id": id})
	return nil
}

func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var in struct {
		Name     string `json:"name"`
		Config   string `json:"config"`
		IsActive *bool  `json:"is_active"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	acc, err := s.currentStore().GetAccount(ctx, id)
	if err != nil {
		return err
	}
	if in.Name != "" {
		acc.Name = in.Name
	}
	if in.Config != "" {
		// 前端回传的凭据字段是占位符时保留数据库里的真实值（见 redactAccount）。
		acc.Config = restoreSecrets(in.Config, acc.Config)
	}
	if in.IsActive != nil {
		acc.IsActive = *in.IsActive
	}
	if err := s.currentStore().UpdateAccount(ctx, acc); err != nil {
		return err
	}
	// 配置变更后重建实例
	s.deps.Accounts.DropAccount(ctx, id)
	respond(w, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	// 删除账号前检查引用：留下悬空 account_id 的任务会永久报「账号不存在」，
	// 且前端账号下拉选不中，用户无法自行修复。
	count, err := s.currentStore().CountTasksByAccount(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return domain.Errorf(domain.CodeValidation,
			"该账号下还有 %d 个转存任务，请先删除这些任务或改派到其他账号", count)
	}
	if err := s.currentStore().DeleteAccount(ctx, id); err != nil {
		return err
	}
	s.deps.Accounts.DropAccount(ctx, id)
	respond(w, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleAccountProbe(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := s.deps.Accounts.Probe(r.Context(), id); err != nil {
		return domain.Errorf(domain.CodeDriverError, "账号探测失败：%v", err)
	}
	respond(w, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleAccountProfile(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var out any
	err = s.deps.Accounts.Run(r.Context(), id, func(drv driver.Driver) error {
		p, ok := drv.(driver.AccountProfileProvider)
		if !ok {
			return domain.Errorf(domain.CodeNotImplement, "驱动不支持账号资料")
		}
		profile, err := p.GetAccountProfile(r.Context())
		if err != nil {
			return err
		}
		out = profile
		return nil
	})
	if err != nil {
		return err
	}
	respond(w, out)
	return nil
}

func (s *Server) handleQRStart(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		DriverType string `json:"driver_type"`
		Config     string `json:"config"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.deps.Accounts.StartQRLogin(r.Context(), in.DriverType, in.Config)
	if err != nil {
		return err
	}
	respond(w, res)
	return nil
}

func (s *Server) handleQRPoll(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token string `json:"token"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.deps.Accounts.PollQRLogin(r.Context(), in.Token)
	if err != nil {
		return err
	}
	respond(w, res)
	return nil
}

// ---------- 分享预览与文件浏览 ----------

func (s *Server) handleSharePreview(w http.ResponseWriter, r *http.Request) error {
	var in transfer.PreviewShareInput
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.transfer().PreviewShare(r.Context(), in)
	if err != nil {
		return err
	}
	respond(w, res)
	return nil
}

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) error {
	accountID, _ := strconv.ParseInt(r.URL.Query().Get("account_id"), 10, 64)
	if accountID == 0 {
		return domain.Errorf(domain.CodeValidation, "缺少 account_id")
	}
	fid := r.URL.Query().Get("fid")
	var items any
	err := s.deps.Accounts.Run(r.Context(), accountID, func(drv driver.Driver) error {
		list, err := drv.ListFiles(r.Context(), fid)
		if err != nil {
			return err
		}
		items = list
		return nil
	})
	if err != nil {
		return err
	}
	respond(w, items)
	return nil
}

func (s *Server) handleFileResolve(w http.ResponseWriter, r *http.Request) error {
	accountID, _ := strconv.ParseInt(r.URL.Query().Get("account_id"), 10, 64)
	path := r.URL.Query().Get("path")
	if accountID == 0 || path == "" {
		return domain.Errorf(domain.CodeValidation, "缺少 account_id 或 path")
	}
	var out any
	err := s.deps.Accounts.Run(r.Context(), accountID, func(drv driver.Driver) error {
		st, ok := drv.(driver.ShareTransferer)
		if !ok {
			return domain.Errorf(domain.CodeNotImplement, "驱动不支持路径解析")
		}
		paths, err := st.ResolvePathToFID(r.Context(), []string{path})
		if err != nil {
			return err
		}
		if len(paths) == 0 || strings.TrimSpace(paths[0].FID) == "" {
			return domain.Errorf(domain.CodeNotFound, "路径不存在：%s", path)
		}
		out = paths[0]
		return nil
	})
	if err != nil {
		return err
	}
	respond(w, out)
	return nil
}

func (s *Server) handleFileMkdir(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		AccountID int64  `json:"account_id"`
		ParentFID string `json:"parent_fid"`
		Name      string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.AccountID == 0 || strings.TrimSpace(in.Name) == "" {
		return domain.Errorf(domain.CodeValidation, "缺少 account_id 或 name")
	}
	var item any
	err := s.deps.Accounts.Run(r.Context(), in.AccountID, func(drv driver.Driver) error {
		fc, ok := drv.(driver.FolderCreator)
		if !ok {
			return domain.Errorf(domain.CodeNotImplement, "驱动不支持创建目录")
		}
		created, err := fc.CreateFolder(r.Context(), in.ParentFID, in.Name)
		if err != nil {
			return err
		}
		item = created
		return nil
	})
	if err != nil {
		return err
	}
	respond(w, item)
	return nil
}

// ---------- 任务 ----------

// taskIn 是任务创建/更新的请求体；links 是多源链接数组。
type taskIn struct {
	ID              int64    `json:"id"`
	TaskName        string   `json:"task_name"`
	AccountID       int64    `json:"account_id"`
	Links           []string `json:"links"`
	SavePath        string   `json:"save_path"`
	Pattern         string   `json:"pattern"`
	Replace         string   `json:"replace"`
	IgnoreExtension bool     `json:"ignore_extension"`
	RunWeek         string   `json:"run_week"`
	RunTime         string   `json:"run_time"`
	EndDate         string   `json:"end_date"`
	UpdateSubdir    string   `json:"update_subdir"`
	// UpdateSubdirResaveMode 控制子目录已存在时的行为：
	// ""（增量，跳过已有文件）| "delete_then_resave"（先删再存）。
	// 该字段一直在领域模型与执行器里（executor.go 会读它决定是否删远端目录），
	// 但此前 taskIn/taskOut 都没带，导致这条分支永久不可达。
	UpdateSubdirResaveMode string `json:"update_subdir_resave_mode"`
	StartFID               string `json:"start_fid"`
	CategoryID             int64  `json:"category_id"`
	SortIndex              int    `json:"sort_index"`
	SkipHistory            string `json:"skip_history"`
	RunMode                string `json:"run_mode"` // manual/global/custom
	RunCron                string `json:"run_cron"` // 5 字段 cron 表达式
	Status                 string `json:"status"`
}

func (in *taskIn) toTask() *domain.DramaTask {
	t := &domain.DramaTask{
		ID:                     in.ID,
		TaskName:               in.TaskName,
		AccountID:              in.AccountID,
		SavePath:               in.SavePath,
		Pattern:                in.Pattern,
		Replace:                in.Replace,
		IgnoreExtension:        in.IgnoreExtension,
		RunWeek:                in.RunWeek,
		RunTime:                in.RunTime,
		EndDate:                in.EndDate,
		UpdateSubdir:           in.UpdateSubdir,
		UpdateSubdirResaveMode: in.UpdateSubdirResaveMode,
		StartFID:               in.StartFID,
		CategoryID:             in.CategoryID,
		SortIndex:              in.SortIndex,
		SkipHistory:            in.SkipHistory,
		RunMode:                in.RunMode,
		RunCron:                in.RunCron,
		Status:                 in.Status,
	}
	transfer.SetLinks(t, in.Links)
	return t
}

func taskOut(t *domain.DramaTask) map[string]any {
	return map[string]any{
		"id":                        t.ID,
		"task_name":                 t.TaskName,
		"account_id":                t.AccountID,
		"links":                     transfer.LinksOf(t),
		"save_path":                 t.SavePath,
		"pattern":                   t.Pattern,
		"replace":                   t.Replace,
		"ignore_extension":          t.IgnoreExtension,
		"run_week":                  t.RunWeek,
		"run_time":                  t.RunTime,
		"end_date":                  t.EndDate,
		"update_subdir":             t.UpdateSubdir,
		"update_subdir_resave_mode": t.UpdateSubdirResaveMode,
		"start_fid":                 t.StartFID,
		"category_id":               t.CategoryID,
		"sort_index":                t.SortIndex,
		"skip_history":              t.SkipHistory,
		"run_mode":                  transfer.EffectiveRunMode(t),
		"run_cron":                  t.RunCron,
		"status":                    t.Status,
		"last_run_at":               t.LastRunAt,
		"last_run_status":           t.LastRunStatus,
		"last_run_message":          t.LastRunMessage,
		"last_tree":                 t.LastTreeSummary,
		"created_at":                t.CreatedAt,
		"updated_at":                t.UpdatedAt,
	}
}

// ---------- 异步运行句柄 ----------

// handleJobList 列出运行中的（及最近结束的）job。
func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) error {
	list := s.transfer().ListJobs()
	running := 0
	for _, j := range list {
		if j.Status == "running" {
			running++
		}
	}
	respond(w, map[string]any{
		"jobs":    list,
		"running": running,
	})
	return nil
}

// handleJobGet 返回单个 job 的进度与日志快照。
func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	job, ok := s.transfer().GetJob(id)
	if !ok {
		return domain.Errorf(domain.CodeNotFound, "运行句柄不存在或已过期（句柄是内存态，重启即清空）")
	}
	respond(w, job)
	return nil
}

// handleJobCancel 请求取消一个运行中的 job。
//
// 注意：取消只是设置 context 取消标记，当前正在进行的单次网盘请求可能仍会
// 完成（驱动层没有逐请求的中断传播）；已完成的转存不会回滚。
func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := s.transfer().CancelJob(id); err != nil {
		return err
	}
	respond(w, map[string]any{"ok": true, "message": "已请求取消；当前进行中的单次请求会自然结束"})
	return nil
}

// handleJobDelete 删除一个已结束的 job 句柄（不影响 task_runs 里的权威记录）。
func (s *Server) handleJobDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	s.transfer().RemoveJob(id)
	respond(w, map[string]bool{"ok": true})
	return nil
}

// handleJobsBatch 批量查 job 进度。
//
// 为什么要这个：任务列表页有 N 个任务在跑时，前端要逐个 GET /api/jobs/{id}。
// 在 SQLite 单连接（SetMaxOpenConns(1)）下这些请求会排队，
// 而轮询是每秒一次——N=10 时每秒 10 个请求全堵在同一个连接上，
// 正常的 API 调用反而被拖慢。一次批量查询把 N 个请求合并成 1 个。
func (s *Server) handleJobsBatch(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	// 上限保护：批量查询的 ids 来自前端自己的列表，
	// 但接口是公开的，必须防止有人塞几万个 id 把它变成放大器。
	if len(in.IDs) > 500 {
		return domain.Errorf(domain.CodeValidation, "单次最多查询 500 个运行句柄")
	}
	svc := s.transfer()
	if svc == nil {
		return errInternalf("转存服务未就绪")
	}
	out := make([]transfer.JobView, 0, len(in.IDs))
	for _, id := range in.IDs {
		if job, ok := svc.GetJob(id); ok {
			out = append(out, *job)
		}
	}
	respond(w, map[string]any{
		"jobs": out,
		// 找不到的 id 由前端自行处理（清理"已经消失的运行中任务"）。
		"found": len(out),
	})
	return nil
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) error {
	list, err := s.transfer().ListTasks(r.Context())
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, taskOut(t))
	}
	respond(w, out)
	return nil
}

func (s *Server) handleTaskCreate(w http.ResponseWriter, r *http.Request) error {
	var in taskIn
	if err := decode(r, &in); err != nil {
		return err
	}
	id, err := s.transfer().SaveTask(r.Context(), in.toTask())
	if err != nil {
		return err
	}
	respond(w, map[string]int64{"id": id})
	return nil
}

func (s *Server) handleTaskUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var in taskIn
	if err := decode(r, &in); err != nil {
		return err
	}
	in.ID = id
	if _, err := s.transfer().SaveTask(r.Context(), in.toTask()); err != nil {
		return err
	}
	respond(w, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := s.transfer().DeleteTask(r.Context(), id); err != nil {
		return err
	}
	respond(w, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if err := s.transfer().SetTaskStatus(r.Context(), id, in.Status); err != nil {
		return err
	}
	respond(w, map[string]bool{"ok": true})
	return nil
}

// handleTaskRun 启动一次任务执行（异步）。
//
// 为什么改成异步：同步路径下整个转存流程（多链接 × 每批 100 文件 × 逐文件重命名）
// 在 HTTP 请求里跑完，最长 30 分钟。后果是反代 60s 超时即断、手机切后台即
// 无响应、页面关掉再也看不到结果、无法取消。
//
// 语义变化（破坏性，但有兼容）：
//   - 响应从 {task_id, status, transfer_count, ...} 变为 {job_id, status: "running", ...}；
//   - 需要进度/结果时轮询 GET /api/jobs/{id} 或 GET /api/jobs。
//   - 兼容：仍在响应里带上 running: true，老前端至少不会误判"运行失败"。
func (s *Server) handleTaskRun(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	job, err := s.transfer().StartTask(r.Context(), id)
	if err != nil {
		return err
	}
	respond(w, map[string]any{
		"job_id":     job.ID,
		"task_id":    job.TaskID,
		"task_name":  job.TaskName,
		"status":     "running",
		"running":    true,
		"stage":      job.Stage,
		"started_at": job.StartedAt,
		"poll_hint":  "轮询 GET /api/jobs/" + fmt.Sprintf("%d", job.ID) + " 获取进度与日志",
	})
	return nil
}

func (s *Server) handleTaskRuns(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.transfer().ListRuns(r.Context(), id, limit)
	if err != nil {
		return err
	}
	respond(w, runs)
	return nil
}

func (s *Server) handleTaskSavedNames(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	names, err := s.transfer().ListSavedNames(r.Context(), id, limit)
	if err != nil {
		return err
	}
	respond(w, names)
	return nil
}

// handleTaskSavedCount 转存历史条数。
func (s *Server) handleTaskSavedCount(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	n, err := s.currentStore().CountSavedNames(r.Context(), id)
	if err != nil {
		return err
	}
	respond(w, map[string]int{"count": n})
	return nil
}

// handleTaskSavedClear 清空任务转存历史（之后按目标目录现状重新判定，可能重复转存）。
func (s *Server) handleTaskSavedClear(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	n, err := s.currentStore().ClearSavedNames(r.Context(), id)
	if err != nil {
		return err
	}
	respond(w, map[string]int64{"cleared": n})
	return nil
}

// ---------- 命名正则规则 ----------

// ruleOut 是规则条目：内置规则标注 builtin，DB 覆盖行标注 overridden。
type ruleOut struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Pattern    string `json:"pattern"`
	Replace    string `json:"replace"`
	Enabled    bool   `json:"enabled"`
	Builtin    bool   `json:"builtin"`
	Custom     bool   `json:"custom"`
	Overridden bool   `json:"overridden"`
}

func (s *Server) handleRuleList(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	custom, err := s.currentStore().ListRules(ctx)
	if err != nil {
		return err
	}
	byKey := map[string]*domain.MagicRegexRule{}
	for _, c := range custom {
		byKey[c.Key] = c
	}
	out := make([]ruleOut, 0, len(transfer.BuiltinRules)+len(custom))
	seen := map[string]bool{}
	for _, b := range transfer.BuiltinRules {
		pattern, replace := transfer.BuiltinRuleDef(b.Key)
		entry := ruleOut{Key: b.Key, Label: b.Label, Pattern: pattern, Replace: replace, Enabled: true, Builtin: true}
		if c, ok := byKey[b.Key]; ok {
			seen[b.Key] = true
			entry.Pattern, entry.Replace, entry.Enabled, entry.Overridden = c.Pattern, c.Replace, c.Enabled, true
		}
		out = append(out, entry)
	}
	for _, c := range custom {
		if seen[c.Key] {
			continue
		}
		out = append(out, ruleOut{Key: c.Key, Label: c.Label, Pattern: c.Pattern, Replace: c.Replace, Enabled: c.Enabled, Custom: true})
	}
	respond(w, out)
	return nil
}

func (s *Server) handleRuleSave(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Key     string `json:"key"`
		Label   string `json:"label"`
		Pattern string `json:"pattern"`
		Replace string `json:"replace"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return domain.Errorf(domain.CodeValidation, "规则标识（$ 开头）不能为空")
	}
	if !strings.HasPrefix(key, "$") {
		key = "$" + strings.TrimLeft(key, "$")
	}
	if strings.TrimSpace(in.Pattern) == "" && strings.TrimSpace(in.Replace) == "" {
		return domain.Errorf(domain.CodeValidation, "pattern 与 replace 不能同时为空")
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	rule := &domain.MagicRegexRule{Key: key, Label: in.Label, Pattern: in.Pattern, Replace: in.Replace, Enabled: enabled}
	if err := s.currentStore().UpsertRule(r.Context(), rule); err != nil {
		return err
	}
	respond(w, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleRuleDelete(w http.ResponseWriter, r *http.Request) error {
	key := r.PathValue("key")
	if !strings.HasPrefix(key, "$") {
		key = "$" + key
	}
	err := s.currentStore().DeleteRule(r.Context(), key)
	if err != nil {
		// 内置规则的"删除"语义 = 恢复默认（覆盖行本就不存在时视为成功）
		if transfer.BuiltinLabel(key) != "" {
			var ae *domain.AppError
			if errors.As(err, &ae) && ae.Code == domain.CodeNotFound {
				respond(w, map[string]bool{"ok": true})
				return nil
			}
		}
		return err
	}
	respond(w, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleRuleTest(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name    string `json:"name"`
		Pattern string `json:"pattern"`
		Replace string `json:"replace"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	matched, result, actualPattern, actualReplace := transfer.TestRename(in.Name, in.Pattern, in.Replace)
	respond(w, map[string]any{
		"name":             in.Name,
		"matched":          matched,
		"result":           result,
		"resolved_pattern": actualPattern,
		"resolved_replace": actualReplace,
	})
	return nil
}

// ---------- 设置与通知 ----------

// handleCronNext 返回 cron 表达式下次触发时间。
func (s *Server) handleCronNext(w http.ResponseWriter, r *http.Request) error {
	expr := strings.TrimSpace(r.URL.Query().Get("expr"))
	if expr == "" {
		return domain.Errorf(domain.CodeValidation, "缺少 expr 参数")
	}
	if err := transfer.ParseCron(expr); err != nil {
		return domain.Errorf(domain.CodeValidation, "cron 表达式无效：%v", err)
	}
	from := r.URL.Query().Get("from")
	next, err := transfer.NextCronRun(expr, from)
	if err != nil {
		return err
	}
	respond(w, map[string]string{"expr": expr, "next_run": next})
	return nil
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	channels, err := s.deps.Notify.ListChannels(ctx)
	if err != nil {
		return err
	}
	skip, _ := s.currentStore().GetSetting(ctx, "skip_history_global")
	if skip == "" {
		skip = "on"
	}
	schedEnabled, _ := s.currentStore().GetSetting(ctx, "schedule_enabled")
	if schedEnabled == "" {
		schedEnabled = "on"
	}
	schedTime, _ := s.currentStore().GetSetting(ctx, "schedule_time")
	schedWeek, _ := s.currentStore().GetSetting(ctx, "schedule_week")
	schedCron, _ := s.currentStore().GetSetting(ctx, "schedule_cron")
	pwd, _ := s.currentStore().GetSetting(ctx, "auth_password")
	retention, _ := s.currentStore().GetSetting(ctx, "run_retention_days")
	if retention == "" {
		retention = "90"
	}
	interval := accounts.DefaultRequestIntervalMS
	if v, err := s.currentStore().GetSetting(ctx, "request_interval_ms"); err == nil && strings.TrimSpace(v) != "" {
		if n, e := strconv.Atoi(strings.TrimSpace(v)); e == nil && n >= 0 {
			interval = n
		}
	}
	respond(w, map[string]any{
		"notify_channels":     channels,
		"skip_history_global": skip,
		"schedule_enabled":    schedEnabled,
		"schedule_time":       schedTime,
		"schedule_week":       schedWeek,
		"schedule_cron":       schedCron,
		"run_retention_days":  retention,
		"request_interval_ms": interval,
		"auth_enabled":        strings.TrimSpace(pwd) != "",
	})
	return nil
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		NotifyChannels    *[]notify.Channel `json:"notify_channels"`
		SkipHistoryGlobal *string           `json:"skip_history_global"`
		ScheduleEnabled   *string           `json:"schedule_enabled"`
		ScheduleTime      *string           `json:"schedule_time"`
		ScheduleWeek      *string           `json:"schedule_week"`
		ScheduleCron      *string           `json:"schedule_cron"`
		RunRetentionDays  *string           `json:"run_retention_days"`
		RequestIntervalMS *int              `json:"request_interval_ms"`
		Password          *string           `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	if in.NotifyChannels != nil {
		if err := s.deps.Notify.SaveChannels(ctx, *in.NotifyChannels); err != nil {
			return err
		}
	}
	if in.SkipHistoryGlobal != nil {
		v := *in.SkipHistoryGlobal
		if v != "on" && v != "off" {
			return domain.Errorf(domain.CodeValidation, "skip_history_global 只能是 on/off")
		}
		if err := s.currentStore().SetSetting(ctx, "skip_history_global", v); err != nil {
			return err
		}
	}
	if in.ScheduleEnabled != nil {
		v := *in.ScheduleEnabled
		if v != "on" && v != "off" {
			return domain.Errorf(domain.CodeValidation, "schedule_enabled 只能是 on/off")
		}
		if err := s.currentStore().SetSetting(ctx, "schedule_enabled", v); err != nil {
			return err
		}
	}
	if in.ScheduleTime != nil {
		v := strings.TrimSpace(*in.ScheduleTime)
		if v != "" {
			if _, err := time.Parse("15:04", v); err != nil {
				return domain.Errorf(domain.CodeValidation, "运行时间格式应为 HH:MM")
			}
		}
		if err := s.currentStore().SetSetting(ctx, "schedule_time", v); err != nil {
			return err
		}
	}
	if in.ScheduleWeek != nil {
		v := strings.TrimSpace(*in.ScheduleWeek)
		if v != "" {
			for _, part := range strings.Split(v, ",") {
				d, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil || d < 1 || d > 7 {
					return domain.Errorf(domain.CodeValidation, "更新日应为 1-7 的逗号分隔列表")
				}
			}
		}
		if err := s.currentStore().SetSetting(ctx, "schedule_week", v); err != nil {
			return err
		}
	}
	if in.ScheduleCron != nil {
		v := strings.TrimSpace(*in.ScheduleCron)
		if v != "" {
			if err := transfer.ParseCron(v); err != nil {
				return domain.Errorf(domain.CodeValidation, "cron 表达式无效：%v", err)
			}
		}
		if err := s.currentStore().SetSetting(ctx, "schedule_cron", v); err != nil {
			return err
		}
	}
	if in.RunRetentionDays != nil {
		v, err := strconv.Atoi(strings.TrimSpace(*in.RunRetentionDays))
		if err != nil || v < 0 {
			return domain.Errorf(domain.CodeValidation, "运行记录保留天数应为非负整数（0 = 永久）")
		}
		if err := s.currentStore().SetSetting(ctx, "run_retention_days", strconv.Itoa(v)); err != nil {
			return err
		}
	}
	if in.RequestIntervalMS != nil {
		v := *in.RequestIntervalMS
		// 上限保护：间隔过大等于自锁（大目录转存会超时），过小等于放弃风控防护。
		if v < 0 || v > 60000 {
			return domain.Errorf(domain.CodeValidation, "请求间隔应在 0–60000 毫秒之间")
		}
		if err := s.currentStore().SetSetting(ctx, "request_interval_ms", strconv.Itoa(v)); err != nil {
			return err
		}
	}
	if in.Password != nil {
		pwd := strings.TrimSpace(*in.Password)
		if pwd == "" {
			// 清空密码 = 关闭鉴权
			if err := s.currentStore().SetSetting(ctx, "auth_password", ""); err != nil {
				return err
			}
		} else {
			if len(pwd) < 4 {
				return domain.Errorf(domain.CodeValidation, "密码至少 4 位")
			}
			h := s.hashPassword(pwd)
			if h == "" {
				return domain.Errorf(domain.CodeInternal, "密码哈希失败，请检查系统随机源是否可用")
			}
			if err := s.currentStore().SetSetting(ctx, "auth_password", h); err != nil {
				return err
			}
			// 改密码后作废此前签发的所有会话，否则旧 token 仍可继续使用 30 天。
			s.bumpSessionVersion(ctx)
		}
	}
	respond(w, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) error {
	var in notify.Channel
	if err := decode(r, &in); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.deps.Notify.SendTo(ctx, in, "PanTransfer 测试通知", "如果你看到这条消息，说明通知渠道配置成功 🎉"); err != nil {
		return domain.Errorf(domain.CodeDriverError, "测试消息发送失败：%v", err)
	}
	respond(w, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": s.deps.Version, "time": time.Now().Format(time.RFC3339),
	})
}
