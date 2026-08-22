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
		stateDir    = flag.String("state", "", "状态目录（gateway.db、data/、static 的锚点；默认当前目录，或 GATEWAY_STATE_DIR）")
		configPath  = flag.String("config", "", "兼容旧参数：devices.json 路径（取其目录作为状态目录，并触发一次性迁移）")
		projectRoot = flag.String("project", "", "WhatsAppDeviceAgent 工程路径")
		derived     = flag.String("derived", "", "xcodebuild derivedDataPath（默认 <state>/derived，兼容 /tmp/WebDriverAgentFarmDerived）")
		listen      = flag.String("listen", "0.0.0.0:8300", "HTTP 监听地址")
		staticDir   = flag.String("static", "", "静态文件目录（默认 <state>/static）")
	)
	flag.Parse()

	state := *stateDir
	if state == "" {
		state = os.Getenv("GATEWAY_STATE_DIR")
	}
	if state == "" && *configPath != "" {
		state = filepath.Dir(*configPath)
	}
	if state == "" {
		if legacy := os.Getenv("GATEWAY_CONFIG"); legacy != "" {
			state = filepath.Dir(legacy)
		}
	}
	if state == "" {
		state = "."
	}
	cfg, err := gateway.OpenConfig(state)
	if err != nil {
		slog.Error("open config db", "state", state, "error", err)
		os.Exit(1)
	}
	defer cfg.Close()
	if *projectRoot == "" {
		*projectRoot = filepath.Join(state, "..", "whatsapp_ai_ios", "WhatsAppDeviceAgent")
	}
	static := *staticDir
	if static == "" {
		static = filepath.Join(state, "static")
	}
	// 静态目录缺 index.html 时生成默认占位页。
	if _, err := os.Stat(filepath.Join(static, "index.html")); err != nil {
		if err := gateway.WriteStatic(static); err != nil {
			slog.Error("write static", "error", err)
			os.Exit(1)
		}
	}

	if *derived == "" {
		*derived = filepath.Join(state, "derived")
	}
	wdaMgr := gateway.NewWDAManager(*projectRoot, *derived, cfg.Signing.Team)
	wdaMgr.ConfigureSigning(cfg.Signing)
	llm := gateway.NewLLMClient(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model)
	resultsDir := filepath.Join(state, "data", "results")
	exec := gateway.NewExecutor(cfg, wdaMgr, llm, resultsDir)
	et := gateway.NewEasyTierManager(state, cfg)
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
	gw.SetAppContext(ctx)
	gw.EnsureCloudLoop() // 云通道已启用则启动；未启用时由「云通道设置」保存后热拉起

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
