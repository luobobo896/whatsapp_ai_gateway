package wda

import (
	"strings"
	"testing"
)

const sampleChatListXML = `<?xml version="1.0" encoding="UTF-8"?>
<XCUIElementTypeApplication name="WA Business" bundleId="net.whatsapp.WhatsAppSMB">
 <XCUIElementTypeTable name="ChatListView_TableView">
  <XCUIElementTypeCell name="" label="filter">
   <XCUIElementTypeButton name="未读" label="未读"/>
   <XCUIElementTypeButton name="群组" label="群组"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="+ 8 6,1 7 6,8 8 5 4,0 7 7 5" label="+ 8 6,1 7 6,8 8 5 4,0 7 7 5">
   <XCUIElementTypeStaticText name="PickerView_ContactCell_Name" label="+86 176 8854 0775"/>
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="你好"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="工作群" label="工作群">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="明天的安排"/>
  </XCUIElementTypeCell>
 </XCUIElementTypeTable>
</XCUIElementTypeApplication>`

func TestParseSourceCells(t *testing.T) {
	cells, err := parseSourceCells(sampleChatListXML)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cells) != 3 {
		t.Fatalf("cells = %d, want 3", len(cells))
	}
	// cell[1] = filter, cell[2] = +8617688540775 会话, cell[3] = 群组
	if digitsOf(cells[0].name) != "" || cells[0].hasMessage {
		t.Fatalf("filter cell should have no digits/message marker: %+v", cells[0])
	}
	if digitsOf(cells[1].name) != "8617688540775" || !cells[1].hasMessage {
		t.Fatalf("chat cell mismatch: %+v", cells[1])
	}
	if digitsOf(cells[2].name) != "" || !cells[2].hasMessage {
		t.Fatalf("group cell mismatch: %+v", cells[2])
	}
}

func TestCompactChatTitle(t *testing.T) {
	if got := compactChatTitle("+ 8 6,1 5 2,1 3 4 7,2 0 8 5"); got != "+86 152 1347 2085" {
		t.Fatalf("spaced phone = %q", got)
	}
	if got := compactChatTitle("罗泓森"); got != "罗泓森" {
		t.Fatalf("name = %q", got)
	}
	if compactChatTitle("filter") != "" || compactChatTitle("  ") != "" {
		t.Fatal("filter/blank should be empty")
	}
}

func TestChatDisplayTitlePrefersNameThenChild(t *testing.T) {
	if got := chatDisplayTitle(sourceCell{name: "张三", label: "+86 176 8854 0775"}); got != "张三" {
		t.Fatalf("saved name: %q", got)
	}
	const xml = `<?xml version="1.0" encoding="UTF-8"?>
<XCUIElementTypeApplication>
 <XCUIElementTypeCell name="+ 8 6,1 9 0,9 8 5 1,3 9 0 9" label="+ 8 6,1 9 0,9 8 5 1,3 9 0 9">
  <XCUIElementTypeStaticText name="ChatSessionCell_Name" label="+86 190 9851 3909"/>
  <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="hi"/>
 </XCUIElementTypeCell>
</XCUIElementTypeApplication>`
	cells, err := parseSourceCells(xml)
	if err != nil || len(cells) != 1 {
		t.Fatalf("parse: %v %+v", err, cells)
	}
	if got := chatDisplayTitle(cells[0]); got != "+86 190 9851 3909" {
		t.Fatalf("child name title = %q cell=%+v", got, cells[0])
	}
}

func TestDigitsOf(t *testing.T) {
	cases := map[string]string{
		"+ 8 6,1 7 6,8 8 5 4,0 7 7 5": "8617688540775",
		"+86 180 7852 6388":           "8618078526388",
		"8613800000001":               "8613800000001",
		"工作群":                         "",
		"":                            "",
	}
	for in, want := range cases {
		if got := digitsOf(in); got != want {
			t.Errorf("digitsOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPhoneDigitsMatch(t *testing.T) {
	if !phoneDigitsMatch("8617688540775", "8617688540775") {
		t.Fatal("full digits should match")
	}
	if !phoneDigitsMatch("17688540775", "8617688540775") {
		t.Fatal("national 11 should match 86+11")
	}
	if !phoneDigitsMatch("+86 176 8854 0775", "8617688540775") {
		t.Fatal("formatted +86 title should match")
	}
	if phoneDigitsMatch("13800000001", "8617688540775") {
		t.Fatal("different national number must not match")
	}
	if phoneDigitsMatch("张三", "8617688540775") {
		t.Fatal("name-only must not match by digits")
	}
	if phoneDigitsMatch("", "8617688540775") {
		t.Fatal("empty must not match")
	}
}

func TestParseSourceCellsSelfChatFromSentToYou(t *testing.T) {
	// 真机聊天列表：自己的会话 name 是资料名，value 是「已发送到你」（带 LTR 不可见字符）。
	const xml = `<?xml version="1.0" encoding="UTF-8"?>
<XCUIElementTypeApplication>
 <XCUIElementTypeTable>
  <XCUIElementTypeCell value="你的消息, 打字自愈优化验证, 已发送到&#x200e;你, 已读" name="罗泓森" label="罗泓森">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="打字自愈优化验证"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell value="你的消息, hello, 已发送到+ 8 6,1 5 2,1 3 4 7,2 0 8 5" name="+ 8 6,1 5 2,1 3 4 7,2 0 8 5" label="+ 8 6,1 5 2,1 3 4 7,2 0 8 5">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="hello"/>
   <XCUIElementTypeOther name="ChatSessionCell_Name" label="+86 152 1347 2085"/>
  </XCUIElementTypeCell>
 </XCUIElementTypeTable>
</XCUIElementTypeApplication>`
	cells, err := parseSourceCells(xml)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 {
		t.Fatalf("cells = %d, want 2", len(cells))
	}
	if !isSelfChatCell(cells[0]) {
		t.Fatalf("罗泓森 + 已发送到你 should be self: %+v", cells[0])
	}
	if isSelfChatCell(cells[1]) {
		t.Fatalf("regular number cell must not be self: %+v", cells[1])
	}
	if !cellMatchesPhone(cells[1], "8615213472085") {
		t.Fatal("ChatSessionCell_Name / cell name digits should match 152")
	}
}

func TestPickerSearchOwnNumberIsSelf(t *testing.T) {
	// 真机新聊天搜本机号 18078526388：可见结果只有「给自己发消息」。
	const xml = `<?xml version="1.0" encoding="UTF-8"?>
<XCUIElementTypeApplication>
 <XCUIElementTypeCell name="PickerView_ContactCell" label="罗泓森 (你), 给自己发消息">
  <XCUIElementTypeStaticText name="PickerView_ContactCell_PhoneNumber" label="给自己发消息"/>
  <XCUIElementTypeStaticText name="PickerView_ContactCell_Name" label="罗泓森 (你)"/>
 </XCUIElementTypeCell>
</XCUIElementTypeApplication>`
	cells, err := parseSourceCells(xml)
	if err != nil {
		t.Fatal(err)
	}
	got := decidePickerResult(cells, "8618078526388")
	if got != pickerSelf {
		t.Fatalf("own number search should be pickerSelf, got %v cells=%+v", got, cells)
	}
}

func TestPickerSearchRegularContactIsHit(t *testing.T) {
	const xml = `<?xml version="1.0" encoding="UTF-8"?>
<XCUIElementTypeApplication>
 <XCUIElementTypeCell name="PickerView_ContactCell" label="+ 8 6,1 5 2,1 3 4 7,2 0 8 5, yangli">
  <XCUIElementTypeStaticText name="PickerView_ContactCell_Name" label="+86 152 1347 2085"/>
 </XCUIElementTypeCell>
</XCUIElementTypeApplication>`
	cells, err := parseSourceCells(xml)
	if err != nil {
		t.Fatal(err)
	}
	if decidePickerResult(cells, "8615213472085") != pickerHit {
		t.Fatalf("saved contact should be pickerHit, cells=%+v", cells)
	}
	if decidePickerResult(cells, "8618078526388") != pickerNone {
		t.Fatal("unrelated number must not hit 152 cell")
	}
}

func TestIsUnknownChatTitle(t *testing.T) {
	for _, s := range []string{"未知", "未知联系人", "Unknown", "unknown", "Unsaved", "陌生人"} {
		if !isUnknownChatTitle(s) {
			t.Fatalf("%q should be unknown", s)
		}
	}
	for _, s := range []string{"", "张三", "+86 176 8854 0775", "聊天"} {
		if isUnknownChatTitle(s) {
			t.Fatalf("%q should not be unknown", s)
		}
	}
}

func TestFriendChatTargetsSkipsGroupsAndFilters(t *testing.T) {
	const xml = `<?xml version="1.0" encoding="UTF-8"?>
<XCUIElementTypeApplication>
 <XCUIElementTypeTable>
  <XCUIElementTypeCell name="" label="filter">
   <XCUIElementTypeButton name="未读" label="未读"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="张三" label="+86 176 8854 0775">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="你好"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="工作群" label="工作群">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="明天的安排"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="+ 8 6,1 3 8,0 0 0 0,0 0 0 1" label="+86 138 0000 0001">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="在吗"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="未知" label="Unknown">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="hi"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="Family Group" label="Family Group">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="ok"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="You" label="Message yourself">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="笔记"/>
  </XCUIElementTypeCell>
 </XCUIElementTypeTable>
</XCUIElementTypeApplication>`
	cells, err := parseSourceCells(xml)
	if err != nil {
		t.Fatal(err)
	}
	got := friendChatTargets(cells)
	if len(got) != 2 {
		t.Fatalf("want 2 friends (self skipped), got %d: %+v", len(got), got)
	}
	if got[0].title != "张三" {
		t.Fatalf("first friend: %+v", got[0])
	}
	if nationalDigits(got[1].digits) != "13800000001" {
		t.Fatalf("second friend digits: %+v", got[1])
	}
}

func TestFriendChatTargetsRealSevenPlusList(t *testing.T) {
	// 真机 iPhone 7 Plus 聊天列表：152、自己（罗泓森/已发送到你）、176。应只发两个好友。
	const xml = `<?xml version="1.0" encoding="UTF-8"?>
<XCUIElementTypeApplication>
 <XCUIElementTypeTable name="ChatListView_TableView">
  <XCUIElementTypeCell name="+ 8 6,1 5 2,1 3 4 7,2 0 8 5" label="+ 8 6,1 5 2,1 3 4 7,2 0 8 5" value="你的消息, hello, 已发送到+ 8 6,1 5 2,1 3 4 7,2 0 8 5">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="hello"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="罗泓森" label="罗泓森" value="你的消息, 打字自愈优化验证, 已发送到&#x200e;你, 已读">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="打字自愈优化验证"/>
  </XCUIElementTypeCell>
  <XCUIElementTypeCell name="+ 8 6,1 7 6,8 8 5 4,0 7 7 5" label="+ 8 6,1 7 6,8 8 5 4,0 7 7 5" value="你的消息, 荔枝, 已发送到+ 8 6,1 7 6,8 8 5 4,0 7 7 5">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="荔枝"/>
  </XCUIElementTypeCell>
 </XCUIElementTypeTable>
</XCUIElementTypeApplication>`
	cells, err := parseSourceCells(xml)
	if err != nil {
		t.Fatal(err)
	}
	got := friendChatTargets(cells)
	if len(got) != 2 {
		t.Fatalf("want 2 friends, got %d: %+v", len(got), got)
	}
	if nationalDigits(got[0].digits) != "15213472085" || nationalDigits(got[1].digits) != "17688540775" {
		t.Fatalf("friends = %+v", got)
	}
}

func TestShouldFallbackChatListOnSelf(t *testing.T) {
	if !ShouldFallbackChatList(ErrSendToSelf) {
		t.Fatal("own number must fall back to chat list")
	}
	if ShouldFallbackChatList(errOpenChatMiss) {
		t.Fatal("unknown number must not spam chat list")
	}
}

func TestClampChatListMax(t *testing.T) {
	if got := ClampChatListMax(0); got != 30 {
		t.Fatalf("default: %d", got)
	}
	if got := ClampChatListMax(-3); got != 30 {
		t.Fatalf("negative: %d", got)
	}
	if got := ClampChatListMax(12); got != 12 {
		t.Fatalf("custom: %d", got)
	}
	if got := ClampChatListMax(1000); got != 100 {
		t.Fatalf("hard cap: %d", got)
	}
}

func TestCapChatTargets(t *testing.T) {
	in := make([]chatTarget, 50)
	for i := range in {
		in[i] = chatTarget{title: string(rune('A' + i%26))}
	}
	got := capChatTargets(in, 30)
	if len(got) != 30 {
		t.Fatalf("len=%d want 30", len(got))
	}
}

func TestIsSelfChatTitle(t *testing.T) {
	for _, s := range []string{"You", "you", "我", "你", "自己", "Message yourself", "给自己发消息", "张三 (You)"} {
		if !isSelfChatTitle(s) {
			t.Fatalf("%q should be self", s)
		}
	}
	for _, s := range []string{"", "张三", "Young", "Your Shop", "+86 176 8854 0775"} {
		if isSelfChatTitle(s) {
			t.Fatalf("%q should not be self", s)
		}
	}
}

func TestIsChatThemeTitle(t *testing.T) {
	for _, s := range []string{"聊天主题", "\u200e聊天主题", "Chat theme", "聊天气泡", "Wallpaper", "壁纸"} {
		if !isChatThemeTitle(s) {
			t.Fatalf("%q should be chat theme", s)
		}
	}
	for _, s := range []string{"", "张三", "聊天", "Chats", "+86 152 1347 2085"} {
		if isChatThemeTitle(s) {
			t.Fatalf("%q should not be chat theme", s)
		}
	}
}

func TestFriendChatTargetsSkipsChatTheme(t *testing.T) {
	cells := []sourceCell{{
		name: "聊天主题", label: "聊天主题", hasMessage: true, visible: true,
	}}
	if got := friendChatTargets(cells); len(got) != 0 {
		t.Fatalf("theme row must not be a send target: %+v", got)
	}
}
func TestIsBackToChatsLabel(t *testing.T) {
	for _, s := range []string{"聊天", "\u200e聊天", "Chats", "\u200eChats", "对话", "Back", "返回", "Back to Chats"} {
		if !isBackToChatsLabel(s) {
			t.Fatalf("%q should be back-to-chats", s)
		}
	}
	for _, s := range []string{"", "聊天主题", "\u200e聊天主题", "Chat theme", "聊天气泡", "Wallpaper", "壁纸", "设置", "张三"} {
		if isBackToChatsLabel(s) {
			t.Fatalf("%q must not be back-to-chats", s)
		}
	}
}

func TestWhatsappBackToChatsPredicatesUseGroupedNot(t *testing.T) {
	joined := strings.Join(whatsappBackToChats, "\n")
	if strings.Contains(joined, "NOT label CONTAINS") || strings.Contains(joined, "NOT name CONTAINS") {
		t.Fatalf("NSPredicate NOT must wrap CONTAINS in parentheses; got:\n%s", joined)
	}
	if !strings.Contains(joined, "NOT (label CONTAINS") {
		t.Fatalf("expected grouped NOT (label CONTAINS ...); got:\n%s", joined)
	}
	if !strings.Contains(joined, "XCUIElementTypeNavigationBar") {
		t.Fatalf("back selectors should prefer NavigationBar scope")
	}
}


func TestLooksLikeGroupChat(t *testing.T) {
	for _, s := range []string{"工作群", "Family Group", "广播列表", "My Channel"} {
		if !looksLikeGroupChat(s) {
			t.Fatalf("%q should be group", s)
		}
	}
	for _, s := range []string{"张三", "+86 176 8854 0775", ""} {
		if looksLikeGroupChat(s) {
			t.Fatalf("%q should not be group", s)
		}
	}
}

func TestParseSourceCellsMatchesLabelPhone(t *testing.T) {
	const xml = `<?xml version="1.0" encoding="UTF-8"?>
<XCUIElementTypeApplication>
 <XCUIElementTypeTable>
  <XCUIElementTypeCell name="张三" label="+86 176 8854 0775">
   <XCUIElementTypeStaticText name="WAChatSessionCell_Message" label="你好"/>
  </XCUIElementTypeCell>
 </XCUIElementTypeTable>
</XCUIElementTypeApplication>`
	cells, err := parseSourceCells(xml)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 1 {
		t.Fatalf("cells=%d", len(cells))
	}
	if !cellMatchesPhone(cells[0], "8617688540775") {
		t.Fatalf("saved contact should match via label: %+v", cells[0])
	}
}
