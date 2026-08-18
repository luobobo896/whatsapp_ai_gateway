// e2e-sim 是针对云平台的端到端测试工具：模拟网关 WSS 客户端 + 平台 REST 调用。
// 仅用于测试环境（默认 hk.hsddns.com），协议与 internal/gateway/cloud.go 保持一致。
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var (
	base  = flag.String("base", "https://hk.hsddns.com", "平台基础 URL")
	wsURL = flag.String("ws", "wss://hk.hsddns.com/api/ios-agent/v1/gateway/ws", "网关 WSS 地址")
	email = flag.String("email", "e2e-test@local", "平台账号")
	pass  = flag.String("pass", "", "平台密码")
	name  = flag.String("name", "e2e-sim-gw", "网关名")
	udid  = flag.String("udid", "e2e0000000000000000000000000000000000000", "模拟设备 UDID(40位)")

	duration      = flag.Duration("duration", 60*time.Second, "wss 模式存活时长")
	heartbeat     = flag.Duration("heartbeat", 20*time.Second, "心跳间隔")
	noHeartbeat   = flag.Bool("no-heartbeat", false, "不发心跳（测 reaper 40s 踢线）")
	dispatchMode  = flag.String("dispatch-mode", "auto", "dispatch 响应: auto|slow|fail|ignore")
	exitOnSummary = flag.Bool("exit-on-summary", false, "收到任务完成即退出")
	dupConn       = flag.Bool("dup", false, "同时开第二条同名连接（测 4002 踢线）")

	method = flag.String("method", "GET", "api 模式 HTTP 方法")
	path   = flag.String("path", "", "api 模式路径")
	body   = flag.String("body", "", "api 模式 JSON 请求体")
)

func logf(format string, a ...any) {
	fmt.Printf("%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, a...))
}

func main() {
	flag.Parse()
	if *pass == "" {
		fmt.Fprintln(os.Stderr, "需要 -pass")
		os.Exit(2)
	}
	switch flag.Arg(0) {
	case "login":
		cmdLogin()
	case "register":
		cmdRegister()
	case "api":
		cmdAPI()
	case "wss":
		cmdWSS()
	case "resend":
		cmdResend()
	default:
		fmt.Fprintln(os.Stderr, "用法: e2e-sim [login|register|api|wss|resend] -pass ...")
		os.Exit(2)
	}
}

// ---- REST 基础 ----

type client struct {
	http *http.Client
	csrf string
}

func newClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{http: &http.Client{Timeout: 30 * time.Second, Jar: jar}}
}

func (c *client) req(method, path string, body any, withCSRF bool) (int, string) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, *base+path, rd)
	if err != nil {
		return -1, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if withCSRF && c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	t0 := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	logf("HTTP %s %s -> %d (%.0fms) %s", method, path, resp.StatusCode, float64(time.Since(t0).Microseconds())/1000, trunc(string(b), 300))
	return resp.StatusCode, string(b)
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// login 建立会话并取 CSRF（/api/auth/me）。
func (c *client) login() bool {
	code, _ := c.req("POST", "/api/auth/login", map[string]string{"email": *email, "password": *pass}, false)
	if code != 200 && code != 204 {
		return false
	}
	code, body := c.req("GET", "/api/auth/me", nil, false)
	if code != 200 {
		return false
	}
	var me struct {
		CSRFToken string `json:"csrfToken"`
	}
	json.Unmarshal([]byte(body), &me)
	c.csrf = me.CSRFToken
	return c.csrf != ""
}

func cmdLogin() {
	c := newClient()
	logf("== 登录流程测试 ==")
	// 错误密码（验证 401 文案；只试 1 次避免触发限流）
	bad := newClient()
	bad.req("POST", "/api/auth/login", map[string]string{"email": *email, "password": "wrong-" + hexRand(4)}, false)
	// 正确登录
	if !c.login() {
		logf("登录失败")
		os.Exit(1)
	}
	logf("登录成功, CSRF 已获取")
	c.req("GET", "/api/auth/me", nil, false)
	c.req("POST", "/api/auth/logout", map[string]string{}, true)
	c.req("GET", "/api/auth/me", nil, false) // 应 401
}

func cmdRegister() {
	c := newClient()
	logf("== 网关凭证注册测试 (POST /api/ios-agent/v1/gateway/register) ==")
	payload := map[string]string{"email": *email, "password": *pass, "name": *name}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", *base+"/api/ios-agent/v1/gateway/register", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	t0 := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		logf("注册失败: %v", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	logf("注册 -> %d (%.0fms) %s", resp.StatusCode, float64(time.Since(t0).Microseconds())/1000, trunc(string(rb), 500))
	if resp.StatusCode == 200 || resp.StatusCode == 201 {
		var out struct {
			Token    string `json:"token"`
			TenantID string `json:"tenantId"`
		}
		json.Unmarshal(rb, &out)
		if out.Token != "" {
			fmt.Println(out.Token) // stdout 最后一行 = token，供脚本取用
		}
	}
}

func cmdAPI() {
	c := newClient()
	if !c.login() {
		logf("登录失败")
		os.Exit(1)
	}
	var payload any
	if *body != "" {
		payload = json.RawMessage(*body)
	}
	code, out := c.req(*method, *path, payload, *method != "GET")
	fmt.Printf("STATUS=%d\n%s\n", code, out)
}

// ---- WSS 网关模拟 ----

type envelope struct {
	V       int    `json:"v"`
	Type    string `json:"type"`
	MsgID   string `json:"msgId"`
	SentAt  string `json:"sentAt"`
	Payload any    `json:"payload"`
}

func hexRand(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// cmdResend 对已终态 item 重复上报 item:result（幂等性验证）。
// -task / -item / -phone 指定，重复 3 次。
var (
	resendTask = flag.String("task", "", "resend: task_id")
	resendItem = flag.String("item", "", "resend: item_id")
	resendPhne = flag.String("phone", "", "resend: phone")
)

func cmdResend() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, *wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + *pass}}})
	if err != nil {
		logf("拨号失败: %v", err)
		os.Exit(1)
	}
	defer conn.CloseNow()
	env := func(typ string, payload any) {
		b, _ := json.Marshal(envelope{V: 1, Type: typ, MsgID: "e2e:" + hexRand(4),
			SentAt: time.Now().UTC().Format(time.RFC3339), Payload: payload})
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		conn.Write(wctx, websocket.MessageText, b)
		wcancel()
	}
	env("gateway:hello", map[string]any{"name": *name, "version": "e2e-sim-1.0"})
	for i := 0; i < 3; i++ {
		time.Sleep(500 * time.Millisecond)
		env("item:result", map[string]any{"task_id": *resendTask, "item_id": *resendItem,
			"phone": *resendPhne, "status": "sent", "error": "", "duration_ms": 900,
			"serial": "E2ESIM", "device_name": "E2E-Sim-iPhone"})
		logf("重复上报 #%d item=%s", i+1, *resendItem)
	}
	time.Sleep(2 * time.Second)
	conn.Close(websocket.StatusNormalClosure, "done")
	logf("resend 完成")
}

func cmdWSS() {
	var wg sync.WaitGroup
	runConn(&wg)
	if *dupConn {
		time.Sleep(2 * time.Second)
		logf("-- 开启第二条同名连接（应导致第一条被 4002 踢）--")
		go runConn(&wg)
	}
	wg.Wait()
}

func runConn(wg *sync.WaitGroup) {
	wg.Add(1)
	defer wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), *duration+30*time.Second)
	defer cancel()

	t0 := time.Now()
	hdr := http.Header{"Authorization": {"Bearer " + *pass}}
	conn, _, err := websocket.Dial(ctx, *wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		logf("[%s] 拨号失败: %v", *name, err)
		return
	}
	defer conn.CloseNow()
	logf("[%s] WSS 已连接 (%.0fms)", *name, float64(time.Since(t0).Microseconds())/1000)

	var wmu sync.Mutex
	send := func(typ string, payload any) bool {
		env := envelope{V: 1, Type: typ, MsgID: "e2e:" + hexRand(4), SentAt: time.Now().UTC().Format(time.RFC3339), Payload: payload}
		wmu.Lock()
		defer wmu.Unlock()
		cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
		defer ccancel()
		if err := conn.Write(cctx, websocket.MessageText, mustJSON(env)); err != nil {
			logf("[%s] 发送 %s 失败: %v", *name, typ, err)
			return false
		}
		return true
	}

	send("gateway:hello", map[string]any{"name": *name, "version": "e2e-sim-1.0"})
	send("device_list", map[string]any{"devices": []any{map[string]any{
		"udid": *udid, "name": "E2E-Sim-iPhone", "model": "iPhone14,2",
		"ios_version": "18.0", "wda_ip": "10.0.0.42", "wda_port": 8100,
		"wda_status": "online", "whatsapp_version": "", "conn_type": "usb",
	}}})

	if !*noHeartbeat {
		go func() {
			t := time.NewTicker(*heartbeat)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if !send("gateway:heartbeat", map[string]any{}) {
						return
					}
				}
			}
		}()
	}

	// dispatch 状态跟踪（slow 模式用）
	var mu sync.Mutex
	pending := map[string][]map[string]any{} // task_id -> 未回复 items
	done := make(chan struct{})              // 首个任务收口信号

	readCtx, readCancel := context.WithCancel(ctx)
	defer readCancel()
	for {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			status := websocket.CloseStatus(err)
			logf("[%s] 连接结束: %v (close=%d, 会话时长 %.1fs)", *name, err, int(status), time.Since(t0).Seconds())
			return
		}
		var env struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(data, &env) != nil {
			logf("[%s] 非法帧: %.200s", *name, data)
			continue
		}
		switch env.Type {
		case "server:ack":
			// hello ack 携带租户身份
			var p struct {
				TenantID   string `json:"tenantId"`
				TenantName string `json:"tenantName"`
				UserEmail  string `json:"userEmail"`
			}
			json.Unmarshal(env.Payload, &p)
			if p.TenantID != "" {
				logf("[%s] hello ack: tenant=%s(%s) user=%s", *name, p.TenantName, p.TenantID, p.UserEmail)
			}
		case "task:dispatch":
			var d dispatchMsg
			json.Unmarshal(env.Payload, &d)
			logf("[%s] 收到 task:dispatch task=%s items=%d content=%.60q", *name, d.TaskID, len(d.Items), d.Content)
			go func() {
				handleDispatch(send, &mu, pending, d)
				select {
				case <-done:
				default:
					close(done)
				}
			}()
		case "task:cancel":
			var p struct {
				TaskID string `json:"task_id"`
			}
			json.Unmarshal(env.Payload, &p)
			logf("[%s] 收到 task:cancel task=%s", *name, p.TaskID)
			mu.Lock()
			items := pending[p.TaskID]
			delete(pending, p.TaskID)
			mu.Unlock()
			for _, it := range items {
				send("item:result", itemResult(p.TaskID, fmt.Sprint(it["item_id"]), fmt.Sprint(it["phone"]), "cancelled", ""))
			}
			if len(items) > 0 {
				send("task:summary", map[string]any{"task_id": p.TaskID, "status": "cancelled",
					"total": len(items) + 1, "sent_ok": 1, "sent_fail": 0, "cancelled": len(items), "duration_ms": 1000})
			}
		case "easytier:config", "model:config":
			logf("[%s] 收到 %s: %.200s", *name, env.Type, env.Payload)
		default:
			logf("[%s] 收到 %s: %.120s", *name, env.Type, env.Payload)
		}
		if *exitOnSummary {
			select {
			case <-done:
				logf("[%s] 任务已收口，正常关闭连接", *name)
				conn.Close(websocket.StatusNormalClosure, "e2e done")
				return
			default:
			}
		}
	}
}

func itemResult(taskID, itemID, phone, status, errStr string) map[string]any {
	return map[string]any{"task_id": taskID, "item_id": itemID, "phone": phone,
		"status": status, "error": errStr, "duration_ms": 1500,
		"serial": "E2ESIM", "device_name": "E2E-Sim-iPhone"}
}

type dispatchMsg struct {
	TaskID      string `json:"task_id"`
	DeviceID    string `json:"device_id"`
	UDID        string `json:"udid"`
	Content     string `json:"content"`
	IntervalSec int    `json:"interval_sec"`
	Items       []struct {
		ItemID  string `json:"item_id"`
		Phone   string `json:"phone"`
		Seq     int    `json:"seq"`
		Content string `json:"content"`
	} `json:"items"`
}

// handleDispatch 按模式响应 task:dispatch。
func handleDispatch(send func(string, any) bool, mu *sync.Mutex, pending map[string][]map[string]any, d dispatchMsg) {
	if *dispatchMode == "ignore" {
		mu.Lock()
		pending[d.TaskID] = toPending(d.Items)
		mu.Unlock()
		return
	}
	if *dispatchMode == "slow" {
		// 只回第 1 条 sent，其余挂起等 task:cancel
		if len(d.Items) > 0 {
			it := d.Items[0]
			time.Sleep(500 * time.Millisecond)
			send("item:result", itemResult(d.TaskID, it.ItemID, it.Phone, "sent", ""))
		}
		mu.Lock()
		pending[d.TaskID] = toPending(d.Items[1:])
		mu.Unlock()
		return
	}
	status, errStr := "sent", ""
	if *dispatchMode == "fail" {
		status, errStr = "failed", "e2e 模拟失败"
	}
	ok := 0
	for _, it := range d.Items {
		time.Sleep(300 * time.Millisecond)
		send("item:result", itemResult(d.TaskID, it.ItemID, it.Phone, status, errStr))
		if status == "sent" {
			ok++
		}
	}
	send("task:summary", map[string]any{"task_id": d.TaskID, "status": "done", "total": len(d.Items),
		"sent_ok": ok, "sent_fail": len(d.Items) - ok, "cancelled": 0, "duration_ms": 1500 + 300*len(d.Items)})
}

func toPending(items []struct {
	ItemID  string `json:"item_id"`
	Phone   string `json:"phone"`
	Seq     int    `json:"seq"`
	Content string `json:"content"`
}) []map[string]any {
	var out []map[string]any
	for _, it := range items {
		out = append(out, map[string]any{"item_id": it.ItemID, "phone": it.Phone})
	}
	return out
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
