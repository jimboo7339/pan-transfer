package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
)

// TestSearchFindsTaskByName 验证按任务名搜索能命中，且路由指向任务页。
func TestSearchFindsTaskByName(t *testing.T) {
	srv, st := newTestServer(t)
	seedTestTask(t, st, "海贼王 第01集")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/search?q="+url.QueryEscape("海贼王"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp searchResponse
	decodeDataInto(t, rec, &resp)

	if resp.Total == 0 {
		t.Fatalf("应命中任务，实际 %s", rec.Body.String())
	}
	found := false
	for _, g := range resp.Groups {
		if g.Kind != "task" {
			continue
		}
		for _, h := range g.Hits {
			if h.Title == "海贼王 第01集" {
				found = true
				if h.Route != "/tasks" {
					t.Errorf("route = %q, want /tasks", h.Route)
				}
			}
		}
	}
	if !found {
		t.Errorf("分组里应有任务命中，实际 %+v", resp.Groups)
	}
}

// TestSearchFindsFileInHistory 验证能搜到已转存文件名。
//
// 「第 7 集转存成功了吗」是最高频的搜索场景，答案就在 saved_names 里。
func TestSearchFindsFileInHistory(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	taskID := seedTestTask(t, st, "海贼王")

	if err := st.AddSavedNames(ctx, taskID, []store.SavedName{
		{TaskID: taskID, NormName: "海贼王.s01e07.mkv", FileName: "海贼王.s01e07.mkv"},
	}); err != nil {
		t.Fatalf("add saved names: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/search?q="+url.QueryEscape("s01e07")+"&scope=file", nil))
	var resp searchResponse
	decodeDataInto(t, rec, &resp)

	found := false
	for _, g := range resp.Groups {
		if g.Kind == "file" && len(g.Hits) > 0 {
			found = true
			if g.Hits[0].TaskID != taskID {
				t.Errorf("file hit task_id = %d, want %d", g.Hits[0].TaskID, taskID)
			}
		}
	}
	if !found {
		t.Errorf("应搜到已转存文件，实际 %+v", resp.Groups)
	}
}

// TestSearchEmptyQueryRejected 验证空关键词被拒绝。
//
// 空查询会让每个域都返回全部内容（LIKE '%%' 匹配一切），
// 既浪费数据库资源，也让搜索框变成"列出全部"的入口。
func TestSearchEmptyQueryRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=", nil))
	if rec.Code == http.StatusOK {
		t.Error("空关键词必须被拒绝，否则 LIKE '%%' 会匹配一切")
	}
}

// TestSearchTooLongQueryRejected 验证超长关键词被拒绝。
func TestSearchTooLongQueryRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'a'
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/search?q="+url.QueryEscape(string(long)), nil))
	if rec.Code == http.StatusOK {
		t.Error("超长关键词应被拒绝")
	}
}

// TestSearchScopeFilter 验证 scope 参数能限定搜索域。
//
// 没有 scope 时用户会被一堆不相关的结果淹没；
// 有 scope 时前端可以做「在当前页内搜索」这种精确操作。
func TestSearchScopeFilter(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	if _, err := st.CreateAccount(ctx, testAccount("我的夸克账号", `{"cookie":"c"}`)); err != nil {
		t.Fatalf("create account: %v", err)
	}
	seedTestTask(t, st, "我的任务")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/search?q="+url.QueryEscape("我的")+"&scope=account", nil))
	var resp searchResponse
	decodeDataInto(t, rec, &resp)

	for _, g := range resp.Groups {
		if g.Kind != "account" {
			t.Errorf("scope=account 却返回了 %q 分组", g.Kind)
		}
	}
	if resp.Total == 0 {
		t.Error("scope=account 应命中账号")
	}
}

// TestSearchTruncatesLargeScopes 验证大结果集被截断。
//
// 搜索是"扫一眼"的交互，返回 200 条结果等于没返回。
func TestSearchTruncatesLargeScopes(t *testing.T) {
	srv, st := newTestServer(t)
	for i := 0; i < 20; i++ {
		seedTestTask(t, st, "同名任务")
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/search?q="+url.QueryEscape("同名任务")+"&scope=task", nil))
	var resp searchResponse
	decodeDataInto(t, rec, &resp)

	if !resp.Truncated {
		t.Errorf("20 条结果应被截断并置 Truncated，实际 body=%s", rec.Body.String())
	}
	for _, g := range resp.Groups {
		if g.Kind == "task" && len(g.Hits) > maxPerScope {
			t.Errorf("单组返回 %d 条，超过上限 %d", len(g.Hits), maxPerScope)
		}
	}
}

// TestSearchRejectsUnknownScope 验证拼错的 scope 被明确拒绝。
//
// 为什么值得单独测：scope 拼错（如 scope=tasks 而合法值是 task）会让
// 所有分组都被跳过，用户看到"搜不到"，但错误信息是"成功返回空结果"——
// 这种静默失败比直接报错难查得多。
func TestSearchRejectsUnknownScope(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/search?q=x&scope=tasks", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("未知 scope 应返回 400，得到 200: %s", rec.Body.String())
	}
}

// decodeDataInto 从响应信封中解出 data 到 out。
func decodeDataInto(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v (body=%s)", err, rec.Body.String())
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		t.Fatalf("解析 data 失败: %v (data=%s)", err, string(env.Data))
	}
}

// 确保 domain 被引用（ruleForTest 返回它）。
var _ = domain.CodeValidation
