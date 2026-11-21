package transfer

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"pan-transfer/internal/store"
)

// newTestStore 打开一个测试库并返回「关库 + 清目录」函数。
//
// 为什么不用 t.TempDir()：它的清理注册在 t.Cleanup（LIFO），
// 而关库也要用 defer —— 两者顺序在不同测试里不一致，Windows 上会出现
// 「目录已被 RemoveAll、SQLite 句柄还没关」的竞态，报"目录非空"。
// 这里把「关库」和「删目录」绑成一个函数，由调用方 defer，顺序确定。
func newTestStore(t *testing.T) (*store.Store, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pt-transfer-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	dbPath := filepath.Join(dir, "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("open store: %v", err)
	}
	var once sync.Once
	closeFn := func() {
		once.Do(func() {
			st.CloseIdle() // 先合并 WAL 并丢弃空闲连接，句柄才会稳定释放
			_ = st.Close()
			for i := 0; i < 20; i++ {
				_ = os.Remove(dbPath + "-wal")
				_ = os.Remove(dbPath + "-shm")
				if _, err := os.Stat(dbPath + "-wal"); os.IsNotExist(err) {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			_ = os.RemoveAll(dir)
		})
	}
	return st, closeFn
}

// newTestService 构造一个真实临时库驱动的 Service（与 callback_test.go 同范式）。
//
// 不用 mock：Reload / sweep / Stats 都是与存储耦合的行为，
// mock 测不出"换了句柄之后还能不能正常读写"这类真实问题。
func newTestService(t *testing.T) *Service {
	t.Helper()
	st, closeStore := newTestStore(t)
	svc := New(Options{Store: st})
	t.Cleanup(func() {
		svc.Close() // 先停调度，避免它在关库期间读写
		closeStore()
	})
	return svc
}
