package transfer

import (
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
// 清理顺序很重要：Windows 上 sql.DB.Close() 不会立刻释放文件句柄，
// t.TempDir() 的 RemoveAll 会报"目录非空"（测试已通过、只是清理失败）。
// 先 SetMaxIdleConns(0) 丢弃空闲连接，再 Close，句柄才会真正释放。
func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "pantransfer.db"))
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
	})
	return svc
}