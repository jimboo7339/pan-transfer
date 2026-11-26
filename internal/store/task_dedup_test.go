package store

import (
	"context"
	"os"
	"testing"
)

// TestTaskDedupStrategyRoundtrip 验证去重策略字段能正确落库与读回。
// 这是「优先清晰度」功能的存储层保障：新增列、Create/Update SQL、
// scanTask 三处必须一致，漏掉任何一处都会静默丢值。
func TestTaskDedupStrategyRoundtrip(t *testing.T) {
	s, dir, closeStore := openTestStore(t)
	defer func() { closeStore(); _ = os.RemoveAll(dir) }()
	ctx := context.Background()

	task := newTestTask("去重策略测试")
	task.DedupStrategy = "quality"
	id, err := s.CreateTask(ctx, task)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DedupStrategy != "quality" {
		t.Fatalf("create 后 dedup_strategy = %q, want quality", got.DedupStrategy)
	}

	got.DedupStrategy = "largest"
	if err := s.UpdateTask(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = s.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.DedupStrategy != "largest" {
		t.Fatalf("update 后 dedup_strategy = %q, want largest", got.DedupStrategy)
	}
}
