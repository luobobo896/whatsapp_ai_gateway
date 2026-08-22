package wda

import "testing"

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
