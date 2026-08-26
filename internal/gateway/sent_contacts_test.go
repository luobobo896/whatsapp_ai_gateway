package gateway

import (
	"testing"
	"time"
)

// 已发联系人持久化 + chatListSkip 合并“最近窗口已发”。
func TestSentContactsPersistAndSkip(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(nil, nil, nil, dir)
	now := time.Now()
	e.store.markSentContacts("u1", []string{"15213472085", "title:张三"}, now)

	set := e.store.sentContactIdentities("u1", now.Add(-24*time.Hour))
	if !set["15213472085"] || !set["title:张三"] {
		t.Fatalf("missing sent contacts: %+v", set)
	}
	if len(e.store.sentContactIdentities("u2", now.Add(-24*time.Hour))) != 0 {
		t.Fatal("other udid should not see sent contacts")
	}

	skip := e.chatListSkip("u1", map[string]bool{"extra": true})
	if !skip["15213472085"] || !skip["extra"] {
		t.Fatalf("skip should merge store + extra: %+v", skip)
	}
}

// 默认 3 天窗口：3 天前的发送已过期，不再跳过（可再触达）。
func TestSentContactsExpiresAfterWindow(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(nil, nil, nil, dir)
	old := time.Now().Add(-4 * 24 * time.Hour) // 4 天前，默认 3 天窗口外
	e.store.markSentContacts("u1", []string{"15213472085"}, old)
	if len(e.chatListSkip("u1", nil)) != 0 {
		t.Fatal("expired contact (default 3d window) should not be skipped")
	}
}

// 窗口可配置：设为 5 天时，4 天前的发送仍应被跳过。
func TestChatListRepeatWindowConfigurable(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(&Config{Web: WebConfig{ChatListRepeatDays: 5}}, nil, nil, dir)
	old := time.Now().Add(-4 * 24 * time.Hour)
	e.store.markSentContacts("u1", []string{"15213472085"}, old)
	if !e.chatListSkip("u1", nil)["15213472085"] {
		t.Fatal("4d ago should still be skipped with 5d repeat window")
	}
}
