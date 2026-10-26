// PanTransfer：轻量网盘转存工具（夸克 / 光鸭）。
// 单二进制：SQLite 存储 + 内嵌 Vue3 前端 + 追剧定时调度 + 多渠道通知。
package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"pan-transfer/internal/accounts"
	"pan-transfer/internal/httpapi"
	"pan-transfer/internal/notify"
	"pan-transfer/internal/store"
	"pan-transfer/internal/transfer"
	web "pan-transfer/web"

	// 驱动注册（init 中 self-register）
	_ "pan-transfer/drivers/guangya"
	_ "pan-transfer/drivers/quark"
)

var version = "dev"

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
	dbPath := filepath.Join(*dataDir, "pantransfer.db")
	st, err := store.Open(dbPath)
	if err != nil {
		log.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	accMgr := accounts.New(st)
	notifier := notify.New(st, log)
	svc := transfer.New(transfer.Options{
		Exec:   accMgr,
		Store:  st,
		Log:    log,
		Notify: notifier,
	})
	svc.Start()
	defer svc.Close()

	srv := httpapi.NewServer(httpapi.Deps{
		Store:    st,
		Accounts: accMgr,
		Transfer: svc,
		Notify:   notifier,
		Log:      log,
		Version:  version,
	})

	root := http.Handler(srv.Handler())
	// 静态资源：web/dist 内嵌；SPA 路由回退 index.html
	static, err := fs.Sub(web.DistFS, "dist")
	if err == nil {
		fileServer := http.FileServer(http.FS(static))
		root = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := strings.TrimPrefix(r.URL.Path, "/")
			if p == "" {
				p = "index.html"
			}
			if _, err := fs.Stat(static, p); err != nil && !strings.HasPrefix(r.URL.Path, "/api/") {
				r.URL.Path = "/"
			}
			fileServer.ServeHTTP(w, r)
		})
		// /api 仍走 API 路由
		mux := http.NewServeMux()
		mux.Handle("/api/", srv.Handler())
		mux.Handle("/", root)
		root = mux
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           root,
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
