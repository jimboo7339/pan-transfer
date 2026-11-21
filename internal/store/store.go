// Package store 提供 pan-transfer 的 SQLite 持久化：账号、任务、运行历史、
// 命名正则规则、转存历史（按名去重）与全局设置。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/secret"

	_ "modernc.org/sqlite"
)

// Store 封装 SQLite 连接与全部仓储方法。
type Store struct {
	db *sql.DB

	// cipher 保护账号凭据（cookie / token）静态加密。
	// 为 nil 时按明文读写，用于尚未初始化的场景与测试。
	cipher *secret.Cipher
}

// Open 打开（必要时创建）SQLite 数据库并执行迁移。
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite 写并发受限，串行化写入最稳。
	db.SetMaxOpenConns(1)
	s := &Store{db: db, cipher: secret.Default()}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
//
// 关闭前做一次 WAL 检查点并丢弃空闲连接，有两个作用：
//
//  1. 生产：干净的关闭会把 WAL 合并进主库，避免下次启动时回放一堆事务，
//     也让「停容器后直接拷贝 db 文件」这种朴素备份不至于漏数据。
//
//  2. 测试：Windows 上 sql.DB.Close() 不立即释放底层文件句柄，
//     t.TempDir() 的 RemoveAll 会间歇性报"目录非空"。先丢弃空闲连接
//     再关闭，句柄才会稳定释放（这是平台特性，不是产品缺陷）。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	// 检查点失败不阻断关闭：WAL 会在下次打开时自动回放。
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	s.db.SetMaxIdleConns(0)
	return s.db.Close()
}

// CloseIdle 丢弃连接池里的空闲连接，但不关闭数据库。
//
// 用途：测试需要在关库之前先把 WAL 合并掉，好让紧随其后的目录清理成功。
// 生产代码不需要调用它（Close 已经包含等价动作）。
func (s *Store) CloseIdle() {
	if s == nil || s.db == nil {
		return
	}
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	s.db.SetMaxIdleConns(0)
}

// SetCipher 注入凭据加密器（启动时由 main 调用；测试可传 nil 关闭加密）。
func (s *Store) SetCipher(c *secret.Cipher) { s.cipher = c }

// encryptConfig 加密账号配置 JSON；未初始化 cipher 时原样返回。
func (s *Store) encryptConfig(raw string) (string, error) {
	if s.cipher == nil || raw == "" {
		return raw, nil
	}
	return s.cipher.Encrypt(raw)
}

// decryptConfig 解密账号配置 JSON；未初始化 cipher 或值为历史明文时原样返回，
// 保证「先升级程序、后处理旧库」不会让所有账号一次性失效。
func (s *Store) decryptConfig(raw string) (string, error) {
	if s.cipher == nil || raw == "" {
		return raw, nil
	}
	out, err := s.cipher.Decrypt(raw)
	if err != nil {
		return "", fmt.Errorf("账号凭据解密失败（主密钥不匹配或数据损坏）：%w", err)
	}
	return out, nil
}

// withShareFIDInURL 把文件夹 fid 写入分享链接（CASX 约定：#/list/share/<fid>），
// 同时去掉已有 fid/fileId 参数与旧 fragment，避免重复或冲突。
func withShareFIDInURL(link, fid string) string {
	base := strings.TrimSpace(link)
	if i := strings.Index(base, "#"); i >= 0 {
		base = base[:i]
	}
	if i := strings.Index(base, "?"); i >= 0 {
		head, query := base[:i], base[i+1:]
		kept := make([]string, 0, 4)
		for _, kv := range strings.Split(query, "&") {
			if kv == "" {
				continue
			}
			k := kv
			if eq := strings.Index(kv, "="); eq >= 0 {
				k = kv[:eq]
			}
			if k == "fid" || k == "fileId" {
				continue
			}
			kept = append(kept, kv)
		}
		if len(kept) == 0 {
			base = head
		} else {
			base = head + "?" + strings.Join(kept, "&")
		}
	}
	f := strings.TrimSpace(fid)
	if f == "" || f == "0" || f == "root" {
		return base
	}
	return base + "#/list/share/" + f
}

func nowStr() string { return time.Now().Format(time.RFC3339) }

// ---------- 账号 ----------

// Account 是网盘账号记录；Config 为驱动 Addition 的 JSON（含 cookie/token）。
type Account struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	DriverType string    `json:"driver_type"`
	Config     string    `json:"config"`
	IsActive   bool      `json:"is_active"`
	SortOrder  int       `json:"sort_order"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	var created, updated string
	var active int
	if err := row.Scan(&a.ID, &a.Name, &a.DriverType, &a.Config, &active, &a.SortOrder, &created, &updated); err != nil {
		return nil, err
	}
	a.IsActive = active != 0
	a.CreatedAt, _ = time.Parse(time.RFC3339, created)
	a.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &a, nil
}

// scanAccountDecrypted 与 scanAccount 相同，但会把 config 从密文还原成明文 JSON。
// 读路径必须经过这里，否则调用方拿到的是密文。
func (s *Store) scanAccountDecrypted(row interface{ Scan(...any) error }) (*Account, error) {
	a, err := scanAccount(row)
	if err != nil {
		return nil, err
	}
	plain, err := s.decryptConfig(a.Config)
	if err != nil {
		return nil, err
	}
	a.Config = plain
	return a, nil
}

const accountCols = "id, name, driver_type, config, is_active, sort_order, created_at, updated_at"

// CreateAccount 新建账号。凭据在写库前加密。
func (s *Store) CreateAccount(ctx context.Context, a *Account) (int64, error) {
	now := nowStr()
	stored, err := s.encryptConfig(a.Config)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO accounts (name, driver_type, config, is_active, sort_order, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		a.Name, a.DriverType, stored, boolInt(a.IsActive), a.SortOrder, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateAccount 更新账号（按 ID 全量更新可变字段）。凭据在写库前加密。
func (s *Store) UpdateAccount(ctx context.Context, a *Account) error {
	stored, err := s.encryptConfig(a.Config)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE accounts SET name=?, driver_type=?, config=?, is_active=?, sort_order=?, updated_at=? WHERE id=?`,
		a.Name, a.DriverType, stored, boolInt(a.IsActive), a.SortOrder, nowStr(), a.ID)
	return err
}

// DeleteAccount 删除账号。
func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id=?`, id)
	return err
}

// CountTasksByAccount 返回引用该账号的任务数。
// 删账号前必须检查：全仓没有真正生效的外键（foreign_keys 只是 PRAGMA），
// 直接删除会留下悬空 account_id，任务永久报「账号不存在」。
func (s *Store) CountTasksByAccount(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE account_id=?`, accountID).Scan(&n)
	return n, err
}

// GetAccount 按 ID 取账号（config 已解密为明文 JSON）。
func (s *Store) GetAccount(ctx context.Context, id int64) (*Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id=?`, id)
	a, err := s.scanAccountDecrypted(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.Errorf(domain.CodeNotFound, "账号不存在")
		}
		return nil, err
	}
	return a, nil
}

// ListAccounts 返回全部账号（sort_order, id 升序；config 已解密）。
func (s *Store) ListAccounts(ctx context.Context) ([]*Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountCols+` FROM accounts ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := s.scanAccountDecrypted(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AccountConfig 是账号 Config JSON 的通用形态：驱动 Addition 字段 + 认证字段平铺。
type AccountConfig map[string]string

// ParseAccountConfig 解析账号配置 JSON。
func ParseAccountConfig(raw string) AccountConfig {
	cfg := AccountConfig{}
	_ = json.Unmarshal([]byte(raw), &cfg)
	return cfg
}

// ---------- 追剧任务 ----------

const taskCols = `id, task_name, account_id, share_url, share_links, save_path, pattern, replace,
	ignore_extension, run_week, run_time, end_date, update_subdir, update_subdir_resave_mode,
	start_fid, sort_index, skip_history, status, last_run_at, last_run_status, last_run_message,
	last_tree_summary, created_at, updated_at, run_mode, run_cron, category_id`

func scanTask(row interface{ Scan(...any) error }) (*domain.DramaTask, error) {
	var t domain.DramaTask
	var ignoreExt, status int
	var created, updated string
	err := row.Scan(&t.ID, &t.TaskName, &t.AccountID, &t.ShareURL, &t.ShareLinks, &t.SavePath,
		&t.Pattern, &t.Replace, &ignoreExt, &t.RunWeek, &t.RunTime, &t.EndDate,
		&t.UpdateSubdir, &t.UpdateSubdirResaveMode, &t.StartFID, &t.SortIndex, &t.SkipHistory,
		&status, &t.LastRunAt, &t.LastRunStatus, &t.LastRunMessage, &t.LastTreeSummary,
		&created, &updated, &t.RunMode, &t.RunCron, &t.CategoryID)
	if err != nil {
		return nil, err
	}
	t.IgnoreExtension = ignoreExt != 0
	if status != 0 {
		t.Status = "running"
	} else {
		t.Status = "paused"
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, created)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &t, nil
}

// CreateTask 新建任务。
func (s *Store) CreateTask(ctx context.Context, t *domain.DramaTask) (int64, error) {
	now := nowStr()
	res, err := s.db.ExecContext(ctx, `INSERT INTO tasks (
		task_name, account_id, share_url, share_links, save_path, pattern, replace, ignore_extension,
		run_week, run_time, end_date, update_subdir, update_subdir_resave_mode, start_fid, sort_index,
		skip_history, status, run_mode, run_cron, category_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.TaskName, t.AccountID, t.ShareURL, t.ShareLinks, t.SavePath, t.Pattern, t.Replace, boolInt(t.IgnoreExtension),
		t.RunWeek, t.RunTime, t.EndDate, t.UpdateSubdir, t.UpdateSubdirResaveMode, t.StartFID, t.SortIndex,
		t.SkipHistory, boolInt(t.Status != "paused"), t.RunMode, t.RunCron, t.CategoryID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateTask 更新任务内容字段（不含运行状态）。
func (s *Store) UpdateTask(ctx context.Context, t *domain.DramaTask) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET
		task_name=?, account_id=?, share_url=?, share_links=?, save_path=?, pattern=?, replace=?,
		ignore_extension=?, run_week=?, run_time=?, end_date=?, update_subdir=?, update_subdir_resave_mode=?,
		start_fid=?, sort_index=?, skip_history=?, status=?, run_mode=?, run_cron=?, category_id=?, updated_at=? WHERE id=?`,
		t.TaskName, t.AccountID, t.ShareURL, t.ShareLinks, t.SavePath, t.Pattern, t.Replace,
		boolInt(t.IgnoreExtension), t.RunWeek, t.RunTime, t.EndDate, t.UpdateSubdir, t.UpdateSubdirResaveMode,
		t.StartFID, t.SortIndex, t.SkipHistory, boolInt(t.Status != "paused"), t.RunMode, t.RunCron, t.CategoryID, nowStr(), t.ID)
	return err
}

// UpdateTaskRunState 更新任务的最近运行结果。
func (s *Store) UpdateTaskRunState(ctx context.Context, id int64, lastRunAt, status, message, tree string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET last_run_at=?, last_run_status=?, last_run_message=?, last_tree_summary=?, updated_at=? WHERE id=?`,
		lastRunAt, status, message, tree, nowStr(), id)
	return err
}

// SetTaskStatus 暂停/恢复任务。
//
// 检查影响行数：UPDATE 对不存在的主键不会报错，只影响 0 行。
// 早先直接返回 err（nil），于是「暂停一个已被删除的任务」会返回成功——
// 批量操作时用户会看到"暂停成功"但什么都没发生。
func (s *Store) SetTaskStatus(ctx context.Context, id int64, status string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, updated_at=? WHERE id=?`,
		boolInt(status != "paused"), nowStr(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.Errorf(domain.CodeNotFound, "任务不存在")
	}
	return nil
}

// DeleteTask 删除任务及其运行历史与转存历史。
//
// 同样检查影响行数：删一个不存在的任务此前返回 nil，
// 批量删除时用户会以为删掉了。
func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM task_runs WHERE task_id=?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM saved_names WHERE task_id=?`, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.Errorf(domain.CodeNotFound, "任务不存在")
	}
	return nil
}

// GetTask 按 ID 取任务。
func (s *Store) GetTask(ctx context.Context, id int64) (*domain.DramaTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.Errorf(domain.CodeNotFound, "任务不存在")
	}
	return t, err
}

// ListTasks 返回全部任务。
func (s *Store) ListTasks(ctx context.Context) ([]*domain.DramaTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.DramaTask
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListActiveTasks 返回启用中的任务。
func (s *Store) ListActiveTasks(ctx context.Context) ([]*domain.DramaTask, error) {
	all, err := s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var out []*domain.DramaTask
	for _, t := range all {
		if t.Status != "paused" {
			out = append(out, t)
		}
	}
	return out, nil
}

// ---------- 运行历史 ----------

// TaskRun 是一次任务运行记录（link 字段记录本次使用的分享链接）。
type TaskRun struct {
	ID            int64     `json:"id"`
	TaskID        int64     `json:"task_id"`
	Status        string    `json:"status"`
	Message       string    `json:"message"`
	TreeSummary   string    `json:"tree_summary"`
	Log           string    `json:"log"`
	TransferCount int       `json:"transfer_count"`
	Link          string    `json:"link"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
}

// CreateRun 写入运行记录（started）。
func (s *Store) CreateRun(ctx context.Context, taskID int64, link string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO task_runs (task_id, status, link, started_at) VALUES (?,?,?,?)`,
		taskID, "running", link, nowStr())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishRun 回写运行结果（log 为执行器详细日志，可为空）。
func (s *Store) FinishRun(ctx context.Context, runID int64, status, message, tree, log string, count int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE task_runs SET status=?, message=?, tree_summary=?, log=?, transfer_count=?, finished_at=? WHERE id=?`,
		status, message, tree, log, count, nowStr(), runID)
	return err
}

// ListRuns 返回任务的运行历史（新→旧）。
func (s *Store) ListRuns(ctx context.Context, taskID int64, limit int) ([]*TaskRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, status, message, tree_summary, log, transfer_count, link, started_at, finished_at
		FROM task_runs WHERE task_id=? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TaskRun
	for rows.Next() {
		var r TaskRun
		var started, finished string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Status, &r.Message, &r.TreeSummary, &r.Log, &r.TransferCount, &r.Link, &started, &finished); err != nil {
			return nil, err
		}
		r.StartedAt, _ = time.Parse(time.RFC3339, started)
		r.FinishedAt, _ = time.Parse(time.RFC3339, finished)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ---------- 命名正则规则 ----------

// UpsertRule 创建或覆盖规则。
func (s *Store) UpsertRule(ctx context.Context, r *domain.MagicRegexRule) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO magic_regex_rules (key, label, pattern, replace, enabled)
		VALUES (?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET label=excluded.label, pattern=excluded.pattern,
		replace=excluded.replace, enabled=excluded.enabled`,
		r.Key, r.Label, r.Pattern, r.Replace, boolInt(r.Enabled))
	return err
}

// DeleteRule 删除规则。
func (s *Store) DeleteRule(ctx context.Context, key string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM magic_regex_rules WHERE key=?`, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.Errorf(domain.CodeNotFound, "规则不存在")
	}
	return nil
}

// GetRule 按 key 取规则。
func (s *Store) GetRule(ctx context.Context, key string) (*domain.MagicRegexRule, error) {
	row := s.db.QueryRowContext(ctx, `SELECT key, label, pattern, replace, enabled FROM magic_regex_rules WHERE key=?`, key)
	var r domain.MagicRegexRule
	var enabled int
	if err := row.Scan(&r.Key, &r.Label, &r.Pattern, &r.Replace, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.Errorf(domain.CodeNotFound, "规则不存在")
		}
		return nil, err
	}
	r.Enabled = enabled != 0
	return &r, nil
}

// ListRules 返回全部规则。
func (s *Store) ListRules(ctx context.Context) ([]*domain.MagicRegexRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, label, pattern, replace, enabled FROM magic_regex_rules ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.MagicRegexRule
	for rows.Next() {
		var r domain.MagicRegexRule
		var enabled int
		if err := rows.Scan(&r.Key, &r.Label, &r.Pattern, &r.Replace, &enabled); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ---------- 转存历史（按名去重） ----------

// SavedName 是一条转存历史。
type SavedName struct {
	ID       int64     `json:"id"`
	TaskID   int64     `json:"task_id"`
	NormName string    `json:"norm_name"`
	FileName string    `json:"file_name"`
	FileSize int64     `json:"file_size"`
	SavedAt  time.Time `json:"saved_at"`
}

// AddSavedNames 批量记录转存历史（忽略冲突）。
func (s *Store) AddSavedNames(ctx context.Context, taskID int64, items []SavedName) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, it := range items {
		if strings.TrimSpace(it.NormName) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO saved_names (task_id, norm_name, file_name, file_size, saved_at)
			VALUES (?,?,?,?,?)`, taskID, it.NormName, it.FileName, it.FileSize, nowStr()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListSavedNames 返回任务最近 limit 条转存历史（新→旧）。
func (s *Store) ListSavedNames(ctx context.Context, taskID int64, limit int) ([]*SavedName, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, norm_name, file_name, file_size, saved_at
		FROM saved_names WHERE task_id=? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SavedName
	for rows.Next() {
		var n SavedName
		var savedAt string
		if err := rows.Scan(&n.ID, &n.TaskID, &n.NormName, &n.FileName, &n.FileSize, &savedAt); err != nil {
			return nil, err
		}
		n.SavedAt, _ = time.Parse(time.RFC3339, savedAt)
		out = append(out, &n)
	}
	return out, rows.Err()
}

// CountSavedNames 返回任务的转存历史条数。
func (s *Store) CountSavedNames(ctx context.Context, taskID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM saved_names WHERE task_id=?`, taskID).Scan(&n)
	return n, err
}

// ClearSavedNames 清空任务的转存历史（之后运行会按目标目录现状重新判定，可能重复转存）。
func (s *Store) ClearSavedNames(ctx context.Context, taskID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM saved_names WHERE task_id=?`, taskID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkInterruptedRuns 把进程崩溃后遗留的 running 运行记录收尾。
//
// 为什么必须做：task_runs.status='running' 是进程内状态。进程被 kill / OOM /
// 断电后没人把它改掉，于是「运行日志」里会永远留着一批转圈的任务，
// 用户无法判断"到底跑没跑完"。启动时统一收尾成 failed 并写明原因。
//
// 必须在服务开始调度前调用（启动时 s.running 必为空，不会误伤真实运行中的任务）。
func (s *Store) MarkInterruptedRuns(ctx context.Context, message string) (int64, error) {
	if strings.TrimSpace(message) == "" {
		message = "服务重启，运行中断"
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE task_runs SET status='failed', message=?, finished_at=? WHERE status='running'`,
		message, nowStr())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneRuns 清理旧的运行记录：删除 started_at 早于 keepDays 天前、且不在每个任务
// 最近 keepPerTask 条之内的记录；返回删除条数。历史是排障依据，少量保底防误删。
func (s *Store) PruneRuns(ctx context.Context, keepDays, keepPerTask int) (int64, error) {
	if keepDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format(time.RFC3339)
	if keepPerTask <= 0 {
		keepPerTask = 50
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM task_runs WHERE started_at < ? AND id NOT IN (
		SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY task_id ORDER BY started_at DESC, id DESC) AS rn FROM task_runs
		) WHERE rn <= ?)`, cutoff, keepPerTask)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DailyTransferCount 是一天的转存统计（仪表盘用）。
type DailyTransferCount struct {
	Date  string `json:"date"`  // YYYY-MM-DD
	Count int    `json:"count"` // 当天转存文件数
	Runs  int    `json:"runs"`  // 当天运行次数
}

// TransferStats 返回最近 N 天的转存统计（按天聚合）。
//
// 为什么在 SQL 里聚合而不是把 task_runs 全捞到内存：运行记录可能上万条
// （单条最大 256KB 的日志），全捞到内存再算既慢又浪费。
//
// 注意：started_at 存的是 RFC3339（带时区偏移，如 2026-11-21T14:30:00+08:00）。
// 直接和 date('now') 这种纯日期字符串比较，会走字符串字典序而非时间序，
// 跨时区时结果不稳定。所以这里统一用 date(started_at) 取日期部分来分组，
// 并在应用层补齐缺失的日期。
func (s *Store) TransferStats(ctx context.Context, days int) ([]DailyTransferCount, error) {
	if days <= 0 {
		days = 7
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT date(started_at) AS d,
		       COALESCE(SUM(transfer_count), 0) AS c,
		       COUNT(*) AS r
		FROM task_runs
		WHERE started_at != ''
		GROUP BY d
		ORDER BY d DESC
		LIMIT ?`, days*3)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byDate := map[string]DailyTransferCount{}
	for rows.Next() {
		var d DailyTransferCount
		if err := rows.Scan(&d.Date, &d.Count, &d.Runs); err != nil {
			return nil, err
		}
		byDate[d.Date] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 补齐缺失的日期（前端画趋势条需要连续 N 个点，
	// 缺的天补 0，否则图会错位）。
	out := make([]DailyTransferCount, 0, days)
	today := time.Now()
	for i := days - 1; i >= 0; i-- {
		day := today.AddDate(0, 0, -i).Format("2006-01-02")
		if v, ok := byDate[day]; ok {
			out = append(out, v)
		} else {
			out = append(out, DailyTransferCount{Date: day})
		}
	}
	return out, nil
}

// RecentFailures 返回最近失败的运行记录（仪表盘告警用）。
func (s *Store) RecentFailures(ctx context.Context, limit int) ([]*TaskRun, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, status, message, tree_summary, log, transfer_count, link, started_at, finished_at
		FROM task_runs
		WHERE status = 'failed'
		ORDER BY id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*TaskRun
	for rows.Next() {
		var r TaskRun
		var started, finished string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Status, &r.Message, &r.TreeSummary,
			&r.Log, &r.TransferCount, &r.Link, &started, &finished); err != nil {
			return nil, err
		}
		r.StartedAt, _ = time.Parse(time.RFC3339, started)
		r.FinishedAt, _ = time.Parse(time.RFC3339, finished)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// CountAllSavedNames 返回全部转存历史条数（仪表盘用）。
//
// 这个数字有意义：saved_names 是唯一无界增长的表（去重依据，不能随意清理），
// 用户在设置页看到"累计转存 12345 个文件"时能直观感受到规模，
// 也能在数字异常暴涨时察觉"是不是在重复转存"。
func (s *Store) CountAllSavedNames(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM saved_names`).Scan(&n)
	return n, err
}

// SavedNormNames 返回任务全部已保存的规范化文件名（去重跳过用）。
func (s *Store) SavedNormNames(ctx context.Context, taskID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT norm_name FROM saved_names WHERE task_id=?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---------- 设置 ----------

// GetSetting 读取设置；不存在返回空串。
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting 写入设置。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
