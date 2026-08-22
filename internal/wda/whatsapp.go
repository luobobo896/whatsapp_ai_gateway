package wda

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrSendToSelf 硬性规则：禁止给本机 WhatsApp「自己」会话发消息。
var ErrSendToSelf = errors.New("禁止给自己发送")

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
	messageInput: `accessibility id: ChatBar_ComposerTextView`,
	sendButton:   `accessibility id: ChatBar_SendButton`,
}

var whatsappSendButtonFallbacks = []string{
	`accessibility id: Send`,
	`accessibility id: send`,
	`predicate string: name == 'Send'`,
	`predicate string: name == '发送'`,
	`predicate string: label == '发送'`,
	`predicate string: type == 'XCUIElementTypeButton' AND (name CONTAINS 'Send' OR label CONTAINS 'Send' OR name CONTAINS '发送' OR label CONTAINS '发送')`,
}

// whatsappBackToChats 返回聊天列表的返回键。
// 不能用单纯 CONTAINS '聊天'：会话页「聊天主题」也会命中，一点就进壁纸设置。
// NOT 必须带括号：NSPredicate 里 `NOT label CONTAINS` 会解析失败，导致中文返回键永远找不到。
// 优先限定 NavigationBar，避免点到会话页里的「聊天主题」入口。
var whatsappBackToChats = []string{
	"class chain: **/XCUIElementTypeNavigationBar/**/XCUIElementTypeButton[`label CONTAINS '聊天' AND NOT (label CONTAINS '主题') AND NOT (label CONTAINS '气泡')`]",
	"class chain: **/XCUIElementTypeNavigationBar/**/XCUIElementTypeButton[`label CONTAINS 'Chats' AND NOT (label CONTAINS 'Theme')`]",
	"class chain: **/XCUIElementTypeNavigationBar/**/XCUIElementTypeButton[`label CONTAINS '对话'`]",
	`predicate string: type == 'XCUIElementTypeButton' AND label CONTAINS '聊天' AND NOT (label CONTAINS '主题') AND NOT (name CONTAINS '主题') AND NOT (label CONTAINS '气泡')`,
	`predicate string: type == 'XCUIElementTypeButton' AND label CONTAINS 'Chats' AND NOT (label CONTAINS 'Theme') AND NOT (name CONTAINS 'Theme')`,
	`predicate string: type == 'XCUIElementTypeButton' AND (name CONTAINS 'Back' OR name CONTAINS '返回' OR label CONTAINS '返回' OR label CONTAINS 'Back')`,
}

// whatsappChatsTab 底栏「聊天」Tab（部分版本会话页仍露出 TabBar）。
var whatsappChatsTab = []string{
	`predicate string: type == 'XCUIElementTypeButton' AND (label == '聊天' OR name == '聊天' OR label == 'Chats' OR name == 'Chats' OR label == '对话' OR name == '对话')`,
	`predicate string: type == 'XCUIElementTypeButton' AND label CONTAINS '聊天' AND NOT (label CONTAINS '主题') AND NOT (label CONTAINS '气泡')`,
	`predicate string: type == 'XCUIElementTypeButton' AND label CONTAINS 'Chats' AND NOT (label CONTAINS 'Theme')`,
}

var (
	whatsappNewChatButton = "accessibility id: NavigationBar_NewChatButton"
	whatsappSearchField   = "accessibility id: PickerView_SearchBar"
	whatsappContactCell   = "accessibility id: PickerView_ContactCell"
)

var whatsappSearchFieldFallbacks = []string{
	`accessibility id: PickerView_SearchBar`,
	`class chain: **/XCUIElementTypeSearchField`,
}

// whatsappLeavePicker 离开「新聊天/搜索」页的取消键（该页没有消息输入框，旧逻辑会当成已在列表）。
var whatsappLeavePicker = []string{
	`accessibility id: PickerView_CloseButton`,
	`predicate string: type == 'XCUIElementTypeButton' AND (label == 'Cancel' OR name == 'Cancel' OR label == '取消' OR name == '取消' OR label == '关闭' OR name == '关闭')`,
	`accessibility id: Cancel`,
	`accessibility id: 取消`,
	`accessibility id: 关闭`,
}

// SetMessageInputSelector / SetSendButtonSelector 供联调时覆盖默认选择器。
func SetMessageInputSelector(using, value string) {
	whatsappSelectors.messageInput = using + ": " + value
}
func SetSendButtonSelector(using, value string) { whatsappSelectors.sendButton = using + ": " + value }

// whatsappChatTitleSelectors 聊天页标题（收件人姓名/号码）候选选择器；真机联调校准后固化。
var whatsappChatTitleSelectors = []string{
	`accessibility id: NavigationBar_ConversationHeader`,
	`accessibility id: NavigationBar_TitleLabel`,
	`accessibility id: NavigationBar_HeaderViewButton`,
	`class chain: **/XCUIElementTypeNavigationBar[1]/XCUIElementTypeStaticText[1]`,
	`accessibility id: ChatTitleView_Title`,
}

// SetChatTitleSelector 供联调时覆盖聊天标题选择器。
func SetChatTitleSelector(using, value string) {
	whatsappChatTitleSelectors = []string{using + ": " + value}
}

// ChatTitle 尽力读取当前聊天页标题（收件人姓名/号码）；读不到返回空串，不影响发送。
func ChatTitle(ctx context.Context, client *Client, sid string) string {
	for _, sel := range whatsappChatTitleSelectors {
		id, err := findElementBySelector(ctx, client, sid, sel)
		if err != nil || id == "" {
			continue
		}
		t, err := client.ElementText(ctx, sid, id)
		if err != nil {
			continue
		}
		if t = strings.TrimSpace(t); t != "" {
			return t
		}
	}
	return ""
}

// ScreenReport 是视觉模型对当前界面的结构化判断（不可信，调用方必须再校验）。
type ScreenReport struct {
	Kind    string `json:"kind"` // chat / list / search / unknown / dialog / other
	Title   string `json:"title"`
	Unknown bool   `json:"unknown"`
	Action  string `json:"action"` // none / tap_back / tap_cancel / tap_input / tap_send / tap_xy
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Note    string `json:"note"`
}

// SendAssist 是发送链路的视觉/LLM 辅助：选择器找不到或会话校验失败时，
// 用截图让模型定位坐标或判断当前界面。未配置（nil）时走选择器逻辑。
type SendAssist interface {
	LocateSendButton(ctx context.Context, screenshotPNG []byte) (x, y int, err error)
	LocateTextInput(ctx context.Context, screenshotPNG []byte) (x, y int, err error)
	DiagnoseScreen(ctx context.Context, screenshotPNG []byte) (ScreenReport, error)
}

// SendMessageToPhone 驱动 WDA 给指定手机号发送一条文本（保持原签名，供 runner 复用）。
func SendMessageToPhone(ctx context.Context, client *Client, phone, content string) error {
	_, err := SendMessageToPhoneInfo(ctx, client, phone, content, nil)
	return err
}

// SendMessageWithAssist 同 SendMessageToPhone，但选择器找不到发送键时用视觉/LLM 兜底定位。
func SendMessageWithAssist(ctx context.Context, client *Client, phone, content string, assist SendAssist) error {
	_, err := SendMessageToPhoneInfo(ctx, client, phone, content, assist)
	return err
}

// SendMessageToPhoneInfo 同 SendMessageToPhone，并返回该条是否为新会话
// （新会话 = 聊天列表中无既有会话、经「新聊天→搜索」打开；用于新会话占比控制）。
func SendMessageToPhoneInfo(ctx context.Context, client *Client, phone, content string, assist SendAssist) (isNew bool, err error) {
	if strings.TrimSpace(phone) == "" {
		_, _, err = SendToChatListFriends(ctx, client, content, assist, 0)
		return false, err
	}
	sid, isNew, err := OpenChatForSendWithAssist(ctx, client, phone, assist)
	if err != nil {
		return false, err
	}
	defer func() { _ = client.DeleteSession(context.WithoutCancel(ctx), sid) }()
	return isNew, TypeAndSend(ctx, client, sid, content, assist)
}

// CreateWhatsAppSession 启动 WhatsApp（普通或 Business）并建立一条可复用的 WDA 会话。
// 群发应整单共用这一条，不要每条 Create/Delete（冷启动一次十数秒，竞品之所以丝滑是 XCTest 常驻）。
func CreateWhatsAppSession(ctx context.Context, client *Client) (sid, bid string, err error) {
	return createWhatsAppSession(ctx, client)
}

// OpenChatOnSession 在已有 WDA 会话里打开目标号码，不新建、不删除会话。
func OpenChatOnSession(ctx context.Context, client *Client, sid, bid, phone string, assist SendAssist) (isNew bool, err error) {
	if strings.TrimSpace(sid) == "" {
		return false, fmt.Errorf("empty wda session")
	}
	if bid == "" {
		bid = WhatsAppBundleID
	}
	if strings.TrimSpace(phone) == "" {
		return false, openDefaultChat(ctx, client, sid)
	}
	digits, err := normalizeMobilePhone(phone)
	if err != nil {
		return false, err
	}
	return openTargetChat(ctx, client, sid, bid, digits, assist)
}

// OpenChatForSend 创建 WhatsApp 会话并打开目标会话（不发送）。返回会话 id 与该会话
// 是否为新会话；调用方发送后需自行 DeleteSession。单条探针用；群发请复用 CreateWhatsAppSession。
func OpenChatForSend(ctx context.Context, client *Client, phone string) (sid string, isNew bool, err error) {
	return OpenChatForSendWithAssist(ctx, client, phone, nil)
}

// OpenChatForSendWithAssist 同 OpenChatForSend，会话校验失败时用视觉模型判断界面并尝试恢复。
func OpenChatForSendWithAssist(ctx context.Context, client *Client, phone string, assist SendAssist) (sid string, isNew bool, err error) {
	sid, bid, err := createWhatsAppSession(ctx, client)
	if err != nil {
		return "", false, fmt.Errorf("create wda session: %w", err)
	}
	isNew, err = OpenChatOnSession(ctx, client, sid, bid, phone, assist)
	return sid, isNew, err
}

// TypeAndSend 在已打开的会话中输入内容并点发送（发送键找不到时可用视觉/LLM 兜底）。
// 点击发送后校验输入框已清空，未清空说明点击未生效，返回错误避免误报 sent。
func TypeAndSend(ctx context.Context, client *Client, sid, content string, assist SendAssist) error {
	if title := ChatTitle(ctx, client, sid); isSelfChatTitle(title) {
		return ErrSendToSelf
	}
	if err := ensureTyped(ctx, client, sid, content, assist); err != nil {
		return err
	}

	sendSelectors := append([]string{whatsappSelectors.sendButton}, whatsappSendButtonFallbacks...)
	sendID, err := waitAnyElement(ctx, client, sid, sendSelectors, 10*time.Second)
	if err != nil {
		if assist != nil {
			actx, cancel := context.WithTimeout(ctx, 4*time.Second)
			if png, serr := client.Screenshot(actx, sid); serr == nil {
				if x, y, lerr := assist.LocateSendButton(actx, png); lerr == nil {
					cancel()
					if terr := client.CoordinateTap(ctx, sid, x, y); terr == nil {
						return confirmSent(ctx, client, sid, content)
					}
				}
			}
			cancel()
		}
		return fmt.Errorf("find send button: %w", err)
	}
	if err := client.Click(ctx, sid, sendID); err != nil {
		return fmt.Errorf("tap send: %w", err)
	}
	return confirmSent(ctx, client, sid, content)
}

func applyScreenAssist(ctx context.Context, client *Client, sid string, assist SendAssist) ScreenReport {
	if assist == nil {
		return ScreenReport{}
	}
	actx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	png, err := client.Screenshot(actx, sid)
	if err != nil || len(png) == 0 {
		return ScreenReport{Note: "screenshot failed"}
	}
	r, err := assist.DiagnoseScreen(actx, png)
	if err != nil {
		return ScreenReport{Note: err.Error()}
	}
	kind := strings.ToLower(strings.TrimSpace(r.Kind))
	act := strings.ToLower(strings.TrimSpace(r.Action))
	if kind == "unknown" || r.Unknown || act == "tap_back" {
		_ = gotoChatList(ctx, client, sid)
	}
	if kind == "search" || act == "tap_cancel" {
		_ = dismissPicker(ctx, client, sid)
	}
	if (act == "tap_xy" || act == "tap_input" || act == "tap_send") && (r.X > 0 || r.Y > 0) {
		_ = client.CoordinateTap(ctx, sid, r.X, r.Y)
	}
	return r
}

// transientCallErr 判定 WDA 请求是否为超时/连接类瞬时故障（页面过渡期元素
// 引用失效会让 value 请求挂满超时，重找元素即可自愈）。
func transientCallErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "context deadline exceeded") ||
		strings.Contains(s, "Client.Timeout") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "timed out")
}

// ensureTyped 确保输入框内恰好是 content（幂等，带一次自愈重试）：
//   - 已有相同文本（上次 type 实际已生效/草稿恰好相等）→ 直接进入发送，避免重复输入；
//   - 有残留草稿 → 先清空再打；
//   - type 超时/瞬时失败 → 等 1.5s 让聊天页加载稳定（深链跳转后姓名未显示期间
//     元素引用易失效），重新查找输入框并点按拉起键盘后重打一次。
func ensureTyped(ctx context.Context, client *Client, sid, content string, assist SendAssist) error {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("type content: %w", ctx.Err())
			case <-time.After(1500 * time.Millisecond):
			}
		}
		inputWait := 8 * time.Second
		if assist != nil && attempt == 0 {
			inputWait = 4 * time.Second
		}
		inputID, err := waitElement(ctx, client, sid, whatsappSelectors.messageInput, inputWait)
		if err != nil {
			// 视觉兜底：配置了 LLM 时截图定位输入框坐标点击，再短等一次输入框出现。
			if assist != nil && attempt == 0 {
				actx, cancel := context.WithTimeout(ctx, 4*time.Second)
				if png, serr := client.Screenshot(actx, sid); serr == nil {
					if x, y, lerr := assist.LocateTextInput(actx, png); lerr == nil {
						if terr := client.CoordinateTap(ctx, sid, x, y); terr == nil {
							inputID, err = waitElement(ctx, client, sid, whatsappSelectors.messageInput, 5*time.Second)
						}
					}
				}
				cancel()
			}
			if err != nil {
				return fmt.Errorf("find message input: %w", err)
			}
		}
		// 幂等防护：读输入框当前文本，避免把内容打成两份。
		if t, terr := client.ElementText(ctx, sid, inputID); terr == nil {
			ts := strings.TrimSpace(t)
			if ts == strings.TrimSpace(content) {
				return nil
			}
			if ts != "" {
				_ = client.ClearElement(ctx, sid, inputID)
			}
		}
		if attempt > 0 {
			// 重试前点按输入框聚焦（过渡期键盘可能未拉起，value 输入会挂起）。
			_ = client.Click(ctx, sid, inputID)
		}
		if err := client.TypeText(ctx, sid, inputID, content); err != nil {
			if !transientCallErr(err) {
				return fmt.Errorf("type content: %w", err)
			}
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("type content: %w", lastErr)
}

// confirmSent 确认消息真的发出：发送成功后 WhatsApp 会清空输入框。
// 轮询输入框文本，仍残留原文说明发送键点击未生效（误点别处/按钮未使能）。
// 输入框不可见或读不到文本时无法校验，按已发送放行（不影响正常路径）。
func confirmSent(ctx context.Context, client *Client, sid, content string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil
		}
		using, value := splitSelector(whatsappSelectors.messageInput)
		id, err := client.FindElement(ctx, sid, using, value)
		if err != nil || id == "" {
			return nil
		}
		t, err := client.ElementText(ctx, sid, id)
		if err != nil {
			return nil
		}
		if strings.TrimSpace(t) != strings.TrimSpace(content) {
			return nil // 已清空（或内容已变化），视为发送成功
		}
		time.Sleep(600 * time.Millisecond)
	}
	return fmt.Errorf("send unconfirmed: 输入框内容仍残留，发送键点击未生效")
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

// openTargetChat 打开指定号码的会话。
// 高版本 iOS（≥16.4）优先深链（whatsapp:// 与 https://wa.me）；老系统先聊天列表匹配再深链。
// 仅指定号码失败后才走「新聊天→搜索」；空号/列表好友路径禁止搜索。
// 返回 isNew 表示是否经「新聊天→搜索」路径打开（即该号码此前无既有会话）。
func openTargetChat(ctx context.Context, client *Client, sid, bid, digits string, assist SendAssist) (isNew bool, err error) {
	// 已经在目标会话（深链刚打开、或上次停在此人聊天页）就直接发，不要先点返回把页面关掉。
	if chatOpenedFor(ctx, client, sid, digits, 1500*time.Millisecond, "") {
		return false, nil
	}
	// 不在目标会话：回到列表再打开，避免停在上一条会话里发错人。
	_ = gotoChatList(ctx, client, sid)
	prevTitle := ChatTitle(ctx, client, sid)
	iosVer := ""
	if st, serr := client.Status(ctx); serr == nil && st != nil {
		iosVer = st.OSVersion
	}
	preferDeep := PreferDeepLink(iosVer)

	tryDeep := func() bool {
		for _, link := range whatsAppSendDeepLinks(digits) {
			if client.OpenDeepLink(ctx, sid, link, bid) != nil {
				continue
			}
			// 深链 HTTP 成功 ≠ 会话已打开（老 iOS 不跳转/号码无效只弹窗）。
			if chatOpenedFor(ctx, client, sid, digits, 8*time.Second, prevTitle) {
				return true
			}
			_ = gotoChatList(ctx, client, sid)
			prevTitle = ChatTitle(ctx, client, sid)
		}
		return false
	}
	tryList := func() bool {
		if err := gotoChatList(ctx, client, sid); err != nil {
			return false
		}
		prevTitle = ChatTitle(ctx, client, sid)
		idx, err := chatIndexByPhone(ctx, client, sid, digits)
		if err != nil {
			return false
		}
		if err := tapCell(ctx, client, sid, idx); err == nil && chatOpenedFor(ctx, client, sid, digits, 6*time.Second, prevTitle) {
			return true
		}
		return false
	}

	if preferDeep {
		if tryDeep() {
			return false, nil
		}
		if tryList() {
			return false, nil
		}
	} else {
		if tryList() {
			return false, nil
		}
		if tryDeep() {
			return false, nil
		}
	}

	// 仅明确指定号码时才允许搜索；未指定号/列表好友群发不得进入此分支。
	if !ShouldSearchContact(digits) {
		return false, fmt.Errorf("deep link unsupported and no chat/contact for %s", digits)
	}
	if err := openNewChatByPhone(ctx, client, sid, digits); err == nil {
		return true, nil
	} else if errors.Is(err, ErrSendToSelf) {
		return false, err
	} else {
		// 保留搜索失败的明确错误（禁止输入后静默挂起）；模型仅作最后界面恢复尝试。
		searchErr := err
		if assist != nil {
			applyScreenAssist(ctx, client, sid, assist)
			if chatOpenedFor(ctx, client, sid, digits, 3*time.Second, prevTitle) {
				return false, nil
			}
		}
		return false, searchErr
	}
}

// ShouldSearchContact 仅当明确提供了手机号时才允许「新聊天→搜索」。
// 空号/未指定收件人必须走聊天列表好友，禁止进搜索框。
func ShouldSearchContact(phone string) bool {
	return strings.TrimSpace(phone) != ""
}

// PreferDeepLink 高版本 iOS（≥16.4）优先用深链打开会话；版本未知时也先试深链再兜底。
func PreferDeepLink(iosVersion string) bool {
	maj, min, ok := parseIOSVersion(iosVersion)
	if !ok {
		return true
	}
	if maj > 16 {
		return true
	}
	return maj == 16 && min >= 4
}

func parseIOSVersion(v string) (major, minor int, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, 0, false
	}
	// 形如 16.4 / 16.4.1 / 15.8.8
	parts := strings.Split(v, ".")
	if len(parts) == 0 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &major); err != nil {
		return 0, 0, false
	}
	if len(parts) > 1 {
		_, _ = fmt.Sscanf(parts[1], "%d", &minor)
	}
	return major, minor, true
}

// whatsAppSendDeepLinks 指定号码深链候选（先原生 scheme，再 https://wa.me）。
func whatsAppSendDeepLinks(digits string) []string {
	d := digitsOf(digits)
	if d == "" {
		return nil
	}
	return []string{
		"whatsapp://send?phone=" + d,
		"https://wa.me/" + d,
	}
}

// chatOpenedFor 等待聊天页打开并校验归属。
//   - 标题含号码：必须与目标匹配（86+11 或国内 11 位均可）
//   - 标题是「未知/Unknown」：不算命中（深链常把已有联系人打开成未保存会话）
//   - 标题为空：页面未就绪，继续等，不能当联系人名放行
//   - 标题是联系人名：必须相对深链前发生变化，避免停在上一条会话
func chatOpenedFor(ctx context.Context, client *Client, sid, digits string, timeout time.Duration, prevTitle string) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		if id, err := findElementBySelector(ctx, client, sid, whatsappSelectors.messageInput); err == nil && id != "" {
			title := strings.TrimSpace(ChatTitle(ctx, client, sid))
			if title == "" {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			if isUnknownChatTitle(title) {
				return false
			}
			if phoneDigitsMatch(title, digits) {
				return true
			}
			if digitsOf(title) != "" {
				return false
			}
			if prevTitle != "" && title == prevTitle {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
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
		if isSelfChatTitle(c.name) || isSelfChatTitle(c.label) {
			continue
		}
		if digitsOf(c.name) != "" || c.hasMessage {
			return tapCell(ctx, client, sid, i+1)
		}
	}
	return fmt.Errorf("no chat available to send to")
}

// GotoChatList 回到并尽量停留在聊天列表页（群发整单结束后必须调用，避免停在最后一条会话）。
func GotoChatList(ctx context.Context, client *Client, sid string) error {
	return gotoChatList(ctx, client, sid)
}

// gotoChatList 回到聊天列表：先关搜索/新聊天页，再从会话页点返回。
func gotoChatList(ctx context.Context, client *Client, sid string) error {
	if dismissPicker(ctx, client, sid) {
		return nil
	}
	if leaveChatTheme(ctx, client, sid) && !composerPresent(ctx, client, sid) {
		return nil
	}
	if !composerPresent(ctx, client, sid) {
		return nil // 不在聊天页，也没有搜索取消键
	}
	if clickAnySelector(ctx, client, sid, whatsappBackToChats, 1500*time.Millisecond) {
		// 误点进主题时先退出；确认消息输入框消失才算回到列表/非会话页。
		_ = leaveChatTheme(ctx, client, sid)
		if !composerPresent(ctx, client, sid) {
			return nil
		}
	}
	// 导航栏左侧返回（会话页第一个按钮通常是回列表；主题页则是回会话）。
	if tapNavBarLeading(ctx, client, sid) {
		time.Sleep(1200 * time.Millisecond)
		if leaveChatTheme(ctx, client, sid) {
			time.Sleep(800 * time.Millisecond)
		}
		if !composerPresent(ctx, client, sid) {
			return nil
		}
		// 仍在会话：再点一次导航栏返回。
		if tapNavBarLeading(ctx, client, sid) {
			time.Sleep(1200 * time.Millisecond)
			if !composerPresent(ctx, client, sid) {
				return nil
			}
		}
	}
	if clickAnySelector(ctx, client, sid, whatsappChatsTab, 1200*time.Millisecond) && !composerPresent(ctx, client, sid) {
		return nil
	}
	if tapTopLeftBack(ctx, client, sid) && !composerPresent(ctx, client, sid) {
		return nil
	}
	if edgeSwipeBack(ctx, client, sid) && !composerPresent(ctx, client, sid) {
		return nil
	}
	return fmt.Errorf("back button not found")
}

func composerPresent(ctx context.Context, client *Client, sid string) bool {
	using, value := splitSelector(whatsappSelectors.messageInput)
	id, err := client.FindElement(ctx, sid, using, value)
	return err == nil && id != ""
}

func clickAnySelector(ctx context.Context, client *Client, sid string, sels []string, wait time.Duration) bool {
	for _, sel := range sels {
		using, value := splitSelector(sel)
		bid, err := client.FindElement(ctx, sid, using, value)
		if err != nil || bid == "" {
			continue
		}
		if client.Click(ctx, sid, bid) == nil {
			if wait > 0 {
				time.Sleep(wait)
			}
			return true
		}
	}
	return false
}

func tapNavBarLeading(ctx context.Context, client *Client, sid string) bool {
	id, err := findElementBySelector(ctx, client, sid, `class chain: **/XCUIElementTypeNavigationBar/XCUIElementTypeButton[1]`)
	if err != nil || id == "" {
		return false
	}
	return client.Click(ctx, sid, id) == nil
}

// tapTopLeftBack 点导航栏左侧空白/返回热区（无障碍名异常时的坐标兜底）。
func tapTopLeftBack(ctx context.Context, client *Client, sid string) bool {
	bar, err := findElementBySelector(ctx, client, sid, `class chain: **/XCUIElementTypeNavigationBar[1]`)
	if err != nil || bar == "" {
		return false
	}
	x, y, w, h, err := client.ElementRect(ctx, sid, bar)
	if err != nil || w <= 0 || h <= 0 {
		return false
	}
	tx := int(x + minFloat(44, w*0.12))
	ty := int(y + h/2)
	if client.CoordinateTap(ctx, sid, tx, ty) != nil {
		return false
	}
	time.Sleep(1200 * time.Millisecond)
	return true
}

// edgeSwipeBack 从屏幕左缘向右滑（iOS 返回手势兜底）。
func edgeSwipeBack(ctx context.Context, client *Client, sid string) bool {
	bar, err := findElementBySelector(ctx, client, sid, `class chain: **/XCUIElementTypeNavigationBar[1]`)
	if err != nil || bar == "" {
		return false
	}
	x, y, w, h, err := client.ElementRect(ctx, sid, bar)
	if err != nil || w < 80 {
		return false
	}
	fromX, fromY := int(x+3), int(y+h/2)
	toX, toY := int(x+w*0.65), fromY
	if client.Drag(ctx, sid, fromX, fromY, toX, toY, 0.25) != nil {
		return false
	}
	time.Sleep(1200 * time.Millisecond)
	return true
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func leaveChatTheme(ctx context.Context, client *Client, sid string) bool {
	if !isChatThemeTitle(ChatTitle(ctx, client, sid)) {
		return false
	}
	if !tapNavBarLeading(ctx, client, sid) {
		return false
	}
	time.Sleep(1200 * time.Millisecond)
	return true
}

func dismissPicker(ctx context.Context, client *Client, sid string) bool {
	for _, sel := range whatsappLeavePicker {
		using, value := splitSelector(sel)
		id, err := client.FindElement(ctx, sid, using, value)
		if err != nil || id == "" {
			continue
		}
		if client.Click(ctx, sid, id) == nil {
			time.Sleep(800 * time.Millisecond)
			return true
		}
	}
	return false
}

// openNewChatByPhone 新聊天 -> 搜索号码 -> 点非自己的联系人。搜到本机号返回 ErrSendToSelf。
// 硬性规则：搜索框输入号码后禁止「什么都不做」——必须选中打开会话，或关闭搜索并返回明确错误。
func openNewChatByPhone(ctx context.Context, client *Client, sid, digits string) error {
	query := nationalDigits(digits)
	if query == "" {
		query = digitsOf(digits)
	}
	if query == "" {
		return fmt.Errorf("%w: 搜索号码为空", errOpenChatMiss)
	}

	typed, opened := false, false
	defer func() {
		// 已输入搜索词却未打开会话：必须关掉搜索/新聊天页，禁止停在搜索页静默挂起。
		if typed && !opened {
			_ = dismissPicker(ctx, client, sid)
		}
	}()

	using, value := splitSelector(whatsappNewChatButton)
	nc, err := client.FindElement(ctx, sid, using, value)
	if err != nil || nc == "" {
		return fmt.Errorf("%w: 未找到新聊天入口", errOpenChatMiss)
	}
	if err := client.Click(ctx, sid, nc); err != nil {
		return fmt.Errorf("%w: 打开新聊天失败", errOpenChatMiss)
	}
	time.Sleep(1500 * time.Millisecond)
	sf, err := waitAnyElement(ctx, client, sid, whatsappSearchFieldFallbacks, 4*time.Second)
	if err != nil || sf == "" {
		_ = dismissPicker(ctx, client, sid)
		return fmt.Errorf("%w: 未找到搜索框", errOpenChatMiss)
	}
	if err := client.Click(ctx, sid, sf); err != nil {
		_ = dismissPicker(ctx, client, sid)
		return fmt.Errorf("%w: 点击搜索框失败", errOpenChatMiss)
	}
	time.Sleep(800 * time.Millisecond)
	// 联系人通常按国内 11 位保存；用 86+11 搜索会排到「未保存/未知」行。
	if err := client.TypeText(ctx, sid, sf, query); err != nil {
		_ = dismissPicker(ctx, client, sid)
		return fmt.Errorf("搜索框输入号码失败(%s): %w", query, err)
	}
	typed = true

	// 老设备（iPhone 7 / iOS 15）搜索结果出得慢：先等 4s，未命中再补等 2s 重试一次。
	var decision pickerDecision
	for _, wait := range []time.Duration{4 * time.Second, 2 * time.Second} {
		time.Sleep(wait)
		cells, serr := sourceCells(ctx, client, sid)
		if serr != nil {
			continue
		}
		decision = decidePickerResult(cells, digits)
		if decision != pickerNone {
			break
		}
	}
	if decision == pickerSelf {
		return ErrSendToSelf
	}
	if decision != pickerHit {
		return fmt.Errorf("%w: 搜索号码 %s 后未找到可发送联系人", errOpenChatMiss, query)
	}
	cellID := ""
	if id, ferr := findElementBySelector(ctx, client, sid, whatsappContactCell); ferr == nil {
		cellID = id
	}
	if cellID == "" {
		return fmt.Errorf("%w: 搜索命中但联系人单元格不可点(%s)", errOpenChatMiss, query)
	}
	// 点 cell 中部（联系人本体）。右侧动作常是「发消息给未保存号码」，会进未知会话。
	if x, y, w, h, err := client.ElementRect(ctx, sid, cellID); err == nil {
		tx, ty := int(x+w/2), int(y+h/2)
		if client.CoordinateTap(ctx, sid, tx, ty) == nil && chatOpenedFor(ctx, client, sid, digits, 6*time.Second, "") {
			opened = true
			return nil
		}
	}
	for _, name := range []string{"PickerView_ContactCell_Name", "PickerView_ContactCell_PhoneNumber", "聊天"} {
		sel := "class chain: **/XCUIElementTypeCell[`name == 'PickerView_ContactCell'`]/**/XCUIElementTypeStaticText[`name == '" + name + "'`]"
		act, err := findElementBySelector(ctx, client, sid, sel)
		if err == nil && act != "" {
			if client.Click(ctx, sid, act) == nil && chatOpenedFor(ctx, client, sid, digits, 6*time.Second, "") {
				opened = true
				return nil
			}
		}
	}
	return fmt.Errorf("%w: 搜索命中但无法打开会话(%s)", errOpenChatMiss, query)
}

// ---- 源码树解析（用于聊天列表定位）----

type sourceCell struct {
	name       string
	label      string
	value      string
	titleHint  string // ChatSessionCell_Name / 联系人名，优先于无障碍拆开的号码
	hasMessage bool
	visible    bool
}

var errOpenChatMiss = errors.New("chat or contact not found")

// ShouldFallbackChatList 指定号码其实是本机号时，改走聊天列表好友，不要在「新聊天」搜号卡住。
func ShouldFallbackChatList(err error) bool {
	return errors.Is(err, ErrSendToSelf)
}

type pickerDecision int

const (
	pickerNone pickerDecision = iota
	pickerSelf
	pickerHit
)

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
					cur = &sourceCell{
						name:    xmlAttr(t, "name"),
						label:   xmlAttr(t, "label"),
						value:   xmlAttr(t, "value"),
						visible: xmlAttr(t, "visible") != "false",
					}
					curDepth = depth
				}
			} else if cur != nil {
				childName := xmlAttr(t, "name")
				if t.Name.Local == "XCUIElementTypeStaticText" && childName == "WAChatSessionCell_Message" {
					cur.hasMessage = true
				}
				if childName == "ChatSessionCell_Name" || childName == "PickerView_ContactCell_Name" {
					if lab := strings.TrimSpace(xmlAttr(t, "label")); lab != "" {
						cur.titleHint = lab
					}
				}
				if (childName == "ChatSessionCell_Name" || childName == "PickerView_ContactCell_Name" ||
					childName == "PickerView_ContactCell_PhoneNumber") && digitsOf(cur.label) == "" && digitsOf(cur.name) == "" {
					if lab := xmlAttr(t, "label"); lab != "" {
						cur.label = lab
					}
				}
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

func nationalDigits(digits string) string {
	d := digitsOf(digits)
	if len(d) >= 11 {
		return d[len(d)-11:]
	}
	return d
}

func phoneDigitsMatch(got, want string) bool {
	g, w := digitsOf(got), digitsOf(want)
	if g == "" || w == "" {
		return false
	}
	if g == w {
		return true
	}
	gn, wn := nationalDigits(g), nationalDigits(w)
	return len(gn) == 11 && gn == wn
}

func isUnknownChatTitle(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	low := strings.ToLower(t)
	switch low {
	case "unknown", "unsaved", "未知", "未知联系人", "陌生人":
		return true
	}
	return strings.HasPrefix(low, "unknown") || strings.HasPrefix(t, "未知")
}

func cellMatchesPhone(c sourceCell, digits string) bool {
	return phoneDigitsMatch(c.name, digits) || phoneDigitsMatch(c.label, digits) || phoneDigitsMatch(c.value, digits)
}

func stripBidi(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u200e', '\u200f', '\u202a', '\u202b', '\u202c', '\u2066', '\u2067', '\u2068', '\u2069':
			return -1
		default:
			return r
		}
	}, s)
}

func isSelfChatCell(c sourceCell) bool {
	for _, s := range []string{c.name, c.label, c.value} {
		plain := stripBidi(s)
		if isSelfChatTitle(plain) || strings.Contains(plain, "已发送到你") || strings.Contains(plain, "给自己发消息") {
			return true
		}
	}
	return false
}

func decidePickerResult(cells []sourceCell, digits string) pickerDecision {
	sawSelf := false
	for _, c := range cells {
		if c.name != "PickerView_ContactCell" || !c.visible {
			continue
		}
		if isSelfChatCell(c) {
			sawSelf = true
			continue
		}
		if cellMatchesPhone(c, digits) {
			return pickerHit
		}
	}
	if sawSelf {
		return pickerSelf
	}
	return pickerNone
}

func chatDisplayTitle(c sourceCell) string {
	// 联系人名优先于无障碍拆开的号码；没有名字时再把号码收成可读格式。
	if t := compactChatTitle(c.name); t != "" && !isSpacedDigitTitle(c.name) {
		return t
	}
	if t := compactChatTitle(c.titleHint); t != "" {
		return t
	}
	if t := compactChatTitle(c.label); t != "" {
		return t
	}
	return compactChatTitle(c.name)
}

func isSpacedDigitTitle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	d := digitsOf(s)
	if len(d) < 8 {
		return false
	}
	other := 0
	for _, r := range s {
		if r >= '0' && r <= '9' || r == ' ' || r == ',' || r == '+' || r == '-' || r == '\u00a0' {
			continue
		}
		other++
	}
	return other == 0
}

// compactChatTitle 把无障碍树里拆开的 "+ 8 6,1 5 2,..." 收成可读号码，普通名字原样返回。
func compactChatTitle(s string) string {
	s = strings.TrimSpace(stripBidi(s))
	if s == "" || s == "filter" {
		return ""
	}
	if !isSpacedDigitTitle(s) {
		return s
	}
	d := digitsOf(s)
	if len(d) == 13 && strings.HasPrefix(d, "86") {
		return "+86 " + d[2:5] + " " + d[5:9] + " " + d[9:]
	}
	if len(d) == 11 {
		return d[:3] + " " + d[3:7] + " " + d[7:]
	}
	return d
}

func isChatThemeTitle(s string) bool {
	t := strings.ToLower(stripBidi(strings.TrimSpace(s)))
	if t == "" {
		return false
	}
	return strings.Contains(t, "聊天主题") || strings.Contains(t, "chat theme") ||
		strings.Contains(t, "聊天气泡") || strings.Contains(t, "chat bubble") ||
		strings.Contains(t, "wallpaper") || strings.Contains(t, "壁纸")
}

// isBackToChatsLabel 判断无障碍文案是否像「回聊天列表」而非「聊天主题」入口。
func isBackToChatsLabel(s string) bool {
	t := strings.ToLower(stripBidi(strings.TrimSpace(s)))
	if t == "" || isChatThemeTitle(s) {
		return false
	}
	for _, bad := range []string{"主题", "theme", "气泡", "bubble", "wallpaper", "壁纸", "设置", "settings"} {
		if strings.Contains(t, bad) {
			return false
		}
	}
	switch t {
	case "聊天", "chats", "对话", "back", "返回":
		return true
	}
	if strings.Contains(t, "聊天") || strings.Contains(t, "chats") || strings.Contains(t, "对话") {
		return true
	}
	if strings.Contains(t, "back") || strings.Contains(t, "返回") {
		return true
	}
	return false
}

func looksLikeGroupChat(s string) bool {
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	keys := []string{"群", "group", "broadcast", "广播", "频道", "channel", "community", "社区"}
	for _, k := range keys {
		if strings.Contains(low, k) || strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func isSelfChatTitle(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	low := strings.ToLower(t)
	switch low {
	case "you", "me", "myself", "你", "我", "自己":
		return true
	}
	plain := stripBidi(t)
	lowPlain := strings.ToLower(plain)
	for _, p := range []string{"message yourself", "messaging yourself", "(you)", "（you）", "(你)", "（你）", "给自己发", "发给自己", "发送给自己"} {
		if strings.Contains(low, p) || strings.Contains(t, p) || strings.Contains(lowPlain, p) || strings.Contains(plain, p) {
			return true
		}
	}
	return false
}

// isFriendChatCell 聊天列表里可群发的 1:1 好友会话：排除筛选条、群、未知、自己。
func isFriendChatCell(c sourceCell) bool {
	title := chatDisplayTitle(c)
	if title == "" || title == "filter" {
		return false
	}
	if isSelfChatCell(c) || isSelfChatTitle(title) || isSelfChatTitle(c.label) || isSelfChatTitle(c.name) {
		return false
	}
	if isChatThemeTitle(title) || isChatThemeTitle(c.label) || isChatThemeTitle(c.name) {
		return false
	}
	if looksLikeGroupChat(title) || looksLikeGroupChat(c.label) {
		return false
	}
	if isUnknownChatTitle(title) || isUnknownChatTitle(c.label) {
		return false
	}
	return c.hasMessage || digitsOf(c.name) != "" || digitsOf(c.label) != ""
}

type chatTarget struct {
	title  string
	digits string
}

func friendChatTargets(cells []sourceCell) []chatTarget {
	var out []chatTarget
	seen := map[string]bool{}
	for _, c := range cells {
		if !isFriendChatCell(c) {
			continue
		}
		t := chatTarget{title: chatDisplayTitle(c)}
		d := digitsOf(c.name)
		if d == "" {
			d = digitsOf(c.label)
		}
		t.digits = d
		key := t.title
		if n := nationalDigits(t.digits); len(n) == 11 {
			key = n
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

func openChatByTarget(ctx context.Context, client *Client, sid string, t chatTarget) error {
	cells, err := sourceCells(ctx, client, sid)
	if err != nil {
		return err
	}
	for i, c := range cells {
		if t.digits != "" && cellMatchesPhone(c, t.digits) {
			return tapCell(ctx, client, sid, i+1)
		}
		if chatDisplayTitle(c) == t.title {
			return tapCell(ctx, client, sid, i+1)
		}
	}
	return fmt.Errorf("chat list missing %q", t.title)
}

const (
	DefaultChatListMaxFriends = 30
	HardChatListMaxFriends    = 100
)

func ClampChatListMax(n int) int {
	if n <= 0 {
		return DefaultChatListMaxFriends
	}
	if n > HardChatListMaxFriends {
		return HardChatListMaxFriends
	}
	return n
}

func capChatTargets(ts []chatTarget, max int) []chatTarget {
	max = ClampChatListMax(max)
	if len(ts) > max {
		return ts[:max]
	}
	return ts
}

// SendToChatListFriends 无指定号码时：打开聊天列表，向当前能看到的 1:1 好友会话逐条发送。
// maxFriends≤0 按 30；超过 100 按 100。不滚动加载更早的会话；群/筛选条/未知/自己跳过。
func SendToChatListFriends(ctx context.Context, client *Client, content string, assist SendAssist, maxFriends int) (sent int, names []string, err error) {
	sid, _, err := createWhatsAppSession(ctx, client)
	if err != nil {
		return 0, nil, fmt.Errorf("create wda session: %w", err)
	}
	defer func() { _ = client.DeleteSession(context.WithoutCancel(ctx), sid) }()
	if err := gotoChatList(ctx, client, sid); err != nil {
		return 0, nil, err
	}
	cells, err := sourceCells(ctx, client, sid)
	if err != nil {
		return 0, nil, err
	}
	targets := capChatTargets(friendChatTargets(cells), maxFriends)
	if len(targets) == 0 {
		return 0, nil, fmt.Errorf("聊天列表未找到好友会话")
	}
	var lastErr error
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return sent, names, ctx.Err()
		}
		if err := gotoChatList(ctx, client, sid); err != nil {
			lastErr = err
			continue
		}
		if err := sendOneChatListFriend(ctx, client, sid, tgt, content, assist); err != nil {
			lastErr = err
			continue
		}
		sent++
		name := compactChatTitle(ChatTitle(ctx, client, sid))
		if name == "" {
			name = tgt.title
		}
		names = append(names, name)
	}
	// 整单结束后回到聊天列表，用户能看到刚发出的最后一条预览。
	if err := gotoChatList(ctx, client, sid); err != nil && lastErr == nil {
		lastErr = err
	}
	if sent == 0 {
		if lastErr != nil {
			return 0, nil, lastErr
		}
		return 0, nil, fmt.Errorf("聊天列表未找到好友会话")
	}
	if lastErr != nil {
		return sent, names, fmt.Errorf("已发送 %d/%d，部分失败: %w", sent, len(targets), lastErr)
	}
	return sent, names, nil
}

func waitForComposer(ctx context.Context, client *Client, sid string, timeout time.Duration) bool {
	_, err := waitElement(ctx, client, sid, whatsappSelectors.messageInput, timeout)
	return err == nil
}

// sendOneChatListFriend 打开目标会话，确认已在聊天页再发送。
// 点进列表后仍停在列表/搜索时，先让视觉模型判断界面再重试一次。
func sendOneChatListFriend(ctx context.Context, client *Client, sid string, tgt chatTarget, content string, assist SendAssist) error {
	if err := openChatByTarget(ctx, client, sid, tgt); err != nil {
		return err
	}
	if leaveChatTheme(ctx, client, sid) {
		return fmt.Errorf("打开 %q 后误入聊天主题", tgt.title)
	}
	if !waitForComposer(ctx, client, sid, 5*time.Second) {
		if assist != nil {
			r := applyScreenAssist(ctx, client, sid, assist)
			if strings.EqualFold(r.Kind, "list") {
				if err := openChatByTarget(ctx, client, sid, tgt); err != nil {
					return err
				}
			}
		}
		if !waitForComposer(ctx, client, sid, 5*time.Second) {
			return fmt.Errorf("打开 %q 后未进入聊天页", tgt.title)
		}
	}
	if err := TypeAndSend(ctx, client, sid, content, assist); err != nil {
		if assist != nil && strings.Contains(err.Error(), "find message input") {
			_ = applyScreenAssist(ctx, client, sid, assist)
			if waitForComposer(ctx, client, sid, 5*time.Second) {
				return TypeAndSend(ctx, client, sid, content, assist)
			}
		}
		return err
	}
	return nil
}

func chatIndexByPhone(ctx context.Context, client *Client, sid, digits string) (int, error) {
	cells, err := sourceCells(ctx, client, sid)
	if err != nil {
		return 0, err
	}
	for i, c := range cells {
		if isSelfChatCell(c) {
			continue
		}
		if cellMatchesPhone(c, digits) {
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
