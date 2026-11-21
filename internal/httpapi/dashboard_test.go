package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDashboardShape 验证仪表盘返回了健康总览所需的全部字段。
func TestDashboardShape(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := t.Context()

	// 造数据：一个账号 + 一条任务 + 一条成功运行 + 一条失败运行
	if _, err := st.CreateAccount(ctx, testAccount("夸克主号", `{"cookie":"c"}`)); err != nil {
		t.Fatalf("create account: %v", err)
	}
	taskID := seedTestTask(t, st, "仪表盘任务")

	// 成功运行（今天）
	runID, err := st.CreateRun(ctx, taskID, "https://pan.quark.cn/s/a")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.FinishRun(ctx, runID, "success", "转存 3 个文件", "树", "", 3); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	// 失败运行
	runID2, _ := st.CreateRun(ctx, taskID, "https://pan.quark.cn/s/b")
	_ = st.FinishRun(ctx, runID2, "failed", "分享链接已失效", "", "", 0)
	if err := st.UpdateTaskRunState(ctx, taskID,
		time.Now().Format("2006-01-02 15:04:05"), "failed", "分享链接已失效", ""); err != nil {
		t.Fatalf("update state: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/dashboard", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	data := decodeData[dashboardData](t, rec)

	// 任务概况
	if data.Tasks.Total != 1 || data.Tasks.Running != 1 {
		t.Errorf("tasks = %+v, want total=1 running=1", data.Tasks)
	}
	// 账号
	if data.Accounts.Total != 1 || data.Accounts.Active != 1 {
		t.Errorf("accounts = %+v", data.Accounts)
	}
	// 转存统计：今天应有 3 个文件、1 次运行
	if data.Transfers.TodayCount != 3 {
		t.Errorf("today_count = %d, want 3", data.Transfers.TodayCount)
	}
	if data.Transfers.TodayRuns != 2 {
		// 测试造了 2 条运行（1 成功 + 1 失败），"今天运行次数"统计的是
		// 运行条数而非成功数——用户关心"跑了几次"，不是"成功几次"。
		t.Errorf("today_runs = %d, want 2", data.Transfers.TodayRuns)
	}
	if len(data.Transfers.Last7Days) != 7 {
		t.Errorf("last_7_days = %d, want 7（前端画趋势条需要连续 7 个点）",
			len(data.Transfers.Last7Days))
	}
	// 失败告警：上次运行失败必须出现在告警里（这是静默失败的唯一信号）
	found := false
	for _, a := range data.Alerts {
		if a.Level == "error" && a.TaskID == taskID {
			found = true
		}
	}
	if !found {
		t.Errorf("告警里应有该任务的失败项，实际 %+v", data.Alerts)
	}
	// 最近失败列表
	if len(data.Failures) != 1 || data.Failures[0].TaskID != taskID {
		t.Errorf("recent_failures = %+v", data.Failures)
	}
}

// TestDashboardEmptyInstance 验证空实例不报错（新用户首次打开就能看到引导）。
func TestDashboardEmptyInstance(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/dashboard", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	data := decodeData[dashboardData](t, rec)
	if data.Tasks.Total != 0 {
		t.Errorf("tasks.total = %d, want 0", data.Tasks.Total)
	}
	// 空实例应提示"还没有添加网盘账号"，而不是静默返回一片空白
	if len(data.Alerts) == 0 {
		t.Error("空实例应给出「还没有添加网盘账号」的提示")
	}
}

// TestDashboardPauseDoesNotAlert 验证已暂停的任务不触发失败告警。
//
// 用户主动暂停任务后，它的 last_run_status 可能还是 failed。
// 此时告警会造成"我明明暂停了为什么一直报错"的困惑。
func TestDashboardPauseDoesNotAlert(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := t.Context()

	if _, err := st.CreateAccount(ctx, testAccount("夸克主号", `{"cookie":"c"}`)); err != nil {
		t.Fatalf("create account: %v", err)
	}
	taskID := seedTestTask(t, st, "已暂停任务")
	runID, _ := st.CreateRun(ctx, taskID, "https://pan.quark.cn/s/a")
	_ = st.FinishRun(ctx, runID, "failed", "上次失败", "", "", 0)
	_ = st.UpdateTaskRunState(ctx, taskID,
		time.Now().Format("2006-01-02 15:04:05"), "failed", "上次失败", "")
	if err := st.SetTaskStatus(ctx, taskID, "paused"); err != nil {
		t.Fatalf("pause: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/dashboard", nil))
	data := decodeData[dashboardData](t, rec)
	for _, a := range data.Alerts {
		if a.TaskID == taskID && a.Level == "error" {
			t.Errorf("已暂停的任务不应触发失败告警，实际 %+v", a)
		}
	}
}
