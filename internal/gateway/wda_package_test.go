package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 网关侧 wda:config 闭环：从云平台下载 → 校验 sha256 → 原子替换 state/wda.ipa → 记录版本。
func TestApplyWdaPackage(t *testing.T) {
	ipa := []byte("----FAKE-WDA-IPA----")
	sha := sha256.Sum256(ipa)
	shaHex := hex.EncodeToString(sha[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/wda/package/download" {
			w.Write(ipa)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	state := t.TempDir()
	cfg, err := OpenConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cloud.WSURL = "http://" + strings.TrimPrefix(srv.URL, "http://")
	gw := New(cfg, nil, nil, nil, nil)

	err = gw.ApplyWdaPackage(context.Background(), gatewayWdaPackage{
		Version: shaHex[:8], SignMode: "personal",
		Download: "/api/wda/package/download", SHA256: shaHex,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(state, "wda.ipa"))
	if string(got) != string(ipa) {
		t.Fatalf("wda.ipa content mismatch")
	}
	if v, _ := cfg.ReadExtra("wda_package_version"); v != shaHex[:8] {
		t.Fatalf("wda_package_version = %q", v)
	}
	if sm, _ := cfg.ReadExtra("wda_package_sign_mode"); sm != "personal" {
		t.Fatalf("sign_mode = %q", sm)
	}
}

// 网关管理页展示的 WDA 包状态。
func TestWdaStatus(t *testing.T) {
	state := t.TempDir()
	cfg, err := OpenConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	gw := New(cfg, nil, nil, nil, nil)
	_ = cfg.WriteExtra("wda_package_version", "abc12345")
	_ = cfg.WriteExtra("wda_package_sign_mode", "personal")
	s := gw.WdaStatus()
	if s["version"] != "abc12345" || s["sign_mode"] != "personal" || s["installed"] != true {
		t.Fatalf("bad wda status: %+v", s)
	}
}
