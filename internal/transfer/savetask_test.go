package transfer

import (
	"context"
	"testing"

	"pan-transfer/internal/domain"
)

// domainTask 构造一个通过必填校验的最小任务。
func domainTask(resaveMode string) *domain.DramaTask {
	return &domain.DramaTask{
		TaskName:               "测试任务",
		AccountID:              1,
		SavePath:               "/影视",
		ShareLinks:             `["https://pan.quark.cn/s/abc"]`,
		SkipHistory:            "global",
		RunMode:                "manual",
		Status:                 "running",
		SortIndex:              1,
		UpdateSubdir:           "Season \\d+",
		UpdateSubdirResaveMode: resaveMode,
	}
}

// TestSaveTaskResaveModeValidation 验证子目录重存模式的白名单校验。
//
// 这个值决定执行器是否**删除远端目录**再重新转存（executor.go 的
// delete_then_resave 分支），拼错的字符串会让行为回落到未知分支，
// 必须在入库前拦住而不是静默接受。
func TestSaveTaskResaveModeValidation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{"空值=增量", "", false},
		{"delete_then_resave", "delete_then_resave", false},
		{"拼错的 delete", "delete", true},
		{"拼错的 resave", "resave", true},
		{"未知模式", "overwrite", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.SaveTask(ctx, domainTask(c.mode))
			if c.wantErr && err == nil {
				t.Errorf("mode=%q 应被拒绝", c.mode)
			}
			if !c.wantErr && err != nil {
				t.Errorf("mode=%q 应通过，得到 %v", c.mode, err)
			}
		})
	}
}

// TestSaveTaskSortIndexDefaultsToOne 验证起始集号 <=0 时回落到 1。
//
// planTransfer 用 SortIndex 作为 {I+} 的起始序号，
// 0 或负数会生成 E00 / E-1 这类非法集号。
func TestSaveTaskSortIndexDefaultsToOne(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	for _, in := range []int{0, -5} {
		task := domainTask("")
		task.SortIndex = in
		id, err := svc.SaveTask(ctx, task)
		if err != nil {
			t.Fatalf("sort_index=%d: %v", in, err)
		}
		got, err := svc.st.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if got.SortIndex != 1 {
			t.Errorf("sort_index=%d 应归一化为 1，得到 %d", in, got.SortIndex)
		}
	}
}

// TestSaveTaskKeepsPositiveSortIndex 验证合法起始集号不被改写。
func TestSaveTaskKeepsPositiveSortIndex(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	task := domainTask("")
	task.SortIndex = 12
	id, err := svc.SaveTask(ctx, task)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := svc.st.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SortIndex != 12 {
		t.Errorf("sort_index 应为 12，得到 %d", got.SortIndex)
	}
}
