package store

import (
	"context"
	"testing"
	"time"

	"pan-transfer/internal/domain"
)

// newTestTask 构造可直接落库的最小任务。
func newTestTask(name string) *domain.DramaTask {
	return &domain.DramaTask{TaskName: name, AccountID: 1, SavePath: "/tv", Status: "running"}
}

// 运行记录：FinishRun 应写入详细日志；PruneRuns 按保留天数清理但每个任务保底。
func TestRunLogAndPrune(t *testing.T) {
	s, err := Open(t.TempDir() + "/runs.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	taskID, err := s.CreateTask(ctx, newTestTask("清理测试"))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	run1, err := s.CreateRun(ctx, taskID, "https://pan.quark.cn/s/a")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := s.FinishRun(ctx, run1, "success", "ok", "树", "=== 解析分享链接 ===\npwd_id: a", 3); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	// 旧记录：直接把 started_at 改到 100 天前
	run2, err := s.CreateRun(ctx, taskID, "https://pan.quark.cn/s/b")
	if err != nil {
		t.Fatalf("create run2: %v", err)
	}
	_ = s.FinishRun(ctx, run2, "failed", "boom", "", "旧日志", 0)
	old := time.Now().AddDate(0, 0, -100).Format(time.RFC3339)
	if _, err := s.db.Exec(`UPDATE task_runs SET started_at=? WHERE id=?`, old, run2); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	runs, err := s.ListRuns(ctx, taskID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	// ListRuns 按 id 倒序：runs[0] = run2（后创建），runs[1] = run1
	if runs[1].Log == "" || runs[1].TransferCount != 3 {
		t.Errorf("run1 log/count = %q/%d, want log + 3", runs[1].Log, runs[1].TransferCount)
	}

	// 清理：保留 30 天，但每个任务保底最近 50 条（按时间最近）→ 都受保底保护
	n, err := s.PruneRuns(ctx, 30, 50)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 0 {
		t.Errorf("prune deleted %d, want 0 (保底保护)", n)
	}

	// 收紧保底到 1 条：100 天前的 run2（按时间更旧）被删，run1 保留
	if n, err = s.PruneRuns(ctx, 30, 1); err != nil {
		t.Fatalf("prune2: %v", err)
	}
	if n != 1 {
		t.Errorf("prune2 deleted %d, want 1", n)
	}
	runs, _ = s.ListRuns(ctx, taskID, 10)
	if len(runs) != 1 || runs[0].ID != run1 {
		t.Errorf("after prune runs = %d 条, want only run %d", len(runs), run1)
	}

	// 0 天 = 永久保留
	if n, err = s.PruneRuns(ctx, 0, 1); err != nil || n != 0 {
		t.Errorf("prune(0) = %d, %v; want 0, nil", n, err)
	}
}

func TestClearSavedNames(t *testing.T) {
	s, err := Open(t.TempDir() + "/saved.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	taskID, err := s.CreateTask(ctx, newTestTask("历史测试"))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.AddSavedNames(ctx, taskID, []SavedName{
		{TaskID: taskID, NormName: "a.mkv", FileName: "a.mkv"},
		{TaskID: taskID, NormName: "b.mkv", FileName: "b.mkv"},
	}); err != nil {
		t.Fatalf("add saved: %v", err)
	}
	if n, _ := s.CountSavedNames(ctx, taskID); n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	cleared, err := s.ClearSavedNames(ctx, taskID)
	if err != nil || cleared != 2 {
		t.Errorf("clear = %d, %v; want 2, nil", cleared, err)
	}
	if n, _ := s.CountSavedNames(ctx, taskID); n != 0 {
		t.Errorf("count after clear = %d, want 0", n)
	}
	names, err := s.SavedNormNames(ctx, taskID)
	if err != nil || len(names) != 0 {
		t.Errorf("norm names after clear = %v, %v; want empty", names, err)
	}
}
