// Package transfer 追剧转存服务层：任务 CRUD + 定时调度 + 单次执行 + 分享预览。
// 执行器与预览逻辑移植自 CASX（经 LitePan 适配），本文件只做编排。
package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/driver"
	"pan-transfer/internal/store"

	"github.com/robfig/cron/v3"
)

// cronParser 支持标准 5 字段 cron（分 时 日 月 周），用于任务与全局调度的表达式校验。
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ParseCron 校验 cron 表达式合法性。
func ParseCron(expr string) error {
	_, err := cronParser.Parse(strings.TrimSpace(expr))
	return err
}

// NextCronRun 返回 cron 表达式下次触发时间（from 起）。
func NextCronRun(expr, from string) (string, error) {
	start := time.Now()
	if strings.TrimSpace(from) != "" {
		if t, err := time.Parse(time.RFC3339, from); err == nil {
			start = t
		}
	}
	s, err := cronParser.Parse(strings.TrimSpace(expr))
	if err != nil {
		return "", fmt.Errorf("无效 cron 表达式：%v", err)
	}
	return s.Next(start).Format("2006-01-02 15:04"), nil
}

// Executor 是账号驱动执行器的最小接口约束（由 accounts.Manager 实现）。
type Executor interface {
	Run(context.Context, int64, func(driver.Driver) error) error
}

// Notifier 任务结果通知接口（由 notify.Sender 适配）。
type Notifier interface {
	Send(ctx context.Context, title, message string) []string
}

// Options 服务构造参数。
type Options struct {
	Exec   Executor
	Store  *store.Store
	Log    *slog.Logger
	Notify Notifier

	// Metrics 是可选的运行指标回调，由 main 注入（httpapi.metrics）。
	//
	// 为什么用回调而不是直接依赖 httpapi：transfer 是领域服务层，
	// 反向依赖 HTTP 层会破坏分层（httpapi 已经依赖 transfer）。
	// 回调保持单向依赖，同时让 Service 在测试里可以传 nil。
	Metrics MetricsSink
}

// MetricsSink 是 Service 向外报告运行指标的接口。
//
// 只包含 Service 真正知道的事件；HTTP 层的请求量/耗时由中间件自己统计。
type MetricsSink interface {
	// SweepCompleted 在一次调度扫描结束后调用。
	SweepCompleted()
	// Transferred 在转存了 n 个文件后调用。
	Transferred(n int)
	// RateLimited 在命中上游限流时调用（风控信号）。
	RateLimited()
}

// Service 追剧转存服务。
// runTimeout 单次任务运行的总超时：每个网盘请求自身有 30s 超时，但多链接、
// 多文件、轮询累加起来仍可能长时间挂起，这里给整体兜底。
var runTimeout = 30 * time.Minute

// linkInterval 同一任务内多条链接之间的间隔，避免背靠背突发请求触发平台限流。
var linkInterval = 2 * time.Second

type Service struct {
	exec  Executor
	log   *slog.Logger
	notif Notifier

	// stMu 保护 st 与 lastPrune 的并发访问。
	//
	// 写侧：Reload（还原备份时，从 HTTP handler goroutine 调用）。
	// 读侧：sweep / pruneRunsDaily（后台调度 goroutine，每 30s 一次）。
	// 早先 Reload 无锁写 s.st、pruneRunsDaily 无锁读写 lastPrune，
	// 是真实的数据竞争（且可能读到已 Close 的旧库句柄）。
	stMu      sync.RWMutex
	st        *store.Store
	lastPrune string // 上次清理运行记录的日期（YYYY-MM-DD），每天最多清一次

	mu      sync.Mutex
	running map[int64]struct{} // 正在执行的任务 ID，防并发重入

	schedMu     sync.Mutex
	lastTrigger map[int64]string // 任务 ID -> 上次触发的分钟窗口（防同分钟重复触发）
	schedSem    chan struct{}    // 定时运行全局并发上限，避免同时到点的任务瞬间打出大量网盘请求

	// dirCache 目录/分享列表短 TTL 缓存：同一轮运行内与紧邻的预览-运行之间复用列表，
	// 避免同一目录被反复翻页拉取（慢且容易触发平台限流）
	dirCache *driver.DirCache

	roundMu  sync.Mutex
	rounds   map[int64]*callbackRound // 分类 ID -> 进行中的回调轮次（内存态，重启丢失）
	roundSeq int64                    // 轮次自增 ID

	// schedMu 保护以下调度可观测字段（与 lastTrigger 同锁，避免额外加锁层级）。
	lastSweep    time.Time // 最近一次 sweep 完成时间
	schedEnabled bool      // 最近一次 sweep 观察到的全局调度开关状态
	sweeps       int64     // sweep 完成次数（区分"从未扫描"与"扫描到已关闭"）

	// metrics 是可选的运行指标接收者（由 main 注入，测试可为 nil）。
	metrics MetricsSink

	appCtx context.Context
	cancel context.CancelFunc

	// jobs 是异步任务句柄表（内存态，进程重启即清空）。
	jobs *jobRegistry

	// schedStartMu 保护 schedStarted，防止重启调度时重复起 goroutine。
	// 还原流程是 Close → Reload → RestartScheduling，
	// 若还原两次（用户点了两次），没有这个标记就会起两个调度循环，
	// 同一个任务会被触发两次。
	schedStartMu sync.Mutex
	schedStarted bool

	// appMu 保护 appCtx / cancel 的替换（RestartScheduling 写，sweep 读）。
	appMu sync.RWMutex
}

// sweepInterval 是调度扫描粒度，与 Start 里的 ticker 周期一致。
const sweepInterval = 30 * time.Second

// New 构造服务。
func New(opts Options) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		exec:        opts.Exec,
		st:          opts.Store,
		log:         log,
		notif:       opts.Notify,
		running:     map[int64]struct{}{},
		lastTrigger: map[int64]string{},
		schedSem:    make(chan struct{}, 2),
		dirCache:    driver.NewDirCache(driver.DefaultDirCacheTTL),
		rounds:      map[int64]*callbackRound{},
		appCtx:      ctx,
		cancel:      cancel,
		jobs:        newJobRegistry(50),
		metrics:     opts.Metrics,
	}
}

// Reload 在底层存储被替换后重置服务状态（用于从备份还原）。
//
// 还原会换掉 SQLite 文件并重开连接，Service 持有的旧 *store.Store 已关闭。
// 这里换掉引用并清空"正在运行/已触发"的进程内标记——那些标记对应的是还原前
// 的世界，留着会永久阻塞同名任务的调度。
//
// 调度 goroutine 不重启（仍读 s.appCtx），避免还原后重复起协程。
//
// 并发说明：sweep / pruneRunsDaily 在另一个 goroutine 里读 s.st 与 s.lastPrune，
// 而还原是在 HTTP handler 里调用的——两边真并发。早先这里无锁写 s.st，
// 是数据竞争，且 s.st 可能指向已 Close 的旧库。现在统一由 stMu 保护。
func (s *Service) Reload(st *store.Store) {
	s.mu.Lock()
	s.running = map[int64]struct{}{}
	s.mu.Unlock()

	s.schedMu.Lock()
	s.lastTrigger = map[int64]string{}
	s.schedMu.Unlock()

	s.roundMu.Lock()
	s.rounds = map[int64]*callbackRound{}
	s.roundMu.Unlock()

	s.stMu.Lock()
	s.st = st
	s.lastPrune = ""
	s.stMu.Unlock()
}

// store 返回当前存储句柄（持读锁取快照）。
//
// 所有后台 goroutine（sweep / pruneRunsDaily）必须通过它拿 store，
// 而不是直接读 s.st——后者与 Reload 的写并发。
func (s *Service) store() *store.Store {
	s.stMu.RLock()
	st := s.st
	s.stMu.RUnlock()
	return st
}

// setLastPrune 记录"今天已清理过"（持锁写，与 pruneRunsDaily 的读互斥）。
func (s *Service) setLastPrune(today string) {
	s.stMu.Lock()
	s.lastPrune = today
	s.stMu.Unlock()
}

// lastPruneDone 返回今天是否已清理过（持锁读）。
func (s *Service) lastPruneDone(today string) bool {
	s.stMu.RLock()
	done := s.lastPrune == today
	s.stMu.RUnlock()
	return done
}

// Close 停止调度循环并取消所有在途运行（进程退出、还原备份前调用）。
//
// 注意这是**不可逆**的：appCtx 被 cancel 后，Start() 里的调度 goroutine
// 会 return 退出。因此还原流程不能只调 Close 就完事，
// 必须再调 RestartScheduling() 把调度重新拉起来，
// 否则「还原一次之后定时任务再也不触发」。
func (s *Service) Close() { s.cancel() }

// RestartScheduling 重新建立 appCtx 并重启调度循环。
//
// 为什么需要：Close() 会 cancel 掉 appCtx，而还原备份的流程是
// 「Close → 换库 → Reload」。Reload 只换 store 引用，不重建 appCtx，
// 于是 Start() 里的 goroutine 早已 return，调度永久停止；
// 同时 StartTask 派生的 jobCtx 继承已取消的 appCtx，
// 所有后续手动运行也会立刻失败。
//
// 幂等：schedStarted 由 Start() 自己维护，重复调用只会重启一次。
func (s *Service) RestartScheduling() {
	ctx, cancel := context.WithCancel(context.Background())
	s.setAppContext(ctx, cancel)
	// Start() 内部会检查 schedStarted：StopScheduling 已把它置 false，
	// 所以这里会真正起一个新的调度循环。
	s.Start()
}

// StopScheduling 标记调度为"未启动"，让下一次 Start 能重新拉起。
//
// 与 Close 的区别：Close 用于进程退出（要连带取消在途运行），
// 而还原备份时需要先等在途任务跑完（它们持有旧库句柄，
// 强行取消会让它们写库失败并留下"运行中断"的记录），
// 所以还原流程只做"停止接新活 → 等在途 → 换库 → 重启调度"。
func (s *Service) StopScheduling() {
	s.schedStartMu.Lock()
	s.schedStarted = false
	s.schedStartMu.Unlock()
}

// WaitRunning 等待所有在途任务结束，最多等 timeout。
// 返回 true 表示已全部结束，false 表示超时仍有任务在跑。
//
// 还原前必须等：在途任务持有旧 *store.Store 句柄，
// 换库后它们继续写会打到已关闭的库上（报错、日志噪声），
// 更糟的是可能把还原前的半截数据写进新库。
func (s *Service) WaitRunning(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		n := len(s.running)
		s.mu.Unlock()
		if n == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Stats 是调度器的运行时快照，供深度健康检查使用。
// 进程活着不等于功能可用：调度协程挂了、DB 锁死、账号全部失效都需要能被发现。
type Stats struct {
	// RunningTaskIDs 是当前正在执行的任务 ID。
	RunningTaskIDs []int64 `json:"running_task_ids"`
	// LastSweepAt 是最近一次调度扫描时间（RFC3339）；从未扫描则为空。
	LastSweepAt string `json:"last_sweep_at,omitempty"`
	// NextTriggerAt 是下一次预期的调度扫描时间（近似值：LastSweepAt + 30s）。
	NextSweepAt string `json:"next_sweep_at,omitempty"`
	// SchedulingEnabled 反映全局调度开关。
	SchedulingEnabled bool `json:"scheduling_enabled"`
	// SweepsCompleted 是已完成扫描次数；0 表示还没跑过第一次。
	// 深度健康检查靠它区分"从未扫描"（正常）与"扫描到已关闭"（用户主动关）。
	SweepsCompleted bool `json:"sweeps_completed"`
}

// Stats 返回调度器运行时快照。
func (s *Service) Stats(ctx context.Context) Stats {
	out := Stats{}

	s.mu.Lock()
	ids := make([]int64, 0, len(s.running))
	for id := range s.running {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out.RunningTaskIDs = ids

	s.schedMu.Lock()
	last := s.lastSweep
	schedEnabled := s.schedEnabled
	sweeps := s.sweeps
	s.schedMu.Unlock()

	out.SweepsCompleted = sweeps > 0
	if !last.IsZero() {
		out.LastSweepAt = last.Format(time.RFC3339)
		out.NextSweepAt = last.Add(sweepInterval).Format(time.RFC3339)
	}
	out.SchedulingEnabled = schedEnabled
	return out
}

// ---------- 链接辅助 ----------

// LinksOf 返回任务的多源链接列表（有序去重）；空列表回退 ShareURL。
func LinksOf(t *domain.DramaTask) []string {
	var links []string
	if strings.TrimSpace(t.ShareLinks) != "" {
		_ = json.Unmarshal([]byte(t.ShareLinks), &links)
	}
	out := make([]string, 0, len(links))
	seen := map[string]struct{}{}
	for _, l := range links {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	if len(out) == 0 && strings.TrimSpace(t.ShareURL) != "" {
		out = append(out, strings.TrimSpace(t.ShareURL))
	}
	return out
}

// SetLinks 把链接列表序列化回任务（首个链接同时写入 ShareURL 便于展示）。
func SetLinks(t *domain.DramaTask, links []string) {
	clean := make([]string, 0, len(links))
	seen := map[string]struct{}{}
	for _, l := range links {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		clean = append(clean, l)
	}
	raw, _ := json.Marshal(clean)
	t.ShareLinks = string(raw)
	if len(clean) > 0 {
		t.ShareURL = clean[0]
	} else {
		t.ShareURL = ""
	}
}

// ---------- 正则规则 ----------

// enabledRegexOverrides 读取启用中的命名正则规则，转为执行器覆盖表。
func (s *Service) enabledRegexOverrides(ctx context.Context) (map[string]MagicRegexEntry, error) {
	rules, err := s.st.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]MagicRegexEntry{}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		key := strings.TrimSpace(r.Key)
		if !strings.HasPrefix(key, "$") {
			continue
		}
		out[key] = MagicRegexEntry{Pattern: r.Pattern, Replace: r.Replace}
	}
	return out, nil
}

// ---------- 调度 ----------

// Start 启动定时调度循环（30s 粒度；命中任务 RunTime 时刻触发）。
//
// 幂等：schedStarted 标记防止重复启动。还原流程会调 RestartScheduling，
// 用户连点两次还原就会走两遍，没有这个标记会起两个调度循环 →
// 同一个任务被触发两次（双倍网盘请求，风控风险翻倍）。
func (s *Service) Start() {
	s.schedStartMu.Lock()
	if s.schedStarted {
		s.schedStartMu.Unlock()
		return
	}
	s.schedStarted = true
	s.schedStartMu.Unlock()

	// 启动时先清一次过期运行记录，之后每天一次
	go s.pruneRunsDaily()
	go func() {
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.appContext().Done():
				return
			case <-ticker.C:
				s.sweep()
				s.pruneRunsDaily()
			}
		}
	}()
}

// appContext 返回当前 appCtx（持读锁取快照）。
//
// RestartScheduling 会替换 appCtx，而调度 goroutine 每次循环都要读它。
// 直接读 s.appCtx 与替换并发就是数据竞争。
func (s *Service) appContext() context.Context {
	s.appMu.RLock()
	ctx := s.appCtx
	s.appMu.RUnlock()
	return ctx
}

// setAppContext 替换 appCtx 与 cancel（持写锁）。
func (s *Service) setAppContext(ctx context.Context, cancel context.CancelFunc) {
	s.appMu.Lock()
	s.appCtx = ctx
	s.cancel = cancel
	s.appMu.Unlock()
}

// pruneRunsDaily 按设置 run_retention_days（0 = 永久保留）清理旧的运行记录，
// 每天最多执行一次；每个任务至少保留最近 50 条作为排障依据。
//
// 注意：这个函数与 Start 里的 goroutine 和 ticker goroutine 都可能并发进入
// （启动瞬间「先清一次」和第一次 30s tick 撞上是必然的），
// 所以 lastPrune 的读改写必须是原子的——用 stMu 包住整个判定+置位。
func (s *Service) pruneRunsDaily() {
	today := time.Now().Format("2006-01-02")

	// 先做一次无锁的快速判断，避免每次都抢锁（绝大多数 tick 都在这里返回）。
	// 但这只是优化：真正判定在下面的锁内做，因为可能有并发。
	s.stMu.RLock()
	done := s.lastPrune == today
	st := s.st
	s.stMu.RUnlock()
	if done || st == nil {
		return
	}

	s.stMu.Lock()
	if s.lastPrune == today {
		// 双检：另一个 goroutine 已经做过今天这次清理了。
		s.stMu.Unlock()
		return
	}
	s.lastPrune = today
	s.stMu.Unlock()

	days := 90
	if v, err := st.GetSetting(s.appCtx, "run_retention_days"); err == nil {
		if n, e := strconv.Atoi(strings.TrimSpace(v)); e == nil && n >= 0 {
			days = n
		}
	}
	if days == 0 {
		return
	}
	n, err := st.PruneRuns(s.appCtx, days, 50)
	if err != nil {
		s.log.Warn("清理运行记录失败", "err", err)
	} else if n > 0 {
		s.log.Info("已清理过期运行记录", "count", n, "keep_days", days)
	}
}

// sweep 扫描到期任务并触发（cron 匹配，支持 */N 步长、周几组合等标准 5 字段表达式）。
func (s *Service) sweep() {
	ctx := s.appCtx
	st := s.store()
	if st == nil {
		// 还原过程中（或重开失败）store 可能短暂为 nil，
		// 此时跳过本轮扫描而不是 panic。
		return
	}
	enabled, _ := st.GetSetting(ctx, "schedule_enabled")

	// 记录调度可观测状态：深度健康检查需要区分「调度器还活着」和「调度被关掉」。
	s.schedMu.Lock()
	s.lastSweep = time.Now()
	s.schedEnabled = enabled != "off"
	s.sweeps++
	s.schedMu.Unlock()

	// 指标：调度扫描次数长期不涨 = 调度协程挂了，这是最直接的存活信号。
	if s.metrics != nil {
		s.metrics.SweepCompleted()
	}

	if enabled == "off" {
		return
	}
	tasks, err := st.ListActiveTasks(ctx)
	if err != nil {
		s.log.Warn("调度扫描失败", "err", err)
		return
	}
	now := time.Now()
	currentMinute := now.Truncate(time.Minute)
	for _, t := range tasks {
		expr := s.effectiveCron(ctx, t)
		if strings.TrimSpace(expr) == "" {
			continue
		}
		sched, err := cronParser.Parse(expr)
		if err != nil {
			s.log.Warn("任务 cron 表达式无效，跳过", "task", t.ID, "expr", expr, "err", err)
			continue
		}
		// 命中判定：上一分钟开始的下一次触发正好落在当前分钟。
		if !sched.Next(currentMinute.Add(-time.Second)).Equal(currentMinute) {
			continue
		}
		// 同分钟窗口防重复触发
		s.schedMu.Lock()
		window := currentMinute.Format("2006-01-02 15:04")
		if s.lastTrigger[t.ID] == window {
			s.schedMu.Unlock()
			continue
		}
		s.lastTrigger[t.ID] = window
		s.schedMu.Unlock()

		// 分类轮次回调：在随机延迟前入轮，避免先跑完的任务把轮次提前关闭
		s.joinCategoryRound(t)

		// 随机延迟必须在 goroutine 内取 time.Now()：
		// 早先用的是循环外取的一个 now，同一轮所有任务拿到同一个
		// UnixNano() → "随机"延迟完全相同 → 批量触发时依旧同时醒，
		// 随机化形同虚设（这是降低风控的关键手段）。
		go func(taskID int64) {
			jitter := time.Duration(1+time.Now().UnixNano()%180) * time.Second
			select {
			case <-ctx.Done():
				return
			case <-time.After(jitter):
			}
			// 全局并发上限：避免同时到点的任务瞬间打出大量网盘请求触发风控
			select {
			case <-ctx.Done():
				return
			case s.schedSem <- struct{}{}:
			}
			defer func() { <-s.schedSem }()
			if _, err := s.RunTask(ctx, taskID, "schedule"); err != nil {
				s.log.Warn("定时任务执行失败", "task", taskID, "err", err)
			}
		}(t.ID)
	}
}

// effectiveCron 计算任务应执行的 cron 表达式（可能返回空表示手动/停用）。
func (s *Service) effectiveCron(ctx context.Context, t *domain.DramaTask) string {
	switch EffectiveRunMode(t) {
	case "manual":
		return ""
	case "global":
		return s.GlobalCron(ctx)
	default: // custom
		if strings.TrimSpace(t.RunCron) != "" {
			return strings.TrimSpace(t.RunCron)
		}
		// 兼容旧数据：RunWeek + RunTime 合成 cron
		if strings.TrimSpace(t.RunTime) != "" {
			parts := strings.SplitN(t.RunTime, ":", 2)
			hh, _ := strconv.Atoi(parts[0])
			mm := 0
			if len(parts) > 1 {
				mm, _ = strconv.Atoi(parts[1])
			}
			dow := strings.TrimSpace(t.RunWeek)
			if dow == "" {
				dow = "*"
			}
			return fmt.Sprintf("%d %d * * %s", mm, hh, dow)
		}
		return ""
	}
}

// GlobalCron 返回全局调度 cron（设置页 schedule_cron；旧字段 schedule_time+week 合成）。
func (s *Service) GlobalCron(ctx context.Context) string {
	if c, _ := s.st.GetSetting(ctx, "schedule_cron"); strings.TrimSpace(c) != "" {
		return strings.TrimSpace(c)
	}
	timeStr, _ := s.st.GetSetting(ctx, "schedule_time")
	week, _ := s.st.GetSetting(ctx, "schedule_week")
	timeStr = strings.TrimSpace(timeStr)
	if timeStr == "" {
		return ""
	}
	parts := strings.SplitN(timeStr, ":", 2)
	hh, _ := strconv.Atoi(parts[0])
	mm := 0
	if len(parts) > 1 {
		mm, _ = strconv.Atoi(parts[1])
	}
	dow := strings.TrimSpace(week)
	if dow == "" {
		dow = "*"
	}
	return fmt.Sprintf("%d %d * * %s", mm, hh, dow)
}

// EffectiveRunMode 返回任务的实际调度模式（兼容 v0.1 无 run_mode 的旧数据）。
func EffectiveRunMode(t *domain.DramaTask) string {
	switch strings.TrimSpace(t.RunMode) {
	case "global", "custom", "manual":
		return strings.TrimSpace(t.RunMode)
	}
	if strings.TrimSpace(t.RunCron) != "" || strings.TrimSpace(t.RunTime) != "" {
		return "custom"
	}
	return "manual"
}

// skipHistoryEnabled 判断任务本次运行是否启用转存历史去重。
func (s *Service) skipHistoryEnabled(ctx context.Context, t *domain.DramaTask) bool {
	mode := strings.TrimSpace(t.SkipHistory)
	if mode == "" || mode == "global" {
		v, err := s.st.GetSetting(ctx, "skip_history_global")
		mode = strings.TrimSpace(v)
		if err != nil || mode == "" {
			mode = "on"
		}
	}
	return mode == "on"
}

// ---------- 执行 ----------

// RunTaskNow 手动执行任务（忽略运行星期，但任务暂停时仍拒绝）。
func (s *Service) RunTaskNow(ctx context.Context, taskID int64) (*RunResult, error) {
	return s.RunTask(ctx, taskID, "manual")
}

// StartTask 异步执行任务：立即返回 job 句柄，转存在后台 goroutine 里跑。
//
// 与 RunTaskNow 的区别只在"调用方是否等待"。API 层用它，因为同步等待最长 30 分钟，
// 反代 60s 超时即断、手机切后台即无响应、无法取消。后台 goroutine 用
// context.WithoutCancel 派生，保证客户端断开不影响执行（与原同步路径一致）。
func (s *Service) StartTask(ctx context.Context, taskID int64) (*JobView, error) {
	t, err := s.st.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if t.Status == "paused" {
		return nil, domain.Errorf(domain.CodeValidation, "任务已暂停，请先启用")
	}

	// 先占位再启动：否则两次快速点击会都通过检查。
	js := s.jobs.create(t.ID, t.TaskName, "manual")

	// 用 appCtx 而不是请求 ctx：请求在提交后可能立刻返回，而转存必须继续。
	// 超时仍由 RunTask 内部的 runTimeout 控制。
	// 挂上进度接收者：执行器各阶段会通过它更新 Stage/Progress。
	jobCtx, cancel := context.WithCancel(s.appCtx)
	js.setCancel(cancel)
	jobCtx = WithProgressSink(jobCtx, js)

	go func() {
		defer cancel()
		res, err := s.RunTask(jobCtx, taskID, "manual")
		if err != nil {
			js.finish(friendlyJobErr(err), nil, friendlyRunErr(err))
			return
		}
		js.finish(res.Status, res, "")
	}()

	v := jobView(js)
	return &v, nil
}

// friendlyJobErr 把 context 取消转成明确的 canceled 状态，
// 其余错误统一记为 failed（具体原因保留在 Err 里）。
func friendlyJobErr(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "failed"
	default:
		return "failed"
	}
}

// GetJob 返回指定 job 的视图（含日志快照）。
func (s *Service) GetJob(id int64) (*JobView, bool) {
	js, ok := s.jobs.get(id)
	if !ok {
		return nil, false
	}
	v := jobView(js)
	return &v, true
}

// ListJobs 返回全部 job（新的在前）。
func (s *Service) ListJobs() []JobView {
	list := s.jobs.list()
	out := make([]JobView, 0, len(list))
	for _, js := range list {
		out = append(out, jobView(js))
	}
	return out
}

// CancelJob 请求取消一个正在运行的 job。
func (s *Service) CancelJob(id int64) error {
	js, ok := s.jobs.get(id)
	if !ok {
		return domain.Errorf(domain.CodeNotFound, "运行句柄不存在或已过期")
	}
	if !js.jobRunning() {
		return domain.Errorf(domain.CodeValidation, "该运行已结束，无法取消")
	}
	// cancelJob 返回 false 有两种情况：已结束、或该 job 不支持取消。
	// 上面已排除"已结束"，所以这里就是不支持取消。
	if !js.cancelJob() {
		return domain.Errorf(domain.CodeValidation, "该运行不支持取消")
	}
	return nil
}

// RemoveJob 删除一个已结束的 job 句柄（内存态，不影响 task_runs 里的权威记录）。
func (s *Service) RemoveJob(id int64) {
	s.jobs.remove(id)
}

// jobView 生成 job 视图（含日志快照）。返回无锁的 JobView，可安全序列化。
func jobView(js *JobState) JobView { return snapshotLog(js) }

// RunResult 一次任务执行的结果汇总。
type RunResult struct {
	TaskID        int64  `json:"task_id"`
	Status        string `json:"status"` // success/failed/partial
	TransferCount int    `json:"transfer_count"`
	LinksUsed     int    `json:"links_used"`
	LinksFailed   int    `json:"links_failed"`
	Message       string `json:"message"`
	TreeSummary   string `json:"tree_summary"`
}

// RunTask 执行任务：遍历多源链接，逐条转存（互去重），汇总结果并通知。
func (s *Service) RunTask(ctx context.Context, taskID int64, trigger string) (*RunResult, error) {
	t, err := s.st.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if t.Status == "paused" {
		return nil, domain.Errorf(domain.CodeValidation, "任务已暂停，请先启用")
	}

	// 并发重入保护
	s.mu.Lock()
	if _, busy := s.running[taskID]; busy {
		s.mu.Unlock()
		return nil, domain.Errorf(domain.CodeValidation, "任务正在运行中")
	}
	s.running[taskID] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, taskID)
		s.mu.Unlock()
	}()

	// 单次运行总超时：整体兜底，防止网络/上游异常导致运行长时间挂起
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	ctx = driver.WithDirCache(ctx, s.dirCache)

	// 分类回调：加入轮次（幂等，手动运行在此入轮；定时触发已在 sweep 决定时入轮）
	s.joinCategoryRound(t)
	res := &RunResult{TaskID: taskID}
	var allNames []string
	// 结算放在 defer：跳过/无链接/超时/中止等所有退出路径都能结算轮次或触发回调。
	//
	// 必须是闭包（func(){ ... allNames ... }）而不是
	// defer s.settleCategory(t, res, allNames, trigger)：
	// Go 的 defer 在**注册时**就对参数求值，那样 allNames 会永远是 nil，
	// 回调的 files 字段也就永远是空数组。闭包在**执行时**才读 allNames，
	// 能看到循环里累积的所有 append。
	defer func() { s.settleCategory(t, res, allNames, trigger) }()

	links := LinksOf(t)
	if len(links) == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "任务没有配置分享链接")
	}

	// 定时触发时校验运行星期与截止日期（手动触发不校验星期）
	if trigger == "schedule" {
		if EffectiveRunMode(t) == "global" {
			if w, _ := s.st.GetSetting(ctx, "schedule_week"); strings.TrimSpace(w) != "" {
				t.RunWeek = strings.TrimSpace(w) // 仅影响本次校验，不落库
			}
		}
		if err := ValidateSchedule(t, false, time.Now()); err != nil {
			return &RunResult{TaskID: taskID, Status: "skipped", Message: "不在运行窗口内"}, nil
		}
	}

	overrides, err := s.enabledRegexOverrides(ctx)
	if err != nil {
		return nil, err
	}
	useHistory := s.skipHistoryEnabled(ctx, t)

	var trees []string
	var lastErr error

	for i, link := range links {
		select {
		case <-ctx.Done():
			return res, domain.Errorf(domain.CodeInternal, "运行中止：%v", friendlyRunErr(ctx.Err()))
		default:
		}
		// 多链接之间留出间隔，避免连续突发请求触发平台限流（CASX 用独立任务+调度天然分散）
		if i > 0 && linkInterval > 0 {
			timer := time.NewTimer(linkInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return res, domain.Errorf(domain.CodeInternal, "运行中止：%v", friendlyRunErr(ctx.Err()))
			case <-timer.C:
			}
		}
		runID, err := s.st.CreateRun(ctx, taskID, link)
		if err != nil {
			return nil, err
		}
		tree, names, runLog, rerr := s.runOneLink(ctx, t, link, overrides, useHistory, allNames)
		status := "success"
		msg := ""
		if rerr != nil {
			status = "failed"
			msg = friendlyRunErr(rerr)
			lastErr = rerr
			res.LinksFailed++
		} else {
			res.LinksUsed++
			res.TransferCount += len(names)
			allNames = append(allNames, names...)
		}
		if tree != "" {
			trees = append(trees, tree)
		}
		_ = s.st.FinishRun(ctx, runID, status, msg, tree, runLog, len(names))

		// 异步路径：每条链接跑完都把日志快照写进 job，前端轮询即可看到滚动输出。
		if js := progressJobFrom(ctx); js != nil {
			js.appendLog(runLog)
			if len(links) > 1 {
				js.setStage(fmt.Sprintf("第 %d/%d 条链接%s", i+1, len(links), statusText(status)), i*100/len(links))
			}
		}
	}

	// 汇总任务状态
	nowStr := time.Now().Format("2006-01-02 15:04:05")
	if res.LinksFailed == 0 {
		res.Status = "success"
		res.Message = fmt.Sprintf("转存 %d 个文件（%d 条链接）", res.TransferCount, res.LinksUsed)
	} else if res.LinksUsed > 0 {
		res.Status = "partial"
		res.Message = fmt.Sprintf("部分成功：%d 条链接转存 %d 个文件，%d 条失效", res.LinksUsed, res.TransferCount, res.LinksFailed)
	} else {
		res.Status = "failed"
		if lastErr != nil {
			res.Message = "全部链接失败：" + friendlyRunErr(lastErr)
		} else {
			res.Message = "全部链接失败"
		}
	}
	res.TreeSummary = strings.Join(trees, "\n")
	_ = s.st.UpdateTaskRunState(ctx, taskID, nowStr, res.Status, res.Message, res.TreeSummary)

	// 指标：累计转存量。
	if s.metrics != nil && res.TransferCount > 0 {
		s.metrics.Transferred(res.TransferCount)
	}

	// 通知：仅在本次有新转存文件、或整次运行失败时推送；
	// 0 文件（无新剧集 / 全部已转存）静默，避免定时任务空跑刷屏。
	if s.notif != nil {
		if !shouldNotify(res.Status, res.TransferCount) {
			s.log.Info("跳过通知：本次未转存新文件", "task", t.TaskName, "status", res.Status)
		} else {
			title := fmt.Sprintf("✅ 转存完成：%s", t.TaskName)
			if res.Status == "failed" {
				title = fmt.Sprintf("❌ 转存失败：%s", t.TaskName)
			} else if res.Status == "partial" {
				title = fmt.Sprintf("⚠️ 转存部分成功：%s", t.TaskName)
			}
			var b strings.Builder
			b.WriteString(res.Message)
			if len(allNames) > 0 {
				b.WriteString("\n本次新转存：")
				for i, n := range allNames {
					if i >= 10 {
						b.WriteString(fmt.Sprintf("\n…等共 %d 个文件", len(allNames)))
						break
					}
					b.WriteString("\n· " + n)
				}
			}
			go s.notif.Send(context.Background(), title, b.String())
		}
	}
	return res, nil
}

// shouldNotify 判断一次运行是否需要推送通知：有新转存文件，或整次运行失败时推送；
// 0 文件的成功/部分成功静默（定时任务空跑不刷屏）。
func shouldNotify(status string, transferred int) bool {
	return transferred > 0 || status == "failed"
}

// friendlyRunErr 把超时/中止类错误转成可读文案，其余原样返回。
func friendlyRunErr(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("运行超时（单次上限 %s），已中止", runTimeout)
	case errors.Is(err, context.Canceled):
		return "运行已中止（服务停止或请求取消）"
	default:
		return err.Error()
	}
}

// runOneLink 对单条分享链接执行一次转存。prevNames 是本运行中前面链接已转存的
// 文件名，用于跨链接去重。返回值：树状摘要、新转存文件名、执行器详细日志、错误。
func (s *Service) runOneLink(ctx context.Context, t *domain.DramaTask, link string, overrides map[string]MagicRegexEntry, useHistory bool, prevNames []string) (string, []string, string, error) {
	// 转存历史（按名去重）
	var skip []string
	if useHistory {
		names, err := s.st.SavedNormNames(ctx, t.ID)
		if err != nil {
			return "", nil, "", err
		}
		skip = append(skip, names...)
	}
	skip = append(skip, prevNames...)

	var tree string
	var names []string
	var runLog string
	err := s.exec.Run(ctx, t.AccountID, func(drv driver.Driver) error {
		taskCopy := *t
		taskCopy.ShareURL = link
		ex, err := NewDramaExecutor(drv, &taskCopy, overrides)
		if err != nil {
			return err
		}
		ex.ExtraSkipNames = skip
		// 阶段/进度回调：仅当日志快照对应一个 job 时生效（异步路径）。
		if sink := progressSinkFrom(ctx); sink != nil {
			ex.onProgress = sink
		}
		tree, err = ex.Execute(ctx)
		// 无论成败都取日志，排障时能看到卡在哪一步
		runLog = truncateRunLog(ex.Log())
		names = ex.TransferredNames()
		return err
	})
	if err != nil {
		return tree, nil, runLog, err
	}
	// 记录转存历史
	if len(names) > 0 {
		items := make([]store.SavedName, 0, len(names))
		for _, n := range names {
			items = append(items, store.SavedName{
				TaskID:   t.ID,
				NormName: normalizeName(n, t.IgnoreExtension),
				FileName: n,
			})
		}
		if err := s.st.AddSavedNames(ctx, t.ID, items); err != nil {
			s.log.Warn("记录转存历史失败", "task", t.ID, "err", err)
		}
	}
	return tree, names, runLog, nil
}

// truncateRunLog 限制落库的运行日志大小，防止异常情况下撑爆数据库。
func truncateRunLog(log string) string {
	const max = 256 * 1024
	if len(log) <= max {
		return log
	}
	return log[:max] + "\n…（日志过长已截断）"
}

// ---------- 任务 CRUD ----------

// ListTasks 返回全部任务。
func (s *Service) ListTasks(ctx context.Context) ([]*domain.DramaTask, error) {
	return s.st.ListTasks(ctx)
}

// GetTask 取单个任务。
func (s *Service) GetTask(ctx context.Context, id int64) (*domain.DramaTask, error) {
	return s.st.GetTask(ctx, id)
}

// SaveTask 新建或更新任务（id>0 为更新）。
func (s *Service) SaveTask(ctx context.Context, t *domain.DramaTask) (int64, error) {
	if strings.TrimSpace(t.TaskName) == "" {
		return 0, domain.Errorf(domain.CodeValidation, "任务名称不能为空")
	}
	if t.AccountID == 0 {
		return 0, domain.Errorf(domain.CodeValidation, "请选择网盘账号")
	}
	if len(LinksOf(t)) == 0 {
		return 0, domain.Errorf(domain.CodeValidation, "至少填写一条分享链接")
	}
	if strings.TrimSpace(t.SavePath) == "" {
		return 0, domain.Errorf(domain.CodeValidation, "请填写保存路径")
	}
	if t.SkipHistory != "on" && t.SkipHistory != "off" {
		t.SkipHistory = "global"
	}
	// 子目录重存模式白名单：该值决定执行器是否删除远端目录，
	// 拼错的字符串会让行为回落到未知分支，必须在入库前拦住。
	switch t.UpdateSubdirResaveMode {
	case "", "delete_then_resave":
	default:
		return 0, domain.Errorf(domain.CodeValidation,
			"update_subdir_resave_mode 只能是空（增量）或 delete_then_resave")
	}
	// 起始集号兜底：<=0 视为 1（planTransfer 用它作为 {I+} 的起始序号）。
	if t.SortIndex <= 0 {
		t.SortIndex = 1
	}
	switch t.RunMode {
	case "global", "custom", "manual":
	default:
		t.RunMode = "" // 兼容空值，读取时按旧规则推断
	}
	if t.RunMode == "custom" && t.RunTime != "" {
		if _, err := time.Parse("15:04", t.RunTime); err != nil {
			return 0, domain.Errorf(domain.CodeValidation, "运行时间格式应为 HH:MM")
		}
	}
	// 归一化链接存储
	SetLinks(t, LinksOf(t))
	if t.ID > 0 {
		if _, err := s.st.GetTask(ctx, t.ID); err != nil {
			return 0, err
		}
		if err := s.st.UpdateTask(ctx, t); err != nil {
			return 0, err
		}
		return t.ID, nil
	}
	id, err := s.st.CreateTask(ctx, t)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteTask 删除任务。
func (s *Service) DeleteTask(ctx context.Context, id int64) error {
	return s.st.DeleteTask(ctx, id)
}

// SetTaskStatus 暂停/恢复任务。
func (s *Service) SetTaskStatus(ctx context.Context, id int64, status string) error {
	if status != "running" && status != "paused" {
		return domain.Errorf(domain.CodeValidation, "非法的任务状态")
	}
	return s.st.SetTaskStatus(ctx, id, status)
}

// ListRuns 返回任务运行历史。
func (s *Service) ListRuns(ctx context.Context, taskID int64, limit int) ([]*store.TaskRun, error) {
	return s.st.ListRuns(ctx, taskID, limit)
}

// ListSavedNames 返回任务转存历史。
func (s *Service) ListSavedNames(ctx context.Context, taskID int64, limit int) ([]*store.SavedName, error) {
	return s.st.ListSavedNames(ctx, taskID, limit)
}

// ---------- 预览（preview.go 提供 PreviewShare 主体） ----------
