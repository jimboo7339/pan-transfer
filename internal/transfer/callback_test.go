package transfer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
)

func TestRenderTemplate(t *testing.T) {
	vars := map[string]string{"task_name": "嫁金钗", "count": "3"}
	cases := []struct{ in, want string }{
		{"任务 {task_name} 转存 {count} 个", "任务 嫁金钗 转存 3 个"},
		{"未知 {nope} 保持", "未知 {nope} 保持"},
		{`{"a":{"b":1}}`, `{"a":{"b":1}}`}, // 普通 JSON 花括号不受影响
		{"", ""},
		// JSON 模板：开头的结构花括号不能吞掉后面的占位符（回归）
		{`{"category":"{task_name}","count":{count}}`, `{"category":"嫁金钗","count":3}`},
	}
	for _, c := range cases {
		if got := renderTemplate(c.in, vars); got != c.want {
			t.Errorf("renderTemplate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEncodeQueryTemplate(t *testing.T) {
	vars := map[string]string{"save_path": "/影视/嫁金钗", "count": "2"}
	got := encodeQueryTemplate("http://x.local/cb?dir={save_path}&count={count}&fixed=abc", vars)
	want := "http://x.local/cb?dir=%2F%E5%BD%B1%E8%A7%86%2F%E5%AB%81%E9%87%91%E9%92%97&count=2&fixed=abc"
	if got != want {
		t.Errorf("encodeQueryTemplate = %q, want %q", got, want)
	}
	if got := encodeQueryTemplate("http://x.local/plain", vars); got != "http://x.local/plain" {
		t.Errorf("no-query url = %q", got)
	}
}

func TestBuildVarsJSONSafety(t *testing.T) {
	vars := buildTaskCallbackVars(&domain.DramaTask{ID: 1, TaskName: `带"引号"任务`, SavePath: "/tv"},
		"分类", &RunResult{Status: "success", TransferCount: 1}, []string{`文"件.mkv`}, "manual")
	// 文件名里的引号必须是合法 JSON（已转义）
	var files []string
	if err := json.Unmarshal([]byte(vars["transferred_files_json"]), &files); err != nil {
		t.Fatalf("transferred_files_json 不是合法 JSON: %v", err)
	}
	if len(files) != 1 || files[0] != `文"件.mkv` {
		t.Errorf("files = %v", files)
	}
}

// 轮次聚合：两个任务入轮，最后一个完成时才回调一次，payload 汇总两个任务。
func TestRoundCallbackFlow(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		mu.Lock()
		bodies = append(bodies, string(buf[:n]))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer ts.Close()

	st, err := store.Open(t.TempDir() + "/cb.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	enabled := true
	catID, err := st.SaveCategory(ctx, &store.TaskCategory{
		Name: "测试分类", CallbackEnabled: enabled, CallbackMode: "round",
		CallbackMethod: "POST", CallbackURL: ts.URL + "/cb", CallbackBody: `{"status":"{status}","count":{total_transferred},"tasks":{task_results_json}}`,
	})
	if err != nil {
		t.Fatalf("save category: %v", err)
	}
	svc := New(Options{Store: st})
	taskA, _ := st.CreateTask(ctx, &domain.DramaTask{TaskName: "任务A", AccountID: 1, SavePath: "/tv/a", Status: "running", CategoryID: catID})
	taskB, _ := st.CreateTask(ctx, &domain.DramaTask{TaskName: "任务B", AccountID: 1, SavePath: "/tv/b", Status: "running", CategoryID: catID})
	a, _ := st.GetTask(ctx, taskA)
	b, _ := st.GetTask(ctx, taskB)

	// 模拟定时批量：两个任务先后入轮
	svc.joinCategoryRound(a)
	svc.joinCategoryRound(b)
	// 第一个完成：不应回调
	svc.settleCategory(a, &RunResult{TaskID: taskA, Status: "success", TransferCount: 2}, []string{"a1.mkv", "a2.mkv"}, "schedule")
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	got := len(bodies)
	mu.Unlock()
	if got != 0 {
		t.Fatalf("第一个任务完成就回调了 %d 次，应为 0", got)
	}
	// 第二个完成：聚合回调一次
	svc.settleCategory(b, &RunResult{TaskID: taskB, Status: "success", TransferCount: 1}, []string{"b1.mkv"}, "schedule")
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got = len(bodies)
		mu.Unlock()
		if got >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != 1 {
		t.Fatalf("轮次回调次数 = %d, want 1", got)
	}
	mu.Lock()
	body := bodies[0]
	mu.Unlock()
	if !strings.Contains(body, `"task_results_json"`) == false {
		t.Error("payload 缺少汇总字段") // 便于排查的宽松检查
	}
	if !strings.Contains(body, "任务A") || !strings.Contains(body, "任务B") {
		t.Errorf("payload 未汇总两个任务: %s", body)
	}
	if !strings.Contains(body, `"count":3`) {
		t.Errorf("payload total_transferred 应为 3: %s", body)
	}
	logs, err := st.ListCallbackLogs(ctx, catID, 10)
	if err != nil || len(logs) != 1 || !logs[0].OK {
		t.Errorf("callback logs = %v, %v; want 1 条成功记录", len(logs), err)
	}
	// 轮次已关闭：再次 settle 走单任务新轮次
	svc.settleCategory(a, &RunResult{TaskID: taskA, Status: "failed"}, nil, "manual")
	deadline = time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got = len(bodies)
		mu.Unlock()
		if got >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != 2 {
		t.Errorf("关闭后的单任务轮次应再回调 1 次，总次数 = %d", got)
	}
}

// per_task + GET：每次完成即回调，查询值需 URL 编码。
func TestPerTaskGetCallback(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.String())
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer ts.Close()

	st, err := store.Open(t.TempDir() + "/cb2.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	enabled := true
	catID, err := st.SaveCategory(ctx, &store.TaskCategory{
		Name: "GET分类", CallbackEnabled: enabled, CallbackMode: "per_task",
		CallbackMethod: "GET", CallbackURL: ts.URL + "/refresh?dir={save_path}&count={transfer_count}",
	})
	if err != nil {
		t.Fatalf("save category: %v", err)
	}
	svc := New(Options{Store: st})
	taskID, _ := st.CreateTask(ctx, &domain.DramaTask{TaskName: "任务G", AccountID: 1, SavePath: "/影视/剧", Status: "running", CategoryID: catID})
	g, _ := st.GetTask(ctx, taskID)
	svc.settleCategory(g, &RunResult{TaskID: taskID, Status: "success", TransferCount: 2}, []string{"x.mkv"}, "manual")
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(paths)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Fatalf("GET 回调次数 = %d, want 1", len(paths))
	}
	if !strings.HasPrefix(paths[0], "/refresh?dir=%2F") || !strings.Contains(paths[0], "count=2") {
		t.Errorf("GET 路径未按预期编码/渲染: %s", paths[0])
	}
	logs, _ := st.ListCallbackLogs(ctx, catID, 5)
	if len(logs) != 1 || logs[0].Kind != "task" {
		t.Errorf("callback log kind = %v", logs)
	}
}
