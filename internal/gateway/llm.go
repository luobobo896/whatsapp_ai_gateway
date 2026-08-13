package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// LLMClient 是 OpenAI-compatible 视觉模型客户端，用于发送键定位兜底（截图 -> 坐标）。
type LLMClient struct {
	BaseURL string
	APIKey  string
	Model   string
	client  *http.Client
}

// NewLLMClient 构造客户端；baseURL 形如 https://api.example.com/v1（可留空=不启用）。
func NewLLMClient(baseURL, apiKey, model string) *LLMClient {
	if baseURL == "" || model == "" {
		return &LLMClient{}
	}
	return &LLMClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		client:  &http.Client{Timeout: 20 * time.Second},
	}
}

// Enabled 是否配置了可用模型。
func (c *LLMClient) Enabled() bool { return c != nil && c.BaseURL != "" && c.Model != "" }

// LocateSendButton 用截图让视觉模型定位 WhatsApp 发送键，返回屏幕中心坐标；找不到返回 NONE。
func (c *LLMClient) LocateSendButton(ctx context.Context, screenshotPNG []byte) (int, int, error) {
	if !c.Enabled() {
		return 0, 0, fmt.Errorf("llm not configured")
	}
	reply, err := c.chat(ctx, []chatMsg{
		{Role: "system", Content: "你是 WhatsApp 界面自动化助手。根据截图找到发送消息的按钮（通常是输入框右侧的纸飞机/圆形箭头图标）。只回复按钮中心坐标，格式为 x,y（整数像素），找不到则只回复 NONE。"},
		{Role: "user", Content: imageContent(screenshotPNG)},
	})
	if err != nil {
		return 0, 0, err
	}
	reply = strings.TrimSpace(reply)
	if strings.EqualFold(reply, "NONE") {
		return 0, 0, fmt.Errorf("send button not found by llm")
	}
	m := regexp.MustCompile(`(\d+)\s*[,，xX]\s*(\d+)`).FindStringSubmatch(reply)
	if m == nil {
		return 0, 0, fmt.Errorf("unparseable llm reply: %s", reply)
	}
	var x, y int
	fmt.Sscanf(m[1], "%d", &x)
	fmt.Sscanf(m[2], "%d", &y)
	return x, y, nil
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

func (c *LLMClient) chat(ctx context.Context, msgs []chatMsg) (string, error) {
	body := map[string]any{"model": c.Model, "messages": msgs, "max_tokens": 32, "temperature": 0}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}
	return out.Choices[0].Message.Content, nil
}
