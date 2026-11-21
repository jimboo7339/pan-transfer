package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
)

// 备份与还原接口。
//
// 为什么必须是产品能力：data/pantransfer.db 是用户唯一的有状态资产（凭据 +
// 任务 + 历史），而容器镜像里没有 sqlite3，README 让"手动 .backup"在 NAS
// 场景下根本无法执行。同时直接拷贝正在写入的 SQLite 会漏掉 WAL 里的事务。
//
// 实现走 store.BackupTo（VACUUM INTO，一致性快照）+ 备份目录轮转。

// registerBackupRoutes 注册备份相关端点。
func (s *Server) registerBackupRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/backup", s.wrap(s.handleBackupList))
	m.HandleFunc("POST /api/backup", s.wrap(s.handleBackupCreate))
	m.HandleFunc("GET /api/backup/download", s.wrap(s.handleBackupDownload))
	m.HandleFunc("POST /api/restore", s.wrap(s.handleRestore))
}

// handleBackupList 列出已有备份。
func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) error {
	dir := store.BackupDir(s.deps.DataDir)
	list, err := store.ListBackups(dir)
	if err != nil {
		return err
	}
	respond(w, map[string]any{
		"dir":     dir,
		"keep":    store.DefaultBackupKeep,
		"backups": list,
	})
	return nil
}

// handleBackupCreate 立即生成一份快照，并轮转旧备份。
func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request) error {
	if s.currentStore() == nil {
		return domain.Errorf(domain.CodeInternal, "存储未初始化")
	}
	dir := store.BackupDir(s.deps.DataDir)
	name := store.BackupFileName(time.Now())
	path := filepath.Join(dir, name)

	if _, err := s.currentStore().BackupTo(r.Context(), path); err != nil {
		return domain.Errorf(domain.CodeInternal, "备份失败: %v", err)
	}
	removed, err := store.PruneBackups(dir, store.DefaultBackupKeep)
	if err != nil {
		// 轮转失败不影响本次备份成功：备份已经在磁盘上了。
		s.deps.Log.Warn("清理旧备份失败", "err", err)
	}
	respond(w, map[string]any{
		"name":    name,
		"pruned":  removed,
		"keep":    store.DefaultBackupKeep,
		"message": fmt.Sprintf("已生成备份 %s（保留最近 %d 份）", name, store.DefaultBackupKeep),
	})
	return nil
}

// handleBackupDownload 把指定（或最新）备份作为附件下载。
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) error {
	dir := store.BackupDir(s.deps.DataDir)
	name := strings.TrimSpace(r.URL.Query().Get("name"))

	if name == "" {
		list, err := store.ListBackups(dir)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return domain.Errorf(domain.CodeNotFound, "还没有任何备份，请先创建")
		}
		name = list[0].Name
	}

	// 防目录穿越：只允许备份目录下的 basename。
	if name != filepath.Base(name) || !strings.HasPrefix(name, store.BackupPrefix) {
		return domain.Errorf(domain.CodeValidation, "非法的备份文件名")
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		return domain.Errorf(domain.CodeNotFound, "备份不存在: %s", name)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	http.ServeFile(w, r, path)
	return nil
}

// handleRestore 从一份备份恢复数据库。
//
// 还原必须停写：SQLite 无法在连接打开时替换底层文件。具体动作委托给
// Deps.Restore（在 main 里装配，因为要同时管理 Store / Manager / 调度器
// 三者的生命周期，而 httpapi 不应知道这些细节）。
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || name != filepath.Base(name) || !strings.HasPrefix(name, store.BackupPrefix) {
		return domain.Errorf(domain.CodeValidation, "非法的备份文件名")
	}
	if s.deps.Restore == nil {
		return domain.Errorf(domain.CodeInternal, "当前构建未提供还原能力")
	}

	backupPath := filepath.Join(store.BackupDir(s.deps.DataDir), name)
	if err := s.deps.Restore(backupPath); err != nil {
		return err
	}

	s.deps.Log.Warn("数据库已从备份还原", "backup", name)
	respond(w, map[string]any{
		"restored": name,
		"message":  "已还原，正在重新加载账号与任务，请刷新页面",
	})
	return nil
}
