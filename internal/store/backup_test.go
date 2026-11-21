package store

import (
	"context"
	"os"
	"path/filepath"
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

// openTestStore 打开一个测试库，并保证在测试结束时彻底释放文件句柄。
//
// Windows 上 sql.DB.Close() 不会立刻释放底层句柄，导致 t.TempDir() 的
// RemoveAll 报"目录非空"（测试本身已通过，只是清理失败）。
// SetMaxIdleConns(0) + 极短 ConnMaxLifetime 会让句柄在 Close 前就被回收。
func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pantransfer.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if db := s.DB(); db != nil {
			db.SetMaxIdleConns(0)
			db.SetConnMaxLifetime(time.Nanosecond)
		}
		_ = s.Close()
	})
	return s, dir
}

// TestBackupAndRestore 验证 VACUUM INTO 备份 + 还原的全流程。
//
// 这是关键路径：容器镜像里没有 sqlite3，用户唯一的备份手段就是产品内置的这个。
// 备份必须包含 WAL 里尚未 checkpoint 的事务，否则用户拿到的是残缺数据。
func TestBackupAndRestore(t *testing.T) {
	s, dir := openTestStore(t)
	dbPath := filepath.Join(dir, "pantransfer.db")
	ctx := context.Background()

	// 写入一个账号，然后**不关闭**直接备份：WAL 里应该还有未合并的数据。
	acc := &Account{Name: "夸克主号", DriverType: "quark", Config: `{"cookie":"abc"}`, IsActive: true}
	id, err := s.CreateAccount(ctx, acc)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	backupDir := filepath.Join(dir, BackupDirName)
	backupPath := filepath.Join(backupDir, BackupFileName(time.Now()))
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

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	forceCloseIdle(s)
	if err := plan.ApplyRestore(); err != nil {
		t.Fatalf("apply restore: %v", err)
	}

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
	s, dir := openTestStore(t)

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
}

// TestListBackupsIgnoresForeignFiles 验证备份目录里的无关文件不会被当成备份。
func TestListBackupsIgnoresForeignFiles(t *testing.T) {
	_, dir := openTestStore(t)

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
	s, _ := openTestStore(t)
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

// TestCountTasksByAccount 验证账号引用检查（删账号前必须知道有多少任务依赖它）。
// 全仓没有生效的外键，不检查就会留下悬空 account_id 的僵尸任务。
func TestCountTasksByAccount(t *testing.T) {
	s, _ := openTestStore(t)
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