package quark

import "testing"

// 预览里「只转存当前文件夹」把文件夹写入链接（CASX 约定），解析时必须优先采用。
func TestExtractShareURL_FolderFid(t *testing.T) {
	const fid = "0123456789abcdef0123456789abcdef"
	cases := []struct {
		name string
		link string
		want string
	}{
		{"plain", "https://pan.quark.cn/s/8f2c41d9e6b7", "0"},
		{"list-fragment", "https://pan.quark.cn/s/8f2c41d9e6b7#/list/share/" + fid, fid},
		{"query-fid", "https://pan.quark.cn/s/8f2c41d9e6b7?fid=" + fid, fid},
		{"passcode+list", "https://pan.quark.cn/s/8f2c41d9e6b7?pwd=1234#/list/share/" + fid, fid},
		{"root-ignored", "https://pan.quark.cn/s/8f2c41d9e6b7?fid=0", "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pwd, _, got, err := (&Driver{}).ExtractShareURL(c.link)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pwd != "8f2c41d9e6b7" {
				t.Errorf("pwd_id = %q, want %q", pwd, "8f2c41d9e6b7")
			}
			if got != c.want {
				t.Errorf("pdir_fid = %q, want %q", got, c.want)
			}
		})
	}
}

func TestExtractShareURL_PasscodePreserved(t *testing.T) {
	_, code, _, err := (&Driver{}).ExtractShareURL("https://pan.quark.cn/s/8f2c41d9e6b7?pwd=ab12#/list/share/0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != "ab12" {
		t.Errorf("passcode = %q, want %q", code, "ab12")
	}
}
