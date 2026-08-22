// wda-probe 是 WDA 真机联调/校准探针：用与网关完全相同的 wda 包驱动真机，
// 输出会话/元素诊断并可选发送一条测试消息（用于新设备/新 WhatsApp 版本的选择器校准）。
//
// 用法:
//
//	go run ./cmd/wda-probe -wda http://<phone-ip>:8100 -dump /tmp/wa-tree.xml
//	go run ./cmd/wda-probe -wda http://<phone-ip>:8100 -phone 8617688540775 -text '【新品上市】...' -send
//	go run ./cmd/wda-probe -wda http://127.0.0.1:18100 -phone 15213472085 -text 'hello' -send \
//	    -shot /tmp/wa-shot -report /tmp/wa-report.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"wda-farm-gateway/internal/wda"
)

func main() {
	wdaURL := flag.String("wda", "http://192.168.10.236:8100", "WDA 地址")
	phone := flag.String("phone", "", "目标手机号（空=当前/最近会话）")
	text := flag.String("text", "", "要发送的文本（需配合 -send）")
	send := flag.Bool("send", false, "执行发送")
	dump := flag.String("dump", "", "会话创建后把 accessibility 树 dump 到该文件")
	shotDir := flag.String("shot", "", "逐步截图目录（打开会话后、发送后各一张）")
	reportPath := flag.String("report", "", "把逐步结果写成 JSON（打开会话 → 输入发送 → 回传）")
	count := flag.Int("count", 1, "同一会话连发条数（>1 时文案追加 #n，用来验群发节奏）")
	interval := flag.Int("interval", 1, "连发条间隔秒（与执行器 IntervalSec 对齐，下限 1）")
	flag.Parse()

	ctx := context.Background()
	client := wda.NewClient(*wdaURL, 40*time.Second)

	st, err := client.Status(ctx)
	if err != nil {
		slog.Error("wda status", "err", err)
		os.Exit(1)
	}
	fmt.Printf("WDA OK: ready=%v os=%s ip=%s\n", st.Ready, st.OSVersion, st.IOSIP)

	if !*send {
		// 只探活/dump：建一次会话看当前页。
		sid, created, err := openWhatsAppSession(ctx, client)
		if err != nil {
			slog.Error("no whatsapp session", "err", err)
			os.Exit(1)
		}
		defer func() { _ = client.DeleteSession(context.WithoutCancel(ctx), sid) }()
		fmt.Printf("session created: bundle=%s sid=%s\n", created, sid)
		dumpSource(ctx, client, sid, *dump)
		return
	}
	if *text == "" {
		slog.Error("-send 需要 -text")
		os.Exit(1)
	}

	if *count < 1 {
		*count = 1
	}
	rep := probeReport{
		WDA:     *wdaURL,
		Phone:   *phone,
		Content: *text,
		Started: time.Now().Format(time.RFC3339),
	}
	start := time.Now()

	tSess := time.Now()
	sid, bid, err := wda.CreateWhatsAppSession(ctx, client)
	rep.SessionMs = time.Since(tSess).Milliseconds()
	if err != nil {
		rep.Status = "failed"
		rep.Error = "create_session: " + err.Error()
		writeReport(*reportPath, rep)
		fmt.Printf("create session failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = client.DeleteSession(context.WithoutCancel(ctx), sid) }()
	fmt.Printf("session: bundle=%s sid=%s duration=%s\n", bid, sid, time.Since(tSess).Round(time.Millisecond))

	failed := 0
	for i := 1; i <= *count; i++ {
		msg := *text
		if *count > 1 {
			msg = fmt.Sprintf("%s #%d", *text, i)
		}
		item := probeItem{N: i, Content: msg}
		tOpen := time.Now()
		isNew, oerr := wda.OpenChatOnSession(ctx, client, sid, bid, *phone, nil)
		item.OpenMs = time.Since(tOpen).Milliseconds()
		item.NewSession = isNew
		if oerr != nil {
			item.Status = "failed"
			item.Error = "open_chat: " + oerr.Error()
			failed++
			rep.Items = append(rep.Items, item)
			fmt.Printf("item %d open failed: %v\n", i, oerr)
			continue
		}
		if i == 1 {
			rep.Title = wda.ChatTitle(ctx, client, sid)
			rep.NewSession = isNew
			rep.OpenMs = item.OpenMs
			fmt.Printf("opened: title=%q new_session=%v duration=%s\n", rep.Title, isNew, time.Since(tOpen).Round(time.Millisecond))
			rep.ShotOpen = saveShot(ctx, client, sid, *shotDir, "02-opened.png")
			dumpSource(ctx, client, sid, *dump)
		} else {
			fmt.Printf("item %d opened: new_session=%v duration=%s\n", i, isNew, time.Since(tOpen).Round(time.Millisecond))
		}
		tSend := time.Now()
		serr := wda.TypeAndSend(ctx, client, sid, msg, nil)
		item.SendMs = time.Since(tSend).Milliseconds()
		item.TotalMs = time.Since(tOpen).Milliseconds()
		if serr != nil {
			item.Status = "failed"
			item.Error = "type_send: " + serr.Error()
			failed++
			rep.Items = append(rep.Items, item)
			fmt.Printf("item %d send failed: %v duration=%s\n", i, serr, time.Since(tOpen).Round(time.Millisecond))
			continue
		}
		item.Status = "sent"
		rep.Items = append(rep.Items, item)
		fmt.Printf("item %d SEND OK open=%dms send=%dms total=%dms\n", i, item.OpenMs, item.SendMs, item.TotalMs)
		if i == *count {
			rep.SendMs = item.SendMs
			rep.ShotSent = saveShot(ctx, client, sid, *shotDir, "03-sent.png")
		}
		if i < *count && *interval > 0 {
			time.Sleep(time.Duration(*interval) * time.Second)
		}
	}
	rep.TotalMs = time.Since(start).Milliseconds()
	if failed > 0 {
		rep.Status = "failed"
		rep.Error = fmt.Sprintf("%d/%d items failed", failed, *count)
	} else {
		rep.Status = "sent"
	}
	writeReport(*reportPath, rep)
	fmt.Printf("batch done: status=%s items=%d failed=%d duration=%s\n", rep.Status, *count, failed, time.Since(start).Round(time.Millisecond))
	if failed > 0 {
		os.Exit(1)
	}
	fmt.Println("SEND OK")
}

type probeItem struct {
	N          int    `json:"n"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	NewSession bool   `json:"new_session"`
	OpenMs     int64  `json:"open_ms"`
	SendMs     int64  `json:"send_ms"`
	TotalMs    int64  `json:"total_ms"`
}

type probeReport struct {
	WDA        string      `json:"wda"`
	Phone      string      `json:"phone"`
	Content    string      `json:"content"`
	Title      string      `json:"title,omitempty"`
	NewSession bool        `json:"new_session"`
	Status     string      `json:"status"`
	Error      string      `json:"error,omitempty"`
	Started    string      `json:"started"`
	SessionMs  int64       `json:"session_ms"`
	OpenMs     int64       `json:"open_ms"`
	SendMs     int64       `json:"send_ms"`
	TotalMs    int64       `json:"total_ms"`
	ShotOpen   string      `json:"shot_open,omitempty"`
	ShotSent   string      `json:"shot_sent,omitempty"`
	Items      []probeItem `json:"items,omitempty"`
}

func openWhatsAppSession(ctx context.Context, client *wda.Client) (sid, bundle string, err error) {
	for _, b := range []string{wda.WhatsAppBundleID, wda.WhatsAppBusinessBundleID} {
		s, cerr := client.CreateSession(ctx, b)
		if cerr == nil && s != "" {
			return s, b, nil
		}
		fmt.Printf("  bundle %s: %v\n", b, cerr)
	}
	return "", "", fmt.Errorf("no whatsapp session")
}

func dumpSource(ctx context.Context, client *wda.Client, sid, path string) {
	if path == "" {
		return
	}
	src, err := client.Source(ctx, sid)
	if err != nil {
		slog.Error("dump source", "err", err)
		return
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		slog.Error("write dump", "err", err)
		return
	}
	fmt.Printf("source dumped: %s (%d bytes)\n", path, len(src))
}

func saveShot(ctx context.Context, client *wda.Client, sid, dir, name string) string {
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Error("mkdir shot", "err", err)
		return ""
	}
	png, err := client.Screenshot(ctx, sid)
	if err != nil {
		slog.Error("screenshot", "name", name, "err", err)
		return ""
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, png, 0o644); err != nil {
		slog.Error("write shot", "path", path, "err", err)
		return ""
	}
	fmt.Printf("shot: %s (%d bytes)\n", path, len(png))
	return path
}

func writeReport(path string, r probeReport) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && !os.IsExist(err) {
		slog.Error("mkdir report", "err", err)
		return
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		slog.Error("marshal report", "err", err)
		return
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		slog.Error("write report", "err", err)
		return
	}
	fmt.Printf("report: %s status=%s\n", path, r.Status)
}
