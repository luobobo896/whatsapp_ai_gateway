package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"wda-farm-gateway/internal/wda"
)

// LLMClient 是 OpenAI-compatible 视觉模型客户端，用于发送键定位兜底（截图 -> 坐标）。
type LLMClient struct {
	BaseURL string
	APIKey  string
	Model   string
	client  *http.Client

	coolMu    sync.Mutex
	coolUntil time.Time
}

const llmAssistTimeout = 4 * time.Second

// NewLLMClient 构造客户端；baseURL 形如 https://api.example.com/v1（可留空=不启用）。
func NewLLMClient(baseURL, apiKey, model string) *LLMClient {
	if baseURL == "" || model == "" {
		return &LLMClient{}
	}
	return &LLMClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		client:  &http.Client{Timeout: llmAssistTimeout},
	}
}

// Enabled 是否配置了可用模型。欠费/401/429 冷却期内视为不可用，主流程当没配模型走。
func (c *LLMClient) Enabled() bool {
	if c == nil || c.BaseURL == "" || c.Model == "" {
		return false
	}
	c.coolMu.Lock()
	defer c.coolMu.Unlock()
	return time.Now().After(c.coolUntil)
}

func (c *LLMClient) markUnavailable(err error) {
	if c == nil || err == nil {
		return
	}
	s := strings.ToLower(err.Error())
	if !strings.Contains(s, "401") && !strings.Contains(s, "402") && !strings.Contains(s, "403") &&
		!strings.Contains(s, "429") && !strings.Contains(s, "quota") && !strings.Contains(s, "arrear") &&
		!strings.Contains(s, "insufficient") && !strings.Contains(s, "unpaid") && !strings.Contains(s, "欠费") &&
		!strings.Contains(s, "balance") {
		return
	}
	c.coolMu.Lock()
	c.coolUntil = time.Now().Add(10 * time.Minute)
	c.coolMu.Unlock()
	slog.Warn("llm cooldown 10m, send continues without model", "error", err)
}

// LocateSendButton 用截图让视觉模型定位 WhatsApp 发送键，返回屏幕中心坐标；找不到返回 NONE。
func (c *LLMClient) LocateSendButton(ctx context.Context, screenshotPNG []byte) (int, int, error) {
	if !c.Enabled() {
		return 0, 0, fmt.Errorf("llm not configured")
	}
	reply, err := c.chat(ctx, []chatMsg{
		{Role: "system", Content: "你是 WhatsApp 界面自动化助手。根据截图找到发送消息的按钮（通常是输入框右侧的纸飞机/圆形箭头图标）。只回复按钮中心坐标，格式为 x,y（整数像素），找不到则只回复 NONE。"},
		{Role: "user", Content: imageContent(screenshotPNG)},
	}, 32)
	if err != nil {
		return 0, 0, err
	}
	reply = strings.TrimSpace(reply)
	if strings.EqualFold(reply, "NONE") {
		return 0, 0, fmt.Errorf("send button not found by llm")
	}
	return parseCoordReply(reply, "send button")
}

func (c *LLMClient) DiagnoseScreen(ctx context.Context, screenshotPNG []byte) (wda.ScreenReport, error) {
	if !c.Enabled() {
		return wda.ScreenReport{}, fmt.Errorf("llm not configured")
	}
	reply, err := c.chat(ctx, []chatMsg{
		{Role: "system", Content: `你是 WhatsApp iOS 自动化排障助手。根据截图判断当前界面。只回复一段 JSON，不要 Markdown：
{"kind":"chat|list|search|unknown|dialog|other","title":"","unknown":false,"action":"none|tap_back|tap_cancel|tap_input|tap_send|tap_xy","x":0,"y":0,"note":""}
kind: chat=一对一聊天, list=聊天列表, search=搜索/新聊天, unknown=未知联系人, dialog=系统弹窗, other=其他。
unknown=true 表示未保存/未知会话。action 是建议的下一步；需要坐标时填 x,y。`},
		{Role: "user", Content: imageContent(screenshotPNG)},
	}, 200)
	if err != nil {
		return wda.ScreenReport{}, err
	}
	return parseScreenReport(reply)
}

// parseCoordReply 解析视觉模型回复中的 x,y 坐标（兼容 x=100,y=200 / 100，200 等写法）。
func parseCoordReply(reply, what string) (int, int, error) {
	m := regexp.MustCompile(`(\d+)\s*[,，xX×]\s*[yY]?\s*[=＝]?\s*(\d+)`).FindStringSubmatch(strings.TrimSpace(reply))
	if m == nil {
		return 0, 0, fmt.Errorf("unparseable llm reply for %s: %s", what, reply)
	}
	var x, y int
	fmt.Sscanf(m[1], "%d", &x)
	fmt.Sscanf(m[2], "%d", &y)
	return x, y, nil
}

// LocateTextInput 用截图让视觉模型定位聊天消息输入框，返回中心坐标；找不到返回 NONE。
func (c *LLMClient) LocateTextInput(ctx context.Context, screenshotPNG []byte) (int, int, error) {
	if !c.Enabled() {
		return 0, 0, fmt.Errorf("llm not configured")
	}
	reply, err := c.chat(ctx, []chatMsg{
		{Role: "system", Content: "你是 WhatsApp 界面自动化助手。根据截图找到输入消息的文本输入框（聊天页底部、键盘上方）。只回复输入框中心坐标，格式为 x,y（整数像素），找不到则只回复 NONE。"},
		{Role: "user", Content: imageContent(screenshotPNG)},
	}, 32)
	if err != nil {
		return 0, 0, err
	}
	reply = strings.TrimSpace(reply)
	if strings.EqualFold(reply, "NONE") {
		return 0, 0, fmt.Errorf("text input not found by llm")
	}
	return parseCoordReply(reply, "text input")
}

type chatMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func imageContent(png []byte) []imagePart {
	return []imagePart{{Type: "image_url", ImageURL: struct {
		URL string `json:"url"`
	}{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}}}
}

type imagePart struct {
	Type     string `json:"type"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

func (c *LLMClient) chat(ctx context.Context, msgs []chatMsg, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 64
	}
	body := map[string]any{"model": c.Model, "messages": msgs, "max_tokens": maxTokens, "temperature": 0}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	ctx, cancel := context.WithTimeout(ctx, llmAssistTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		err := fmt.Errorf("llm http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		c.markUnavailable(err)
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}
	text := extractChoiceText(out.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("llm returned empty content")
	}
	return text, nil
}

func extractChoiceText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return strings.TrimSpace(b.String())
	}
	return strings.TrimSpace(string(raw))
}

func parseScreenReport(s string) (wda.ScreenReport, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	var r wda.ScreenReport
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return wda.ScreenReport{Kind: "other", Note: s}, nil
	}
	return r, nil
}
