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
