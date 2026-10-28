// 任务分类与分类回调接口：CRUD、发送测试、回调日志。
package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/store"
	"pan-transfer/internal/transfer"
)

func (s *Server) registerCategoryRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/categories", s.wrap(s.handleCategoryList))
	m.HandleFunc("POST /api/categories", s.wrap(s.handleCategorySave))
	m.HandleFunc("DELETE /api/categories/{id}", s.wrap(s.handleCategoryDelete))
	m.HandleFunc("POST /api/categories/{id}/test", s.wrap(s.handleCategoryTest))
	m.HandleFunc("GET /api/categories/{id}/callback-logs", s.wrap(s.handleCategoryLogs))
}

type categoryIn struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	CallbackEnabled    *bool  `json:"callback_enabled"`
	CallbackMode       string `json:"callback_mode"`
	CallbackMethod     string `json:"callback_method"`
	CallbackURL        string `json:"callback_url"`
	CallbackHeaders    string `json:"callback_headers"`
	CallbackBody       string `json:"callback_body"`
	CallbackTimeoutSec int    `json:"callback_timeout_sec"`
	SkipZero           *bool  `json:"skip_zero"`
	SortOrder          int    `json:"sort_order"`
}

func (in *categoryIn) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return domain.Errorf(domain.CodeValidation, "分类名称不能为空")
	}
	if in.CallbackEnabled != nil && *in.CallbackEnabled && strings.TrimSpace(in.CallbackURL) == "" {
		return domain.Errorf(domain.CodeValidation, "启用回调时必须填写回调 URL")
	}
	if in.CallbackHeaders != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(in.CallbackHeaders), &m); err != nil {
			return domain.Errorf(domain.CodeValidation, "请求头必须是 JSON 对象，如 {\"Authorization\":\"Bearer xxx\"}")
		}
	}
	return nil
}

func (s *Server) handleCategoryList(w http.ResponseWriter, r *http.Request) error {
	cats, err := s.deps.Store.ListCategories(r.Context())
	if err != nil {
		return err
	}
	for _, c := range cats {
		if c.TaskCount, err = s.deps.Store.CountTasksInCategory(r.Context(), c.ID); err != nil {
			return err
		}
		if last, err := s.deps.Store.LastCallback(r.Context(), c.ID); err == nil && last != nil {
			c.LastCallbackAt = last.CreatedAt.Format("2006-01-02 15:04")
			ok := last.OK
			c.LastCallbackOK = &ok
		}
	}
	respond(w, cats)
	return nil
}

func (s *Server) handleCategorySave(w http.ResponseWriter, r *http.Request) error {
	var in categoryIn
	if err := decode(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	if in.ID != 0 {
		if _, err := s.deps.Store.GetCategory(r.Context(), in.ID); err != nil {
			return err
		}
	}
	cat := &store.TaskCategory{
		ID:                 in.ID,
		Name:               strings.TrimSpace(in.Name),
		CallbackEnabled:    in.CallbackEnabled != nil && *in.CallbackEnabled,
		CallbackMode:       in.CallbackMode,
		CallbackMethod:     in.CallbackMethod,
		CallbackURL:        strings.TrimSpace(in.CallbackURL),
		CallbackHeaders:    strings.TrimSpace(in.CallbackHeaders),
		CallbackBody:       in.CallbackBody,
		CallbackTimeoutSec: in.CallbackTimeoutSec,
		SkipZero:           in.SkipZero != nil && *in.SkipZero,
		SortOrder:          in.SortOrder,
	}
	id, err := s.deps.Store.SaveCategory(r.Context(), cat)
	if err != nil {
		return err
	}
	respond(w, map[string]int64{"id": id})
	return nil
}

func (s *Server) handleCategoryDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := s.deps.Store.DeleteCategory(r.Context(), id); err != nil {
		return err
	}
	respond(w, map[string]bool{"ok": true})
	return nil
}

// handleCategoryTest 用示例 payload 实际发送一次回调，返回结果供前端展示。
func (s *Server) handleCategoryTest(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	cat, err := s.deps.Store.GetCategory(r.Context(), id)
	if err != nil {
		return err
	}
	vars := transfer.SampleCallbackVars(cat.Name, cat.CallbackMode == "round")
	res := s.deps.Transfer.TestCallback(r.Context(), cat, vars)
	respond(w, res)
	return nil
}

func (s *Server) handleCategoryLogs(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	logs, err := s.deps.Store.ListCallbackLogs(r.Context(), id, limit)
	if err != nil {
		return err
	}
	respond(w, logs)
	return nil
}
