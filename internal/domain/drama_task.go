package domain

import (
	"context"
	"time"
)

// DramaTask 追剧转存任务（来自Trae）。
// 字段语义与 CASX tasks 表对齐，仅取追剧相关字段。
type DramaTask struct {
	ID                      int64
	TaskName                string
	AccountID               int64
	ShareURL                string // 当前生效的分享链接（运行时由服务从 ShareLinks 中选出写入）
	ShareLinks              string // JSON 数组：多源链接，按顺序轮换；单链接场景与 ShareURL 等价
	SavePath                string
	Pattern                 string
	Replace                 string
	IgnoreExtension         bool
	RunWeek                 string // 逗号分隔的 1-7，留空表示每天都跑（来自Trae）
	RunTime                 string // "HH:MM" 自动运行时刻（追剧定时）；空表示不限时刻
	EndDate                 string // YYYY-MM-DD
	UpdateSubdir            string
	UpdateSubdirResaveMode  string
	StartFID                string
	SortIndex               int
	SkipHistory             string // global/on/off：是否按转存历史跳过已保存文件
	RunMode                 string // manual/global/custom：manual=仅手动，global=用全局调度，custom=用本任务 RunWeek/RunTime
	Status                  string // running/paused
	LastRunAt               string
	LastRunStatus           string
	LastRunMessage          string
	LastTreeSummary         string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// DramaTaskRun 追剧任务执行历史（来自Trae）
type DramaTaskRun struct {
	ID            int64
	TaskID        int64
	Status        string // running/success/failed/skipped
	Message       string
	TreeSummary   string
	TransferCount int
	StartedAt     time.Time
	FinishedAt    time.Time
}

// DramaTaskRepository 追剧任务仓储契约（来自Trae）
type DramaTaskRepository interface {
	Create(ctx context.Context, t *DramaTask) (int64, error)
	Update(ctx context.Context, t *DramaTask) error
	Delete(ctx context.Context, id int64) error
	Get(ctx context.Context, id int64) (*DramaTask, error)
	List(ctx context.Context) ([]*DramaTask, error)
	ListActive(ctx context.Context) ([]*DramaTask, error)

	CreateRun(ctx context.Context, r *DramaTaskRun) (int64, error)
	UpdateRun(ctx context.Context, r *DramaTaskRun) error
	ListRuns(ctx context.Context, taskID int64, limit int) ([]*DramaTaskRun, error)
}
