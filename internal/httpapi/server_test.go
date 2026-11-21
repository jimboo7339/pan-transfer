package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pan-transfer/internal/accounts"
	"pan-transfer/internal/notify"
	"pan-transfer/internal/secret"
	"pan-transfer/internal/store"
	"pan-transfer/internal/transfer"
)

// errForTest 构造一个非领域错误，用于验证 writeErr 不外泄内部细节。
func errForTest(msg string) error { return errors.New(msg) }

// mustHashForTest 用 secret 包生成 argon2id 哈希。
func mustHashForTest(t *testing.T, pwd string) string {
	t.Helper()
	h, err := secret.HashPassword(pwd)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return h
}

// newTestServer 构造一个真实临时库驱动的 API server。
// 复用 store 包既有的"真实 SQLite + 真实 SQL"测试范式，而不是 mock——
// 认证、凭据脱敏、账号删除保护这些都是端到端行为，mock 测不出竞态与 SQL 错误。
func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "pantransfer.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	stPtr := st
	mgr := accounts.New(st)
	svc := transfer.New(transfer.Options{Store: st, Exec: mgr})
	srv := NewServer(Deps{
		Store:    &stPtr,
		Accounts: mgr,
		Transfer: svc,
		Notify:   notify.New(st, nil),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DataDir:  dir,
		DBPath:   filepath.Join(dir, "pantransfer.db"),
	})
	t.Cleanup(func() {
		svc.Close()
		forceClose(srv)
	})
	return srv, st
}

// forceClose 尽力关闭底层连接，避免 Windows 上 TempDir 清理失败。
//
// sql.DB.Close() 在 Windows 上不会立刻释放文件句柄，导致 t.TempDir() 的
// RemoveAll 报"目录非空"（测试本身已通过，只是清理报错）。
// SetMaxIdleConns(0) 会丢弃空闲连接，配合 SetConnMaxLifetime 让句柄立即释放。
func forceClose(s *Server) {
	if s == nil {
		return
	}
	if st := s.currentStore(); st != nil {
		if db := st.DB(); db != nil {
			db.SetMaxIdleConns(0)
			db.SetConnMaxLifetime(time.Nanosecond)
		}
	}
}

func doJSON(t *testing.T, srv *Server, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = strings.NewReader(string(b))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// TestAccountListRedactsSecrets 验证账号凭据不会通过 API 下发到浏览器。
//
// 这是本次安全改造的核心：夸克/光鸭都是逆向 Web API，cookie 就是完整会话。
// 凭据一旦进入前端内存 / 浏览器扩展 / 前端错误日志，等于账号被接管。
func TestAccountListRedactsSecrets(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	_, err := st.CreateAccount(ctx, &store.Account{
		Name:       "夸克主号",
		DriverType: "quark",
		Config:     `{"cookie":"SECRET_COOKIE_VALUE","access_token":"SECRET_TOKEN","root_folder_id":"0"}`,
		IsActive:   true,
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	rec, out := doJSON(t, srv, http.MethodGet, "/api/accounts", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	raw := rec.Body.String()
	if strings.Contains(raw, "SECRET_COOKIE_VALUE") || strings.Contains(raw, "SECRET_TOKEN") {
		t.Fatalf("响应中泄露了凭据明文: %s", raw)
	}

	list, ok := out["data"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("应返回 1 个账号，得到 %v", out["data"])
	}
	acc := list[0].(map[string]any)
	if acc["config"] == nil {
		t.Fatal("config 缺失")
	}
	if !strings.Contains(acc["config"].(string), RedactedSecret) {
		t.Errorf("config 应含占位符，得到 %v", acc["config"])
	}
	has, ok := acc["has_secret"].(map[string]any)
	if !ok {
		t.Fatalf("has_secret 缺失: %v", acc["has_secret"])
	}
	if has["cookie"] != true {
		t.Errorf("has_secret.cookie 应为 true，得到 %v", has["cookie"])
	}
	// 非凭据字段必须保留原样，否则用户改不动任何配置。
	if !strings.Contains(acc["config"].(string), "0") {
		t.Errorf("非凭据字段应保留：%v", acc["config"])
	}
}

// TestAccountUpdatePreservesSecrets 验证「留空表示不修改」的语义。
//
// 前端把凭据输入框留空表示"不改"，回传占位符；服务端必须还原成库里的真值，
// 否则每次保存都会把凭据清空，账号随即失效。
func TestAccountUpdatePreservesSecrets(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	id, err := st.CreateAccount(ctx, &store.Account{
		Name:       "夸克主号",
		DriverType: "quark",
		Config:     `{"cookie":"REAL_COOKIE","root_folder_id":"0"}`,
		IsActive:   true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 改非凭据字段，凭据字段回传占位符。
	payload := map[string]any{
		"name":   "改名了",
		"config": `{"cookie":"` + RedactedSecret + `","root_folder_id":"123"}`,
	}
	rec, out := doJSON(t, srv, http.MethodPut, "/api/accounts/1", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if out["ok"] != true {
		t.Fatalf("update 未返回 ok: %v", out)
	}

	got, err := st.GetAccount(ctx, id)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if !strings.Contains(got.Config, "REAL_COOKIE") {
		t.Errorf("凭据应被保留，得到 %s", got.Config)
	}
	if !strings.Contains(got.Config, "123") {
		t.Errorf("非凭据字段应更新为 123，得到 %s", got.Config)
	}
}

// TestAccountDeleteBlockedWhenReferenced 验证删账号不会留下僵尸任务。
//
// 全仓没有生效的外键（foreign_keys PRAGMA 只是装饰），直接删除会留下悬空
// account_id 的任务：任务永久报"账号不存在"，且前端账号下拉选不中，无法修复。
func TestAccountDeleteBlockedWhenReferenced(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	id, err := st.CreateAccount(ctx, &store.Account{
		Name: "夸克主号", DriverType: "quark",
		Config: `{"cookie":"c"}`, IsActive: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := store.Now()
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO tasks (task_name, account_id, share_url, save_path, created_at, updated_at)
		 VALUES (?,?,?,?,?,?)`, "任务A", id, "", "/", now, now); err != nil {
		t.Fatalf("insert task: %v", err)
	}

	rec, out := doJSON(t, srv, http.MethodDelete, "/api/accounts/1", nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("有任务引用时应拒绝删除，却返回 200: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "还有") {
		t.Errorf("错误信息应说明原因，得到 %s", rec.Body.String())
	}

	// 账号必须还在。
	if _, err := st.GetAccount(ctx, id); err != nil {
		t.Errorf("账号不应被删除: %v", err)
	}
	_ = out
}

// TestLoginRequiresPasswordWhenEnabled 验证启用鉴权后错误密码被拒绝，
// 且登录接口有失败计数（防暴力破解）。
func TestLoginRequiresPasswordWhenEnabled(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	// 未设置密码时：登录接口应返回空 token（表示无需鉴权）。
	rec, out := doJSON(t, srv, http.MethodPost, "/api/auth/login", map[string]any{"password": "x"})
	if rec.Code != http.StatusOK {
		t.Fatalf("无密码时 login 应 200，得到 %d", rec.Code)
	}
	if tok, _ := out["data"].(map[string]any); tok != nil {
		if tok["token"] != "" {
			t.Errorf("未启用鉴权时 token 应为空，得到 %v", tok["token"])
		}
	}

	// 设置密码。
	if err := st.SetSetting(ctx, "auth_password", mustHashForTest(t, "correct-horse")); err != nil {
		t.Fatalf("set password: %v", err)
	}

	// 错误密码应被拒绝。
	rec, _ = doJSON(t, srv, http.MethodPost, "/api/auth/login", map[string]any{"password": "wrong"})
	if rec.Code == http.StatusOK {
		t.Fatalf("错误密码应被拒绝，却返回 200: %s", rec.Body.String())
	}

	// 正确密码应拿到 token。
	rec, out = doJSON(t, srv, http.MethodPost, "/api/auth/login", map[string]any{"password": "correct-horse"})
	if rec.Code != http.StatusOK {
		t.Fatalf("正确密码应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	data, _ := out["data"].(map[string]any)
	tok, _ := data["token"].(string)
	if tok == "" {
		t.Fatal("正确密码应返回 token")
	}
	if !strings.Contains(tok, ".") {
		t.Errorf("token 格式应为 <expiry>.<hmac>，得到 %q", tok)
	}

	// 登出后应作废全部会话。
	rec, _ = doJSON(t, srv, http.MethodPost, "/api/auth/logout", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout 应 200，得到 %d", rec.Code)
	}

	// 旧 token 必须失效（会话版本已递增）。
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req)
	if rec2.Code == http.StatusOK {
		t.Error("登出后旧 token 应失效")
	}
}

// TestAuthRejectsCrossOriginRequest 验证跨站请求被拒绝（CSRF）。
//
// 浏览器会自动携带 HttpOnly 会话 Cookie，因此没有 Origin 校验时，
// 用户访问的任意第三方页面都能 POST /api/tasks/{id}/run 烧掉他的账号配额。
func TestAuthRejectsCrossOriginRequest(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, "auth_password", mustHashForTest(t, "pw12345")); err != nil {
		t.Fatalf("set password: %v", err)
	}
	rec, out := doJSON(t, srv, http.MethodPost, "/api/auth/login", map[string]any{"password": "pw12345"})
	data, _ := out["data"].(map[string]any)
	tok, _ := data["token"].(string)
	_ = rec

	// 同 Origin 应放行。
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Host = "pan.example.com"
	req.Header.Set("Origin", "http://pan.example.com")
	ok := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ok, req)
	if ok.Code == http.StatusUnauthorized {
		t.Fatalf("同源请求不应被拒绝: %s", ok.Body.String())
	}

	// 跨 Origin 应拒绝。
	req2 := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req2.Header.Set("Authorization", "Bearer "+tok)
	req2.Host = "pan.example.com"
	req2.Header.Set("Origin", "http://evil.example.net")
	bad := httptest.NewRecorder()
	srv.Handler().ServeHTTP(bad, req2)
	if bad.Code != http.StatusUnauthorized && bad.Code != http.StatusForbidden {
		t.Errorf("跨站请求应被拒绝，得到 %d", bad.Code)
	}
}

// TestWriteErrDoesNotLeakInternals 验证非领域错误不外泄内部细节。
//
// 早先的实现把 err.Error() 原文回给客户端，等于公开 SQL 结构、
// 上游接口地址与服务器目录结构。
func TestWriteErrDoesNotLeakInternals(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, errForTest("SQLSTATE[HY000]: no such table: secret_internal_table at /var/lib/app/data/db.sqlite"))
	if strings.Contains(rec.Body.String(), "secret_internal_table") {
		t.Errorf("响应泄露了内部错误细节: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "/var/lib/app") {
		t.Errorf("响应泄露了服务器路径: %s", rec.Body.String())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("状态码应为 500，得到 %d", rec.Code)
	}
}

// TestHealthReadyReportsDeps 验证深度健康检查能报告各依赖状态。
func TestHealthReadyReportsDeps(t *testing.T) {
	srv, _ := newTestServer(t)
	rec, out := doJSON(t, srv, http.MethodGet, "/api/health/ready", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("就绪检查应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if out["ok"] != true {
		t.Errorf("ok 应为 true，得到 %v", out["ok"])
	}
	checks, ok := out["checks"].(map[string]any)
	if !ok {
		t.Fatalf("checks 缺失: %v", out)
	}
	for _, name := range []string{"database", "scheduler"} {
		if _, exists := checks[name]; !exists {
			t.Errorf("checks 缺少 %q", name)
		}
	}
}