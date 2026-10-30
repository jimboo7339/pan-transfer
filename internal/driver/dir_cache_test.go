package driver

import (
	"context"
	"testing"
	"time"

	"pan-transfer/internal/domain"
)

// fakeLister 只实现 ListFiles，其余接口方法由内嵌接口占位（不会被调用）。
type fakeLister struct {
	Driver
	calls int
	items []domain.FileItem
}

func (f *fakeLister) ListFiles(ctx context.Context, parentID string) ([]domain.FileItem, error) {
	f.calls++
	return f.items, nil
}

// fakeShareLister 只实现 ListShareItems。
type fakeShareLister struct {
	ShareTransferer
	calls int
	items []ShareEntry
}

func (f *fakeShareLister) ListShareItems(ctx context.Context, pwdID, stoken, pdirFID string) ([]ShareEntry, error) {
	f.calls++
	return f.items, nil
}

func TestDirCacheReuseAndInvalidate(t *testing.T) {
	cache := NewDirCache(time.Minute)
	drv := &fakeLister{items: []domain.FileItem{{ID: "1", Name: "a.mkv"}}}
	ctx := WithDirCache(context.Background(), cache)

	items, cached, err := ListFilesCached(ctx, drv, 1, "dest")
	if err != nil || cached || len(items) != 1 {
		t.Fatalf("first call: items=%d cached=%v err=%v", len(items), cached, err)
	}
	if drv.calls != 1 {
		t.Fatalf("calls = %d, want 1", drv.calls)
	}

	// 第二次命中缓存，不再调用驱动
	items, cached, err = ListFilesCached(ctx, drv, 1, "dest")
	if err != nil || !cached || len(items) != 1 {
		t.Fatalf("second call: items=%d cached=%v err=%v", len(items), cached, err)
	}
	if drv.calls != 1 {
		t.Errorf("calls = %d, want 1 (应命中缓存)", drv.calls)
	}

	// 其他账号不共享缓存
	if _, cached, _ = ListFilesCached(ctx, drv, 2, "dest"); cached {
		t.Error("不同账号不应命中同一缓存")
	}

	// 写操作后失效 → 重新拉取
	InvalidateDirCache(ctx, 1, "dest")
	if _, cached, _ = ListFilesCached(ctx, drv, 1, "dest"); cached {
		t.Error("失效后不应命中缓存")
	}
	if drv.calls != 3 {
		t.Errorf("calls = %d, want 3", drv.calls)
	}
}

func TestDirCacheExpiry(t *testing.T) {
	cache := NewDirCache(20 * time.Millisecond)
	drv := &fakeLister{}
	ctx := WithDirCache(context.Background(), cache)

	if _, _, err := ListFilesCached(ctx, drv, 1, "dest"); err != nil {
		t.Fatal(err)
	}
	if _, cached, _ := ListFilesCached(ctx, drv, 1, "dest"); !cached {
		t.Fatal("应立即命中缓存")
	}
	time.Sleep(30 * time.Millisecond)
	if _, cached, _ := ListFilesCached(ctx, drv, 1, "dest"); cached {
		t.Error("TTL 过期后不应命中缓存")
	}
}

func TestShareListCache(t *testing.T) {
	cache := NewDirCache(time.Minute)
	st := &fakeShareLister{items: []ShareEntry{{FID: "f1", FileName: "x.mkv"}}}
	ctx := WithDirCache(context.Background(), cache)

	if _, cached, _ := ListShareItemsCached(ctx, st, 1, "pwd", "stoken", "dir"); cached {
		t.Fatal("首次不应命中")
	}
	if _, cached, _ := ListShareItemsCached(ctx, st, 1, "pwd", "stoken", "dir"); !cached {
		t.Error("第二次应命中缓存")
	}
	if st.calls != 1 {
		t.Errorf("calls = %d, want 1", st.calls)
	}
	// 不同目录互不影响
	if _, cached, _ := ListShareItemsCached(ctx, st, 1, "pwd", "stoken", "other"); cached {
		t.Error("不同目录不应命中")
	}
}

// 未挂缓存时行为退化为直连（不影响老路径）。
func TestNoCacheFallsThrough(t *testing.T) {
	drv := &fakeLister{}
	if _, cached, err := ListFilesCached(context.Background(), drv, 1, "dest"); err != nil || cached {
		t.Fatalf("cached=%v err=%v", cached, err)
	}
	if drv.calls != 1 {
		t.Errorf("calls = %d, want 1", drv.calls)
	}
}
