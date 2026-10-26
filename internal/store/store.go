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

	_ "modernc.org/sqlite"
)

// Store 封装 SQLite 连接与全部仓储方法。
type Store struct {
	db *sql.DB
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
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS accounts (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL,
	driver_type TEXT NOT NULL,
	config      TEXT NOT NULL DEFAULT '{}',
	is_active   INTEGER NOT NULL DEFAULT 1,
	sort_order  INTEGER NOT NULL DEFAULT 0,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
	id                      INTEGER PRIMARY KEY AUTOINCREMENT,
	task_name               TEXT NOT NULL,
	account_id              INTEGER NOT NULL,
	share_url               TEXT NOT NULL DEFAULT '',
	share_links             TEXT NOT NULL DEFAULT '[]',
	save_path               TEXT NOT NULL DEFAULT '',
	pattern                 TEXT NOT NULL DEFAULT '',
	replace                 TEXT NOT NULL DEFAULT '',
	ignore_extension        INTEGER NOT NULL DEFAULT 0,
	run_week                TEXT NOT NULL DEFAULT '',
	run_time                TEXT NOT NULL DEFAULT '',
	end_date                TEXT NOT NULL DEFAULT '',
	update_subdir           TEXT NOT NULL DEFAULT '',
	update_subdir_resave_mode TEXT NOT NULL DEFAULT '',
	start_fid               TEXT NOT NULL DEFAULT '',
	sort_index              INTEGER NOT NULL DEFAULT 1,
	skip_history            TEXT NOT NULL DEFAULT 'global',
	status                  TEXT NOT NULL DEFAULT 'running',
	last_run_at             TEXT NOT NULL DEFAULT '',
	last_run_status         TEXT NOT NULL DEFAULT '',
	last_run_message        TEXT NOT NULL DEFAULT '',
	last_tree_summary       TEXT NOT NULL DEFAULT '',
	created_at              TEXT NOT NULL,
	updated_at              TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_runs (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id       INTEGER NOT NULL,
	status        TEXT NOT NULL,
	message       TEXT NOT NULL DEFAULT '',
	tree_summary  TEXT NOT NULL DEFAULT '',
	transfer_count INTEGER NOT NULL DEFAULT 0,
	link          TEXT NOT NULL DEFAULT '',
	started_at    TEXT NOT NULL,
	finished_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs(task_id, id DESC);
CREATE TABLE IF NOT EXISTS magic_regex_rules (
	key     TEXT PRIMARY KEY,
	label   TEXT NOT NULL DEFAULT '',
	pattern TEXT NOT NULL DEFAULT '',
	replace TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS saved_names (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id   INTEGER NOT NULL,
	norm_name TEXT NOT NULL,
	file_name TEXT NOT NULL,
	file_size INTEGER NOT NULL DEFAULT 0,
	saved_at  TEXT NOT NULL,
	UNIQUE(task_id, norm_name)
);
CREATE INDEX IF NOT EXISTS idx_saved_names_task ON saved_names(task_id);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// 旧库迁移：v0.1 任务无 run_mode 列，补上（已存在则忽略）
	if _, err := s.db.Exec(`ALTER TABLE tasks ADD COLUMN run_mode TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	// 旧库迁移：v0.1 任务无 run_cron 列
	if _, err := s.db.Exec(`ALTER TABLE tasks ADD COLUMN run_cron TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	return nil
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

const accountCols = "id, name, driver_type, config, is_active, sort_order, created_at, updated_at"

// CreateAccount 新建账号。
func (s *Store) CreateAccount(ctx context.Context, a *Account) (int64, error) {
	now := nowStr()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO accounts (name, driver_type, config, is_active, sort_order, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		a.Name, a.DriverType, a.Config, boolInt(a.IsActive), a.SortOrder, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateAccount 更新账号（按 ID 全量更新可变字段）。
func (s *Store) UpdateAccount(ctx context.Context, a *Account) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET name=?, driver_type=?, config=?, is_active=?, sort_order=?, updated_at=? WHERE id=?`,
		a.Name, a.DriverType, a.Config, boolInt(a.IsActive), a.SortOrder, nowStr(), a.ID)
	return err
}

// DeleteAccount 删除账号。
func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id=?`, id)
	return err
}

// GetAccount 按 ID 取账号。
func (s *Store) GetAccount(ctx context.Context, id int64) (*Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id=?`, id)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.Errorf(domain.CodeNotFound, "账号不存在")
	}
	return a, err
}

// ListAccounts 返回全部账号（sort_order, id 升序）。
func (s *Store) ListAccounts(ctx context.Context) ([]*Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountCols+` FROM accounts ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
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
	last_tree_summary, created_at, updated_at, run_mode, run_cron`

func scanTask(row interface{ Scan(...any) error }) (*domain.DramaTask, error) {
	var t domain.DramaTask
	var ignoreExt, status int
	var created, updated string
	err := row.Scan(&t.ID, &t.TaskName, &t.AccountID, &t.ShareURL, &t.ShareLinks, &t.SavePath,
		&t.Pattern, &t.Replace, &ignoreExt, &t.RunWeek, &t.RunTime, &t.EndDate,
		&t.UpdateSubdir, &t.UpdateSubdirResaveMode, &t.StartFID, &t.SortIndex, &t.SkipHistory,
		&status, &t.LastRunAt, &t.LastRunStatus, &t.LastRunMessage, &t.LastTreeSummary,
		&created, &updated, &t.RunMode, &t.RunCron)
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
		skip_history, status, run_mode, run_cron, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.TaskName, t.AccountID, t.ShareURL, t.ShareLinks, t.SavePath, t.Pattern, t.Replace, boolInt(t.IgnoreExtension),
		t.RunWeek, t.RunTime, t.EndDate, t.UpdateSubdir, t.UpdateSubdirResaveMode, t.StartFID, t.SortIndex,
		t.SkipHistory, boolInt(t.Status != "paused"), t.RunMode, t.RunCron, now, now)
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
		start_fid=?, sort_index=?, skip_history=?, status=?, run_mode=?, run_cron=?, updated_at=? WHERE id=?`,
		t.TaskName, t.AccountID, t.ShareURL, t.ShareLinks, t.SavePath, t.Pattern, t.Replace,
		boolInt(t.IgnoreExtension), t.RunWeek, t.RunTime, t.EndDate, t.UpdateSubdir, t.UpdateSubdirResaveMode,
		t.StartFID, t.SortIndex, t.SkipHistory, boolInt(t.Status != "paused"), t.RunMode, t.RunCron, nowStr(), t.ID)
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
func (s *Store) SetTaskStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, updated_at=? WHERE id=?`,
		boolInt(status != "paused"), nowStr(), id)
	return err
}

// DeleteTask 删除任务及其运行历史与转存历史。
func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM task_runs WHERE task_id=?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM saved_names WHERE task_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id=?`, id)
	return err
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

// FinishRun 回写运行结果。
func (s *Store) FinishRun(ctx context.Context, runID int64, status, message, tree string, count int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE task_runs SET status=?, message=?, tree_summary=?, transfer_count=?, finished_at=? WHERE id=?`,
		status, message, tree, count, nowStr(), runID)
	return err
}

// ListRuns 返回任务的运行历史（新→旧）。
func (s *Store) ListRuns(ctx context.Context, taskID int64, limit int) ([]*TaskRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, status, message, tree_summary, transfer_count, link, started_at, finished_at
		FROM task_runs WHERE task_id=? ORDER BY id DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TaskRun
	for rows.Next() {
		var r TaskRun
		var started, finished string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Status, &r.Message, &r.TreeSummary, &r.TransferCount, &r.Link, &started, &finished); err != nil {
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
