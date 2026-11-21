package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// forceCloseIdle 强制关闭连接池里的空闲连接。
//
// Windows 上 sql.DB.Close() 只是把连接标记为待关闭，底层文件句柄可能还短暂
// 存在；随后 t.TempDir() 的 RemoveAll 会因句柄未释放而失败，
// 表现为"目录非空"的清理报错（测试本身已通过）。
func forceCloseIdle(s *Store) {
	if s == nil || s.db == nil {
		return
	}
	s.db.SetMaxIdleConns(0)
	s.db.SetConnMaxLifetime(time.Nanosecond)
}

// removeWithRetry 删除目录，失败时短暂重试。
//
// Windows 上文件的句柄释放是异步的：即使 Close 已经返回，
// 紧接着的 RemoveAll 仍可能报"目录非空"。这不是测试逻辑问题，
// 而是平台特性；重试几次即可（Go 自己的 os/exec 测试也这么处理）。
func removeWithRetry(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		err := os.RemoveAll(path)
		if err == nil {
			return
		}
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	// 最后一次仍然失败就交给 t.TempDir 的清理逻辑，不在这里让测试失败：
	// 这个函数只做"尽力清理"，产品代码路径的正确性由断言保证。
}

// tempDBDir 创建一个由测试自己管理生命周期的临时目录。
//
// 为什么不用 t.TempDir()：t.TempDir 的清理注册在 t.Cleanup 里，
// 而本包的辅助函数也要用 t.Cleanup 关库 —— Go 的 Cleanup 是 LIFO，
// 先注册的后执行，于是会出现「目录已被 RemoveAll，SQLite 句柄还没关」
// 的竞态，Windows 上表现为大量 "The directory is not empty"。
//
// 这里改成：目录由调用方 defer 删除，且删除发生在关库之后。
func tempDBDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pt-store-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	return dir
}

// openTestStore 打开一个测试库，返回库、目录与「幂等关闭」函数。
//
// 调用方必须在拿到 closeStore 后 defer 调用它，并在其后删除目录：
//
//	s, dir, closeStore := openTestStore(t)
//	defer func() { closeStore(); _ = os.RemoveAll(dir) }()
//
// 关闭函数是幂等的：测试中途需要主动关库（例如替换数据库文件）时可以
// 提前调用，defer 里的第二次调用是空操作。
func openTestStore(t *testing.T) (*Store, string, func()) {
	t.Helper()
	dir := tempDBDir(t)
	dbPath := filepath.Join(dir, "pantransfer.db")
	s, err := Open(dbPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("open: %v", err)
	}
	var once sync.Once
	closeFn := func() {
		once.Do(func() {
			_ = s.Close()
			// Close 已经做了 wal_checkpoint + SetMaxIdleConns(0)，
			// 这里再删一次残留的 WAL/SHM 兜底（Windows 句柄释放是异步的）。
			for i := 0; i < 20; i++ {
				_ = os.Remove(dbPath + "-wal")
				_ = os.Remove(dbPath + "-shm")
				if _, err := os.Stat(dbPath + "-wal"); os.IsNotExist(err) {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
		})
	}
	return s, dir, closeFn
}

// TestBackupAndRestore 验证 VACUUM INTO 备份 + 还原的全流程。
//
// 这是关键路径：容器镜像里没有 sqlite3，用户唯一的备份手段就是产品内置的这个。
// 备份必须包含 WAL 里尚未 checkpoint 的事务，否则用户拿到的是残缺数据。
func TestBackupAndRestore(t *testing.T) {
	s, dir, closeStore := openTestStore(t)
	defer func() { closeStore(); _ = os.RemoveAll(dir) }()
	dbPath := filepath.Join(dir, "pantransfer.db")
	ctx := context.Background()

	// 备份放在 t.TempDir() 之外（另一个 TempDir 会在测试结束后整体清理，
	// 而这里需要在测试结束前保证句柄已释放，Windows 上句柄释放有延迟）。
	backupDir := t.TempDir()
	backupPath := filepath.Join(backupDir, BackupFileName(time.Now()))

	// 写入一个账号，然后**不关闭**直接备份：WAL 里应该还有未合并的数据。
	acc := &Account{Name: "夸克主号", DriverType: "quark", Config: `{"cookie":"abc"}`, IsActive: true}
	id, err := s.CreateAccount(ctx, acc)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	if _, err := s.BackupTo(ctx, backupPath); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// 备份文件应存在且非空。
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("备份文件不存在: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("备份文件为空")
	}

	// 备份应能通过 quick_check（PrepareRestore 内部会做）。
	plan, err := s.PrepareRestore(backupPath, dbPath)
	if err != nil {
		t.Fatalf("备份校验失败: %v", err)
	}
	if plan.TargetPath != dbPath {
		t.Errorf("还原目标应为 %s，得到 %s", dbPath, plan.TargetPath)
	}

	// 删除原数据，再用备份还原，验证数据确实回来了。
	if _, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id=?`, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("删除后应无账号，实际 %d", n)
	}

	// 关闭连接才能替换文件（幂等：t.Cleanup 里的第二次调用是空操作）。
	closeStore()
	if err := plan.ApplyRestore(); err != nil {
		t.Fatalf("apply restore: %v", err)
	}
	// 备份文件已经用完，立刻删除：Windows 上刚关闭的句柄可能仍短暂占用文件，
	// 留在 TempDir 里会让 RemoveAll 报"目录非空"。
	removeWithRetry(t, backupPath)

	// 重新打开，验证数据确实回来了。
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen after restore: %v", err)
	}
	t.Cleanup(func() {
		if db := s2.DB(); db != nil {
			db.SetMaxIdleConns(0)
			db.SetConnMaxLifetime(time.Nanosecond)
		}
		_ = s2.Close()
	})

	got, err := s2.GetAccount(ctx, id)
	if err != nil {
		t.Fatalf("还原后账号应存在: %v", err)
	}
	if got.Name != "夸克主号" {
		t.Errorf("还原后账号名 = %q，want 夸克主号", got.Name)
	}
}

// TestBackupRotation 验证只保留最近 N 份备份。
func TestBackupRotation(t *testing.T) {
	s, dir, closeStore := openTestStore(t)
	defer func() { closeStore(); _ = os.RemoveAll(dir) }()

	backupDir := filepath.Join(dir, BackupDirName)
	ctx := context.Background()

	// 生成 5 份备份（文件名带时间戳，手动错开避免同名）。
	for i := 0; i < 5; i++ {
		name := BackupPrefix + "20260101-00000" + string(rune('0'+i)) + ".db"
		if _, err := s.BackupTo(ctx, filepath.Join(backupDir, name)); err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
	}

	list, err := ListBackups(backupDir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 5 {
		t.Fatalf("应列出 5 份备份，实际 %d", len(list))
	}

	removed, err := PruneBackups(backupDir, 3)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 2 {
		t.Errorf("应删除 2 份，实际 %d", removed)
	}
	list, _ = ListBackups(backupDir)
	if len(list) != 3 {
		t.Errorf("应剩 3 份，实际 %d", len(list))
	}

	// 显式清理备份目录：Windows 上 VACUUM INTO 产生的文件句柄释放有延迟，
	// 直接交给 t.TempDir() 的 RemoveAll 会报"目录非空"。
	removeWithRetry(t, backupDir)
}

// TestListBackupsIgnoresForeignFiles 验证备份目录里的无关文件不会被当成备份。
func TestListBackupsIgnoresForeignFiles(t *testing.T) {
	s, dir, closeStore := openTestStore(t)
	defer func() { closeStore(); _ = os.RemoveAll(dir) }()
	_ = s

	backupDir := filepath.Join(dir, BackupDirName)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 用户可能把别的东西放进来。
	if err := os.WriteFile(filepath.Join(backupDir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "evil.db"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	list, err := ListBackups(backupDir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("非备份文件应被忽略，实际列出 %d 项", len(list))
	}
}

// TestMarkInterruptedRuns 验证进程崩溃留下的 running 记录会被收尾。
//
// 不修这个问题，"运行日志"里会永远留着一批转圈的任务，
// 用户无法判断"到底跑没跑完"。
func TestMarkInterruptedRuns(t *testing.T) {
	s, dir, closeStore := openTestStore(t)
	defer func() { closeStore(); _ = os.RemoveAll(dir) }()
	ctx := context.Background()

	now := nowStr()
	// 两条 running（模拟崩溃遗留）+ 一条 success（不应被改）。
	ids := []struct {
		status string
	}{{("running")}, {"running"}, {"success"}}
	for i, c := range ids {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO task_runs (task_id, status, message, started_at) VALUES (?,?,?,?)`,
			int64(i+1), c.status, "", now); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	n, err := s.MarkInterruptedRuns(ctx, "服务重启，运行中断")
	if err != nil {
		t.Fatalf("mark: %v", err)
	}
	if n != 2 {
		t.Errorf("应收尾 2 条 running，实际 %d", n)
	}

	var running, failed int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE status='running'`).Scan(&running)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE status='failed'`).Scan(&failed)
	if running != 0 {
		t.Errorf("应无 running 残留，实际 %d", running)
	}
	if failed != 2 {
		t.Errorf("应有 2 条 failed，实际 %d", failed)
	}

	// 幂等：再跑一次不应有影响。
	n2, err := s.MarkInterruptedRuns(ctx, "服务重启，运行中断")
	if err != nil {
		t.Fatalf("second mark: %v", err)
	}
	if n2 != 0 {
		t.Errorf("第二次应无记录可收尾，实际 %d", n2)
	}
}

// TestSetTaskStatusRejectsMissing 验证暂停一个不存在的任务会报错。
//
// 这是个真实踩到的 bug：UPDATE 对不存在的 id 不报错、只影响 0 行，
// 于是「暂停一个已被删除的任务」返回 nil。批量操作时用户会看到
// "暂停成功"但什么都没发生——批量场景下一屏几十个 ID，
// 这种静默失败极难排查。
func TestSetTaskStatusRejectsMissing(t *testing.T) {
	s, dir, closeStore := openTestStore(t)
	defer func() { closeStore(); _ = os.RemoveAll(dir) }()
	ctx := context.Background()

	if err := s.SetTaskStatus(ctx, 99999, "paused"); err == nil {
		t.Error("暂停不存在的任务应返回错误")
	}
}

// TestDeleteTaskRejectsMissing 验证删除不存在的任务会报错（同上）。
func TestDeleteTaskRejectsMissing(t *testing.T) {
	s, dir, closeStore := openTestStore(t)
	defer func() { closeStore(); _ = os.RemoveAll(dir) }()
	ctx := context.Background()

	if err := s.DeleteTask(ctx, 99999); err == nil {
		t.Error("删除不存在的任务应返回错误")
	}
}

// TestCountTasksByAccount 验证账号引用检查（删账号前必须知道有多少任务依赖它）。
// 全仓没有生效的外键，不检查就会留下悬空 account_id 的僵尸任务。
func TestCountTasksByAccount(t *testing.T) {
	s, dir, closeStore := openTestStore(t)
	defer func() { closeStore(); _ = os.RemoveAll(dir) }()
	ctx := context.Background()

	n, err := s.CountTasksByAccount(ctx, 42)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("空库应返回 0，实际 %d", n)
	}

	now := nowStr()
	for i := 0; i < 3; i++ {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO tasks (task_name, account_id, share_url, save_path, created_at, updated_at)
			 VALUES (?,?,?,?,?,?)`, "t", int64(42), "", "/", now, now); err != nil {
			t.Fatalf("insert task: %v", err)
		}
	}
	n, err = s.CountTasksByAccount(ctx, 42)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("应返回 3，实际 %d", n)
	}
}
