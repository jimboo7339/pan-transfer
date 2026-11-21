package httpapi

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/transfer"
)

// errInternalf 构造一个内部错误（消息给前端，细节进日志）。
// 仪表盘依赖多个存储查询，任一步失败都要明确告知"仪表盘暂时不可用"，
// 而不是静默返回半截数据让用户以为"真的没有数据"。
func errInternalf(format string, args ...any) error {
	return domain.Errorf(domain.CodeInternal, format, args...)
}

// 仪表盘聚合。
//
// 为什么需要：打开面板的第一诉求不是「有哪些任务」，而是「现在系统健康吗」——
// 账号掉线了吗 / 今天转存了多少 / 有没有失败 / 下一次调度是什么时候。
// 此前首屏直接是任务卡片，用户要扫 N 张卡片的 badge 才能回答这些问题。
//
// 实现方式：一次请求聚合服务端已有数据，避免前端发 6 个请求再自己拼。
// 这同时绕开了「首屏并发 4 个请求全在 SQLite 单连接（SetMaxOpenConns(1)）
// 上排队」的问题——少发请求就是少排队。
//
// 数据全部来自持久层，不用内存计数器：计数器在进程重启后就归零，
// 而用户关心的是"今天到底转了多少"这种跨重启的事实。

// dashboardData 是仪表盘数据。
type dashboardData struct {
	GeneratedAt string `json:"generated_at"`

	Tasks     dashboardTasks     `json:"tasks"`
	Transfers dashboardTransfers `json:"transfers"`
	Accounts  dashboardAccounts  `json:"accounts"`
	Schedule  dashboardSchedule  `json:"schedule"`
	Running   dashboardRunning   `json:"running"`
	Alerts    []dashboardAlert   `json:"alerts"`
	Failures  []dashboardFailure `json:"recent_failures"`
}

type dashboardTasks struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Paused  int `json:"paused"`
}

type dashboardTransfers struct {
	TodayCount int        `json:"today_count"`
	TodayRuns  int        `json:"today_runs"`
	Last7Days  []dayCount `json:"last_7_days"`
	TotalSaved int        `json:"total_saved"`
}

type dayCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type dashboardAccounts struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Healthy int `json:"healthy"`
}

type dashboardSchedule struct {
	Enabled bool   `json:"enabled"`
	Cron    string `json:"cron"`
	NextRun string `json:"next_run,omitempty"`
}

type dashboardRunning struct {
	Count int     `json:"count"`
	IDs   []int64 `json:"ids"`
}

type dashboardAlert struct {
	Level  string `json:"level"` // info | warn | error
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	TaskID int64  `json:"task_id,omitempty"`
}

type dashboardFailure struct {
	TaskID   int64  `json:"task_id"`
	TaskName string `json:"task_name"`
	Message  string `json:"message"`
	FailedAt string `json:"failed_at"`
}

// handleDashboard 返回仪表盘聚合数据。
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	svc := s.transfer()
	st := s.currentStore()
	if svc == nil || st == nil {
		return errInternalf("服务未就绪")
	}

	data := dashboardData{GeneratedAt: time.Now().Format(time.RFC3339)}

	// ---- 任务概况 + 上次失败告警 ----
	tasks, err := svc.ListTasks(ctx)
	if err != nil {
		return err
	}
	taskName := make(map[int64]string, len(tasks))
	for _, t := range tasks {
		data.Tasks.Total++
		if t.Status == "paused" {
			data.Tasks.Paused++
		} else {
			data.Tasks.Running++
		}
		taskName[t.ID] = t.TaskName

		// 上次失败是「静默失败」的唯一可见信号，而静默失败正是本类工具
		// 最危险的故障模式：转存没发生，但定时任务看起来一直在跑
		// （同类项目里有过「连续 20 轮任务没执行、10 小时后才被发现」的案例）。
		if t.LastRunStatus == "failed" && t.Status != "paused" {
			data.Alerts = append(data.Alerts, dashboardAlert{
				Level:  "error",
				Title:  "任务上次运行失败：" + t.TaskName,
				Detail: t.LastRunMessage,
				TaskID: t.ID,
			})
		}
	}

	// ---- 账号健康 ----
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	for _, a := range accounts {
		data.Accounts.Total++
		if a.IsActive {
			data.Accounts.Active++
		}
	}
	if data.Accounts.Total > 0 && data.Accounts.Active == 0 {
		data.Alerts = append(data.Alerts, dashboardAlert{
			Level:  "warn",
			Title:  "所有网盘账号都处于停用状态",
			Detail: "定时任务仍会被触发，但运行时会因账号不可用而失败。",
		})
	}
	if data.Accounts.Total == 0 {
		data.Alerts = append(data.Alerts, dashboardAlert{
			Level:  "warn",
			Title:  "还没有添加网盘账号",
			Detail: "先添加夸克或光鸭账号，才能创建转存任务。",
		})
	}

	// ---- 调度 ----
	if enabled, _ := st.GetSetting(ctx, "schedule_enabled"); enabled != "off" {
		data.Schedule.Enabled = true
		if c, _ := st.GetSetting(ctx, "schedule_cron"); strings.TrimSpace(c) != "" {
			data.Schedule.Cron = strings.TrimSpace(c)
			if next, err := transfer.NextCronRun(data.Schedule.Cron, ""); err == nil {
				data.Schedule.NextRun = next
			}
		}
	}

	// ---- 运行中 ----
	if stats := svc.Stats(ctx); len(stats.RunningTaskIDs) > 0 {
		data.Running.Count = len(stats.RunningTaskIDs)
		data.Running.IDs = stats.RunningTaskIDs
	}

	// ---- 转存统计 ----
	daily, err := st.TransferStats(ctx, 7)
	if err != nil {
		return err
	}
	// TransferStats 返回**时间正序**（最旧在前，今天在最后），
	// 因为前端画趋势条要按时间从左到右。
	data.Transfers.Last7Days = make([]dayCount, 0, len(daily))
	for _, d := range daily {
		data.Transfers.Last7Days = append(data.Transfers.Last7Days, dayCount{
			Date: d.Date, Count: d.Count,
		})
	}
	// 「今天」取最后一个元素（不是第 0 个：那是 7 天前）。
	if n := len(daily); n > 0 {
		data.Transfers.TodayCount = daily[n-1].Count
		data.Transfers.TodayRuns = daily[n-1].Runs
	}
	if total, err := st.CountAllSavedNames(ctx); err == nil {
		data.Transfers.TotalSaved = total
	}

	// ---- 最近失败（最多 5 条，用于快速定位）----
	failures, err := st.RecentFailures(ctx, 5)
	if err == nil {
		for _, f := range failures {
			data.Failures = append(data.Failures, dashboardFailure{
				TaskID:   f.TaskID,
				TaskName: taskName[f.TaskID],
				Message:  f.Message,
				FailedAt: f.StartedAt.Format(time.RFC3339),
			})
		}
	}

	// 告警排序：error 在前，warn 其次。
	sort.SliceStable(data.Alerts, func(i, j int) bool {
		return alertWeight(data.Alerts[i].Level) < alertWeight(data.Alerts[j].Level)
	})

	respond(w, data)
	return nil
}

// alertWeight 给告警等级一个排序权重（数字越小越严重）。
func alertWeight(level string) int {
	switch level {
	case "error":
		return 0
	case "warn":
		return 1
	default:
		return 2
	}
}
