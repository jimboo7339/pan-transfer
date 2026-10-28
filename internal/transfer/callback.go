// 分类回调：任务/轮次完成后按分类配置调用下游接口。
// 模板变量用 {key} 占位；GET 查询值自动 URL 编码；2xx 视为成功；结果落 callback_logs。
package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
)

// defaultCallbackBody 单任务 POST 未填模板时的默认 Body。
const defaultCallbackBody = `{"event":"transfer.completed","category":"{category}","status":"{status}","count":{transfer_count},"files":{transferred_files_json},"path":"{save_path}"}`

// defaultRoundCallbackBody 轮次 POST 未填模板时的默认 Body。
const defaultRoundCallbackBody = `{"event":"transfer.round_completed","category":"{category}","status":"{status}","round_id":{round_id},"task_count":{task_count},"total_transferred":{total_transferred},"tasks":{task_results_json}}`

// roundTaskResult 轮次里单个任务的结果（也是 task_results_json 的元素）。
type roundTaskResult struct {
	TaskID   int64    `json:"task_id"`
	TaskName string   `json:"task_name"`
	Status   string   `json:"status"`
	Count    int      `json:"transfer_count"`
	Files    []string `json:"files"`
	SavePath string   `json:"save_path"`
	Message  string   `json:"message,omitempty"`
}

// callbackRound 一个分类的进行中轮次。
type callbackRound struct {
	id      int64
	started map[int64]struct{}
	results map[int64]*roundTaskResult
}

// renderTemplate 用 vars 替换 tpl 里的 {key}（键仅限字母/数字/下划线）。
// 不构成合法占位符的花括号按字面保留，因此 JSON 模板里的结构花括号是安全的。
func renderTemplate(tpl string, vars map[string]string) string {
	if tpl == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(tpl); {
		if tpl[i] != '{' {
			b.WriteByte(tpl[i])
			i++
			continue
		}
		j := strings.IndexByte(tpl[i:], '}')
		if j < 0 {
			b.WriteString(tpl[i:])
			break
		}
		j += i
		key := tpl[i+1 : j]
		if isPlaceholderKey(key) {
			if v, ok := vars[key]; ok {
				b.WriteString(v)
			} else {
				b.WriteString(tpl[i : j+1])
			}
			i = j + 1
			continue
		}
		// 非占位符（如 JSON 结构花括号）：按字面输出，继续向后扫描
		b.WriteByte('{')
		i++
	}
	return b.String()
}

// isPlaceholderKey 判断是否为合法占位符键（仅字母/数字/下划线）。
func isPlaceholderKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// buildTaskCallbackVars 单任务回调变量表。
func buildTaskCallbackVars(t *domain.DramaTask, catName string, res *RunResult, names []string, trigger string) map[string]string {
	status, count, message := "failed", 0, "运行失败"
	if res != nil {
		if res.Status != "" {
			status = res.Status
		}
		count = res.TransferCount
		message = res.Message
	}
	if names == nil {
		names = []string{}
	}
	filesJSON, _ := json.Marshal(names)
	return map[string]string{
		"task_id":                fmt.Sprintf("%d", t.ID),
		"task_name":              t.TaskName,
		"category":               catName,
		"status":                 status,
		"transfer_count":         fmt.Sprintf("%d", count),
		"transferred_files":      strings.Join(names, "\n"),
		"transferred_files_json": string(filesJSON),
		"save_path":              t.SavePath,
		"trigger":                trigger,
		"finished_at":            time.Now().Format("2006-01-02 15:04:05"),
		"message":                message,
	}
}

// buildRoundVars 轮次回调变量表。除轮次专属变量外，单任务变量也映射到聚合值
// （transfer_count=总数、files=全部新文件、save_path=逗号连接），同一份 Body 模板两种模式通用。
func buildRoundVars(catName string, roundID int64, results []*roundTaskResult, trigger string) map[string]string {
	total := 0
	status := "success"
	var allFiles, paths, names []string
	for _, r := range results {
		total += r.Count
		allFiles = append(allFiles, r.Files...)
		if r.SavePath != "" {
			paths = append(paths, r.SavePath)
		}
		names = append(names, r.TaskName)
		if r.Status == "failed" {
			status = "failed"
		} else if r.Status == "partial" && status != "failed" {
			status = "partial"
		}
	}
	if results == nil {
		results = []*roundTaskResult{}
	}
	if allFiles == nil {
		allFiles = []string{}
	}
	resultsJSON, _ := json.Marshal(results)
	filesJSON, _ := json.Marshal(allFiles)
	return map[string]string{
		"category":          catName,
		"round_id":          fmt.Sprintf("%d", roundID),
		"task_count":        fmt.Sprintf("%d", len(results)),
		"total_transferred": fmt.Sprintf("%d", total),
		"status":            status,
		"task_results_json": string(resultsJSON),
		"trigger":           trigger,
		"finished_at":       time.Now().Format("2006-01-02 15:04:05"),
		// 单任务变量的轮次映射
		"task_id":                "",
		"task_name":              strings.Join(names, ","),
		"transfer_count":         fmt.Sprintf("%d", total),
		"transferred_files":      strings.Join(allFiles, "\n"),
		"transferred_files_json": string(filesJSON),
		"save_path":              strings.Join(paths, ","),
		"message":                fmt.Sprintf("共 %d 个任务，转存 %d 个文件", len(results), total),
	}
}

// parseHeadersJSON 解析自定义请求头 JSON；解析失败返回空表。
func parseHeadersJSON(raw string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return out
	}
	for k, v := range m {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}

// encodeQueryTemplate 渲染 GET URL：path 部分不编码，查询键值渲染后按 URL 编码。
func encodeQueryTemplate(raw string, vars map[string]string) string {
	i := strings.IndexByte(raw, '?')
	if i < 0 {
		return renderTemplate(raw, vars)
	}
	base := renderTemplate(raw[:i], vars)
	var parts []string
	for _, kv := range strings.Split(raw[i+1:], "&") {
		if kv == "" {
			continue
		}
		k, v := kv, ""
		if eq := strings.IndexByte(kv, '='); eq >= 0 {
			k, v = kv[:eq], kv[eq+1:]
		}
		parts = append(parts, renderTemplate(k, vars)+"="+url.QueryEscape(renderTemplate(v, vars)))
	}
	if len(parts) == 0 {
		return base
	}
	return base + "?" + strings.Join(parts, "&")
}

// callbackResult 一次回调的执行结果。
type callbackResult struct {
	OK         bool   `json:"ok"`
	StatusCode int    `json:"status_code"`
	Response   string `json:"response"`
	Error      string `json:"error,omitempty"`
}

// runCallback 按分类配置实际发起回调（同步执行，调用方决定是否放 goroutine）。
// defaultBody 为 POST 未填模板时的兜底模板（单任务与轮次各有默认值）。
func (s *Service) runCallback(cat *store.TaskCategory, kind string, vars map[string]string, defaultBody string) callbackResult {
	method := cat.CallbackMethod
	if method != "GET" {
		method = "POST"
	}
	target := encodeQueryTemplate(cat.CallbackURL, vars)
	body := ""
	headers := parseHeadersJSON(cat.CallbackHeaders)
	if method == "POST" {
		tpl := cat.CallbackBody
		if strings.TrimSpace(tpl) == "" {
			tpl = defaultBody
		}
		body = renderTemplate(tpl, vars)
		if _, ok := headers["Content-Type"]; !ok {
			headers["Content-Type"] = "application/json"
		}
	}

	timeout := time.Duration(cat.CallbackTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result := callbackResult{}
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		result.Error = err.Error()
	} else {
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			result.Error = err.Error()
		} else {
			defer resp.Body.Close()
			result.StatusCode = resp.StatusCode
			result.OK = resp.StatusCode >= 200 && resp.StatusCode < 300
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			result.Response = string(snippet)
		}
	}
	if err := s.st.SaveCallbackLog(context.Background(), cat.ID, kind, method+" "+target, result.StatusCode, result.OK, firstNonEmpty(result.Error, result.Response)); err != nil {
		s.log.Warn("记录回调日志失败", "category", cat.ID, "err", err)
	}
	if result.OK {
		s.log.Info("分类回调成功", "category", cat.Name, "kind", kind, "status", result.StatusCode)
	} else {
		s.log.Warn("分类回调失败", "category", cat.Name, "kind", kind, "status", result.StatusCode, "err", result.Error)
	}
	return result
}

// fireCallback 异步发起回调；defaultBody 为 POST 未填模板时的兜底。
func (s *Service) fireCallback(cat *store.TaskCategory, kind string, vars map[string]string, defaultBody string) {
	go s.runCallback(cat, kind, vars, defaultBody)
}

// TestCallback 用给定变量同步发送一次回调（设置页「发送测试」用），返回实际结果。
func (s *Service) TestCallback(ctx context.Context, cat *store.TaskCategory, vars map[string]string) callbackResult {
	def := defaultCallbackBody
	if cat.CallbackMode == "round" {
		def = defaultRoundCallbackBody
	}
	return s.runCallback(cat, "test", vars, def)
}

// SampleCallbackVars 构造「发送测试」用的示例变量。
func SampleCallbackVars(catName string, round bool) map[string]string {
	files := []string{"示例文件.S01E01.mkv", "示例文件.S01E02.mkv"}
	if round {
		return buildRoundVars(catName, 1, []*roundTaskResult{
			{TaskID: 1, TaskName: "示例任务A", Status: "success", Count: 2, Files: files, SavePath: "/影视/示例A"},
			{TaskID: 2, TaskName: "示例任务B", Status: "success", Count: 0, Files: []string{}, SavePath: "/影视/示例B"},
		}, "test")
	}
	return buildTaskCallbackVars(&domain.DramaTask{ID: 1, TaskName: "示例任务", SavePath: "/影视/示例"},
		catName, &RunResult{Status: "success", TransferCount: 2, Message: "转存 2 个文件（1 条链接）"}, files, "test")
}

// joinCategoryRound 任务加入其分类当前轮次（幂等）。在随机延迟/实际运行前调用，
// 保证批量触发时轮次不会先跑完的任务提前关闭。
func (s *Service) joinCategoryRound(t *domain.DramaTask) {
	if t == nil || t.CategoryID == 0 {
		return
	}
	cat, err := s.st.GetCategory(context.Background(), t.CategoryID)
	if err != nil || cat == nil || !cat.CallbackEnabled || cat.CallbackMode != "round" {
		return
	}
	s.roundMu.Lock()
	defer s.roundMu.Unlock()
	r := s.rounds[t.CategoryID]
	if r == nil {
		s.roundSeq++
		r = &callbackRound{id: s.roundSeq, started: map[int64]struct{}{}, results: map[int64]*roundTaskResult{}}
		s.rounds[t.CategoryID] = r
	}
	r.started[t.ID] = struct{}{}
}

// settleCategory 任务结束后的回调结算：per_task 立即回调；round 记入轮次，凑齐即聚合回调。
// 由 RunTask 的 defer 调用，跳过/失败/中止都会走到。
func (s *Service) settleCategory(t *domain.DramaTask, res *RunResult, names []string, trigger string) {
	if t == nil || t.CategoryID == 0 {
		return
	}
	cat, err := s.st.GetCategory(context.Background(), t.CategoryID)
	if err != nil || cat == nil || !cat.CallbackEnabled {
		return
	}
	outcome := &roundTaskResult{TaskID: t.ID, TaskName: t.TaskName, SavePath: t.SavePath, Files: names}
	if res != nil {
		outcome.Status = res.Status
		outcome.Count = res.TransferCount
		outcome.Message = res.Message
	} else {
		outcome.Status = "failed"
		outcome.Message = "运行失败"
	}

	if cat.CallbackMode == "per_task" {
		if outcome.Status == "skipped" || outcome.Status == "" {
			return // 没有实际运行，不打扰下游
		}
		s.fireCallback(cat, "task", buildTaskCallbackVars(t, cat.Name, res, names, trigger), defaultCallbackBody)
		return
	}

	// round 模式
	s.roundMu.Lock()
	r := s.rounds[t.CategoryID]
	if r == nil {
		// 无进行中轮次（如服务重启后）：按单任务轮次处理，避免丢回调
		s.roundSeq++
		r = &callbackRound{id: s.roundSeq, started: map[int64]struct{}{}, results: map[int64]*roundTaskResult{}}
		r.started[t.ID] = struct{}{}
		s.rounds[t.CategoryID] = r
	}
	r.started[t.ID] = struct{}{} // 防御：异常路径未 join 的任务并入当前轮
	r.results[t.ID] = outcome
	fire := len(r.results) >= len(r.started)
	var (
		roundID int64
		results []*roundTaskResult
	)
	if fire {
		roundID = r.id
		for _, rs := range r.results {
			results = append(results, rs)
		}
		delete(s.rounds, t.CategoryID)
	}
	s.roundMu.Unlock()

	if fire {
		sort.Slice(results, func(i, j int) bool { return results[i].TaskID < results[j].TaskID })
		s.fireCallback(cat, "round", buildRoundVars(cat.Name, roundID, results, trigger), defaultRoundCallbackBody)
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
