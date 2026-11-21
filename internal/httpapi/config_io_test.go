package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
	"pan-transfer/internal/transfer"
)

// TestConfigExportShape 验证导出结构包含可移植所需的全部字段。
func TestConfigExportShape(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	// 准备数据：分类 + 账号 + 任务 + 规则
	catID, err := st.SaveCategory(ctx, &store.TaskCategory{
		Name: "Emby 刷新", CallbackEnabled: true, CallbackMode: "round",
		CallbackMethod: "POST", CallbackURL: "http://192.168.1.10/hook",
		CallbackTimeoutSec: 15,
	})
	if err != nil {
		t.Fatalf("save category: %v", err)
	}
	accID, err := st.CreateAccount(ctx, &store.Account{
		Name: "夸克主号", DriverType: "quark",
		Config: `{"cookie":"SECRET_COOKIE"}`, IsActive: true,
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	now := nowForConfigTest()
	links := `["https://pan.quark.cn/s/abc"]`
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO tasks (id, task_name, account_id, share_url, share_links, save_path,
			pattern, replace, category_id, sort_index, skip_history, status, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		int64(1), "测试任务", accID, "https://pan.quark.cn/s/abc", links,
		"/影视", "", "", catID, 1, "global", 1, now, now); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if err := st.UpsertRule(ctx, ruleForTest("$TV_MAGIC", "剧集", "第(\\d+)集", "{TASKNAME}.S01E{E}.{EXT}")); err != nil {
		t.Fatalf("upsert rule: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config/export", nil))
	if rec.Code != http.StatusOK {
		// 导出失败时把真实错误打出来，而不是只看到一个 500。
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	// 关键：凭据绝不能出现在导出文件里。
	if strings.Contains(rec.Body.String(), "SECRET_COOKIE") {
		t.Error("导出文件中泄露了凭据明文")
	}

	var b configBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.SchemaVersion != currentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", b.SchemaVersion, currentSchemaVersion)
	}
	if b.Summary.TaskCount != 1 || b.Summary.CategoryCount != 1 || b.Summary.RuleCount != 1 {
		t.Errorf("summary = %+v", b.Summary)
	}
	if len(b.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(b.Tasks))
	}
	task := b.Tasks[0]
	if task.Name != "测试任务" {
		t.Errorf("task name = %q", task.Name)
	}
	if task.AccountName != "夸克主号" {
		t.Errorf("task should reference account by name, got %q", task.AccountName)
	}
	// 凭据存在性必须透出，让用户在导入时知道要不要重新扫码。
	if !task.HasCredentials {
		t.Error("HasCredentials should be true (cookie is set)")
	}
	if task.CategoryName != "Emby 刷新" {
		t.Errorf("category name = %q", task.CategoryName)
	}
	if len(task.Links) != 1 || task.Links[0] != "https://pan.quark.cn/s/abc" {
		t.Errorf("links = %v", task.Links)
	}
}

// TestConfigImportRoundTrip 验证导出后再导入，能在空实例上重建。
func TestConfigImportRoundTrip(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	// 源数据：分类 + 任务 + 规则
	catID, _ := st.SaveCategory(ctx, &store.TaskCategory{
		Name: "Emby 刷新", CallbackEnabled: true, CallbackMode: "round",
		CallbackMethod: "POST", CallbackURL: "http://192.168.1.10/hook",
		CallbackTimeoutSec: 15,
	})
	accID, _ := st.CreateAccount(ctx, &store.Account{
		Name: "夸克主号", DriverType: "quark",
		Config: `{"cookie":"c"}`, IsActive: true,
	})
	now := nowForConfigTest()
	links := `["https://pan.quark.cn/s/x"]`
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO tasks (id, task_name, account_id, share_url, share_links, save_path,
			category_id, status, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		int64(1), "源任务", accID, "https://pan.quark.cn/s/x", links,
		"/影视", catID, 1, now, now); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// 导出
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d", rec.Code)
	}
	bundle := rec.Body.Bytes()

	// 模拟新实例：另起一个 server，账号同名但无凭据。
	srv2, st2 := newTestServer(t)
	if _, err := st2.CreateAccount(ctx, &store.Account{
		Name: "夸克主号", DriverType: "quark",
		Config: `{}`, IsActive: true, // 无 cookie
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}

	// 预览
	var preview importPreview
	if code := postJSON(t, srv2, "/api/config/import/preview", json.RawMessage(bundle), &preview); code != http.StatusOK {
		t.Fatalf("preview status = %d", code)
	}
	if !preview.Valid {
		t.Fatalf("preview should be valid, errors = %v", preview.Errors)
	}
	if len(preview.TaskNames) != 1 || preview.TaskNames[0] != "源任务" {
		t.Errorf("preview tasks = %v", preview.TaskNames)
	}
	// 目标实例没配凭据，必须给出警告而不是静默导入。
	if len(preview.Warnings) == 0 {
		t.Error("preview should warn about missing credentials")
	}

	// 执行导入
	var res applyResult
	if code := postJSON(t, srv2, "/api/config/import", json.RawMessage(bundle), &res); code != http.StatusOK {
		t.Fatalf("import status = %d", code)
	}
	if res.TasksCreated != 1 {
		t.Errorf("tasks_created = %d, want 1 (failed: %v)", res.TasksCreated, res.Failed)
	}
	if res.CategoriesCreated != 1 {
		t.Errorf("categories_created = %d, want 1", res.CategoriesCreated)
	}

	// 验证新实例里确实有这条任务，且路径/链接都对。
	svc2 := srv2.transfer()
	tasks, err := svc2.ListTasks(ctx)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasks))
	}
	if tasks[0].TaskName != "源任务" || tasks[0].SavePath != "/影视" {
		t.Errorf("imported task = %+v", tasks[0])
	}
	if got := transfer.LinksOf(tasks[0]); len(got) != 1 || got[0] != "https://pan.quark.cn/s/x" {
		t.Errorf("imported links = %v", got)
	}
	if tasks[0].CategoryID != catID && tasks[0].CategoryID == 0 {
		t.Error("imported task should be linked to the category")
	}
}

// TestConfigImportRejectsUnknownAccount 验证账号缺失时任务被跳过而非写坏。
//
// 全仓没有生效的外键，硬塞一个不存在的 account_id 会留下永久报
// 「账号不存在」的僵尸任务，且前端账号下拉选不中，用户无法修复。
func TestConfigImportRejectsUnknownAccount(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	bundle := configBundle{
		SchemaVersion: 1,
		Tasks: []bundleTask{{
			Name:        "孤儿任务",
			AccountName: "不存在的账号",
			Links:       []string{"https://pan.quark.cn/s/y"},
			SavePath:    "/影视",
			Status:      "running",
		}},
	}

	var preview importPreview
	postJSON(t, srv, "/api/config/import/preview", bundle, &preview)
	if len(preview.Skipped) != 1 {
		t.Errorf("preview skipped = %v, want 1 entry", preview.Skipped)
	}

	// 实际导入也不应创建任何任务。
	var res applyResult
	postJSON(t, srv, "/api/config/import", bundle, &res)
	if res.TasksCreated != 0 {
		t.Errorf("tasks_created = %d, want 0", res.TasksCreated)
	}
	if res.TasksSkipped != 1 {
		t.Errorf("tasks_skipped = %d, want 1", res.TasksSkipped)
	}
	if tasks, _ := srv.transfer().ListTasks(ctx); len(tasks) != 0 {
		t.Errorf("应该没有任务，实际 %d", len(tasks))
	}
}

// TestConfigImportRejectsBadRuleKey 验证规则标识校验。
func TestConfigImportRejectsBadRuleKey(t *testing.T) {
	srv, _ := newTestServer(t)

	bundle := configBundle{
		SchemaVersion: 1,
		Rules: []bundleRule{{
			Key:     "NO_DOLLAR", // 缺少 $ 前缀
			Pattern: "x", Replace: "y", Enabled: true,
		}},
	}
	var preview importPreview
	postJSON(t, srv, "/api/config/import/preview", bundle, &preview)
	if preview.Valid {
		t.Error("规则标识缺少 $ 时应校验失败")
	}
	if len(preview.Errors) == 0 {
		t.Error("应给出错误说明")
	}
}

// TestConfigImportRejectsFutureVersion 验证版本高于当前时拒绝导入。
func TestConfigImportRejectsFutureVersion(t *testing.T) {
	srv, _ := newTestServer(t)

	bundle := configBundle{SchemaVersion: currentSchemaVersion + 5}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/import", jsonBody(t, bundle))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Errorf("高版本配置应被拒绝，得到 200: %s", rec.Body.String())
	}
}

// jsonBody 把任意值序列化为请求体。
func jsonBody(t *testing.T, v any) *strings.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return strings.NewReader(string(b))
}

// postJSON 发一个 JSON 请求并解出 data 字段。
//
// 注意信封：所有 respond() 的响应都是 {"ok":true,"data":{...}}，
// 直接 Unmarshal 到目标结构会拿到零值（这正是本测试初版失败的原因）。
func postJSON(t *testing.T, srv *Server, path string, v any, out any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, jsonBody(t, v))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if out != nil {
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("解析响应信封失败: %v (body=%s)", err, rec.Body.String())
		}
		if len(env.Data) == 0 {
			t.Fatalf("响应缺少 data 字段: %s", rec.Body.String())
		}
		if err := json.Unmarshal(env.Data, out); err != nil {
			t.Fatalf("解析 data 失败: %v (data=%s)", err, string(env.Data))
		}
	}
	return rec.Code
}

// nowForConfigTest 返回与存储层一致的时间格式。
func nowForConfigTest() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// ruleForTest 构造一条测试规则。
func ruleForTest(key, label, pattern, replace string) *domain.MagicRegexRule {
	return &domain.MagicRegexRule{
		Key: key, Label: label, Pattern: pattern, Replace: replace, Enabled: true,
	}
}
