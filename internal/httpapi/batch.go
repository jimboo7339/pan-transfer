package httpapi

import (
	"context"
	"net/http"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
	"pan-transfer/internal/transfer"
)

// 批量任务操作。
//
// 为什么需要：10 个任务要临时全部暂停、或清理误建的任务，
// 前端只能一个个点（每个动作一次 HTTP 往返 + 一次列表刷新），
// N 个任务就是 N 次等待。批量端点是纯前端体验优化，但收益很明显。
//
// 设计取舍：
//   - 一次请求处理一个动作，不做成「命令序列」：批量的语义是「对这些做同一件事」，
//     复杂编排应该让用户去点多次而不是在一个 API 里塞 DSL。
//   - 逐条执行并返回逐条结果，不用一个大事务：SQLite 单连接
//     （SetMaxOpenConns(1)）下，长事务会阻塞所有读请求。
//   - 破坏性动作（delete）要求 ids 非空且返回逐条结果，便于前端展示"删了几个"。
//   - 部分失败不整体回滚：批量删除时删不掉的那条要明确告诉用户原因。

// batchAction 是支持的批量动作。
var batchActions = map[string]bool{
	"run":          true,
	"pause":        true,
	"resume":       true,
	"delete":       true,
	"set_category": true,
	"set_mode":     true,
}

type batchRequest struct {
	// IDs 是目标任务 ID 列表。
	IDs []int64 `json:"ids"`
	// Action 是批量动作：run | pause | resume | delete | set_category | set_mode。
	Action string `json:"action"`
	// CategoryID 仅 set_category 使用（0 = 未分类）。
	CategoryID int64 `json:"category_id,omitempty"`
	// RunMode 仅 set_mode 使用：manual | global | custom。
	RunMode string `json:"run_mode,omitempty"`
}

// batchItemResult 是单个任务的执行结果。
type batchItemResult struct {
	ID       int64  `json:"id"`
	TaskName string `json:"task_name,omitempty"`
	OK       bool   `json:"ok"`
	Status   string `json:"status,omitempty"`
	Message  string `json:"message,omitempty"`
	// JobID 仅 run 动作使用：异步句柄，前端据此轮询进度。
	JobID int64 `json:"job_id,omitempty"`
}

// batchResult 是整体结果。
type batchResult struct {
	Total     int               `json:"total"`
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
	Items     []batchItemResult `json:"items"`
}

// handleTaskBatch 执行批量任务操作。
func (s *Server) handleTaskBatch(w http.ResponseWriter, r *http.Request) error {
	var in batchRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	if len(in.IDs) == 0 {
		return domain.Errorf(domain.CodeValidation, "请至少选择一个任务")
	}
	// 上限保护：一次选 500 个任务不是正常操作，多半是前端有 bug 或恶意调用。
	if len(in.IDs) > 500 {
		return domain.Errorf(domain.CodeValidation, "单次批量操作最多 500 个任务")
	}
	if !batchActions[in.Action] {
		return domain.Errorf(domain.CodeValidation, "不支持的批量动作：%s", in.Action)
	}
	if in.Action == "set_mode" {
		switch in.RunMode {
		case "manual", "global", "custom":
		default:
			return domain.Errorf(domain.CodeValidation,
				"run_mode 只能是 manual / global / custom")
		}
	}

	svc := s.transfer()
	if svc == nil {
		return domain.Errorf(domain.CodeInternal, "转存服务未就绪")
	}
	ctx := r.Context()
	st := s.currentStore()

	// 先查一遍任务名，用于结果展示（用户看到的是名字而不是 ID）。
	tasks, _ := svc.ListTasks(ctx)
	nameByID := map[int64]string{}
	for _, t := range tasks {
		nameByID[t.ID] = t.TaskName
	}

	out := batchResult{Total: len(in.IDs)}
	for _, id := range in.IDs {
		res := s.applyBatchOne(ctx, svc, st, id, in, nameByID[id])
		if res.OK {
			out.Succeeded++
		} else {
			out.Failed++
		}
		out.Items = append(out.Items, res)
	}

	// 危险动作（批量删除）必须留痕，便于事后追溯"是谁在什么时候删了什么"。
	if in.Action == "delete" {
		var names []string
		for _, it := range out.Items {
			if it.OK {
				names = append(names, it.TaskName)
			}
		}
		s.deps.Log.Warn("批量删除任务", "count", len(names), "tasks", names)
	}

	respond(w, out)
	return nil
}

// applyBatchOne 对单个任务执行动作。
//
// run 动作走异步 StartTask（与其他运行入口一致），返回 job_id 供前端轮询；
// 其余动作是同步的状态变更，立即返回结果。
func (s *Server) applyBatchOne(
	ctx context.Context, svc *transfer.Service, st *store.Store,
	id int64, in batchRequest, name string,
) batchItemResult {
	res := batchItemResult{ID: id, TaskName: name}

	switch in.Action {
	case "run":
		job, err := svc.StartTask(ctx, id)
		if err != nil {
			res.OK = false
			res.Message = err.Error()
			return res
		}
		res.OK = true
		res.Status = "running"
		res.JobID = job.ID
		res.Message = "已开始运行"
		return res

	case "pause":
		if err := svc.SetTaskStatus(ctx, id, "paused"); err != nil {
			res.OK = false
			res.Message = err.Error()
			return res
		}
		res.OK = true
		res.Status = "paused"
		res.Message = "已暂停"
		return res

	case "resume":
		if err := svc.SetTaskStatus(ctx, id, "running"); err != nil {
			res.OK = false
			res.Message = err.Error()
			return res
		}
		res.OK = true
		res.Status = "running"
		res.Message = "已启用"
		return res

	case "delete":
		if err := svc.DeleteTask(ctx, id); err != nil {
			res.OK = false
			res.Message = err.Error()
			return res
		}
		res.OK = true
		res.Message = "已删除（运行记录与转存历史一并删除）"
		return res

	case "set_category":
		t, err := svc.GetTask(ctx, id)
		if err != nil {
			res.OK = false
			res.Message = err.Error()
			return res
		}
		// 校验分类存在：不留悬空 category_id（虽然全仓没有外键，
		// 但指向不存在的分类会让回调静默失效）。
		if in.CategoryID != 0 {
			if _, err := st.GetCategory(ctx, in.CategoryID); err != nil {
				res.OK = false
				res.Message = "分类不存在"
				return res
			}
		}
		t.CategoryID = in.CategoryID
		if _, err := svc.SaveTask(ctx, t); err != nil {
			res.OK = false
			res.Message = err.Error()
			return res
		}
		res.OK = true
		if in.CategoryID == 0 {
			res.Message = "已移出分类"
		} else {
			res.Message = "已修改分类"
		}
		return res

	case "set_mode":
		t, err := svc.GetTask(ctx, id)
		if err != nil {
			res.OK = false
			res.Message = err.Error()
			return res
		}
		t.RunMode = in.RunMode
		if _, err := svc.SaveTask(ctx, t); err != nil {
			res.OK = false
			res.Message = err.Error()
			return res
		}
		res.OK = true
		res.Status = "running"
		res.Message = "调度模式已改为 " + modeLabel(in.RunMode)
		return res
	}

	res.OK = false
	res.Message = "未实现的动作"
	return res
}

// modeLabel 把调度模式转成中文，用于批量结果展示。
func modeLabel(m string) string {
	switch m {
	case "manual":
		return "手动运行"
	case "global":
		return "跟随全局"
	case "custom":
		return "自定义定时"
	}
	return m
}
