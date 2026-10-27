package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// 旧的 task 级 share_fid 应迁移进链接：单链接写入 #/list/share/<fid>，
// 多链接无法确定归属则只清空、不改链接。
func TestMigrateLegacyShareFID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	const fid = "0123456789abcdef0123456789abcdef"
	now := nowStr()

	singleLink := "https://pan.quark.cn/s/single"
	res, err := s.db.Exec(`INSERT INTO tasks (task_name, account_id, share_url, share_links, save_path, share_fid, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		"单链接", 1, singleLink, `["`+singleLink+`"]`, "/tv", fid, now, now)
	if err != nil {
		t.Fatalf("insert single: %v", err)
	}
	singleID, _ := res.LastInsertId()

	multiLinks := `["https://pan.quark.cn/s/a","https://pan.quark.cn/s/b"]`
	res, err = s.db.Exec(`INSERT INTO tasks (task_name, account_id, share_url, share_links, save_path, share_fid, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		"多链接", 1, "https://pan.quark.cn/s/a", multiLinks, "/tv", fid, now, now)
	if err != nil {
		t.Fatalf("insert multi: %v", err)
	}
	multiID, _ := res.LastInsertId()

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 重新打开触发迁移
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	var linksJSON, shareURL, shareFID string
	if err := s2.db.QueryRow(`SELECT share_links, share_url, share_fid FROM tasks WHERE id=?`, singleID).
		Scan(&linksJSON, &shareURL, &shareFID); err != nil {
		t.Fatalf("query single: %v", err)
	}
	wantLink := singleLink + "#/list/share/" + fid
	var links []string
	if err := json.Unmarshal([]byte(linksJSON), &links); err != nil {
		t.Fatalf("unmarshal links: %v", err)
	}
	if len(links) != 1 || links[0] != wantLink {
		t.Errorf("single link = %v, want [%s]", links, wantLink)
	}
	if shareURL != wantLink {
		t.Errorf("share_url = %q, want %q", shareURL, wantLink)
	}
	if shareFID != "" {
		t.Errorf("share_fid = %q, want empty", shareFID)
	}

	var multiJSON, multiFID string
	if err := s2.db.QueryRow(`SELECT share_links, share_fid FROM tasks WHERE id=?`, multiID).
		Scan(&multiJSON, &multiFID); err != nil {
		t.Fatalf("query multi: %v", err)
	}
	if multiJSON != multiLinks {
		t.Errorf("multi links changed to %q, want %q", multiJSON, multiLinks)
	}
	if multiFID != "" {
		t.Errorf("multi share_fid = %q, want empty", multiFID)
	}
}

func TestWithShareFIDInURL(t *testing.T) {
	const fid = "abc123def456"
	cases := []struct{ in, want string }{
		{"https://pan.quark.cn/s/x", "https://pan.quark.cn/s/x#/list/share/" + fid},
		{"https://pan.quark.cn/s/x?pwd=1234", "https://pan.quark.cn/s/x?pwd=1234#/list/share/" + fid},
		{"https://pan.quark.cn/s/x?fid=old", "https://pan.quark.cn/s/x#/list/share/" + fid},
		{"https://pan.quark.cn/s/x#/list/share/old", "https://pan.quark.cn/s/x#/list/share/" + fid},
	}
	for _, c := range cases {
		if got := withShareFIDInURL(c.in, fid); got != c.want {
			t.Errorf("withShareFIDInURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := withShareFIDInURL("https://pan.quark.cn/s/x#/list/share/old", ""); got != "https://pan.quark.cn/s/x" {
		t.Errorf("clear fid = %q, want %q", got, "https://pan.quark.cn/s/x")
	}
}
