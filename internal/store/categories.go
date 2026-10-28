// 任务分类与分类回调：分类是回调的载体，任务按 category_id 归属；
// 回调日志记录每次实际调用的结果供排障。
package store

import (
	"context"
	"time"
)

// TaskCategory 任务分类 + 分类回调配置。
type TaskCategory struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	CallbackEnabled    bool      `json:"callback_enabled"`
	CallbackMode       string    `json:"callback_mode"` // round=分类轮次完成 | per_task=单任务完成
	CallbackMethod     string    `json:"callback_method"`
	CallbackURL        string    `json:"callback_url"`
	CallbackHeaders    string    `json:"callback_headers"` // JSON 对象字符串
	CallbackBody       string    `json:"callback_body"`    // Body 模板，空=用默认模板
	CallbackTimeoutSec int       `json:"callback_timeout_sec"`
	SortOrder          int       `json:"sort_order"`
	TaskCount          int       `json:"task_count"`       // 列表接口附带
	LastCallbackAt     string    `json:"last_callback_at"` // 列表接口附带
	LastCallbackOK     *bool     `json:"last_callback_ok"` // nil=从未回调
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// CallbackLog 一次分类回调的执行记录。
type CallbackLog struct {
	ID         int64     `json:"id"`
	CategoryID int64     `json:"category_id"`
	Kind       string    `json:"kind"` // task | round | test
	Target     string    `json:"target"`
	StatusCode int       `json:"status_code"`
	OK         bool      `json:"ok"`
	Response   string    `json:"response"`
	CreatedAt  time.Time `json:"created_at"`
}

const categoryCols = `id, name, callback_enabled, callback_mode, callback_method, callback_url,
	callback_headers, callback_body, callback_timeout_sec, sort_order, created_at, updated_at`

func scanCategory(row interface{ Scan(...any) error }) (*TaskCategory, error) {
	var c TaskCategory
	var enabled int
	var created, updated string
	if err := row.Scan(&c.ID, &c.Name, &enabled, &c.CallbackMode, &c.CallbackMethod,
		&c.CallbackURL, &c.CallbackHeaders, &c.CallbackBody, &c.CallbackTimeoutSec,
		&c.SortOrder, &created, &updated); err != nil {
		return nil, err
	}
	c.CallbackEnabled = enabled != 0
	c.CreatedAt, _ = time.Parse(time.RFC3339, created)
	c.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &c, nil
}

// ListCategories 全部分类（按 sort_order, id）。
func (s *Store) ListCategories(ctx context.Context) ([]*TaskCategory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+categoryCols+` FROM task_categories ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TaskCategory
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCategory 单个分类。
func (s *Store) GetCategory(ctx context.Context, id int64) (*TaskCategory, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+categoryCols+` FROM task_categories WHERE id=?`, id)
	c, err := scanCategory(row)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// SaveCategory 新建（ID=0）或更新分类。
func (s *Store) SaveCategory(ctx context.Context, c *TaskCategory) (int64, error) {
	now := nowStr()
	if c.CallbackMode != "per_task" {
		c.CallbackMode = "round"
	}
	if c.CallbackMethod != "GET" {
		c.CallbackMethod = "POST"
	}
	if c.CallbackTimeoutSec <= 0 {
		c.CallbackTimeoutSec = 15
	}
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO task_categories (
			name, callback_enabled, callback_mode, callback_method, callback_url, callback_headers,
			callback_body, callback_timeout_sec, sort_order, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			c.Name, boolInt(c.CallbackEnabled), c.CallbackMode, c.CallbackMethod, c.CallbackURL,
			c.CallbackHeaders, c.CallbackBody, c.CallbackTimeoutSec, c.SortOrder, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE task_categories SET
		name=?, callback_enabled=?, callback_mode=?, callback_method=?, callback_url=?, callback_headers=?,
		callback_body=?, callback_timeout_sec=?, sort_order=?, updated_at=? WHERE id=?`,
		c.Name, boolInt(c.CallbackEnabled), c.CallbackMode, c.CallbackMethod, c.CallbackURL,
		c.CallbackHeaders, c.CallbackBody, c.CallbackTimeoutSec, c.SortOrder, now, c.ID)
	return c.ID, err
}

// DeleteCategory 删除分类；其下任务置为未分类，回调日志一并清理。
func (s *Store) DeleteCategory(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET category_id=0 WHERE category_id=?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM callback_logs WHERE category_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM task_categories WHERE id=?`, id)
	return err
}

// CountTasksInCategory 分类下的任务数。
func (s *Store) CountTasksInCategory(ctx context.Context, id int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE category_id=?`, id).Scan(&n)
	return n, err
}

// SaveCallbackLog 记录一次回调并只保留该分类最近 100 条。
func (s *Store) SaveCallbackLog(ctx context.Context, categoryID int64, kind, target string, statusCode int, ok bool, response string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO callback_logs (category_id, kind, target, status_code, ok, response, created_at)
		VALUES (?,?,?,?,?,?,?)`, categoryID, kind, target, statusCode, boolInt(ok), response, nowStr()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM callback_logs WHERE category_id=? AND id NOT IN (
		SELECT id FROM callback_logs WHERE category_id=? ORDER BY id DESC LIMIT 100)`, categoryID, categoryID)
	return err
}

// ListCallbackLogs 分类最近回调记录（新→旧）。
func (s *Store) ListCallbackLogs(ctx context.Context, categoryID int64, limit int) ([]*CallbackLog, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, category_id, kind, target, status_code, ok, response, created_at
		FROM callback_logs WHERE category_id=? ORDER BY id DESC LIMIT ?`, categoryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CallbackLog
	for rows.Next() {
		var l CallbackLog
		var okInt int
		var createdAt string
		if err := rows.Scan(&l.ID, &l.CategoryID, &l.Kind, &l.Target, &l.StatusCode, &okInt, &l.Response, &createdAt); err != nil {
			return nil, err
		}
		l.OK = okInt != 0
		l.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		out = append(out, &l)
	}
	return out, rows.Err()
}

// LastCallback 返回分类最近一次回调（无则 nil）。
func (s *Store) LastCallback(ctx context.Context, categoryID int64) (*CallbackLog, error) {
	logs, err := s.ListCallbackLogs(ctx, categoryID, 1)
	if err != nil || len(logs) == 0 {
		return nil, err
	}
	return logs[0], nil
}
