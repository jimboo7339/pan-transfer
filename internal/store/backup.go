package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 备份与还原。
//
// 背景：data/pantransfer.db 是用户唯一的有状态资产（凭据 + 任务 + 历史），
// 而 Docker 镜像里没有 sqlite3 可执行文件，README 让用户「手动 .backup」在容器
// 场景下根本做不到。因此备份必须是产品能力，不能是文档里的一句提示。
//
// 做法：VACUUM INTO 生成一致性快照。
// 直接拷贝正在写入的 SQLite 主文件会漏掉 WAL 里的事务（实测 data/*.db-wal
// 已达数百 KB，而主库只有 4 KB），VACUUM INTO 由 SQLite 自己保证拿到一个
// 已提交且自洽的副本，且不需要停库。

// BackupDirName 是数据目录下的备份子目录名。
const BackupDirName = "backups"

// BackupPrefix 是备份文件名前缀。
const BackupPrefix = "pantransfer-"

// DefaultBackupKeep 是默认保留的备份份数。
const DefaultBackupKeep = 7

// BackupInfo 描述一份备份文件。
type BackupInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// BackupTo 用 VACUUM INTO 生成一份一致性快照并返回文件路径。
//
// 目标文件必须不存在（VACUUM INTO 不会覆盖），调用方负责清理同名旧文件。
func (s *Store) BackupTo(ctx context.Context, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("备份路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("创建备份目录失败: %w", err)
	}
	// VACUUM INTO 需要目标文件不存在；先清掉同名的残留。
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("清理旧备份失败: %w", err)
	}
	// VACUUM INTO 不接受绑定参数，路径必须内联；单引号在 SQLite 字符串里需翻倍。
	escaped := strings.ReplaceAll(path, "'", "''")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+escaped+"'"); err != nil {
		return "", fmt.Errorf("生成备份失败: %w", err)
	}
	return path, nil
}

// BackupDir 返回备份目录（dataDir/backups）。
func BackupDir(dataDir string) string { return filepath.Join(dataDir, BackupDirName) }

// BackupFileName 生成按时间戳命名的备份文件名。
func BackupFileName(t time.Time) string {
	return BackupPrefix + t.Format("20060102-150405") + ".db"
}

// ListBackups 按时间倒序列出备份目录中的快照。
func ListBackups(dir string) ([]BackupInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]BackupInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), BackupPrefix) || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// PruneBackups 只保留最近 keep 份备份，返回被删除的文件数。
func PruneBackups(dir string, keep int) (int, error) {
	if keep <= 0 {
		keep = DefaultBackupKeep
	}
	list, err := ListBackups(dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, b := range list[minInt(keep, len(list)):] {
		if err := os.Remove(filepath.Join(dir, b.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// RestoreFrom 用一份备份快照替换当前数据库。
//
// SQLite 无法在连接打开时替换底层文件，因此这里返回一个「待执行的还原计划」：
// 调用方（main）需要先关闭 *sql.DB，替换文件，再重新 Open。
// 这样设计是为了让 store 层不持有服务生命周期，避免出现半开状态。
type RestorePlan struct {
	// BackupPath 是源快照路径。
	BackupPath string
	// TargetPath 是当前数据库路径。
	TargetPath string
}

// PrepareRestore 校验备份文件可用，并返回还原计划。
// 真正的文件替换由调用方在关闭连接后执行（见 main.restoreAfterClose）。
func (s *Store) PrepareRestore(backupPath, targetPath string) (*RestorePlan, error) {
	abs, err := filepath.Abs(backupPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("备份文件不可用: %w", err)
	}
	// 校验快照是否可读且是合法 SQLite：只读打开并跑一次 quick_check。
	if err := verifySnapshot(abs); err != nil {
		return nil, err
	}
	return &RestorePlan{BackupPath: abs, TargetPath: targetPath}, nil
}

// verifySnapshot 只读打开快照并执行 PRAGMA quick_check。
func verifySnapshot(path string) error {
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("打开备份失败: %w", err)
	}
	defer func() { _ = db.Close() }()

	var result string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("备份完整性检查失败: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("备份完整性检查未通过: %s", result)
	}
	return nil
}

// ApplyRestore 执行还原：把快照复制成目标文件，并清理可能残留的 WAL/SHM。
// 必须在数据库连接关闭后调用，否则会破坏正在使用的连接。
func (p *RestorePlan) ApplyRestore() error {
	if err := copyFile(p.BackupPath, p.TargetPath); err != nil {
		return fmt.Errorf("写入数据库文件失败: %w", err)
	}
	// 旧的 WAL 属于被替换掉的数据库，必须删除，否则下次打开会把旧事务
	// 重放到新库上，造成数据错乱。
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(p.TargetPath + suffix)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp := dst + ".restore-tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// 原子替换：还原过程中崩溃不会留下半个数据库。
	return os.Rename(tmp, dst)
}
