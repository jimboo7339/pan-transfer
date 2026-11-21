package transfer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pan-transfer/internal/store"
)

// newTestService 构造一个真实临时库驱动的 Service（与 callback_test.go 同范式）。
//
// 不用 mock：Reload / sweep / Stats 都是与存储耦合的行为，
// mock 测不出"换了句柄之后还能不能正常读写"这类真实问题。
//
// 清理顺序与重试：Windows 上 sql.DB.Close() 不会立刻释放文件句柄，
// t.TempDir() 的 RemoveAll 会间歇性报"目录非空"（同一份代码连续跑，
// 有时通过有时失败）。这不是产品缺陷，而是平台特性；这里在关闭后做一次
// "把残留的 -wal/-shm 删掉并短暂等待"的收尾，让清理稳定下来。
func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pantransfer.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	svc := New(Options{Store: st})
	t.Cleanup(func() {
		svc.Close()
		if db := st.DB(); db != nil {
			db.SetMaxIdleConns(0)
			db.SetConnMaxLifetime(time.Nanosecond)
		}
		_ = st.Close()

		// 尽力清掉 WAL/SHM：它们才是 RemoveAll 失败的真正原因
		// （句柄释放是异步的，短重试即可）。
		for i := 0; i < 20; i++ {
			_ = os.Remove(dbPath + "-wal")
			_ = os.Remove(dbPath + "-shm")
			if _, err := os.Stat(dbPath + "-wal"); os.IsNotExist(err) {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
	return svc
}
