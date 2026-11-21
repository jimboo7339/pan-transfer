package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// parseLinksJSON 解析 share_links 字段（JSON 字符串数组）。
func parseLinksJSON(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var links []string
	if err := json.Unmarshal([]byte(raw), &links); err != nil {
		return nil
	}
	return links
}

// 数据库迁移的版本化。
//
// 之前的实现是一串 `ALTER TABLE ... ADD COLUMN`，靠匹配错误字符串
// （strings.Contains(err.Error(), "duplicate column")）判断列是否存在。三个问题：
//
//  1. 无法表达"向前不兼容"的变更（改类型、拆表、回填只能写一次性特化函数）；
//  2. 没有回滚路径：新版本写入的数据一旦落库，退回旧镜像即数据不可读；
//  3. 错误文案依赖驱动实现。modernc 与 mattn、甚至不同版本的措辞一旦变化，
//     迁移会在启动时 return err 让进程直接退出（main 里是 os.Exit(1)），
//     用户看到的是"升级后打不开"。
//
// 这里改成标准做法：一张 schema_migrations 记录已应用的版本号，每个迁移在
// 单个事务内完成并写回版本号。重复执行是幂等的。
//
// 迁移规则：
//   - 只追加，不修改已发布的迁移（否则老库与新库的 schema 会分叉）；
//   - 每个迁移必须可重入：用 PRAGMA table_info 判断而不是捕获错误串；
//   - 数据回填放迁移内，大表回填会拉长启动时间，属预期行为。

// migration 是一个带版本号的迁移。
type migration struct {
	version int
	name    string
	up      func(*sql.Tx) error
}

// migrations 按版本升序排列。新增迁移追加到末尾，不要复用版本号。
var migrations = []migration{
	{version: 1, name: "baseline_schema", up: migrateBaseline},
	{version: 2, name: "tasks_run_mode", up: addColumn("tasks", "run_mode", "TEXT NOT NULL DEFAULT ''")},
	{version: 3, name: "tasks_run_cron", up: addColumn("tasks", "run_cron", "TEXT NOT NULL DEFAULT ''")},
	{version: 4, name: "tasks_share_fid", up: addColumn("tasks", "share_fid", "TEXT NOT NULL DEFAULT ''")},
	{version: 5, name: "task_runs_log", up: addColumn("task_runs", "log", "TEXT NOT NULL DEFAULT ''")},
	{version: 6, name: "tasks_category_id", up: addColumn("tasks", "category_id", "INTEGER NOT NULL DEFAULT 0")},
	{version: 7, name: "categories_skip_zero", up: addColumn("task_categories", "skip_zero", "INTEGER NOT NULL DEFAULT 0")},
	{version: 8, name: "settings_auth_hardening", up: migrateAuthHardening},
}

// CurrentSchemaVersion 返回迁移列表的最新版本号。
func CurrentSchemaVersion() int { return migrations[len(migrations)-1].version }

// migrate 应用所有未执行的迁移。
//
// 每个迁移在独立事务里跑（DDL + 版本号一起提交），任一失败则回滚该迁移并
// 中止启动——保留"启动即失败"的语义，但错误信息里带上版本号，便于定位。
func (s *Store) migrate() error {
	const schema = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	applied_at TEXT NOT NULL
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("创建 schema_migrations 失败: %w", err)
	}

	applied, err := s.appliedVersions()
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		// 全新库：先建基线 schema，再补齐后续列（基线里已经包含的列会被
		// addColumn 的 PRAGMA 检查跳过，因此直接全量跑一遍最省心）。
		if err := s.runMigration(migrations[0]); err != nil {
			return err
		}
		applied[1] = true
	}

	for _, m := range migrations {
		if m.version == 1 && applied[1] {
			continue
		}
		if applied[m.version] {
			continue
		}
		if err := s.runMigration(m); err != nil {
			return fmt.Errorf("迁移 v%d (%s) 失败: %w", m.version, m.name, err)
		}
	}

	// 幂等数据回填：每次启动都跑一次，而不是只跑一次。
	//
	// 为什么不做成版本迁移：旧数据迁移必须在"列已存在"之后才有意义，
	// 而全新库首启时 tasks 表可能刚建好还是空的；把它绑成一次性迁移会让
	// 「先插入带 share_fid 的旧数据、再升级」这条路径彻底失效（历史上正是
	// 这个原因导致测试里迁移不生效）。
	// 该函数本身幂等（只处理 share_fid != '' 的行），重复执行无副作用。
	if err := s.migrateLegacyShareFID(); err != nil {
		return fmt.Errorf("回填旧 share_fid 失败: %w", err)
	}
	return nil
}

// migrateLegacyShareFID 把旧的 task 级 share_fid 并入链接后清空；可重复执行。
func (s *Store) migrateLegacyShareFID() error {
	rows, err := s.db.Query(`SELECT id, share_links, share_url, share_fid FROM tasks WHERE share_fid != ''`)
	if err != nil {
		// 列还不存在时无需回填（全新库基线已含该列）。
		if strings.Contains(err.Error(), "no such column") {
			return nil
		}
		return err
	}
	type legacy struct {
		id    int64
		links string
		url   string
		fid   string
	}
	var list []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.id, &l.links, &l.url, &l.fid); err != nil {
			rows.Close()
			return err
		}
		list = append(list, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range list {
		links := parseLinksJSON(l.links)
		if len(links) == 0 && strings.TrimSpace(l.url) != "" {
			links = []string{strings.TrimSpace(l.url)}
		}
		if len(links) == 1 {
			link := withShareFIDInURL(links[0], l.fid)
			b, _ := json.Marshal([]string{link})
			if _, err := s.db.Exec(`UPDATE tasks SET share_links=?, share_url=?, share_fid='' WHERE id=?`,
				string(b), link, l.id); err != nil {
				return err
			}
			continue
		}
		if _, err := s.db.Exec(`UPDATE tasks SET share_fid='' WHERE id=?`, l.id); err != nil {
			return err
		}
	}
	return nil
}

// appliedVersions 返回已应用版本集合。
func (s *Store) appliedVersions() (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// runMigration 在单个事务内执行迁移并记录版本号。
func (s *Store) runMigration(m migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := m.up(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(version) DO UPDATE SET name=excluded.name`,
		m.version, m.name); err != nil {
		return err
	}
	return tx.Commit()
}

// addColumn 返回一个"列不存在才添加"的迁移。
// 用 PRAGMA table_info 判断，而不是匹配 ALTER 的错误文案。
func addColumn(table, column, definition string) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		has, err := columnExists(tx, table, column)
		if err != nil {
			return err
		}
		if has {
			return nil
		}
		_, err = tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
		return err
	}
}

// columnExists 判断表是否已有某列。
func columnExists(tx *sql.Tx, table, column string) (bool, error) {
	// table_info 不接受参数，表名只能内联；表名均为代码内常量，非用户输入。
	rows, err := tx.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		// 表还不存在（全新库首次迁移），视为没有该列，交给基线建表处理。
		if strings.Contains(err.Error(), "no such table") {
			return false, nil
		}
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// migrateBaseline 建全新库的基线表结构。
// 与旧的 const schema 等价，但版本化之后不再需要 IF NOT EXISTS 兜底。
func migrateBaseline(tx *sql.Tx) error {
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
	share_fid               TEXT NOT NULL DEFAULT '',
	category_id             INTEGER NOT NULL DEFAULT 0,
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
	log           TEXT NOT NULL DEFAULT '',
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
CREATE TABLE IF NOT EXISTS task_categories (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	name               TEXT NOT NULL,
	callback_enabled   INTEGER NOT NULL DEFAULT 0,
	callback_mode      TEXT NOT NULL DEFAULT 'round',
	callback_method    TEXT NOT NULL DEFAULT 'POST',
	callback_url       TEXT NOT NULL DEFAULT '',
	callback_headers   TEXT NOT NULL DEFAULT '',
	callback_body      TEXT NOT NULL DEFAULT '',
	callback_timeout_sec INTEGER NOT NULL DEFAULT 15,
	skip_zero          INTEGER NOT NULL DEFAULT 0,
	sort_order         INTEGER NOT NULL DEFAULT 0,
	created_at         TEXT NOT NULL,
	updated_at         TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS callback_logs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	category_id INTEGER NOT NULL,
	kind        TEXT NOT NULL DEFAULT '',
	target      TEXT NOT NULL DEFAULT '',
	status_code INTEGER NOT NULL DEFAULT 0,
	ok          INTEGER NOT NULL DEFAULT 0,
	response    TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_callback_logs_cat ON callback_logs(category_id, id DESC);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);`
	_, err := tx.Exec(schema)
	return err
}

// migrateLegacyShareFIDTx 把旧的 task 级 share_fid 并入链接后清空。
// 单链接任务能确定归属；多链接无法判断归属哪一条，直接清空，避免套用到错误链接。
func migrateLegacyShareFIDTx(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, share_links, share_url, share_fid FROM tasks WHERE share_fid != ''`)
	if err != nil {
		// 老库可能还没有该列；由 addColumn 迁移负责，这里静默跳过。
		if strings.Contains(err.Error(), "no such column") {
			return nil
		}
		return err
	}
	type legacy struct {
		id    int64
		links string
		url   string
		fid   string
	}
	var list []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.id, &l.links, &l.url, &l.fid); err != nil {
			rows.Close()
			return err
		}
		list = append(list, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range list {
		links := parseLinksJSON(l.links)
		if len(links) == 0 && strings.TrimSpace(l.url) != "" {
			links = []string{strings.TrimSpace(l.url)}
		}
		if len(links) == 1 {
			link := withShareFIDInURL(links[0], l.fid)
			b, _ := json.Marshal([]string{link})
			if _, err := tx.Exec(`UPDATE tasks SET share_links=?, share_url=?, share_fid='' WHERE id=?`,
				string(b), link, l.id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(`UPDATE tasks SET share_fid='' WHERE id=?`, l.id); err != nil {
			return err
		}
	}
	return nil
}

// migrateAuthHardening 把旧的无盐 SHA-256 口令哈希迁移掉。
//
// 旧格式是 sha256("pan-transfer:" + pwd)，DB 泄露后可离线彩虹表爆破。
// 这里不能"就地转换"（哈希不可逆），只能给旧哈希打上标记，让登录成功时
// 顺带升级（见 httpapi.handleLogin）。标记放在 settings 里，避免污染凭据表。
func migrateAuthHardening(tx *sql.Tx) error {
	var pwd string
	err := tx.QueryRow(`SELECT value FROM settings WHERE key='auth_password'`).Scan(&pwd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// 已是 argon2id（形如 "argon2id$v=19$..."）就无需处理。
	if strings.HasPrefix(pwd, "argon2id$") || strings.TrimSpace(pwd) == "" {
		return nil
	}
	// 保留旧哈希（否则用户被锁在门外），但记录待升级状态，登录成功后替换。
	_, err = tx.Exec(
		`INSERT INTO settings (key, value) VALUES ('auth_password_legacy', '1')
		 ON CONFLICT(key) DO UPDATE SET value='1'`)
	return err
}
