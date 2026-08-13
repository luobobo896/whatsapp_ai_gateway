package wda

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

// WhatsApp App bundle id（普通版 + Business 版）。
const (
	WhatsAppBundleID         = "net.whatsapp.WhatsApp"
	WhatsAppBusinessBundleID = "net.whatsapp.WhatsAppSMB"
)

var whatsappBundleIDs = []string{WhatsAppBundleID, WhatsAppBusinessBundleID}

// whatsappSelectors 是 WhatsApp 界面元素选择器（与本地网关 Python 版对齐；真机联调校准后固化）。
var whatsappSelectors = struct {
	messageInput string
	sendButton   string
}{
	messageInput: `class chain: **/XCUIElementTypeTextView[1]`,
	sendButton:   `accessibility id: ChatBar_SendButton`,
}

var whatsappSendButtonFallbacks = []string{
	`accessibility id: Send`,
	`accessibility id: send`,
	`predicate string: name == 'Send'`,
	`predicate string: name == '发送'`,
	`predicate string: label == '发送'`,
}

// whatsappBackToChats 返回聊天列表的返回键候选（label 带 RTL 不可见字符，用 CONTAINS；限定 Button）。
var whatsappBackToChats = []string{
	`predicate string: type == 'XCUIElementTypeButton' AND label CONTAINS '聊天'`,
	`predicate string: type == 'XCUIElementTypeButton' AND label CONTAINS 'Chats'`,
	`predicate string: type == 'XCUIElementTypeButton' AND name CONTAINS 'Back'`,
	`predicate string: type == 'XCUIElementTypeButton' AND name CONTAINS '返回'`,
}

var (
	whatsappNewChatButton = "accessibility id: NavigationBar_NewChatButton"
	whatsappSearchField   = "class chain: **/XCUIElementTypeSearchField"
	whatsappContactCell   = "accessibility id: PickerView_ContactCell"
)

// SetMessageInputSelector / SetSendButtonSelector 供联调时覆盖默认选择器。
func SetMessageInputSelector(using, value string) {
	whatsappSelectors.messageInput = using + ": " + value
}
func SetSendButtonSelector(using, value string) { whatsappSelectors.sendButton = using + ": " + value }

// SendAssist 是发送链路的视觉/LLM 辅助：选择器找不到发送键时，用截图让视觉模型定位发送键坐标。
type SendAssist interface {
	LocateSendButton(ctx context.Context, screenshotPNG []byte) (x, y int, err error)
}

// SendMessageToPhone 驱动 WDA 给指定手机号发送一条文本（保持原签名，供 runner 复用）。
func SendMessageToPhone(ctx context.Context, client *Client, phone, content string) error {
	return sendMessage(ctx, client, phone, content, nil)
}

// SendMessageWithAssist 同 SendMessageToPhone，但选择器找不到发送键时用视觉/LLM 兜底定位。
func SendMessageWithAssist(ctx context.Context, client *Client, phone, content string, assist SendAssist) error {
	return sendMessage(ctx, client, phone, content, assist)
}

func sendMessage(ctx context.Context, client *Client, phone, content string, assist SendAssist) error {
	digits := ""
	if phone != "" {
		var err error
		if digits, err = normalizeMobilePhone(phone); err != nil {
			return err
		}
	}
	sid, bid, err := createWhatsAppSession(ctx, client)
	if err != nil {
		return fmt.Errorf("create wda session: %w", err)
	}
	defer func() { _ = client.DeleteSession(context.WithoutCancel(ctx), sid) }()

	if digits != "" {
		if err := openTargetChat(ctx, client, sid, bid, digits); err != nil {
			return err
		}
	} else {
		if err := openDefaultChat(ctx, client, sid); err != nil {
			return err
		}
	}

	inputID, err := waitElement(ctx, client, sid, whatsappSelectors.messageInput, 15*time.Second)
	if err != nil {
		return fmt.Errorf("find message input: %w", err)
	}
	if err := client.TypeText(ctx, sid, inputID, content); err != nil {
		return fmt.Errorf("type content: %w", err)
	}

	sendSelectors := append([]string{whatsappSelectors.sendButton}, whatsappSendButtonFallbacks...)
	sendID, err := waitAnyElement(ctx, client, sid, sendSelectors, 10*time.Second)
	if err != nil {
		if assist != nil {
			if png, serr := client.Screenshot(ctx, sid); serr == nil {
				if x, y, lerr := assist.LocateSendButton(ctx, png); lerr == nil {
					if terr := client.CoordinateTap(ctx, sid, x, y); terr == nil {
						return nil
					}
				}
			}
		}
		return fmt.Errorf("find send button: %w", err)
	}
	if err := client.Click(ctx, sid, sendID); err != nil {
		return fmt.Errorf("tap send: %w", err)
	}
	return nil
}

// createWhatsAppSession 按候选 bundle 自动识别（普通 WhatsApp / WhatsApp Business）。
func createWhatsAppSession(ctx context.Context, client *Client) (sid, bid string, err error) {
	var lastErr error
	for _, b := range whatsappBundleIDs {
		sid, err = client.CreateSession(ctx, b)
		if err == nil && sid != "" {
			return sid, b, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no whatsapp session created")
	}
	return "", "", fmt.Errorf("whatsapp not installed (tried %s): %w", strings.Join(whatsappBundleIDs, ", "), lastErr)
}

// openTargetChat 打开指定号码的会话：优先深链（iOS 16.4+）；失败则聊天列表按号码匹配，再走新聊天搜索。
func openTargetChat(ctx context.Context, client *Client, sid, bid, digits string) error {
	deeplink := "whatsapp://send?phone=" + digits
	if err := client.OpenDeepLink(ctx, sid, deeplink, bid); err == nil {
		return nil
	}
	if err := gotoChatList(ctx, client, sid); err != nil {
		return err
	}
	idx, err := chatIndexByPhone(ctx, client, sid, digits)
	if err == nil {
		return tapCell(ctx, client, sid, idx)
	}
	if openNewChatByPhone(ctx, client, sid, digits) {
		return nil
	}
	return fmt.Errorf("deep link unsupported and no chat/contact for %s", digits)
}

// openDefaultChat 不指定号码：当前已有打开的会话则直接使用，否则打开聊天列表第一个会话。
func openDefaultChat(ctx context.Context, client *Client, sid string) error {
	if _, err := findElementBySelector(ctx, client, sid, whatsappSelectors.messageInput); err == nil {
		return nil
	}
	cells, err := sourceCells(ctx, client, sid)
	if err != nil {
		return fmt.Errorf("no chat available to send to: %w", err)
	}
	for i, c := range cells {
		if digitsOf(c.name) != "" || c.hasMessage {
			return tapCell(ctx, client, sid, i+1)
		}
	}
	return fmt.Errorf("no chat available to send to")
}

// gotoChatList 若当前停在聊天页（输入框可见）则点返回回到聊天列表。
func gotoChatList(ctx context.Context, client *Client, sid string) error {
	using, value := splitSelector(whatsappSelectors.messageInput)
	if _, err := client.FindElement(ctx, sid, using, value); err != nil {
		return nil // 不在聊天页
	}
	for _, sel := range whatsappBackToChats {
		using, value := splitSelector(sel)
		bid, err := client.FindElement(ctx, sid, using, value)
		if err == nil && bid != "" {
			if err := client.Click(ctx, sid, bid); err == nil {
				time.Sleep(1500 * time.Millisecond)
				return nil
			}
		}
	}
	return fmt.Errorf("back button not found")
}

// openNewChatByPhone 新聊天 -> 搜索号码 -> 点联系人动作，成功后输入框出现即返回 true。
func openNewChatByPhone(ctx context.Context, client *Client, sid, digits string) bool {
	using, value := splitSelector(whatsappNewChatButton)
	nc, err := client.FindElement(ctx, sid, using, value)
	if err != nil || nc == "" {
		return false
	}
	if err := client.Click(ctx, sid, nc); err != nil {
		return false
	}
	time.Sleep(1500 * time.Millisecond)
	sf, err := findElementBySelector(ctx, client, sid, whatsappSearchField)
	if err != nil || sf == "" {
		return false
	}
	if err := client.Click(ctx, sid, sf); err != nil {
		return false
	}
	time.Sleep(800 * time.Millisecond)
	if err := client.TypeText(ctx, sid, sf, digits); err != nil {
		return false
	}
	time.Sleep(2500 * time.Millisecond)

	cellID, err := findElementBySelector(ctx, client, sid, whatsappContactCell)
	if err != nil || cellID == "" {
		return false
	}
	// 动作在 cell 右侧：先坐标点击，再尝试 cell 内动作文本。
	if x, y, w, h, err := client.ElementRect(ctx, sid, cellID); err == nil {
		tx, ty := int(x+w-30), int(y+h/2)
		if client.CoordinateTap(ctx, sid, tx, ty) == nil && chatOpened(ctx, client, sid, 6*time.Second) {
			return true
		}
	}
	for _, name := range []string{"PickerView_ContactCell_PhoneNumber", "聊天"} {
		sel := "class chain: **/XCUIElementTypeCell[`name == 'PickerView_ContactCell'`]/**/XCUIElementTypeStaticText[`name == '" + name + "'`]"
		act, err := findElementBySelector(ctx, client, sid, sel)
		if err == nil && act != "" {
			if client.Click(ctx, sid, act) == nil && chatOpened(ctx, client, sid, 6*time.Second) {
				return true
			}
		}
	}
	return false
}

func chatOpened(ctx context.Context, client *Client, sid string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := findElementBySelector(ctx, client, sid, whatsappSelectors.messageInput); err == nil {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// ---- 源码树解析（用于聊天列表定位）----

type sourceCell struct {
	name       string
	hasMessage bool
}

func sourceCells(ctx context.Context, client *Client, sid string) ([]sourceCell, error) {
	src, err := client.Source(ctx, sid)
	if err != nil {
		return nil, err
	}
	return parseSourceCells(src)
}

func parseSourceCells(src string) ([]sourceCell, error) {
	dec := xml.NewDecoder(strings.NewReader(src))
	var out []sourceCell
	var cur *sourceCell
	var curDepth int
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "XCUIElementTypeCell" {
				if cur == nil {
					cur = &sourceCell{name: xmlAttr(t, "name")}
					curDepth = depth
				}
			} else if cur != nil && t.Name.Local == "XCUIElementTypeStaticText" && xmlAttr(t, "name") == "WAChatSessionCell_Message" {
				cur.hasMessage = true
			}
			depth++
		case xml.EndElement:
			depth--
			if cur != nil && depth == curDepth {
				out = append(out, *cur)
				cur = nil
			}
		}
	}
	return out, nil
}

func xmlAttr(el xml.StartElement, name string) string {
	for _, a := range el.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func chatIndexByPhone(ctx context.Context, client *Client, sid, digits string) (int, error) {
	cells, err := sourceCells(ctx, client, sid)
	if err != nil {
		return 0, err
	}
	for i, c := range cells {
		if digitsOf(c.name) == digits {
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("no chat for %s in chat list", digits)
}

func tapCell(ctx context.Context, client *Client, sid string, idx int) error {
	id, err := client.FindElement(ctx, sid, "class chain", fmt.Sprintf("**/XCUIElementTypeCell[%d]", idx))
	if err != nil || id == "" {
		return fmt.Errorf("chat cell [%d] not found", idx)
	}
	if err := client.Click(ctx, sid, id); err != nil {
		return err
	}
	time.Sleep(1500 * time.Millisecond)
	return nil
}

// ---- 轮询查找 ----

func waitElement(ctx context.Context, client *Client, sid, selector string, timeout time.Duration) (string, error) {
	using, value := splitSelector(selector)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		id, err := client.FindElement(ctx, sid, using, value)
		if err == nil && id != "" {
			return id, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("element not found within %s: %s", timeout, selector)
}

func waitAnyElement(ctx context.Context, client *Client, sid string, selectors []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		for _, sel := range selectors {
			using, value := splitSelector(sel)
			id, err := client.FindElement(ctx, sid, using, value)
			if err == nil && id != "" {
				return id, nil
			}
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no element found within %s: %v", timeout, selectors)
}

// findElementBySelector 按 "using: value" 选择器查找单个元素。
func findElementBySelector(ctx context.Context, client *Client, sid, selector string) (string, error) {
	using, value := splitSelector(selector)
	return client.FindElement(ctx, sid, using, value)
}

// splitSelector 拆分 "using: value" 形式的选择器。
func splitSelector(selector string) (using, value string) {
	for i := 0; i < len(selector); i++ {
		if selector[i] == ':' && i+1 < len(selector) && selector[i+1] == ' ' {
			return selector[:i], selector[i+2:]
		}
	}
	return "class chain", selector
}
