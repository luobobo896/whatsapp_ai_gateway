package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	gw := gateway.New(cfg, wdaMgr, exec, llm, et)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// easytier 后备通道自愈：按最新配置恢复（若已配置）。
	if et.Configured() {
		et.Recover()
	}
	go gw.WatchdogLoop(ctx)
	go gw.CloudLoop(ctx)

	srv := &http.Server{Addr: *listen, Handler: gw.Handler(static), ReadHeaderTimeout: 10 * time.Second}
	slog.Info("gateway listening", "addr", *listen, "project", *projectRoot)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}
