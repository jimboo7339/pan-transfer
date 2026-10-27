package transfer

import "testing"

func TestShouldNotify(t *testing.T) {
	cases := []struct {
		status      string
		transferred int
		want        bool
	}{
		{"success", 0, false}, // 空跑：无新剧集 / 全部已转存
		{"success", 3, true},  // 有新文件
		{"partial", 0, false}, // 0 文件的部分成功也不推送
		{"partial", 2, true},  // 有文件 + 部分链接失效
		{"failed", 0, true},   // 整次失败仍然推送（失效/凭据问题需告警）
		{"failed", 1, true},   // 失败+已转存部分文件
	}
	for _, c := range cases {
		if got := shouldNotify(c.status, c.transferred); got != c.want {
			t.Errorf("shouldNotify(%q, %d) = %v, want %v", c.status, c.transferred, got, c.want)
		}
	}
}
