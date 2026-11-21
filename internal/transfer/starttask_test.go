package transfer

import (
	"context"
	"testing"
	"time"
)

// seedTask 直接写一条任务记录（绕开 Service 层的账号校验）。
//
// 这是本次改造的核心断言：同步路径下 HTTP 请求要等整个转存流程（最长 30 分钟），
// 反代 60s 超时即断、手机切后台即无响应、无法取消。
func TestStartTaskReturnsImmediately(t *testing.T) {
	svc := newTestService(t)
	seedTask(t, svc, 1, "测试任务")

	start := time.Now()
	job, err := svc.StartTask(context.Background(), 1)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	// 阈值取 3 秒而不是 500ms：Windows 上并行跑多个包时，
	// goroutine 调度 + SQLite 写入偶发会超过 500ms，导致这个测试间歇性失败。
	// 但它要防的 bug 是"同步等待整个转存流程"（runTimeout = 30 分钟），
	// 3 秒的阈值与之相差四个数量级，仍能可靠地抓住回归。
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("StartTask 应立即返回，实际耗时 %s", elapsed)
	}

	if job.ID == 0 {
		t.Error("应返回非 0 的 job_id")
	}
	if job.Status != "running" {
		t.Errorf("初始状态应为 running，得到 %q", job.Status)
	}
	if job.TaskID != 1 || job.TaskName != "测试任务" {
		t.Errorf("job 应携带任务信息，得到 %+v", job)
	}

	// 句柄应能在注册表里查到。
	got, ok := svc.GetJob(job.ID)
	if !ok {
		t.Fatal("应能查到刚创建的 job")
	}
	if got.Status != "running" {
		t.Errorf("查到的状态应为 running，得到 %q", got.Status)
	}
}

// TestStartTaskRejectsPaused 验证暂停的任务不能被启动。
func TestStartTaskRejectsPaused(t *testing.T) {
	svc := newTestService(t)
	seedTask(t, svc, 2, "暂停任务")
	if err := svc.st.SetTaskStatus(context.Background(), 2, "paused"); err != nil {
		t.Fatalf("暂停任务失败: %v", err)
	}

	if _, err := svc.StartTask(context.Background(), 2); err == nil {
		t.Fatal("暂停的任务不应能启动")
	}
}

// TestStartTaskRejectsMissing 验证不存在的任务返回错误。
func TestStartTaskRejectsMissing(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.StartTask(context.Background(), 999); err == nil {
		t.Fatal("不存在的任务应返回错误")
	}
}

// TestCancelJobRejectsFinished 验证已结束的 job 不能被取消。
func TestCancelJobRejectsFinished(t *testing.T) {
	svc := newTestService(t)
	r := svc.jobs
	js := r.create(1, "A", "manual")
	js.finish("success", nil, "")

	if err := svc.CancelJob(js.ID); err == nil {
		t.Fatal("已结束的 job 不应能取消")
	}
}

// TestCancelJobRejectsMissing 验证不存在的 job 返回错误。
func TestCancelJobRejectsMissing(t *testing.T) {
	svc := newTestService(t)
	if err := svc.CancelJob(999); err == nil {
		t.Fatal("不存在的 job 应返回错误")
	}
}

// TestJobStatusProgressesToTerminal 验证 job 最终会进入终态并被查到。
//
// 用真实任务跑一次会触网，因此这里直接构造一个会立刻失败的运行：
// 任务存在但账号 999 不存在 → RunTask 在 GetAccount 阶段失败 →
// job 应被标记为 failed 而不是永远停在 running。
func TestJobStatusProgressesToTerminal(t *testing.T) {
	svc := newTestService(t)
	seedTask(t, svc, 3, "无账号任务") // account_id = 999（不存在）

	job, err := svc.StartTask(context.Background(), 3)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	// 等待 job 结束（GetAccount 会立刻失败）。
	deadline := time.Now().Add(10 * time.Second)
	var last JobView
	for time.Now().Before(deadline) {
		v, ok := svc.GetJob(job.ID)
		if !ok {
			t.Fatal("job 不应被提前删除")
		}
		last = *v
		if v.Status != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if last.Status == "running" {
		t.Fatal("job 应进入终态，却一直是 running")
	}
	if last.Status != "failed" {
		t.Errorf("账号不存在时应失败，得到 %q（%s）", last.Status, last.Err)
	}
	if last.FinishedAt.IsZero() {
		t.Error("终态应有 FinishedAt")
	}
}

// TestListJobsReturnsNewestFirst 验证列表按新的在前（前端"最近运行"依赖这个顺序）。
func TestListJobsReturnsNewestFirst(t *testing.T) {
	svc := newTestService(t)
	a := svc.jobs.create(1, "A", "manual")
	a.finish("success", nil, "")
	b := svc.jobs.create(2, "B", "manual")
	b.finish("success", nil, "")

	list := svc.ListJobs()
	if len(list) != 2 {
		t.Fatalf("应有 2 个 job，实际 %d", len(list))
	}
	if list[0].ID != b.ID {
		t.Errorf("列表应新的在前，got %d,%d", list[0].ID, list[1].ID)
	}
}

// TestRemoveJobOnlyDeletesHandle 验证 RemoveJob 不影响任务本身的运行记录。
func TestRemoveJobOnlyDeletesHandle(t *testing.T) {
	svc := newTestService(t)
	js := svc.jobs.create(1, "A", "manual")
	js.finish("success", nil, "")

	svc.RemoveJob(js.ID)
	if _, ok := svc.GetJob(js.ID); ok {
		t.Error("RemoveJob 后句柄应被删除")
	}
}

// seedTask 直接写一条任务记录（绕开 Service 层的账号校验）。
//
// 注意 status 列在库里是整数（1=running / 0=paused，见 store.scanTask），
// 不是字符串；share_links 是 JSON 字符串。
func seedTask(t *testing.T, svc *Service, id int64, name string) {
	t.Helper()
	ctx := context.Background()
	now := nowForTest()
	if _, err := svc.st.DB().ExecContext(ctx,
		`INSERT INTO tasks (id, task_name, account_id, share_url, share_links, save_path,
			skip_history, status, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		id, name, int64(999), "", `[]`, "/", "global", 1, now, now); err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

// nowForTest 返回与存储层一致的时间格式。
func nowForTest() string { return time.Now().Format("2006-01-02 15:04:05") }
