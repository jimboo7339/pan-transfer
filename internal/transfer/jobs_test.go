package transfer

import (
	"context"
	"testing"
	"time"
)

// TestJobRegistryLifecycle 验证 job 句柄的创建/查询/淘汰。
//
// 异步运行是本次改造的核心：如果 job 句柄表本身有泄漏或淘汰错误，
// 长跑进程会内存膨胀，或者把还在运行的句柄误删。
func TestJobRegistryLifecycle(t *testing.T) {
	r := newJobRegistry(3)

	a := r.create(1, "任务A", "manual")
	b := r.create(2, "任务B", "manual")
	if a.ID == b.ID {
		t.Fatal("job ID 不应重复")
	}
	if len(r.list()) != 2 {
		t.Fatalf("应有 2 个 job，实际 %d", len(r.list()))
	}

	// list 应新的在前。
	list := r.list()
	if list[0].ID != b.ID {
		t.Errorf("list 应按新的在前，got %v", []int64{list[0].ID, list[1].ID})
	}

	// 取单个 job。
	if _, ok := r.get(a.ID); !ok {
		t.Error("应能取到 job A")
	}
	if _, ok := r.get(999); ok {
		t.Error("不存在的 job 不应返回 ok")
	}
}

// TestJobRegistryEvictsOldest 验证只保留 max 个句柄，且不会淘汰运行中的。
func TestJobRegistryEvictsOldest(t *testing.T) {
	r := newJobRegistry(2)

	a := r.create(1, "A", "manual")
	a.finish("success", nil, "")
	b := r.create(2, "B", "manual")
	b.finish("success", nil, "")
	c := r.create(3, "C", "manual")
	c.finish("success", nil, "")

	// 3 个 job、上限 2：最老的 a 应被淘汰。
	if _, ok := r.get(a.ID); ok {
		t.Error("最老的 job 应被淘汰")
	}
	if len(r.list()) != 2 {
		t.Errorf("应保留 2 个，实际 %d", len(r.list()))
	}
}

// TestJobRegistryKeepsRunning 验证运行中的 job 不会被淘汰。
//
// 这个场景很实际：用户启动 3 个任务（上限 2），
// 如果淘汰逻辑不判断状态，最老的运行中 job 会被悄悄删掉，前端轮询就 404。
func TestJobRegistryKeepsRunning(t *testing.T) {
	r := newJobRegistry(2)

	a := r.create(1, "A", "manual") // 保持 running
	b := r.create(2, "B", "manual")
	b.finish("success", nil, "")
	c := r.create(3, "C", "manual")
	c.finish("success", nil, "")

	if _, ok := r.get(a.ID); !ok {
		t.Fatal("运行中的 job 不应被淘汰")
	}
}

// TestJobFinishClosesDone 验证 finish 会关闭 done 且只关一次。
//
// done 是等待方（含测试与未来的优雅关闭逻辑）的同步信号，
// 重复 close 会 panic。
func TestJobFinishClosesDone(t *testing.T) {
	r := newJobRegistry(10)
	js := r.create(1, "A", "manual")

	js.finish("success", nil, "")
	select {
	case <-js.done:
	default:
		t.Fatal("finish 后应关闭 done")
	}

	// 再次 finish 不应 panic。
	js.finish("failed", nil, "duplicate")
}

// TestJobFinishSetsResultAndProgress 验证终态写入结果与进度。
func TestJobFinishSetsResultAndProgress(t *testing.T) {
	r := newJobRegistry(10)
	js := r.create(1, "A", "manual")

	res := &RunResult{TaskID: 1, Status: "success", TransferCount: 3, Message: "转存 3 个文件"}
	js.finish("success", res, "")

	if js.Progress != 100 {
		t.Errorf("成功时进度应为 100，得到 %d", js.Progress)
	}
	if js.Stage != "已完成" {
		t.Errorf("阶段应为「已完成」，得到 %q", js.Stage)
	}
	if js.Result == nil || js.Result.TransferCount != 3 {
		t.Errorf("结果未被记录: %+v", js.Result)
	}
}

// TestJobAppendLogConcurrent 验证日志追加在并发下不丢数据（-race 下运行）。
func TestJobAppendLogConcurrent(t *testing.T) {
	r := newJobRegistry(10)
	js := r.create(1, "A", "manual")

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				js.appendLog("x")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}

	js.mu.Lock()
	got := len(js.log)
	js.mu.Unlock()
	if got != 800 {
		t.Errorf("800 次追加应得 800 字符，得到 %d", got)
	}
}

// TestJobViewDoesNotCopyLock 验证 jobView 返回的是无锁快照。
//
// 返回含锁的值会让 go vet 报错，也会让调用方误以为可以操作内部同步原语。
func TestJobViewDoesNotCopyLock(t *testing.T) {
	r := newJobRegistry(10)
	js := r.create(1, "任务A", "manual")
	js.appendLog("hello")
	js.setStage("重命名", 80)

	v := snapshotLog(js)
	if v.Log != "hello" {
		t.Errorf("日志快照 = %q，want hello", v.Log)
	}
	if v.Stage != "重命名" || v.Progress != 80 {
		t.Errorf("阶段快照错误: %q %d", v.Stage, v.Progress)
	}
	if v.Status != "running" {
		t.Errorf("状态应为 running，得到 %q", v.Status)
	}
}

// TestProgressSinkFromContext 验证 ctx 进度回调的挂载与读取。
func TestProgressSinkFromContext(t *testing.T) {
	r := newJobRegistry(10)
	js := r.create(1, "A", "manual")

	// 没有挂载时应返回 nil。
	if progressSinkFrom(context.Background()) != nil {
		t.Error("未挂载时 sink 应为 nil")
	}

	ctx := WithProgressSink(context.Background(), js)
	sink := progressSinkFrom(ctx)
	if sink == nil {
		t.Fatal("挂载后应能取到 sink")
	}
	sink("执行转存", 30)
	if js.Stage != "执行转存" || js.Progress != 30 {
		t.Errorf("sink 未生效: %q %d", js.Stage, js.Progress)
	}
}

// TestStatusText 验证状态文案转换。
func TestStatusText(t *testing.T) {
	cases := map[string]string{
		"success": "成功",
		"failed":  "失败",
		"other":   "other",
	}
	for in, want := range cases {
		if got := statusText(in); got != want {
			t.Errorf("statusText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStatsReportsRunningTasks 验证 Stats 能报告运行中的任务（深度健康检查依赖它）。
func TestStatsReportsRunningTasks(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// 手动塞一个 running 标记（不需要真的跑任务）。
	svc.mu.Lock()
	svc.running[7] = struct{}{}
	svc.mu.Unlock()

	st := svc.Stats(ctx)
	if len(st.RunningTaskIDs) != 1 || st.RunningTaskIDs[0] != 7 {
		t.Errorf("应报告任务 7 在运行，得到 %v", st.RunningTaskIDs)
	}
}

// TestReloadClearsProcessState 验证 Reload 会清空进程内标记。
//
// 从备份还原时，旧的内存标记（running / lastTrigger / rounds）对应的是还原前
// 的世界，留着会永久阻塞同名任务的调度。
func TestReloadClearsProcessState(t *testing.T) {
	svc := newTestService(t)

	svc.mu.Lock()
	svc.running[1] = struct{}{}
	svc.mu.Unlock()
	svc.schedMu.Lock()
	svc.lastTrigger[1] = "2026-11-21 10:00"
	svc.schedMu.Unlock()
	svc.roundMu.Lock()
	svc.rounds[1] = &callbackRound{}
	svc.roundMu.Unlock()

	svc.Reload(svc.st)

	if n := len(svc.Stats(context.Background()).RunningTaskIDs); n != 0 {
		t.Errorf("Reload 后应无运行中任务，得到 %d", n)
	}
	svc.schedMu.Lock()
	n := len(svc.lastTrigger)
	svc.schedMu.Unlock()
	if n != 0 {
		t.Errorf("Reload 后应清空 lastTrigger，得到 %d", n)
	}
}

// TestReloadDoesNotBreakScheduling 验证 Reload 后调度循环仍可工作。
//
// Reload 不重启调度 goroutine（它读 appCtx），
// 因此 Reload 后再触发一次 sweep 不应 panic。
func TestReloadDoesNotBreakScheduling(t *testing.T) {
	svc := newTestService(t)
	svc.Reload(svc.st)

	done := make(chan struct{})
	go func() {
		svc.sweep()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Reload 后 sweep 应正常返回")
	}
}
