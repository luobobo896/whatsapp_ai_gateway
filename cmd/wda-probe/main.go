// wda-probe 是 WDA 真机联调/校准探针：用与网关完全相同的 wda 包驱动真机，
// 输出会话/元素诊断并可选发送一条测试消息（用于新设备/新 WhatsApp 版本的选择器校准）。
//
// 用法:
//
//	go run ./cmd/wda-probe -wda http://<phone-ip>:8100 -dump /tmp/wa-tree.xml
//	go run ./cmd/wda-probe -wda http://<phone-ip>:8100 -phone 8617688540775 -text '【新品上市】...' -send
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"wda-farm-gateway/internal/wda"
)

func main() {
	wdaURL := flag.String("wda", "http://192.168.10.236:8100", "WDA 地址")
	phone := flag.String("phone", "", "目标手机号（空=当前/最近会话）")
	text := flag.String("text", "", "要发送的文本（需配合 -send）")
	send := flag.Bool("send", false, "执行发送")
	dump := flag.String("dump", "", "会话创建后把 accessibility 树 dump 到该文件")
	flag.Parse()

	ctx := context.Background()
	client := wda.NewClient(*wdaURL, 40*time.Second)

	st, err := client.Status(ctx)
	if err != nil {
		slog.Error("wda status", "err", err)
		os.Exit(1)
	}
	fmt.Printf("WDA OK: ready=%v\n", st.Ready)

	// 会话创建：自动识别普通/Business bundle（与网关 createWhatsAppSession 同逻辑）。
	var sid string
	created := ""
	for _, b := range []string{wda.WhatsAppBundleID, wda.WhatsAppBusinessBundleID} {
		s, err := client.CreateSession(ctx, b)
		if err == nil && s != "" {
			sid, created = s, b
			break
		}
		fmt.Printf("  bundle %s: %v\n", b, err)
	}
	if sid == "" {
		slog.Error("no whatsapp session")
		os.Exit(1)
	}
	defer func() { _ = client.DeleteSession(context.WithoutCancel(ctx), sid) }()
	fmt.Printf("session created: bundle=%s sid=%s\n", created, sid)

	if *dump != "" {
		src, err := client.Source(ctx, sid)
		if err != nil {
			slog.Error("dump source", "err", err)
		} else if werr := os.WriteFile(*dump, []byte(src), 0o644); werr != nil {
			slog.Error("write dump", "err", werr)
		} else {
			fmt.Printf("source dumped: %s (%d bytes)\n", *dump, len(src))
		}
	}

	if !*send {
		return
	}
	if *text == "" {
		slog.Error("-send 需要 -text")
		os.Exit(1)
	}
	start := time.Now()
	err = wda.SendMessageToPhone(ctx, client, *phone, *text)
	fmt.Printf("send done: phone=%q err=%v duration=%s\n", *phone, err, time.Since(start).Round(time.Millisecond))
	if err != nil {
		os.Exit(1)
	}
	fmt.Println("SEND OK")
}
