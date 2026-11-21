package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
	"pan-transfer/internal/transfer"
)

// 全局搜索。
//
// 为什么需要：此前只有任务页有客户端关键词过滤，日志页没有搜索，
// 且日志固定只显示最近 10 条。「第 7 集转存成功了吗」这种最高频的问题，
// 用户要一个任务一个任务地切换 + 翻页才能回答。
//
// 设计要点：
//   - 一次请求搜多个域（任务 / 账号 / 规则 / 分类 / 转存文件 / 运行记录），
//     返回分组结果。前端按域分组渲染，而不是做成"只能搜一种东西"的搜索框。
//   - 每个域有独立上限，避免某个域的结果淹没其它域：
//     转存文件可能有几万条，如果和任务混在一起返回，任务永远看不到。
//   - 结果只返回定位所需的最小信息（id + 名称 + 可跳转的 hint），
//     详情交给对应的详情接口按需加载。
//   - 转存文件搜索是**按需的**（scope=files 时才搜）：
//     它需要扫描 saved_names 表，几万行时比其它域慢一个量级。

// maxPerScope 是每个搜索域返回的条数上限。
// 用户扫一眼能看懂的列表大概是 5–10 条/域，再多就变成"翻页找"了。
const maxPerScope = 8

type searchRequest struct {
	// Q 是搜索关键词（去空格后作为子串匹配，不区分大小写）。
	Q string `json:"q"`
	// Scope 限定搜索范围：all（默认）| tasks | accounts | rules | categories | files | runs。
	Scope string `json:"scope,omitempty"`
}

type searchHit struct {
	// Kind 是结果类型：task | account | rule | category | file | run。
	Kind string `json:"kind"`
	// ID 是该类型的标识（任务 id / 账号 id / 规则 key / 分类 id / 文件 fid / 运行 id）。
	ID string `json:"id"`
	// Title 是主展示文本。
	Title string `json:"title"`
	// Subtitle 是次要说明（账号类型、保存路径、转存时间…）。
	Subtitle string `json:"subtitle,omitempty"`
	// TaskID 用于「这个文件属于哪个任务」这类跨域跳转。
	TaskID int64 `json:"task_id,omitempty"`
	// Route 是点击后应跳转的前端路径（相对 hash 路由）。
	Route string `json:"route,omitempty"`
	// Badge 是可选的状态标记（如 "success" / "failed"）。
	Badge string `json:"badge,omitempty"`
}

type searchResponse struct {
	Query string `json:"q"`
	// Groups 是分组结果，顺序固定：tasks → accounts → rules → categories → runs → files。
	Groups []searchGroup `json:"groups"`
	// Total 是所有分组的命中总数（用于"共 N 条"）。
	Total int `json:"total"`
	// Truncated 表示是否有分组被截断（超过 maxPerScope）。
	Truncated bool `json:"truncated"`
}

type searchGroup struct {
	Kind  string      `json:"kind"`
	Label string      `json:"label"`
	Hits  []searchHit `json:"hits"`
	// More 是该分组被截断的提示文案（"还有更多，请用更具体的关键词"）。
	More string `json:"more,omitempty"`
}

// groupLabels 是分组的中文标签，顺序即展示顺序。
var groupLabels = []struct {
	kind  string
	label string
}{
	{"task", "转存任务"},
	{"account", "网盘账号"},
	{"rule", "命名规则"},
	{"category", "任务分类"},
	{"run", "运行记录"},
	{"file", "已转存文件"},
}

// knownScopes 是合法的 scope 取值集合（用于校验，避免拼错后静默返回空）。
var knownScopes = func() map[string]bool {
	m := map[string]bool{"all": true}
	for _, g := range groupLabels {
		m[g.kind] = true
	}
	return m
}()

// handleSearch 执行全局搜索。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		return domain.Errorf(domain.CodeValidation, "请输入搜索关键词")
	}
	// 关键词长度上限：过长的输入没有搜索价值，还会让 SQL LIKE 变慢。
	if len(q) > 100 {
		return domain.Errorf(domain.CodeValidation, "关键词过长（最多 100 字符）")
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = "all"
	}
	// scope 必须是已知的域，否则拼错（如 scope=tasks 而实际是 task）会让
	// 所有分组都被跳过，用户看到"搜不到"但不知道为什么。
	if scope != "all" && !knownScopes[scope] {
		return domain.Errorf(domain.CodeValidation,
			"未知的搜索范围：%s（可选 all / task / account / rule / category / run / file）", scope)
	}
	// 任务 ID（可选）：限定只在某个任务里搜文件与运行记录。
	taskID, _ := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)

	ctx := r.Context()
	st := s.currentStore()
	svc := s.transfer()
	if st == nil || svc == nil {
		return errInternalf("服务未就绪")
	}

	resp := searchResponse{Query: q}
	for _, g := range groupLabels {
		if scope != "all" && scope != g.kind {
			continue
		}
		group, truncated, err := s.searchOne(ctx, st, svc, g.kind, q, scope, taskID)
		if err != nil {
			// 单个域失败不影响其它域：搜索是"尽力而为"的聚合查询，
			// 一个域挂了就让整个搜索 500 是不合理的。
			s.deps.Log.Warn("搜索分组失败", "kind", g.kind, "err", err)
			continue
		}
		if len(group.Hits) == 0 {
			continue
		}
		group.Kind = g.kind
		group.Label = g.label
		if truncated {
			group.More = "结果较多，请补充更具体的关键词"
			resp.Truncated = true
		}
		resp.Groups = append(resp.Groups, group)
		resp.Total += len(group.Hits)
	}
	respond(w, resp)
	return nil
}

// searchOne 搜索一个域。返回该组的命中、是否被截断、错误。
func (s *Server) searchOne(
	ctx context.Context, st *store.Store, svc *transfer.Service,
	kind, q, scope string, taskID int64,
) (searchGroup, bool, error) {
	out := searchGroup{}
	needle := strings.ToLower(q)

	switch kind {
	case "task":
		tasks, err := svc.ListTasks(ctx)
		if err != nil {
			return out, false, err
		}
		for _, t := range tasks {
			hay := strings.ToLower(strings.Join([]string{
				t.TaskName, t.SavePath, strings.Join(transfer.LinksOf(t), " "),
			}, " "))
			if !strings.Contains(hay, needle) {
				continue
			}
			out.Hits = append(out.Hits, searchHit{
				Kind:     "task",
				ID:       strconv.FormatInt(t.ID, 10),
				Title:    t.TaskName,
				Subtitle: t.SavePath,
				Route:    "/tasks",
				Badge:    t.LastRunStatus,
			})
			if len(out.Hits) >= maxPerScope {
				// 还有更多：告知调用方截断，但**必须带着已收集的结果一起返回**。
				// 早先这里是 `return out, true, nil`，而调用方用
				// `if len(group.Hits) == 0 { continue }` 过滤空组，
				// 结果"截断 + 有结果"变成了"空组被丢弃"，搜索结果整体消失。
				return out, true, nil
			}
		}
		return out, false, nil

	case "account":
		accounts, err := st.ListAccounts(ctx)
		if err != nil {
			return out, false, err
		}
		for _, a := range accounts {
			if !strings.Contains(strings.ToLower(a.Name+" "+a.DriverType), needle) {
				continue
			}
			out.Hits = append(out.Hits, searchHit{
				Kind:     "account",
				ID:       strconv.FormatInt(a.ID, 10),
				Title:    a.Name,
				Subtitle: a.DriverType,
				Route:    "/accounts",
			})
			if len(out.Hits) >= maxPerScope {
				return out, true, nil
			}
		}

	case "rule":
		rules, err := st.ListRules(ctx)
		if err != nil {
			return out, false, err
		}
		for _, r := range rules {
			hay := strings.ToLower(r.Key + " " + r.Label + " " + r.Pattern + " " + r.Replace)
			if !strings.Contains(hay, needle) {
				continue
			}
			sub := r.Pattern
			if r.Replace != "" {
				sub = r.Pattern + " → " + r.Replace
			}
			out.Hits = append(out.Hits, searchHit{
				Kind:     "rule",
				ID:       r.Key,
				Title:    r.Key,
				Subtitle: sub,
				Route:    "/rules",
			})
			if len(out.Hits) >= maxPerScope {
				return out, true, nil
			}
		}

	case "category":
		cats, err := st.ListCategories(ctx)
		if err != nil {
			return out, false, err
		}
		for _, c := range cats {
			hay := strings.ToLower(c.Name + " " + c.CallbackURL)
			if !strings.Contains(hay, needle) {
				continue
			}
			sub := c.CallbackURL
			if sub == "" {
				sub = "仅分组"
			}
			out.Hits = append(out.Hits, searchHit{
				Kind:     "category",
				ID:       strconv.FormatInt(c.ID, 10),
				Title:    c.Name,
				Subtitle: sub,
				Route:    "/settings",
			})
			if len(out.Hits) >= maxPerScope {
				return out, true, nil
			}
		}

	case "run":
		// 限定任务时只搜该任务的运行记录（用更快的按任务索引查询）；
		// 否则搜全库最近的失败/成功记录（按时间倒序扫，遇到 limit 停）。
		if taskID > 0 {
			runs, err := svc.ListRuns(ctx, taskID, 200)
			if err != nil {
				return out, false, err
			}
			for _, run := range runs {
				if !strings.Contains(strings.ToLower(run.Message+" "+run.Status), needle) {
					continue
				}
				out.Hits = append(out.Hits, searchHit{
					Kind:     "run",
					ID:       strconv.FormatInt(run.ID, 10),
					Title:    run.Message,
					Subtitle: run.StartedAt.Format("2006-01-02 15:04"),
					TaskID:   taskID,
					Route:    "/logs",
					Badge:    run.Status,
				})
				if len(out.Hits) >= maxPerScope {
					return out, true, nil
				}
			}
			return out, false, nil
		}
		// 全库搜索：没有跨任务的"最近运行"索引，退化为遍历任务。
		// 这对任务数量在几十的量级是够用的；任务上千时应该加搜索表。
		tasks, _ := svc.ListTasks(ctx)
		for _, t := range tasks {
			runs, err := svc.ListRuns(ctx, t.ID, 50)
			if err != nil {
				continue
			}
			for _, run := range runs {
				if !strings.Contains(strings.ToLower(run.Message+" "+run.Status), needle) {
					continue
				}
				out.Hits = append(out.Hits, searchHit{
					Kind:     "run",
					ID:       strconv.FormatInt(run.ID, 10),
					Title:    t.TaskName + "：" + run.Message,
					Subtitle: run.StartedAt.Format("2006-01-02 15:04"),
					TaskID:   t.ID,
					Route:    "/logs",
					Badge:    run.Status,
				})
				if len(out.Hits) >= maxPerScope {
					return out, true, nil
				}
			}
		}

	case "file":
		// 转存文件是量级最大的域（几万条很常见），因此只在显式 scope 时搜。
		if scope != "all" && scope != "file" {
			return out, false, nil
		}
		// 按任务搜索：saved_names 有 (task_id, norm_name) 索引，
		// 单任务内搜索很快。全库搜索需要扫描整表，因此要求指定任务或限制条数。
		tasks, _ := svc.ListTasks(ctx)
		for _, t := range tasks {
			if taskID > 0 && t.ID != taskID {
				continue
			}
			names, err := st.SavedNormNames(ctx, t.ID)
			if err != nil {
				continue
			}
			for _, n := range names {
				if !strings.Contains(strings.ToLower(n), needle) {
					continue
				}
				out.Hits = append(out.Hits, searchHit{
					Kind:     "file",
					ID:       n,
					Title:    n,
					Subtitle: t.TaskName,
					TaskID:   t.ID,
					Route:    "/tasks",
				})
				if len(out.Hits) >= maxPerScope {
					return out, true, nil
				}
			}
		}
	}

	return out, false, nil
}
