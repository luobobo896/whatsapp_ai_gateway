package wda

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebDriverAgent HTTP 客户端（服务器直接驱动设备上的 WDA :8100，需求二 2.1）。
// 设备 WDA 监听 0.0.0.0:8100，其 easytier 子网地址（10.168.x.x）即服务器可访问的地址。

// ErrNotReachable 表示 WDA 不可达（设备离线/子网不通）。
var ErrNotReachable = errors.New("wda not reachable")

// Client 是 WDA 会话客户端。
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient 构造 WDA 客户端；baseURL 形如 http://10.168.1.2:8100。
func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// wdaResp 是 WDA 标准响应：{value: ...}。
type wdaResp struct {
	Value json.RawMessage `json:"value"`
	// 元素查找等失败时 WDA 返回 {value:{error:..., message:...}}。
	Err *wdaErr `json:"-"`
	raw map[string]any
}

type wdaErr struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotReachable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 500 {
		// 5xx（设备离线/WDA 未就绪/超载）：视为不可达，执行器据此保留 pending 待续发。
		return nil, fmt.Errorf("%w: wda http %d: %s", ErrNotReachable, resp.StatusCode, truncateBody(data))
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("wda http %d: %s", resp.StatusCode, truncateBody(data))
	}
	return data, nil
}

// truncateBody 错误信息里的响应体只保留头部（WDA 的 no-such-element 会带整段
// ObjC traceback，全量下发会污染网关/平台的明细展示与落盘）。
func truncateBody(data []byte) string {
	s := strings.TrimSpace(string(data))
	const max = 300
	if len(s) > max {
		return s[:max] + "…(truncated)"
	}
	return s
}

// decodeValue 解析 {value: ...}；value 为 string 或 object（元素 id / 会话 id）。
func decodeValue(data []byte) (string, error) {
	var envelope struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return "", err
	}
	switch v := envelope.Value.(type) {
	case string:
		return v, nil
	case map[string]any:
		if id, ok := v["ELEMENT"].(string); ok {
			return id, nil
		}
		if id, ok := v["sessionId"].(string); ok {
			return id, nil
		}
		if msg, ok := v["message"].(string); ok {
			return "", errors.New(msg)
		}
		return "", fmt.Errorf("wda value: %v", v)
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("wda value type %T", envelope.Value)
	}
}

// CreateSession 启动目标 App（bundleID）并建立 WDA 会话。
// shouldTerminateApp=false：会话删除/重建时 WDA 不强杀 App（否则每条发完闪退）；
// forceAppLaunch=false：App 已在前台时复用，不终止重拉（批量发送间不重启）。
func (c *Client) CreateSession(ctx context.Context, bundleID string) (string, error) {
	body := map[string]any{
		"capabilities": map[string]any{
			"alwaysMatch": map[string]any{
				"bundleId":           bundleID,
				"shouldTerminateApp": false,
				"forceAppLaunch":     false,
			},
		},
	}
	data, err := c.do(ctx, http.MethodPost, "/session", body)
	if err != nil {
		return "", err
	}
	return decodeValue(data)
}

// DeleteSession 结束会话（同时可关闭 App）。
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	_, err := c.do(ctx, http.MethodDelete, "/session/"+url.PathEscape(sessionID), nil)
	return err
}

// OpenDeepLink 打开深链（如 whatsapp://send?phone=xxx）唤起目标会话。
// WDA 实际路由是 POST /session/:id/url；携带 bundleId 时在指定 App 内打开。
func (c *Client) OpenDeepLink(ctx context.Context, sessionID, deeplink, bundleID string) error {
	_, err := c.do(ctx, http.MethodPost, "/session/"+url.PathEscape(sessionID)+"/url",
		map[string]any{"url": deeplink, "bundleId": bundleID, "idleTimeoutMs": 3000})
	return err
}

// FindElement 按策略查找元素，返回元素 id。
// using: "id" | "accessibility id" | "class chain" | "predicate string" | "xpath"。
func (c *Client) FindElement(ctx context.Context, sessionID, using, value string) (string, error) {
	data, err := c.do(ctx, http.MethodPost, "/session/"+url.PathEscape(sessionID)+"/element",
		map[string]string{"using": using, "value": value})
	if err != nil {
		return "", err
	}
	return decodeValue(data)
}

// Click 点击元素。
func (c *Client) Click(ctx context.Context, sessionID, elementID string) error {
	_, err := c.do(ctx, http.MethodPost,
		"/session/"+url.PathEscape(sessionID)+"/element/"+url.PathEscape(elementID)+"/click", nil)
	return err
}

// TypeText 在可输入元素中设置文本（覆盖式，适合文本输入框）。
func (c *Client) TypeText(ctx context.Context, sessionID, elementID, text string) error {
	_, err := c.do(ctx, http.MethodPost,
		"/session/"+url.PathEscape(sessionID)+"/element/"+url.PathEscape(elementID)+"/value",
		map[string]any{"value": []string{text}})
	return err
}

// ClearElement 清空元素内容（输入框残留草稿时先清再打，防内容重复）。
func (c *Client) ClearElement(ctx context.Context, sessionID, elementID string) error {
	_, err := c.do(ctx, http.MethodPost,
		"/session/"+url.PathEscape(sessionID)+"/element/"+url.PathEscape(elementID)+"/clear", nil)
	return err
}

// Source 返回当前页面 accessibility 树（用于调试/校准选择器）。
func (c *Client) Source(ctx context.Context, sessionID string) (string, error) {
	data, err := c.do(ctx, http.MethodGet, "/session/"+url.PathEscape(sessionID)+"/source", nil)
	if err != nil {
		return "", err
	}
	return decodeValue(data)
}

// FindElements 返回匹配的全部元素 id（无匹配返回空切片）。
func (c *Client) FindElements(ctx context.Context, sessionID, using, value string) ([]string, error) {
	data, err := c.do(ctx, http.MethodPost, "/session/"+url.PathEscape(sessionID)+"/elements",
		map[string]string{"using": using, "value": value})
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Value []struct {
			ELEMENT string `json:"ELEMENT"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(envelope.Value))
	for _, e := range envelope.Value {
		if e.ELEMENT != "" {
			ids = append(ids, e.ELEMENT)
		}
	}
	return ids, nil
}

// ElementText 返回元素可见文本（W3C GET /element/:id/text）。
func (c *Client) ElementText(ctx context.Context, sessionID, elementID string) (string, error) {
	data, err := c.do(ctx, http.MethodGet,
		"/session/"+url.PathEscape(sessionID)+"/element/"+url.PathEscape(elementID)+"/text", nil)
	if err != nil {
		return "", err
	}
	return decodeValue(data)
}

// ElementRect 返回元素 frame（x,y,width,height），用于坐标点击。
func (c *Client) ElementRect(ctx context.Context, sessionID, elementID string) (x, y, width, height float64, err error) {
	data, err := c.do(ctx, http.MethodGet,
		"/session/"+url.PathEscape(sessionID)+"/element/"+url.PathEscape(elementID)+"/rect", nil)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	var wrap struct {
		Value struct {
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		return 0, 0, 0, 0, err
	}
	return wrap.Value.X, wrap.Value.Y, wrap.Value.Width, wrap.Value.Height, nil
}

// CoordinateTap 在 (x,y) 坐标点击（WDA /wda/tap）。
func (c *Client) CoordinateTap(ctx context.Context, sessionID string, x, y int) error {
	_, err := c.do(ctx, http.MethodPost, "/session/"+url.PathEscape(sessionID)+"/wda/tap",
		map[string]int{"x": x, "y": y})
	return err
}

// Drag 从 (fromX,fromY) 拖到 (toX,toY)，duration 为秒（WDA /wda/dragfromtoforduration）。
func (c *Client) Drag(ctx context.Context, sessionID string, fromX, fromY, toX, toY int, duration float64) error {
	if duration <= 0 {
		duration = 0.2
	}
	_, err := c.do(ctx, http.MethodPost, "/session/"+url.PathEscape(sessionID)+"/wda/dragfromtoforduration",
		map[string]any{
			"fromX": fromX, "fromY": fromY,
			"toX": toX, "toY": toY,
			"duration": duration,
		})
	return err
}

// Screenshot 返回当前屏幕 PNG 原始字节（解码后的 base64）。
func (c *Client) Screenshot(ctx context.Context, sessionID string) ([]byte, error) {
	data, err := c.do(ctx, http.MethodGet, "/session/"+url.PathEscape(sessionID)+"/screenshot", nil)
	if err != nil {
		return nil, err
	}
	b64, err := decodeValue(data)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(b64)
}

// WDAStatus 是 /status 的关键字段（无会话，用于探测设备可达性与 iOS 版本）。
type WDAStatus struct {
	Ready     bool   `json:"ready"`
	OSVersion string `json:"-"`
	IOSIP     string `json:"-"`
}

// Status 返回 WDA /status（无需会话）。
func (c *Client) Status(ctx context.Context) (*WDAStatus, error) {
	data, err := c.do(ctx, http.MethodGet, "/status", nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Value struct {
			Ready bool `json:"ready"`
			OS    struct {
				Version string `json:"version"`
			} `json:"os"`
			IOS struct {
				IP string `json:"ip"`
			} `json:"ios"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	return &WDAStatus{
		Ready:     envelope.Value.Ready,
		OSVersion: envelope.Value.OS.Version,
		IOSIP:     envelope.Value.IOS.IP,
	}, nil
}

// DeviceInfo 返回 /wda/device/info 的 uuid(identifierForVendor)/name/model（无需会话）。
func (c *Client) DeviceInfo(ctx context.Context) (uuid, name, model string, err error) {
	data, err := c.do(ctx, http.MethodGet, "/wda/device/info", nil)
	if err != nil {
		return "", "", "", err
	}
	var envelope struct {
		Value struct {
			UUID  string `json:"uuid"`
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return "", "", "", err
	}
	return envelope.Value.UUID, envelope.Value.Name, envelope.Value.Model, nil
}
