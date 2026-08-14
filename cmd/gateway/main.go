package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"wda-farm-gateway/internal/gateway"
)

func main() {
	var (
		configPath  = flag.String("config", "", "devices.json 路径（默认当前目录，或 GATEWAY_CONFIG）")
		projectRoot = flag.String("project", "", "WhatsAppDeviceAgent 工程路径")
		derived     = flag.String("derived", "/tmp/WebDriverAgentFarmDerived", "xcodebuild derivedDataPath")
		listen      = flag.String("listen", "0.0.0.0:8300", "HTTP 监听地址")
		staticDir   = flag.String("static", "", "静态文件目录（默认生成临时 index.html）")
	)
	flag.Parse()

	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = os.Getenv("GATEWAY_CONFIG")
	}
	if cfgPath == "" {
		cfgPath = "devices.json"
	}
	cfg, err := gateway.LoadConfig(cfgPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	if *projectRoot == "" {
		*projectRoot = filepath.Join(filepath.Dir(cfgPath), "..", "whatsapp_ai_ios", "WhatsAppDeviceAgent")
	}
	static := *staticDir
	if static == "" {
		static = filepath.Join(filepath.Dir(cfgPath), "static")
	}
	// 静态目录缺 index.html 时生成默认占位页。
	if _, err := os.Stat(filepath.Join(static, "index.html")); err != nil {
		if err := gateway.WriteStatic(static); err != nil {
			slog.Error("write static", "error", err)
			os.Exit(1)
		}
	}

	wdaMgr := gateway.NewWDAManager(*projectRoot, *derived)
	llm := gateway.NewLLMClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model)
	resultsDir := filepath.Join(filepath.Dir(cfgPath), "data", "results")
	exec := gateway.NewExecutor(cfg, wdaMgr, llm, resultsDir)
	et := gateway.NewEasyTierManager(filepath.Dir(cfgPath), cfg)
	defer et.Stop() // 网关退出时停掉托管的 easytier-core，避免孤儿进程占用端口
	gw := gateway.New(cfg, wdaMgr, exec, llm, et)

	// 优雅停机：收到信号后先取消上下文——云会话会正常发送关闭帧再退出，
	// 平台侧立即释放会话，避免重启期间旧连接残留导致新连接被拒/告警。
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	// easytier 后备通道自愈：按最新配置恢复（若已配置）。
	if et.Configured() {
		et.Recover()
	}
	go gw.WatchdogLoop(ctx)
	go gw.CloudLoop(ctx)

	h, err := gw.Handler(static)
	if err != nil {
		slog.Error("init web handler (session store)", "error", err)
		os.Exit(1)
	}
	srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("gateway listening", "addr", *listen, "project", *projectRoot)
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			slog.Error("serve", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		slog.Info("shutdown signal received, closing cloud session and http server")
		shCtx, shCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shCancel()
		_ = srv.Shutdown(shCtx)
	}
}
