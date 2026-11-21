package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
)

// seedTestTask 插入一条可直接落库的最小任务（绕开账号探测）。
//
// status 列在库里是整数（1=running / 0=paused），不是字符串。
func seedTestTask(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	task := &domain.DramaTask{
		TaskName:    name,
		AccountID:   1,
		SavePath:    "/影视",
		ShareLinks:  `["https://pan.quark.cn/s/abc"]`,
		SkipHistory: "global",
		Status:      "running",
		RunMode:     "manual",
	}
	id, err := st.CreateTask(context.Background(), task)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return id
}

// TestBatchPauseAndResume 验证批量暂停与恢复。
//
// 这是批量端点最典型的用途：临时把一批任务全部停下（比如要改账号），
// 再一次性恢复。此前只能一个个点，N 个任务就是 N 次等待 + N 次列表刷新。
func TestBatchPauseAndResume(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	ids := []int64{}
	for i := 0; i < 3; i++ {
		ids = append(ids, seedTestTask(t, st, "批量任务"))
	}

	// 批量暂停
	var pauseRes batchResult
	if code := postJSON(t, srv, "/api/tasks/batch", map[string]any{
		"ids": ids, "action": "pause",
	}, &pauseRes); code != http.StatusOK {
		t.Fatalf("batch pause status = %d", code)
	}
	if pauseRes.Succeeded != 3 || pauseRes.Failed != 0 {
		t.Errorf("pause: succeeded=%d failed=%d, items=%+v",
			pauseRes.Succeeded, pauseRes.Failed, pauseRes.Items)
	}

	// 全部应为 paused
	tasks, _ := srv.transfer().ListTasks(ctx)
	for _, tk := range tasks {
		if tk.Status != "paused" {
			t.Errorf("task %q status = %q, want paused", tk.TaskName, tk.Status)
		}
	}

	// 批量恢复
	var resumeRes batchResult
	if code := postJSON(t, srv, "/api/tasks/batch", map[string]any{
		"ids": ids, "action": "resume",
	}, &resumeRes); code != http.StatusOK {
		t.Fatalf("batch resume status = %d", code)
	}
	if resumeRes.Succeeded != 3 {
		t.Errorf("resume: succeeded=%d, items=%+v", resumeRes.Succeeded, resumeRes.Items)
	}
	tasks, _ = srv.transfer().ListTasks(ctx)
	for _, tk := range tasks {
		if tk.Status == "paused" {
			t.Errorf("task %q should be running after resume", tk.TaskName)
		}
	}
}

// TestBatchSetCategory 验证批量改分类。
func TestBatchSetCategory(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	catID, err := st.SaveCategory(ctx, &store.TaskCategory{
		Name: "新分类", CallbackEnabled: false,
	})
	if err != nil {
		t.Fatalf("save category: %v", err)
	}
	id := seedTestTask(t, st, "改分类任务")

	var res batchResult
	if code := postJSON(t, srv, "/api/tasks/batch", map[string]any{
		"ids": []int64{id}, "action": "set_category", "category_id": catID,
	}, &res); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if res.Succeeded != 1 {
		t.Fatalf("succeeded = %d, items=%+v", res.Succeeded, res.Items)
	}

	task, err := srv.transfer().GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.CategoryID != catID {
		t.Errorf("category_id = %d, want %d", task.CategoryID, catID)
	}
}

// TestBatchSetCategoryRejectsUnknown 验证分类不存在时不留悬空引用。
//
// 虽然全仓没有生效的外键，但指向不存在的 category_id 会让分类回调静默失效，
// 用户排查半天也找不到原因——所以在入口拦住比事后补救便宜。
func TestBatchSetCategoryRejectsUnknown(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	id := seedTestTask(t, st, "任务")

	var res batchResult
	postJSON(t, srv, "/api/tasks/batch", map[string]any{
		"ids": []int64{id}, "action": "set_category", "category_id": 99999,
	}, &res)
	if res.Succeeded != 0 || res.Failed != 1 {
		t.Errorf("应失败：succeeded=%d failed=%d", res.Succeeded, res.Failed)
	}
	if len(res.Items) != 1 || res.Items[0].Message != "分类不存在" {
		t.Errorf("items = %+v", res.Items)
	}

	// 原分类不能被改写。
	task, _ := srv.transfer().GetTask(ctx, id)
	if task.CategoryID != 0 {
		t.Errorf("category_id 应保持不变（0），得到 %d", task.CategoryID)
	}
}

// TestBatchDelete 验证批量删除。
func TestBatchDelete(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()

	ids := []int64{}
	for i := 0; i < 2; i++ {
		ids = append(ids, seedTestTask(t, st, "待删除"))
	}

	var res batchResult
	if code := postJSON(t, srv, "/api/tasks/batch", map[string]any{
		"ids": ids, "action": "delete",
	}, &res); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if res.Succeeded != 2 {
		t.Errorf("succeeded = %d, items=%+v", res.Succeeded, res.Items)
	}

	tasks, _ := srv.transfer().ListTasks(ctx)
	if len(tasks) != 0 {
		t.Errorf("应无任务，实际 %d", len(tasks))
	}
}

// TestBatchRejectsEmptyIDs 验证空 ids 被拒绝（避免误删/误操作）。
func TestBatchRejectsEmptyIDs(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/batch",
		jsonBody(t, map[string]any{"ids": []int64{}, "action": "delete"}))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Error("空 ids 应被拒绝")
	}
}

// TestBatchRejectsUnknownAction 验证未知动作被拒绝。
func TestBatchRejectsUnknownAction(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/batch",
		jsonBody(t, map[string]any{"ids": []int64{1}, "action": "explode"}))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Error("未知动作应被拒绝")
	}
}

// TestBatchRejectsInvalidMode 验证 set_mode 的枚举校验。
func TestBatchRejectsInvalidMode(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/batch",
		jsonBody(t, map[string]any{"ids": []int64{1}, "action": "set_mode", "run_mode": "turbo"}))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Error("非法 run_mode 应被拒绝")
	}
}
