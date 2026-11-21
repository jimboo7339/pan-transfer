package driver

import (
	"context"
	"testing"
	"time"
)

// TestWaitRequestInterval_NilGateIsNoop 验证未注入 gate 时直接放行（未接线路径的兜底）。
func TestWaitRequestInterval_NilGateIsNoop(t *testing.T) {
	start := time.Now()
	if err := WaitRequestInterval(context.Background(), nil, 300); err != nil {
		t.Fatalf("nil gate 应直接返回 nil，得到 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("nil gate 不应等待，实际等待 %s", elapsed)
	}
}

// TestWaitRequestInterval_ZeroBaseUsesDefault 验证 baseMS=0 回落到默认间隔，
// 即「驱动未声明间隔」不再等价于「零节流」（夸克此前 requestInterval=0）。
func TestWaitRequestInterval_ZeroBaseUsesDefault(t *testing.T) {
	dc := NewDelayController()
	gate := dc.Gate(1)

	// 第一次请求不等待（没有 last 记录）。
	if err := gate.Wait(context.Background(), DefaultRequestIntervalMS*time.Millisecond); err != nil {
		t.Fatalf("第一次等待失败：%v", err)
	}

	// 第二次通过 WaitRequestInterval 走 baseMS=0，应回落默认并真实等待。
	start := time.Now()
	if err := WaitRequestInterval(context.Background(), gate, 0); err != nil {
		t.Fatalf("第二次等待失败：%v", err)
	}
	if elapsed := time.Since(start); elapsed < DefaultRequestIntervalMS*time.Millisecond/2 {
		t.Fatalf("baseMS=0 应回落到 %dms，实际只等待 %s", DefaultRequestIntervalMS, elapsed)
	}
}

// TestWaitRequestInterval_NegativeBaseOptsOut 验证负值表示驱动显式关闭节流。
func TestWaitRequestInterval_NegativeBaseOptsOut(t *testing.T) {
	dc := NewDelayController()
	gate := dc.Gate(1)

	if err := gate.Wait(context.Background(), DefaultRequestIntervalMS*time.Millisecond); err != nil {
		t.Fatalf("预热等待失败：%v", err)
	}
	start := time.Now()
	if err := WaitRequestInterval(context.Background(), gate, -1); err != nil {
		t.Fatalf("负值应直接返回 nil，得到 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("负值应跳过等待，实际等待 %s", elapsed)
	}
}

// TestDelayController_PerAccountIsolation 验证间隔按账号隔离：A 账号的请求不阻塞 B 账号。
func TestDelayController_PerAccountIsolation(t *testing.T) {
	dc := NewDelayController()
	gateA := dc.Gate(1)
	gateB := dc.Gate(2)

	if err := gateA.Wait(context.Background(), 200*time.Millisecond); err != nil {
		t.Fatalf("A 预热失败：%v", err)
	}

	// B 账号没有历史记录，不应被 A 的间隔阻塞。
	start := time.Now()
	if err := gateB.Wait(context.Background(), 200*time.Millisecond); err != nil {
		t.Fatalf("B 等待失败：%v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("账号间应互不影响，B 实际等待 %s", elapsed)
	}
}

// TestDelayController_SameAccountSerialized 验证同账号连续请求确实被拉开间隔。
func TestDelayController_SameAccountSerialized(t *testing.T) {
	dc := NewDelayController()
	gate := dc.Gate(1)
	const interval = 120 * time.Millisecond

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := gate.Wait(context.Background(), interval); err != nil {
			t.Fatalf("第 %d 次等待失败：%v", i+1, err)
		}
	}
	elapsed := time.Since(start)
	// 3 次请求：第 1 次不等待，后两次各等 interval。
	if elapsed < 2*interval {
		t.Fatalf("3 次同账号请求至少应耗时 %s，实际 %s", 2*interval, elapsed)
	}
}

// TestDelayController_DropAccount 验证删除账号后状态被清理，不会无界增长。
func TestDelayController_DropAccount(t *testing.T) {
	dc := NewDelayController()
	gate := dc.Gate(1)
	if err := gate.Wait(context.Background(), 10*time.Millisecond); err != nil {
		t.Fatalf("等待失败：%v", err)
	}

	dc.mu.Lock()
	_, exists := dc.accounts[1]
	dc.mu.Unlock()
	if !exists {
		t.Fatal("等待后应存在账号状态")
	}

	dc.DropAccount(1)

	dc.mu.Lock()
	_, exists = dc.accounts[1]
	dc.mu.Unlock()
	if exists {
		t.Fatal("DropAccount 后应清理账号状态")
	}
}

// TestWaitRequestInterval_ContextCancel 验证等待期间取消会立即返回。
func TestWaitRequestInterval_ContextCancel(t *testing.T) {
	dc := NewDelayController()
	gate := dc.Gate(1)

	if err := gate.Wait(context.Background(), time.Second); err != nil {
		t.Fatalf("预热失败：%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := WaitRequestInterval(ctx, gate, 1000)
	if err == nil {
		t.Fatal("ctx 取消时应返回错误")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("取消后应立即返回，实际耗时 %s", elapsed)
	}
}

// TestWithExtraAPIDelay_AddsToBase 验证任务级额外间隔与基础间隔相加。
func TestWithExtraAPIDelay_AddsToBase(t *testing.T) {
	dc := NewDelayController()
	gate := dc.Gate(1)

	if err := gate.Wait(context.Background(), 50*time.Millisecond); err != nil {
		t.Fatalf("预热失败：%v", err)
	}

	// base 50ms + extra 150ms = 200ms
	ctx := WithExtraAPIDelay(context.Background(), 150)
	start := time.Now()
	if err := WaitRequestInterval(ctx, gate, 50); err != nil {
		t.Fatalf("等待失败：%v", err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("应至少等待 base+extra=200ms，实际 %s", elapsed)
	}
}
