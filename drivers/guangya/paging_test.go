package guangya

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"pan-transfer/internal/driver"
	"pan-transfer/internal/httpx"
)

// cursorPagingTest 用假服务端模拟光鸭的游标分页（total=176，每页 50），
// 验证：连续传 cursor 能翻页拉全 176 条；只认 page 会永远停在第一页。
func TestListShareItems_CursorPaging(t *testing.T) {
	const total = 176
	const pageSize = 50

	var mu sync.Mutex
	requests := []map[string]any{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()

		cursor := 0
		if v, ok := body["cursor"].(float64); ok {
			cursor = int(v)
		}
		start := cursor
		end := start + pageSize
		if end > total {
			end = total
		}
		list := make([]map[string]any, 0, end-start)
		for i := start; i < end; i++ {
			list = append(list, map[string]any{
				"fileId":   "fid-" + jsonNumber(i+1),
				"fileName": "file-" + jsonNumber(i+1) + ".mkv",
				"fileSize": 1024,
				"resType":  1,
			})
		}
		next := end
		out := map[string]any{
			"code": 0,
			"msg":  "success",
			"data": map[string]any{
				"total":   total,
				"list":    list,
				"cursor":  next,
				"hasMore": end < total,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer ts.Close()

	old := apiBaseURL
	apiBaseURL = ts.URL
	defer func() { apiBaseURL = old }()

	d := &Driver{}
	d.client = httpx.NewClient(httpx.ClientOptions{Timeout: 5 * time.Second})
	// intervalGate 为 nil 时 waitOperationDelay 直接跳过，不会阻塞

	// stoken 是内部 JSON：{share_id, access_token, code}
	stoken := `{"share_id":"sid","access_token":"tok","code":""}`
	items, err := d.ListShareItems(context.Background(), "sid", stoken, "parent")
	if err != nil {
		t.Fatalf("ListShareItems: %v", err)
	}
	if len(items) != total {
		t.Fatalf("items = %d, want %d（应通过 cursor 拉全）", len(items), total)
	}

	// 验证请求序列：第 1 页不传 cursor，之后依次传 50/100/150
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("requests = %d, want 4 页", len(requests))
	}
	if _, ok := requests[0]["cursor"]; ok {
		t.Error("首页不应带 cursor")
	}
	for i, want := range []int{50, 100, 150} {
		got, ok := requests[i+1]["cursor"].(float64)
		if !ok || int(got) != want {
			t.Errorf("第 %d 页 cursor = %v, want %d", i+2, requests[i+1]["cursor"], want)
		}
	}
	// 文件 id 全局唯一（无重复）
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.FID] {
			t.Errorf("重复 fid %s", it.FID)
		}
		seen[it.FID] = true
	}
	_ = driver.ShareEntry{}
}

// 模拟「服务端忽略 cursor、始终返回同一页」时，循环必须靠去重兜底停止，不能死循环。
func TestListShareItems_StopsOnRepeatedPage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list := []map[string]any{}
		for i := 0; i < 50; i++ {
			list = append(list, map[string]any{"fileId": "fid-" + jsonNumber(i+1), "fileName": "x.mkv", "resType": 1})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "success",
			"data": map[string]any{"total": 176, "list": list, "cursor": 50, "hasMore": true},
		})
	}))
	defer ts.Close()

	old := apiBaseURL
	apiBaseURL = ts.URL
	defer func() { apiBaseURL = old }()

	d := &Driver{}
	d.client = httpx.NewClient(httpx.ClientOptions{Timeout: 5 * time.Second})
	items, err := d.ListShareItems(context.Background(), "sid", `{"share_id":"sid","access_token":"tok","code":""}`, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 50 {
		t.Errorf("items = %d, want 50（重复页应停止）", len(items))
	}
}

func jsonNumber(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
