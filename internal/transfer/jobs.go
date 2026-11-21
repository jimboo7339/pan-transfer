package transfer

import (
	"context"
	"sync"
	"time"
)

// 异步任务句柄。
//
// 为什么必须有：POST /api/tasks/{id}/run 此前是同步的，整个转存流程（多链接 ×
// 每批 100 文件 × 每文件重命名）在 HTTP 请求里跑完，最长 30 分钟。后果是
// 反代 60s 超时即断、手机切后台即"无响应"、页面关掉就再也看不到结果、无法取消。
// 用户只能对着一个转圈的小 spinner 猜进度。
//
// 设计要点：
//   - job 状态放内存（与 s.running 同生命周期），不落库：它是运行中的瞬时视图，
//     持久化反而会引入"重启后如何恢复半截任务"的难题；任务本身的最终结果仍然
//     落在 task_runs 表里（CreateRun / FinishRun），日志页看到的是权威数据。
//   - 日志用快照而不是流：执行器内部是 strings.Builder，读的时候复制一份即可。
//     避免为了推流把 executor 改成 channel 架构（改动面大、收益有限）。
//   - 前端轮询 1s 一次即可：转存本身是分钟级操作，秒级进度足够。

// JobState 是一个正在执行（或刚结束）的任务句柄。
//
// 注意：JobState 含 sync.Mutex，因此**不得整体拷贝**（go vet 会拒绝）。
// 对外输出一律走 jobView()，由它显式挑选字段构造无锁的 JobView。
type JobState struct {
	// ID 是运行标识（同一任务多次运行会新建 Job）。
	ID int64 `json:"id"`
	// TaskID 是任务 ID。
	TaskID int64 `json:"task_id"`
	// TaskName 便于前端展示，不必再查任务列表。
	TaskName string `json:"task_name"`
	// Trigger 是 manual / schedule。
	Trigger string `json:"trigger"`

	// Status 是 job 级状态：running / success / failed / partial / canceled。
	Status string `json:"status"`
	// Stage 是当前阶段的人话描述（解析分享链接 / 转存中 / 重命名 / 收尾）。
	Stage string `json:"stage"`
	// Progress 是 0–100 的粗略进度；无法估算时为 -1。
	Progress int `json:"progress"`

	// StartedAt / FinishedAt 用于前端显示耗时。
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`

	// Result 是最终汇总（Status 非 running 时有效）。
	Result *RunResult `json:"result,omitempty"`
	// Err 是失败原因（失败时有值）。
	Err string `json:"error,omitempty"`

	// mu 保护 log：后台 goroutine 写、HTTP handler 读。
	mu sync.Mutex
	// log 是执行器日志的当前快照。
	log string

	// cancel 用于取消运行。
	cancel context.CancelFunc

	// done 在 job 结束时关闭，供等待方（含测试）同步。
	done chan struct{}
}

// JobView 是返回给前端的视图（无锁，可安全拷贝与序列化）。
type JobView struct {
	ID        int64      `json:"id"`
	TaskID    int64      `json:"task_id"`
	TaskName  string     `json:"task_name"`
	Trigger   string     `json:"trigger"`
	Status    string     `json:"status"`
	Stage     string     `json:"stage"`
	Progress  int        `json:"progress"`
	StartedAt time.Time  `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Result    *RunResult `json:"result,omitempty"`
	Err       string     `json:"error,omitempty"`
	Log       string     `json:"log,omitempty"`
}

// jobRegistry 管理进程内的 job 句柄。
type jobRegistry struct {
	mu     sync.Mutex
	jobs   map[int64]*JobState
	nextID int64
	// order 记录插入顺序，便于列表返回最近的在前。
	order []int64
	// max 限制保留的历史 job 数，避免长跑进程无限增长。
	max int
}

func newJobRegistry(max int) *jobRegistry {
	if max <= 0 {
		max = 50
	}
	return &jobRegistry{jobs: map[int64]*JobState{}, max: max}
}

// create 登记一个新的 job。
func (r *jobRegistry) create(taskID int64, taskName, trigger string) *JobState {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	js := &JobState{
		ID:        r.nextID,
		TaskID:    taskID,
		TaskName:  taskName,
		Trigger:   trigger,
		Status:    "running",
		Stage:     "排队中",
		Progress:  -1,
		StartedAt: time.Now(),
		done:      make(chan struct{}),
	}
	r.jobs[js.ID] = js
	r.order = append(r.order, js.ID)
	r.evictLocked()
	return js
}

// evictLocked 丢弃最老的已结束 job，防止 map 无限增长。调用方必须持锁。
func (r *jobRegistry) evictLocked() {
	for len(r.order) > r.max {
		oldest := r.order[0]
		r.order = r.order[1:]
		if js, ok := r.jobs[oldest]; ok && js.Status != "running" {
			delete(r.jobs, oldest)
			continue
		}
		// 最老的那个还在跑：放到队尾继续等，避免丢正在运行的句柄。
		r.order = append(r.order, oldest)
		break
	}
}

// get 返回指定 job。
func (r *jobRegistry) get(id int64) (*JobState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	js, ok := r.jobs[id]
	return js, ok
}

// list 返回全部 job（新的在前）。
func (r *jobRegistry) list() []*JobState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*JobState, 0, len(r.order))
	for i := len(r.order) - 1; i >= 0; i-- {
		if js, ok := r.jobs[r.order[i]]; ok {
			out = append(out, js)
		}
	}
	return out
}

// remove 删除一个 job 句柄（终态才允许）。
func (r *jobRegistry) remove(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if js, ok := r.jobs[id]; ok && js.Status != "running" {
		delete(r.jobs, id)
		for i, v := range r.order {
			if v == id {
				r.order = append(r.order[:i], r.order[i+1:]...)
				break
			}
		}
	}
}

// snapshotLog 生成 job 的视图（含日志快照）。不拷贝锁。
func snapshotLog(js *JobState) JobView {
	js.mu.Lock()
	log := js.log
	js.mu.Unlock()
	return JobView{
		ID:         js.ID,
		TaskID:     js.TaskID,
		TaskName:   js.TaskName,
		Trigger:    js.Trigger,
		Status:     js.Status,
		Stage:      js.Stage,
		Progress:   js.Progress,
		StartedAt:  js.StartedAt,
		FinishedAt: js.FinishedAt,
		Result:     js.Result,
		Err:        js.Err,
		Log:        log,
	}
}

// appendLog 原子地追加一段日志（由执行过程调用）。
func (js *JobState) appendLog(s string) {
	if s == "" {
		return
	}
	js.mu.Lock()
	js.log += s
	js.mu.Unlock()
}

// ---------- 进度回调传递 ----------

type progressSinkKey struct{}

// WithProgressSink 把进度回调挂到 ctx 上，供执行器内部各阶段调用。
//
// 为什么不给 Executor 传函数参数：Executor 的构造签名是
// NewDramaExecutor(drv, task, overrides)，加参数会牵动所有调用点与测试；
// 而 ctx 本来就是逐层透传的，进度接收者也只关心"当前这一轮"。
func WithProgressSink(ctx context.Context, js *JobState) context.Context {
	return context.WithValue(ctx, progressSinkKey{}, js)
}

// progressSinkFrom 取出 ctx 上的 job 句柄，没有则返回 nil。
//
// 返回的回调只更新 Stage/Progress：日志快照由 runOneLink 在执行器返回后
// 一次性写入（executor 内部是 strings.Builder，中途拼接会读到半截行）。
func progressSinkFrom(ctx context.Context) func(stage string, pct int) {
	js := progressJobFrom(ctx)
	if js == nil {
		return nil
	}
	return func(stage string, pct int) { js.setStage(stage, pct) }
}

// progressJobFrom 取出 ctx 上的 job 句柄（可能为 nil）。
func progressJobFrom(ctx context.Context) *JobState {
	js, _ := ctx.Value(progressSinkKey{}).(*JobState)
	return js
}

// statusText 把运行状态转成可读的中文片段，用于"第 1/3 条链接成功"。
func statusText(status string) string {
	switch status {
	case "success":
		return "成功"
	case "failed":
		return "失败"
	default:
		return status
	}
}

// setStage 更新阶段与进度。
func (js *JobState) setStage(stage string, progress int) {
	js.Stage = stage
	if progress >= 0 {
		js.Progress = progress
	}
}

// finish 收尾：写入终态并关闭 done。
func (js *JobState) finish(status string, res *RunResult, errMsg string) {
	js.Status = status
	js.FinishedAt = time.Now()
	if res != nil {
		cp := *res
		js.Result = &cp
		if status == "success" || status == "partial" {
			js.Progress = 100
			js.Stage = "已完成"
		}
	}
	if errMsg != "" {
		js.Err = errMsg
		if status == "running" || status == "" {
			js.Status = "failed"
			js.Stage = "失败"
		}
	}
	select {
	case <-js.done:
	default:
		close(js.done)
	}
}