package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
	"pan-transfer/internal/transfer"
)

// nowRFC3339 / nowDate 是导出时间戳的小工具。
func nowRFC3339() string { return time.Now().Format(time.RFC3339) }
func nowDate() string    { return time.Now().Format("20060102") }

// writeJSONIndent 输出带缩进的 JSON（导出文件要给人看，不能压成一行）。
func writeJSONIndent(w http.ResponseWriter, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// 配置导入 / 导出。
//
// 为什么需要：用户换 NAS、重装系统、或在几台机器间同步配置时，
// 现在只能对着空表单把任务一条条敲回来。而任务的配置项有 20 多个
// （多源链接、正则、子目录策略、cron、分类回调…），手工重建的出错率高得离谱。
//
// 设计约束：
//  1. 导出的是「可移植的配置」，不是数据库文件。凭据（cookie / token）
//     只导出「是否存在」的布尔标记，绝不含明文——否则导出的文件就成了
//     账号泄露的载体，用户在论坛/群里传一份配置就等于把账号送出去。
//  2. 导入必须整体校验后一次性写入：写一半失败会留下比不导入更糟的状态。
//  3. 跨实例的 ID 不稳定（account_id / category_id 在另一台机器上对不上），
//     因此导入时按「名称」重映射，映射不上的字段明确标记为待用户处理，
//     而不是静默塞一个错误的 ID。

// configBundle 是导入导出的顶层结构。
type configBundle struct {
	// SchemaVersion 是配置格式版本，便于未来做迁移。
	SchemaVersion int `json:"schema_version"`
	// ExportedAt 是导出时间（RFC3339）。
	ExportedAt string `json:"exported_at"`
	// AppVersion 是导出时的程序版本，便于判断是否需要迁移。
	AppVersion string `json:"app_version"`
	// Summary 是人类可读的摘要，导入前预览用。
	Summary bundleSummary `json:"summary"`
	// Tasks 是任务配置数组（不含运行时状态）。
	Tasks []bundleTask `json:"tasks"`
	// Rules 是命名规则（含内置规则的覆盖）。
	Rules []bundleRule `json:"rules"`
	// Categories 是任务分类与回调配置。
	Categories []bundleCategory `json:"categories"`
}

type bundleSummary struct {
	TaskCount     int `json:"task_count"`
	RuleCount     int `json:"rule_count"`
	CategoryCount int `json:"category_count"`
	AccountCount  int `json:"account_count"`
}

// bundleTask 是一个可移植的任务定义。
type bundleTask struct {
	Name string `json:"name"`
	// AccountName 用名称而非 ID 引用账号：跨实例 ID 不稳定。
	AccountName string `json:"account_name"`
	// AccountID 是导出时所在实例的 ID，仅作参考（导入时忽略）。
	AccountID int64 `json:"account_id,omitempty"`
	// HasCredentials 仅用于提示「该账号在新实例上还没配凭据」。
	HasCredentials bool `json:"has_credentials,omitempty"`

	Links      []string `json:"links"`
	SavePath   string   `json:"save_path"`
	Pattern    string   `json:"pattern"`
	Replace    string   `json:"replace"`
	IgnoreExt  bool     `json:"ignore_extension"`
	RunMode    string   `json:"run_mode"`
	RunCron    string   `json:"run_cron"`
	RunWeek    string   `json:"run_week"`
	RunTime    string   `json:"run_time"`
	EndDate    string   `json:"end_date"`
	UpdateSub  string   `json:"update_subdir"`
	ResaveMode string   `json:"update_subdir_resave_mode,omitempty"`
	StartFID   string   `json:"start_fid"`
	SortIndex  int      `json:"sort_index"`
	SkipHist   string   `json:"skip_history"`
	Status     string   `json:"status"`

	// CategoryName 用名称引用分类（同上）。
	CategoryName string `json:"category_name,omitempty"`
	CategoryID   int64  `json:"category_id,omitempty"`
}

type bundleRule struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Pattern string `json:"pattern"`
	Replace string `json:"replace"`
	Enabled bool   `json:"enabled"`
}

type bundleCategory struct {
	Name               string `json:"name"`
	CallbackEnabled    bool   `json:"callback_enabled"`
	CallbackMode       string `json:"callback_mode"`
	CallbackMethod     string `json:"callback_method"`
	CallbackURL        string `json:"callback_url"`
	CallbackHeaders    string `json:"callback_headers,omitempty"`
	CallbackBody       string `json:"callback_body,omitempty"`
	CallbackTimeoutSec int    `json:"callback_timeout_sec"`
	SkipZero           bool   `json:"skip_zero"`
}

// currentSchemaVersion 是当前导出格式版本。
const currentSchemaVersion = 1

// handleConfigExport 导出配置（不含凭据）。
func (s *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	st := s.currentStore()
	svc := s.transfer()

	bundle := configBundle{
		SchemaVersion: currentSchemaVersion,
		ExportedAt:    nowRFC3339(),
		AppVersion:    s.deps.Version,
	}

	// 账号：只导出名称 + 是否有凭据，用于导入时做名称映射与提示。
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	acctHasCred := map[int64]bool{}
	for _, a := range accounts {
		cfg := store.ParseAccountConfig(a.Config)
		acctHasCred[a.ID] = strings.TrimSpace(cfg["cookie"]) != "" ||
			strings.TrimSpace(cfg["access_token"]) != ""
	}
	bundle.Summary.AccountCount = len(accounts)

	// 分类：先导出，任务引用它时才能按名称映射。
	cats, err := st.ListCategories(ctx)
	if err != nil {
		return err
	}
	catName := map[int64]string{}
	for _, c := range cats {
		catName[c.ID] = c.Name
		bundle.Categories = append(bundle.Categories, bundleCategory{
			Name:               c.Name,
			CallbackEnabled:    c.CallbackEnabled,
			CallbackMode:       c.CallbackMode,
			CallbackMethod:     c.CallbackMethod,
			CallbackURL:        c.CallbackURL,
			CallbackHeaders:    c.CallbackHeaders,
			CallbackBody:       c.CallbackBody,
			CallbackTimeoutSec: c.CallbackTimeoutSec,
			SkipZero:           c.SkipZero,
		})
	}
	bundle.Summary.CategoryCount = len(cats)

	// 任务：排除运行时状态（last_run_* 等），那是本机历史而非配置。
	if svc != nil {
		tasks, err := svc.ListTasks(ctx)
		if err != nil {
			return err
		}
		acctName := map[int64]string{}
		for _, a := range accounts {
			acctName[a.ID] = a.Name
		}
		for _, t := range tasks {
			bundle.Tasks = append(bundle.Tasks, bundleTask{
				Name:           t.TaskName,
				AccountName:    acctName[t.AccountID],
				AccountID:      t.AccountID,
				HasCredentials: acctHasCred[t.AccountID],
				Links:          transfer.LinksOf(t),
				SavePath:       t.SavePath,
				Pattern:        t.Pattern,
				Replace:        t.Replace,
				IgnoreExt:      t.IgnoreExtension,
				RunMode:        transfer.EffectiveRunMode(t),
				RunCron:        t.RunCron,
				RunWeek:        t.RunWeek,
				RunTime:        t.RunTime,
				EndDate:        t.EndDate,
				UpdateSub:      t.UpdateSubdir,
				ResaveMode:     t.UpdateSubdirResaveMode,
				StartFID:       t.StartFID,
				SortIndex:      t.SortIndex,
				SkipHist:       t.SkipHistory,
				Status:         t.Status,
				CategoryName:   catName[t.CategoryID],
				CategoryID:     t.CategoryID,
			})
		}
	}
	bundle.Summary.TaskCount = len(bundle.Tasks)

	// 规则：内置规则的「覆盖」也要导出，否则用户在另一台机器上
	// 会得到不同的命名结果（内置规则被改了但没带过去）。
	rules, err := st.ListRules(ctx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		bundle.Rules = append(bundle.Rules, bundleRule{
			Key:     r.Key,
			Label:   r.Label,
			Pattern: r.Pattern,
			Replace: r.Replace,
			Enabled: r.Enabled,
		})
	}
	bundle.Summary.RuleCount = len(rules)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="pantransfer-config-%s.json"`, nowDate()))
	return writeJSONIndent(w, bundle)
}

// importPreview 是导入前的预检结果：告诉用户会发生什么、哪里有问题，
// 而不是直接写库。导入是破坏性操作（会新建任务、覆盖同名规则），
// 用户有权在点「确认」之前看清楚。
type importPreview struct {
	Valid         bool          `json:"valid"`
	Errors        []string      `json:"errors,omitempty"`
	Warnings      []string      `json:"warnings,omitempty"`
	Summary       bundleSummary `json:"summary"`
	TaskNames     []string      `json:"task_names,omitempty"`
	RuleKeys      []string      `json:"rule_keys,omitempty"`
	CategoryNames []string      `json:"category_names,omitempty"`
	// Skipped 说明哪些任务因账号/分类缺失而无法导入。
	Skipped []string `json:"skipped,omitempty"`
}

// handleConfigImportPreview 只校验并返回预览，不写库。
func (s *Server) handleConfigImportPreview(w http.ResponseWriter, r *http.Request) error {
	var bundle configBundle
	if err := decode(r, &bundle); err != nil {
		return err
	}
	preview := s.previewImport(r.Context(), &bundle)
	respond(w, preview)
	return nil
}

// handleConfigImport 执行导入。
//
// 导入策略（刻意保守）：
//   - 分类：按名称匹配，不存在则新建
//   - 账号：按名称匹配；匹配不上则跳过该任务（而不是硬塞一个错误 ID）
//   - 规则：按 key 覆盖（内置规则的覆盖也会被带过来）
//   - 任务：按名称匹配，已存在则更新、不存在则新建
//
// 为什么不用事务包一个大事务：导入 N 个任务 = N 次 INSERT，
// 一个事务持有写锁的时间会随 N 线性增长，SQLite 单连接下会阻塞所有 API。
// 改为逐项写入 + 返回逐条结果，用户能看清哪条失败。
func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) error {
	var bundle configBundle
	if err := decode(r, &bundle); err != nil {
		return err
	}
	if bundle.SchemaVersion > currentSchemaVersion {
		return domain.Errorf(domain.CodeValidation,
			"配置格式版本 %d 高于当前支持版本 %d，请升级后再导入",
			bundle.SchemaVersion, currentSchemaVersion)
	}
	preview := s.previewImport(r.Context(), &bundle)
	if !preview.Valid {
		return domain.Errorf(domain.CodeValidation, "配置校验未通过：%s",
			strings.Join(preview.Errors, "；"))
	}

	res := s.applyImport(r.Context(), &bundle)
	respond(w, res)
	return nil
}

// applyResult 是导入的逐条结果。
type applyResult struct {
	CategoriesCreated int      `json:"categories_created"`
	RulesWritten      int      `json:"rules_written"`
	TasksCreated      int      `json:"tasks_created"`
	TasksUpdated      int      `json:"tasks_updated"`
	TasksSkipped      int      `json:"tasks_skipped"`
	Skipped           []string `json:"skipped,omitempty"`
	Failed            []string `json:"failed,omitempty"`
}

// previewImport 校验配置并生成预览（不写库）。
func (s *Server) previewImport(ctx context.Context, b *configBundle) importPreview {
	p := importPreview{Summary: b.Summary}

	if b.SchemaVersion <= 0 {
		// 兼容没有版本号的旧导出文件
		b.SchemaVersion = 1
	}
	if b.SchemaVersion > currentSchemaVersion {
		p.Errors = append(p.Errors,
			fmt.Sprintf("配置格式版本 %d 高于当前支持版本 %d", b.SchemaVersion, currentSchemaVersion))
		return p
	}
	if len(b.Tasks) == 0 && len(b.Rules) == 0 && len(b.Categories) == 0 {
		p.Errors = append(p.Errors, "配置里没有任何任务、规则或分类")
		return p
	}

	st := s.currentStore()

	// 现有账号（按名称索引），用于判断任务能否落位。
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		p.Errors = append(p.Errors, "读取账号列表失败："+err.Error())
		return p
	}
	acctByName := map[string]*store.Account{}
	for _, a := range accounts {
		acctByName[a.Name] = a
	}
	acctHasCred := map[string]bool{}
	for _, a := range accounts {
		cfg := store.ParseAccountConfig(a.Config)
		acctHasCred[a.Name] = strings.TrimSpace(cfg["cookie"]) != "" ||
			strings.TrimSpace(cfg["access_token"]) != ""
	}

	// 现有分类（按名称索引）。
	cats, _ := st.ListCategories(ctx)
	catByName := map[string]bool{}
	for _, c := range cats {
		catByName[c.Name] = true
	}

	for _, t := range b.Tasks {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			p.Errors = append(p.Errors, "存在没有名称的任务")
			continue
		}
		if len(t.Links) == 0 {
			p.Errors = append(p.Errors, fmt.Sprintf("任务「%s」没有分享链接", name))
			continue
		}
		if strings.TrimSpace(t.SavePath) == "" {
			p.Errors = append(p.Errors, fmt.Sprintf("任务「%s」没有保存路径", name))
			continue
		}
		// 账号映射：名称优先，退化到导出时记录的 ID（同实例重装时可用）。
		resolved := ""
		if _, ok := acctByName[t.AccountName]; ok {
			resolved = t.AccountName
		}
		if resolved == "" {
			p.Skipped = append(p.Skipped, fmt.Sprintf(
				"任务「%s」：账号「%s」在新实例上不存在（请先添加同名账号，或在账号页改名）",
				name, t.AccountName))
			continue
		}
		if !acctHasCred[resolved] && t.HasCredentials {
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"任务「%s」对应的账号「%s」在新实例上还没有凭据，需要重新扫码或填写 Cookie",
				name, resolved))
		}
		if t.CategoryName != "" && !catByName[t.CategoryName] {
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"任务「%s」引用的分类「%s」不存在，导入时会自动创建", name, t.CategoryName))
		}
		p.TaskNames = append(p.TaskNames, name)
	}

	for _, r := range b.Rules {
		if !strings.HasPrefix(strings.TrimSpace(r.Key), "$") {
			p.Errors = append(p.Errors,
				fmt.Sprintf("规则标识「%s」必须以 $ 开头", r.Key))
			continue
		}
		p.RuleKeys = append(p.RuleKeys, r.Key)
	}
	for _, c := range b.Categories {
		if strings.TrimSpace(c.Name) == "" {
			p.Errors = append(p.Errors, "存在没有名称的分类")
			continue
		}
		if c.CallbackEnabled && strings.TrimSpace(c.CallbackURL) == "" {
			p.Errors = append(p.Errors,
				fmt.Sprintf("分类「%s」启用了回调但没有填写 URL", c.Name))
		}
		p.CategoryNames = append(p.CategoryNames, c.Name)
	}

	p.Valid = len(p.Errors) == 0
	return p
}

// applyImport 执行导入，返回逐条结果。
func (s *Server) applyImport(ctx context.Context, b *configBundle) applyResult {
	var res applyResult
	st := s.currentStore()

	// 1. 分类：按名称复用，缺失则新建。
	catID := map[string]int64{}
	if existing, err := st.ListCategories(ctx); err == nil {
		for _, c := range existing {
			catID[c.Name] = c.ID
		}
	}
	for i, c := range b.Categories {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		if _, ok := catID[name]; ok {
			continue
		}
		timeout := c.CallbackTimeoutSec
		if timeout <= 0 {
			timeout = 15
		}
		id, err := st.SaveCategory(ctx, &store.TaskCategory{
			Name:               name,
			CallbackEnabled:    c.CallbackEnabled,
			CallbackMode:       firstNonEmpty(c.CallbackMode, "round"),
			CallbackMethod:     firstNonEmpty(strings.ToUpper(c.CallbackMethod), "POST"),
			CallbackURL:        strings.TrimSpace(c.CallbackURL),
			CallbackHeaders:    c.CallbackHeaders,
			CallbackBody:       c.CallbackBody,
			CallbackTimeoutSec: timeout,
			SkipZero:           c.SkipZero,
			SortOrder:          i,
		})
		if err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("分类「%s」创建失败：%v", name, err))
			continue
		}
		catID[name] = id
		res.CategoriesCreated++
	}

	// 2. 规则：按 key 覆盖（幂等）。
	for _, r := range b.Rules {
		key := strings.TrimSpace(r.Key)
		if key == "" {
			continue
		}
		if err := st.UpsertRule(ctx, &domain.MagicRegexRule{
			Key:     key,
			Label:   r.Label,
			Pattern: r.Pattern,
			Replace: r.Replace,
			Enabled: r.Enabled,
		}); err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("规则「%s」写入失败：%v", key, err))
			continue
		}
		res.RulesWritten++
	}

	// 3. 任务：按名称匹配，存在则更新、缺失账号则跳过。
	svc := s.transfer()
	if svc == nil {
		res.Failed = append(res.Failed, "转存服务未就绪，无法导入任务")
		return res
	}
	accounts, _ := st.ListAccounts(ctx)
	acctByName := map[string]int64{}
	for _, a := range accounts {
		acctByName[a.Name] = a.ID
	}

	existing, _ := svc.ListTasks(ctx)
	taskIDByName := map[string]int64{}
	for _, t := range existing {
		taskIDByName[t.TaskName] = t.ID
	}

	for _, bt := range b.Tasks {
		name := strings.TrimSpace(bt.Name)
		if name == "" {
			continue
		}
		acctID, ok := acctByName[strings.TrimSpace(bt.AccountName)]
		if !ok {
			res.TasksSkipped++
			res.Skipped = append(res.Skipped, name)
			continue
		}

		status := strings.TrimSpace(bt.Status)
		if status != "paused" {
			status = "running"
		}
		task := &domain.DramaTask{
			ID:                     taskIDByName[name],
			TaskName:               name,
			AccountID:              acctID,
			SavePath:               bt.SavePath,
			Pattern:                bt.Pattern,
			Replace:                bt.Replace,
			IgnoreExtension:        bt.IgnoreExt,
			RunMode:                firstNonEmpty(bt.RunMode, "manual"),
			RunCron:                strings.TrimSpace(bt.RunCron),
			RunWeek:                bt.RunWeek,
			RunTime:                bt.RunTime,
			EndDate:                bt.EndDate,
			UpdateSubdir:           bt.UpdateSub,
			UpdateSubdirResaveMode: bt.ResaveMode,
			StartFID:               bt.StartFID,
			SortIndex:              bt.SortIndex,
			SkipHistory:            firstNonEmpty(bt.SkipHist, "global"),
			Status:                 status,
		}
		if catID, ok := catID[strings.TrimSpace(bt.CategoryName)]; ok {
			task.CategoryID = catID
		}
		transfer.SetLinks(task, bt.Links)

		id, err := svc.SaveTask(ctx, task)
		if err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("任务「%s」导入失败：%v", name, err))
			continue
		}
		if task.ID > 0 {
			res.TasksUpdated++
		} else {
			res.TasksCreated++
		}
		_ = id
	}

	return res
}

// firstNonEmpty 返回第一个非空串（去空格后）。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
