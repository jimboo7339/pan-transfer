package transfer

import (
	"context"
	"testing"
	"time"
)

// TestStopSchedulingClosesOldLoop 验证 StopScheduling 会取消并等待旧调度循环退出，
// 且重建 schedCtx/schedDone 供 RestartScheduling 使用。
//
// 回归背景：旧实现 RestartScheduling 直接替换 appCtx 而不调用旧 cancel，
// 监听 appCtx.Done() 的 ticker goroutine 永不退出 → 每还原一次多一个循环。
func TestStopSchedulingClosesOldLoop(t *testing.T) {
	svc := newTestService(t)

	oldDone := svc.schedDone
	svc.Start()

	stopReturned := make(chan struct{})
	go func() {
		svc.StopScheduling()
		close(stopReturned)
	}()
	select {
	case <-stopReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("StopScheduling 应等旧循环退出后立即返回")
	}

	// 旧 done 必须已关闭（循环退出），新 done 必须是不同 channel。
	select {
	case <-oldDone:
	default:
		t.Fatal("StopScheduling 返回后旧调度循环的 done 应已关闭")
	}
	if svc.schedDone == oldDone {
		t.Fatal("StopScheduling 后应重建 schedDone")
	}

	// 重启后新循环能再次 Start / Stop，不复用已关闭的 channel。
	svc.RestartScheduling()
	newDone := svc.schedDone
	svc.StopScheduling()
	select {
	case <-newDone:
	default:
		t.Fatal("第二次 StopScheduling 后新循环的 done 应已关闭")
	}
}

// TestAppContextSnapshotStable 验证 appContext() 返回的是快照：
// 替换 appCtx 后，先前取得的快照仍然有效（不会变成新 ctx）。
func TestAppContextSnapshotStable(t *testing.T) {
	svc := newTestService(t)
	before := svc.appContext()

	ctx, cancel := context.WithCancel(context.Background())
	svc.setAppContext(ctx, cancel)
	after := svc.appContext()
	if before == after {
		t.Fatal("appContext() 应返回替换前后的不同快照")
	}

	// 旧快照不应因替换而被取消（在途任务依赖这一点）。
	select {
	case <-before.Done():
		t.Fatal("替换 appCtx 不应取消旧快照")
	default:
	}
}
