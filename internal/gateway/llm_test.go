package gateway

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestExtractChoiceText(t *testing.T) {
	if got := extractChoiceText(json.RawMessage(`"hello"`)); got != "hello" {
		t.Fatalf("string content: %q", got)
	}
	raw := json.RawMessage(`[{"type":"text","text":"foo"},{"type":"text","text":"bar"}]`)
	if got := extractChoiceText(raw); got != "foobar" {
		t.Fatalf("array content: %q", got)
	}
}

func TestParseScreenReport(t *testing.T) {
	r, err := parseScreenReport("```json\n{\"kind\":\"unknown\",\"unknown\":true,\"action\":\"tap_back\",\"note\":\"未保存\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "unknown" || !r.Unknown || r.Action != "tap_back" {
		t.Fatalf("report: %+v", r)
	}
}

func TestLLMConfigAcceptsCamelCase(t *testing.T) {
	var c LLMConfig
	if err := json.Unmarshal([]byte(`{"baseUrl":"https://qwen.example/v1","apiKey":"k","model":"qwen-vl"}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://qwen.example/v1" || c.APIKey != "k" || c.Model != "qwen-vl" {
		t.Fatalf("camelCase not mapped: %+v", c)
	}
}

func TestLLMCooldownOnPaymentError(t *testing.T) {
	c := NewLLMClient("https://example.invalid/v1", "k", "qwen-vl")
	if !c.Enabled() {
		t.Fatal("fresh client should be enabled")
	}
	c.markUnavailable(fmt.Errorf("llm http 402: Arrearage"))
	if c.Enabled() {
		t.Fatal("payment error should disable assist so send continues without model")
	}
}

func TestLLMAssistNilWhenDisabled(t *testing.T) {
	e := NewExecutor(&Config{}, nil, NewLLMClient("", "", ""), t.TempDir())
	if e.llmAssist() != nil {
		t.Fatal("empty config should not enable assist")
	}
	e.SetLLM(NewLLMClient("https://example/v1", "k", "qwen-vl"))
	if e.llmAssist() == nil {
		t.Fatal("full config should enable assist")
	}
}
