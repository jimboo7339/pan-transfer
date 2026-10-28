package guangya

import (
	"testing"

	"pan-transfer/internal/domain"
)

// 光鸭分享链接的文件夹 fid 解析：支持 #/list/share/<fid> 与 fragment query。
func TestExtractShareURL_FolderFid(t *testing.T) {
	cases := []struct {
		name string
		link string
		want string
	}{
		{"plain", "https://www.guangyapan.com/s/1958142256766681182_ak", "0"},
		{"list-fragment", "https://www.guangyapan.com/s/1958142256766681182_ak#/list/share/123456789", "123456789"},
		{"fragment-query", "https://www.guangyapan.com/s/1958142256766681182_ak#/list/share?parentId=987654", "987654"},
		{"query-fid", "https://www.guangyapan.com/s/1958142256766681182_ak?fid=555666", "555666"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pwd, _, got, err := (&Driver{}).ExtractShareURL(c.link)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pwd != "1958142256766681182_ak" {
				t.Errorf("share_id = %q, want %q", pwd, "1958142256766681182_ak")
			}
			if got != c.want {
				t.Errorf("pdir_fid = %q, want %q", got, c.want)
			}
		})
	}
}

// 业务码 127（操作过于频繁）必须归类为限流，否则不会触发退避重试。
func TestMapAPIErrorRateLimited(t *testing.T) {
	limited := []int{127, 429, 354}
	for _, code := range limited {
		err := mapAPIError(code, "操作过于频繁，请稍后重试")
		ae, ok := domain.AsAppError(err)
		if !ok || ae.Code != domain.CodeRateLimited {
			t.Errorf("mapAPIError(%d) = %v, want CodeRateLimited", code, err)
		}
	}
	err := mapAPIError(500, "boom")
	ae, ok := domain.AsAppError(err)
	if !ok || ae.Code != domain.CodeDriverError {
		t.Errorf("mapAPIError(500) = %v, want CodeDriverError", err)
	}
}
