// PanTransfer：轻量网盘转存工具（夸克 / 光鸭）。
// 单二进制：SQLite 存储 + 内嵌 Vue3 前端 + 追剧定时调度 + 多渠道通知。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"pan-transfer/internal/accounts"
	"pan-transfer/internal/httpapi"
	"pan-transfer/internal/notify"
	"pan-transfer/internal/secret"
	"pan-transfer/internal/store"
	"pan-transfer/internal/transfer"
	web "pan-transfer/web"

	// 驱动注册（init 中 self-register）
	_ "pan-transfer/drivers/guangya"
	_ "pan-transfer/drivers/quark"
)

var version = "dev"

// runtime 持有可替换的数据库与依赖它的组件。
//
// 还原备份时必须整体替换（换文件 → 关库 → 开库 → 重建 Manager），
// 用一个互斥锁包住，避免出现"半个组件指向旧库"的中间态。
//
// stPtr 是 *runtime 上的稳定字段（不是临时变量），把它取地址交给 httpapi.Deps，
// 于是 API 层每次请求读到的都是还原后的最新句柄，不需要重启进程。
type runtime struct {
	mu        sync.Mutex
	st        *store.Store
	stSlot    **store.Store // 指向 st 字段自身，供 Deps.Store 持有
	mgr       *accounts.Manager
	svc       *transfer.Service
	data      string
	restoring atomic.Bool // 还原换库窗口，API 层据此拒绝请求（避免拿到已关闭的库）
}

// stPtr 返回指向 st 字段的指针。
func (rt *runtime) stPtr() **store.Store { return rt.stSlot }

// setStore 在持锁状态下替换当前存储句柄（同时更新 st 与暴露给 API 的槽位）。
func (rt *runtime) setStore(st *store.Store) {
	rt.st = st
	*rt.stSlot = st
}

// current 返回当前存储（还原期间为一致快照）。
func (rt *runtime) current() (*store.Store, *accounts.Manager, *transfer.Service) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.st, rt.mgr, rt.svc
}

// attachService 记录 Service（装配顺序为「先 API server 后 Service」，
// 因此构造 runtime 时还没有 svc；还原时需要它能停调度）。
func (rt *runtime) attachService(svc *transfer.Service) {
	rt.mu.Lock()
	rt.svc = svc
	rt.mu.Unlock()
}

// restoreFrom 用备份替换数据库并重建全部依赖。
//
// 顺序很关键：
//  1. 先停调度（不再接新任务），并等在途任务跑完——它们持有旧库句柄，
//     换库后继续写会打到已关闭的库上，甚至把还原前的半截数据写进新库；
//  2. 关闭旧连接（SQLite 不允许在打开时替换底层文件）；
//  3. 复制快照 + 删除旧 WAL（旧事务重放到新库会造成数据错乱）；
//  4. 重新打开并重建 Manager（丢弃全部驱动实例——它们持有还原前的凭据）；
//  5. 重启调度（关键！早先漏了这一步，Close 之后 appCtx 已取消，
//     导致「还原一次之后定时任务再也不触发」，且后续手动运行也会立刻失败）。
func (rt *runtime) restoreFrom(backupPath string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	// 换库窗口内旧句柄会被 Close，所有 API handler 此时若执行会拿到坏句柄
	// （500），鉴权读库失败还会 fail-open。提前置标志让 wrap 统一返回 503；
	// defer 保证成功/失败路径都恢复服务。
	rt.restoring.Store(true)
	defer rt.restoring.Store(false)

	dbPath := rt.dbPath()

	svc := rt.svc

	// 1. 停调度，等在途任务结束。
	//    这里不能用 Close()：Close 会 cancel appCtx，
	//    在途任务会立刻失败并留下"运行中断"的记录。
	if svc != nil {
		svc.StopScheduling()
		if !svc.WaitRunning(2 * time.Minute) {
			return fmt.Errorf("有任务仍在运行，请等待其完成后再还原")
		}
	}

	// 2. 关库。
	if rt.st != nil {
		_ = rt.st.Close()
	}

	// 3. 替换文件。PrepareRestore 会做 quick_check 校验。
	plan, err := (&store.Store{}).PrepareRestore(backupPath, dbPath)
	if err != nil {
		rt.reopenLocked(dbPath)
		rt.resumeScheduling()
		return err
	}
	if err := plan.ApplyRestore(); err != nil {
		rt.reopenLocked(dbPath)
		rt.resumeScheduling()
		return err
	}

	// 4. 重开并重建。
	rt.reopenLocked(dbPath)
	if rt.st == nil {
		rt.resumeScheduling()
		return fmt.Errorf("还原后无法重新打开数据库")
	}

	// 5. 重启调度。
	rt.resumeScheduling()
	return nil
}

// dbPath 返回当前数据库的完整路径。
//
// 早先这个路径在三处硬编码（main、restoreFrom、Deps.DBPath），
// 改一次文件名就会让还原静默失效（打开一个新库）。
func (rt *runtime) dbPath() string {
	return filepath.Join(rt.data, "pantransfer.db")
}

// resumeScheduling 重新拉起调度循环。
func (rt *runtime) resumeScheduling() {
	if rt.svc != nil {
		rt.svc.RestartScheduling()
	}
}

// reopenLocked 关闭旧句柄并重新打开 + 重建依赖。调用方必须已持锁。
// 失败时把 rt.st 置为 nil 并返回错误，由调用方决定如何上报。
//
// 注意：不重建 Manager / Service，而是调用它们的 Reload。这样 Deps 里保存的
// 指针始终有效，调度 goroutine 也不用重启（Service 内部只换 store 引用并清空
// 进程内标记）。
func (rt *runtime) reopenLocked(dbPath string) error {
	st, err := store.Open(dbPath)
	if err != nil {
		rt.setStore(nil)
		return fmt.Errorf("重开数据库失败: %w", err)
	}
	st.SetCipher(secret.Default())
	if rt.mgr != nil {
		rt.mgr.Reload(st)
	}
	if rt.svc != nil {
		rt.svc.Reload(st)
	}
	rt.setStore(st)
	return nil
}

func main() {
	addr := flag.String("addr", ":8935", "HTTP 监听地址")
	dataDir := flag.String("data", "./data", "数据目录（SQLite 所在）")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Error("创建数据目录失败", "err", err)
		os.Exit(1)
	}

	// 凭据加密必须在打开数据库之前完成：store 打开时即用该 cipher 解密账号。
	// 主密钥优先取 PAN_TRANSFER_MASTER_KEY（容器场景），否则落在 data/master.key。
	keySource, err := secret.Init(*dataDir)
	if err != nil {
		log.Error("初始化凭据加密失败", "err", err)
		log.Warn("可设置 PAN_TRANSFER_ALLOW_PLAINTEXT=1 以明文模式启动（不推荐，仅用于排障）")
		os.Exit(1)
	}
	plainMode := strings.EqualFold(strings.TrimSpace(os.Getenv("PAN_TRANSFER_ALLOW_PLAINTEXT")), "1")
	if plainMode {
		log.Warn("凭据加密已禁用（PAN_TRANSFER_ALLOW_PLAINTEXT=1）：账号 cookie/token 将以明文落库")
	} else {
		log.Info("凭据加密已启用", "key_source", keySource, "algo", "AES-256-GCM")
	}

	dbPath := filepath.Join(*dataDir, "pantransfer.db")
	st, err := store.Open(dbPath)
	if err != nil {
		log.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	if plainMode {
		st.SetCipher(nil)
	}

	// 启动即收尾：进程崩溃留下的 running 记录永远转圈，用户无法判断是否跑完。
	if n, err := st.MarkInterruptedRuns(context.Background(), "服务重启，运行中断"); err != nil {
		log.Warn("收尾中断的运行记录失败", "err", err)
	} else if n > 0 {
		log.Warn("已把上次运行中断的记录标记为失败", "count", n)
	}

	accMgr := accounts.New(st)
	notifier := notify.New(st, log)

	rt := &runtime{st: st, mgr: accMgr, data: *dataDir}
	rt.stSlot = &rt.st

	// 装配顺序有依赖：Service 需要 Metrics 才能统计「转存了多少文件」「调度扫了几次」，
	// 而 metrics 实例住在 httpapi（指标是运维关注点）。所以先建 API server 拿到
	// 桥接器，再建 Service，最后回填 Transfer 依赖。
	srv := httpapi.NewServer(httpapi.Deps{
		Store:             rt.stSlot,
		Accounts:          accMgr,
		Notify:            notifier,
		Log:               log,
		Version:           version,
		AllowedOrigins:    os.Getenv("PAN_TRANSFER_ALLOWED_ORIGINS"),
		ForceSecureCookie: os.Getenv("PAN_TRANSFER_SECURE_COOKIE"),
		DataDir:           *dataDir,
		DBPath:            dbPath,
		Restore:           rt.restoreFrom,
		Restoring:         &rt.restoring,
	})

	svc := transfer.New(transfer.Options{
		Exec:    accMgr,
		Store:   st,
		Log:     log,
		Notify:  notifier,
		Metrics: srv.MetricsSink(),
	})
	svc.Start()
	rt.attachService(svc)
	srv.SetTransfer(svc)

	// 每日自动备份：data/pantransfer.db 是唯一有状态资产，容器里没有 sqlite3，
	// 让用户"手动备份"是不现实的。保留最近 7 份。
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		// 启动后延迟 5 分钟先做一次，避免和首屏请求抢 IO。
		time.Sleep(5 * time.Minute)
		for {
			cur, _, _ := rt.current()
			if cur != nil {
				name := store.BackupFileName(time.Now())
				path := filepath.Join(store.BackupDir(rt.data), name)
				if _, err := cur.BackupTo(context.Background(), path); err != nil {
					log.Warn("自动备份失败", "err", err)
				} else if removed, err := store.PruneBackups(store.BackupDir(rt.data), store.DefaultBackupKeep); err != nil {
					log.Warn("清理旧备份失败", "err", err)
				} else {
					log.Info("自动备份完成", "name", name, "pruned", removed)
				}
			}
			<-ticker.C
		}
	}()

	root := http.Handler(srv.Handler())
	// 静态资源：web/dist 内嵌；SPA 路由回退 index.html
	static, err := fs.Sub(web.DistFS, "dist")
	if err == nil {
		// 带 hash 的 Vite 产物可以长缓存；index.html 必须每次校验，
		// 否则升级后用户会一直拿到旧页面（进而请求已经不存在的旧 chunk）。
		fileServer := http.FileServer(http.FS(static))
		root = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := strings.TrimPrefix(r.URL.Path, "/")
			if p == "" {
				p = "index.html"
			}
			// /api/ 与 /metrics 是后端路由，不能回退到 index.html
			// （否则 Prometheus 抓 /metrics 会拿到 200 + HTML 文本，看起来"成功"但全是垃圾）。
			if _, err := fs.Stat(static, p); err != nil &&
				!strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/metrics" {
				r.URL.Path = "/"
			}
			switch {
			case strings.HasPrefix(r.URL.Path, "/assets/"):
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			case strings.HasSuffix(r.URL.Path, ".html"):
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
		})
		// /api 与 /metrics 走 API 路由（含鉴权与指标中间件）
		mux := http.NewServeMux()
		mux.Handle("/api/", srv.Handler())
		mux.Handle("/metrics", srv.Handler())
		mux.Handle("/", root)
		root = mux
	}

	httpSrv := &http.Server{
		Addr:    *addr,
		Handler: root,
		// ReadHeaderTimeout 已有；补上 ReadTimeout/IdleTimeout/MaxHeaderBytes，
		// 避免慢速连接长期占用 fd。
		// WriteTimeout 故意留 0：异步任务改造前 POST /tasks/{id}/run 需要长时间写出。
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Info("PanTransfer 已启动", "addr", *addr, "version", version, "data", dbPath)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP 服务异常退出", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Info("正在关闭…")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
