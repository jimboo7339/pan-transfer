// Package httpapi 提供 pan-transfer 的 REST API 与静态资源托管。
package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
	"pan-transfer/internal/store"
	"pan-transfer/internal/transfer"
)

// Deps 是 API 层依赖。
type Deps struct {
	Store    *store.Store
	Accounts *accounts.Manager
	Transfer *transfer.Service
	Notify   *notify.Sender
	Log      *slog.Logger
	Version  string
}

// Server 是 HTTP API 服务。
type Server struct {
	deps Deps
	mux  *http.ServeMux

	secret     string // 会话签名密钥
	secretOnce sync.Once
}

// NewServer 构造 API 服务（含路由注册）。
func NewServer(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler 返回根 Handler（含静态资源）。
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	m := s.mux
	s.registerCategoryRoutes()

	// 认证
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
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
	m.HandleFunc("PUT /api/tasks/{id}", s.wrap(s.handleTaskUpdate))
	m.HandleFunc("DELETE /api/tasks/{id}", s.wrap(s.handleTaskDelete))
	m.HandleFunc("POST /api/tasks/{id}/status", s.wrap(s.handleTaskStatus))
	m.HandleFunc("POST /api/tasks/{id}/run", s.wrap(s.handleTaskRun))
	m.HandleFunc("GET /api/tasks/{id}/runs", s.wrap(s.handleTaskRuns))
	m.HandleFunc("GET /api/tasks/{id}/saved-names", s.wrap(s.handleTaskSavedNames))
	m.HandleFunc("GET /api/tasks/{id}/saved-names/count", s.wrap(s.handleTaskSavedCount))
	m.HandleFunc("DELETE /api/tasks/{id}/saved-names", s.wrap(s.handleTaskSavedClear))

	// 命名正则规则
	m.HandleFunc("GET /api/rules", s.wrap(s.handleRuleList))
	m.HandleFunc("POST /api/rules", s.wrap(s.handleRuleSave))
	m.HandleFunc("DELETE /api/rules/{key}", s.wrap(s.handleRuleDelete))
	m.HandleFunc("POST /api/rules/test", s.wrap(s.handleRuleTest))

	// 调度辅助
	m.HandleFunc("GET /api/cron/next", s.wrap(s.handleCronNext))

	// 设置与通知
	m.HandleFunc("GET /api/settings", s.wrap(s.handleSettingsGet))
	m.HandleFunc("PUT /api/settings", s.wrap(s.handleSettingsPut))
	m.HandleFunc("POST /api/notify/test", s.wrap(s.handleNotifyTest))

	// 服务信息
	m.HandleFunc("GET /api/health", s.handleHealth)
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
	writeJSON(w, http.StatusInternalServerError, map[string]apiError{"error": {
		Code: domain.CodeInternal, Message: err.Error(),
	}})
}

// wrap 统一鉴权 + 错误处理。handler 返回错误时写 JSON 错误响应。
func (s *Server) wrap(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.checkAuth(r); err != nil {
			writeErr(w, err)
			return
		}
		if err := fn(w, r); err != nil {
			s.deps.Log.Warn("API 错误", "path", r.URL.Path, "err", err)
			writeErr(w, err)
		}
	}
}

type stdBody struct {
	Ok   bool `json:"ok"`
	Data any  `json:"data,omitempty"`
}

func respond(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, stdBody{Ok: true, Data: data})
}

func decode(r *http.Request, v any) error {
	defer func() { _ = r.Body.Close() }()
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
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

func (s *Server) secretKey() string {
	s.secretOnce.Do(func() {
		ctx := context.Background()
		v, _ := s.deps.Store.GetSetting(ctx, "app_secret")
		if v == "" {
			buf := make([]byte, 32)
			_, _ = rand.Read(buf)
			v = hex.EncodeToString(buf)
			_ = s.deps.Store.SetSetting(ctx, "app_secret", v)
		}
		s.secret = v
	})
	return s.secret
}

func (s *Server) authEnabled(ctx context.Context) bool {
	v, _ := s.deps.Store.GetSetting(ctx, "auth_password")
	return strings.TrimSpace(v) != ""
}

func hashPassword(pwd string) string {
	sum := sha256.Sum256([]byte("pan-transfer:" + pwd))
	return hex.EncodeToString(sum[:])
}

func (s *Server) signSession(expiry int64) string {
	msg := fmt.Sprintf("session:%d", expiry)
	mac := hmac.New(sha256.New, []byte(s.secretKey()))
	mac.Write([]byte(msg))
	return fmt.Sprintf("%d.%s", expiry, hex.EncodeToString(mac.Sum(nil)))
}

func (s *Server) verifySession(token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	var expiry int64
	if _, err := fmt.Sscanf(parts[0], "%d", &expiry); err != nil {
		return false
	}
	if time.Now().Unix() > expiry {
		return false
	}
	return hmac.Equal([]byte(s.signSession(expiry)), []byte(token))
}

func (s *Server) checkAuth(r *http.Request) error {
	ctx := r.Context()
	if !s.authEnabled(ctx) {
		return nil
	}
	// 登录接口自身放行（login/status/health 在 wrap 外或单独注册）
	token := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	} else if c, err := r.Cookie("pt_session"); err == nil {
		token = c.Value
	} else if q := r.URL.Query().Get("token"); q != "" {
		token = q
	}
	if token != "" && s.verifySession(token) {
		return nil
	}
	return domain.Errorf(domain.CodeAuthExpired, "未登录或会话已过期")
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	required := s.authEnabled(ctx)
	token := ""
	if c, err := r.Cookie("pt_session"); err == nil {
		token = c.Value
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_required": required,
		"authed":        !required || (token != "" && s.verifySession(token)),
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
	stored, _ := s.deps.Store.GetSetting(ctx, "auth_password")
	if strings.TrimSpace(stored) == "" {
		respond(w, map[string]string{"token": ""}) // 未启用鉴权
		return
	}
	if hashPassword(in.Password) != stored {
		writeErr(w, domain.Errorf(domain.CodeAuthExpired, "密码错误"))
		return
	}
	expiry := time.Now().Add(30 * 24 * time.Hour).Unix()
	token := s.signSession(expiry)
	http.SetCookie(w, &http.Cookie{
		Name: "pt_session", Value: token, Path: "/", MaxAge: 30 * 24 * 3600, HttpOnly: true,
	})
	respond(w, map[string]string{"token": token})
}

// ---------- 驱动与账号 ----------

func (s *Server) handleDrivers(w http.ResponseWriter, _ *http.Request) error {
	respond(w, s.deps.Accounts.ListDriverTypes())
	return nil
}

func (s *Server) handleAccountList(w http.ResponseWriter, r *http.Request) error {
	list, err := s.deps.Store.ListAccounts(r.Context())
	if err != nil {
		return err
	}
	respond(w, list)
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
	id, err := s.deps.Store.CreateAccount(r.Context(), acc)
	if err != nil {
		return err
	}
	// 立即构建实例验证配置；失败则回滚创建。
	ctx := r.Context()
	created, err := s.deps.Store.GetAccount(ctx, id)
	if err == nil {
		if berr := s.deps.Accounts.Probe(ctx, id); berr != nil {
			_ = s.deps.Store.DeleteAccount(ctx, id)
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
	acc, err := s.deps.Store.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	if in.Name != "" {
		acc.Name = in.Name
	}
	if in.Config != "" {
		acc.Config = in.Config
	}
	if in.IsActive != nil {
		acc.IsActive = *in.IsActive
	}
	if err := s.deps.Store.UpdateAccount(ctx, acc); err != nil {
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
	if err := s.deps.Store.DeleteAccount(ctx, id); err != nil {
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
	res, err := s.deps.Transfer.PreviewShare(r.Context(), in)
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
	StartFID        string   `json:"start_fid"`
	CategoryID      int64    `json:"category_id"`
	SortIndex       int      `json:"sort_index"`
	SkipHistory     string   `json:"skip_history"`
	RunMode         string   `json:"run_mode"` // manual/global/custom
	RunCron         string   `json:"run_cron"` // 5 字段 cron 表达式
	Status          string   `json:"status"`
}

func (in *taskIn) toTask() *domain.DramaTask {
	t := &domain.DramaTask{
		ID:              in.ID,
		TaskName:        in.TaskName,
		AccountID:       in.AccountID,
		SavePath:        in.SavePath,
		Pattern:         in.Pattern,
		Replace:         in.Replace,
		IgnoreExtension: in.IgnoreExtension,
		RunWeek:         in.RunWeek,
		RunTime:         in.RunTime,
		EndDate:         in.EndDate,
		UpdateSubdir:    in.UpdateSubdir,
		StartFID:        in.StartFID,
		CategoryID:      in.CategoryID,
		SortIndex:       in.SortIndex,
		SkipHistory:     in.SkipHistory,
		RunMode:         in.RunMode,
		RunCron:         in.RunCron,
		Status:          in.Status,
	}
	transfer.SetLinks(t, in.Links)
	return t
}

func taskOut(t *domain.DramaTask) map[string]any {
	return map[string]any{
		"id":               t.ID,
		"task_name":        t.TaskName,
		"account_id":       t.AccountID,
		"links":            transfer.LinksOf(t),
		"save_path":        t.SavePath,
		"pattern":          t.Pattern,
		"replace":          t.Replace,
		"ignore_extension": t.IgnoreExtension,
		"run_week":         t.RunWeek,
		"run_time":         t.RunTime,
		"end_date":         t.EndDate,
		"update_subdir":    t.UpdateSubdir,
		"start_fid":        t.StartFID,
		"category_id":      t.CategoryID,
		"sort_index":       t.SortIndex,
		"skip_history":     t.SkipHistory,
		"run_mode":         transfer.EffectiveRunMode(t),
		"run_cron":         t.RunCron,
		"status":           t.Status,
		"last_run_at":      t.LastRunAt,
		"last_run_status":  t.LastRunStatus,
		"last_run_message": t.LastRunMessage,
		"last_tree":        t.LastTreeSummary,
		"created_at":       t.CreatedAt,
		"updated_at":       t.UpdatedAt,
	}
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) error {
	list, err := s.deps.Transfer.ListTasks(r.Context())
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
	id, err := s.deps.Transfer.SaveTask(r.Context(), in.toTask())
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
	if _, err := s.deps.Transfer.SaveTask(r.Context(), in.toTask()); err != nil {
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
	if err := s.deps.Transfer.DeleteTask(r.Context(), id); err != nil {
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
	if err := s.deps.Transfer.SetTaskStatus(r.Context(), id, in.Status); err != nil {
		return err
	}
	respond(w, map[string]bool{"ok": true})
	return nil
}

func (s *Server) handleTaskRun(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	// 脱离请求上下文：客户端刷新/断开/反代超时不应中断正在进行的转存
	ctx := context.WithoutCancel(r.Context())
	res, err := s.deps.Transfer.RunTaskNow(ctx, id)
	if err != nil {
		return err
	}
	respond(w, res)
	return nil
}

func (s *Server) handleTaskRuns(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.deps.Transfer.ListRuns(r.Context(), id, limit)
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
	names, err := s.deps.Transfer.ListSavedNames(r.Context(), id, limit)
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
	n, err := s.deps.Store.CountSavedNames(r.Context(), id)
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
	n, err := s.deps.Store.ClearSavedNames(r.Context(), id)
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
	custom, err := s.deps.Store.ListRules(ctx)
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
	if err := s.deps.Store.UpsertRule(r.Context(), rule); err != nil {
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
	err := s.deps.Store.DeleteRule(r.Context(), key)
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
	skip, _ := s.deps.Store.GetSetting(ctx, "skip_history_global")
	if skip == "" {
		skip = "on"
	}
	schedEnabled, _ := s.deps.Store.GetSetting(ctx, "schedule_enabled")
	if schedEnabled == "" {
		schedEnabled = "on"
	}
	schedTime, _ := s.deps.Store.GetSetting(ctx, "schedule_time")
	schedWeek, _ := s.deps.Store.GetSetting(ctx, "schedule_week")
	schedCron, _ := s.deps.Store.GetSetting(ctx, "schedule_cron")
	pwd, _ := s.deps.Store.GetSetting(ctx, "auth_password")
	retention, _ := s.deps.Store.GetSetting(ctx, "run_retention_days")
	if retention == "" {
		retention = "90"
	}
	respond(w, map[string]any{
		"notify_channels":     channels,
		"skip_history_global": skip,
		"schedule_enabled":    schedEnabled,
		"schedule_time":       schedTime,
		"schedule_week":       schedWeek,
		"schedule_cron":       schedCron,
		"run_retention_days":  retention,
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
		if err := s.deps.Store.SetSetting(ctx, "skip_history_global", v); err != nil {
			return err
		}
	}
	if in.ScheduleEnabled != nil {
		v := *in.ScheduleEnabled
		if v != "on" && v != "off" {
			return domain.Errorf(domain.CodeValidation, "schedule_enabled 只能是 on/off")
		}
		if err := s.deps.Store.SetSetting(ctx, "schedule_enabled", v); err != nil {
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
		if err := s.deps.Store.SetSetting(ctx, "schedule_time", v); err != nil {
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
		if err := s.deps.Store.SetSetting(ctx, "schedule_week", v); err != nil {
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
		if err := s.deps.Store.SetSetting(ctx, "schedule_cron", v); err != nil {
			return err
		}
	}
	if in.RunRetentionDays != nil {
		v, err := strconv.Atoi(strings.TrimSpace(*in.RunRetentionDays))
		if err != nil || v < 0 {
			return domain.Errorf(domain.CodeValidation, "运行记录保留天数应为非负整数（0 = 永久）")
		}
		if err := s.deps.Store.SetSetting(ctx, "run_retention_days", strconv.Itoa(v)); err != nil {
			return err
		}
	}
	if in.Password != nil {
		pwd := strings.TrimSpace(*in.Password)
		if pwd == "" {
			// 清空密码 = 关闭鉴权
			if err := s.deps.Store.SetSetting(ctx, "auth_password", ""); err != nil {
				return err
			}
		} else {
			if len(pwd) < 4 {
				return domain.Errorf(domain.CodeValidation, "密码至少 4 位")
			}
			if err := s.deps.Store.SetSetting(ctx, "auth_password", hashPassword(pwd)); err != nil {
				return err
			}
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
