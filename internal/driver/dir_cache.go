// 目录/分享列表的短 TTL 缓存。
//
// 背景：目标目录每次运行（每条链接）、重命名校验、子目录同步、每次预览都会
// 重新翻页拉取一遍完整目录列表；大目录下既慢又容易触发平台限流（如光鸭 127）。
// 这里提供运行期共享的缓存：同一轮运行内复用，紧邻的「预览→运行」也能复用；
// 我们自己的写操作（转存/重命名/删除）会失效对应目录，保证不会因为陈旧列表而重复转存。
package driver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"pan-transfer/internal/domain"
)

// DefaultDirCacheTTL 目录列表缓存有效期：足够覆盖「预览后立即运行」与一轮运行内的多次列举，
// 又足够短，外部改动最多滞后这么久被发现。
const DefaultDirCacheTTL = 30 * time.Second

type cachedDir struct {
	items []domain.FileItem
	at    time.Time
}

type cachedShare struct {
	items []ShareEntry
	at    time.Time
}

// DirCache 线程安全的短 TTL 列表缓存，键按账号隔离。
type DirCache struct {
	ttl    time.Duration
	mu     sync.Mutex
	dirs   map[string]cachedDir
	shares map[string]cachedShare
}

// NewDirCache 创建缓存；ttl<=0 时使用默认值。
func NewDirCache(ttl time.Duration) *DirCache {
	if ttl <= 0 {
		ttl = DefaultDirCacheTTL
	}
	return &DirCache{
		ttl:    ttl,
		dirs:   make(map[string]cachedDir),
		shares: make(map[string]cachedShare),
	}
}

func dirKey(accountID int64, fid string) string {
	return fmt.Sprintf("%d|%s", accountID, fid)
}

func shareKey(accountID int64, pwdID, pdirFID string) string {
	return fmt.Sprintf("%d|%s|%s", accountID, pwdID, pdirFID)
}

func (c *DirCache) fresh(at time.Time) bool {
	return time.Since(at) < c.ttl
}

// GetDir 取目录列表缓存。
func (c *DirCache) GetDir(accountID int64, fid string) ([]domain.FileItem, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.dirs[dirKey(accountID, fid)]
	if !ok || !c.fresh(e.at) {
		return nil, false
	}
	return e.items, true
}

// PutDir 写入目录列表缓存。
func (c *DirCache) PutDir(accountID int64, fid string, items []domain.FileItem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dirs[dirKey(accountID, fid)] = cachedDir{items: items, at: time.Now()}
}

// InvalidateDir 失效某目录（我们对其进行写操作后必须调用）。
func (c *DirCache) InvalidateDir(accountID int64, fid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.dirs, dirKey(accountID, fid))
}

// GetShare 取分享目录列表缓存。
func (c *DirCache) GetShare(accountID int64, pwdID, pdirFID string) ([]ShareEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.shares[shareKey(accountID, pwdID, pdirFID)]
	if !ok || !c.fresh(e.at) {
		return nil, false
	}
	return e.items, true
}

// PutShare 写入分享目录列表缓存。
func (c *DirCache) PutShare(accountID int64, pwdID, pdirFID string, items []ShareEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shares[shareKey(accountID, pwdID, pdirFID)] = cachedShare{items: items, at: time.Now()}
}

// Clear 清空全部缓存。
func (c *DirCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dirs = make(map[string]cachedDir)
	c.shares = make(map[string]cachedShare)
}

type dirCacheKey struct{}

// WithDirCache 把缓存挂到 ctx 上，供本轮运行的列举调用复用。
func WithDirCache(ctx context.Context, c *DirCache) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, dirCacheKey{}, c)
}

// DirCacheFrom 取出 ctx 上的缓存（可能为 nil）。
func DirCacheFrom(ctx context.Context) *DirCache {
	c, _ := ctx.Value(dirCacheKey{}).(*DirCache)
	return c
}

// ListFilesCached 列举目录：优先命中缓存；fromCache 表示是否复用。
func ListFilesCached(ctx context.Context, drv Driver, accountID int64, fid string) (items []domain.FileItem, fromCache bool, err error) {
	cache := DirCacheFrom(ctx)
	if cache != nil {
		if items, ok := cache.GetDir(accountID, fid); ok {
			return items, true, nil
		}
	}
	items, err = drv.ListFiles(ctx, fid)
	if err != nil {
		return nil, false, err
	}
	if cache != nil {
		cache.PutDir(accountID, fid, items)
	}
	return items, false, nil
}

// ListShareItemsCached 列举分享目录：优先命中缓存。
func ListShareItemsCached(ctx context.Context, st ShareTransferer, accountID int64, pwdID, stoken, pdirFID string) (items []ShareEntry, fromCache bool, err error) {
	cache := DirCacheFrom(ctx)
	if cache != nil {
		if items, ok := cache.GetShare(accountID, pwdID, pdirFID); ok {
			return items, true, nil
		}
	}
	items, err = st.ListShareItems(ctx, pwdID, stoken, pdirFID)
	if err != nil {
		return nil, false, err
	}
	if cache != nil {
		cache.PutShare(accountID, pwdID, pdirFID, items)
	}
	return items, false, nil
}

// InvalidateDirCache 让 ctx 上缓存中的指定目录失效（写操作后调用）。
func InvalidateDirCache(ctx context.Context, accountID int64, fid string) {
	if c := DirCacheFrom(ctx); c != nil {
		c.InvalidateDir(accountID, fid)
	}
}
