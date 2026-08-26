package wda

import "testing"

func TestChatContactIdentity(t *testing.T) {
	if got := chatContactIdentity(chatTarget{digits: "8615213472085", title: "张三"}); got != "15213472085" {
		t.Fatalf("national identity = %q, want 15213472085", got)
	}
	if got := chatContactIdentity(chatTarget{title: "张三"}); got != "title:张三" {
		t.Fatalf("title identity = %q", got)
	}
}

func TestFilterChatTargets(t *testing.T) {
	in := []chatTarget{{digits: "8615213472085"}, {digits: "8617688540775"}}
	got := filterChatTargets(in, map[string]bool{"15213472085": true})
	if len(got) != 1 || chatContactIdentity(got[0]) != "17688540775" {
		t.Fatalf("filter should keep only un-sent: %+v", got)
	}
	if len(filterChatTargets(in, nil)) != 2 {
		t.Fatal("empty skip should keep all")
	}
}

func TestRenderChatContent(t *testing.T) {
	if got := renderChatContent("您好${name}，{name}加油", "张三"); got != "您好张三，张三加油" {
		t.Fatalf("render = %q", got)
	}
	if got := renderChatContent("plain", ""); got != "plain" {
		t.Fatalf("empty name should keep original: %q", got)
	}
}
