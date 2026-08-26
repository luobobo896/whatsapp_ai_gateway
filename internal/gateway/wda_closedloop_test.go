package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const wdaTestToken = "WDA_PUBLISHER_TEST_TOKEN"

// 模拟云平台 wda 端点：上传(multipart)→记录→下载；均校验 WDA_PUBLISHER_TOKEN。
func newWDAMockCloud(t *testing.T, ipa []byte) (*httptest.Server, map[string]string) {
	t.Helper()
	meta := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/wda/") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+wdaTestToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/wda/package":
			if err := r.ParseMultipartForm(64 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f, _, err := r.FormFile("file")
			if err != nil {
				http.Error(w, "missing file", http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(f)
			_ = f.Close()
			sha := sha256.Sum256(data)
			shaHex := hex.EncodeToString(sha[:])
			meta["data"] = string(data)
			meta["version"] = shaHex[:8]
			meta["sha"] = shaHex
			meta["sign_mode"] = r.FormValue("sign_mode")
			_ = json.NewEncoder(w).Encode(map[string]any{"package": map[string]any{
				"version": meta["version"], "sign_mode": meta["sign_mode"],
				"download": "/api/wda/package/download", "sha256": shaHex,
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/wda/package/download":
			w.Write([]byte(meta["data"]))
		case r.Method == http.MethodGet && r.URL.Path == "/api/wda/udids":
			_ = json.NewEncoder(w).Encode(map[string]any{"udids": []string{"5060c403afdee4c15a0edeab69dba0524e2ce592"}})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, meta
}

// 端到端：上传(云) → 记录版本 → 下载 → 网关替换 state/wda.ipa → 状态显示。
func TestWdaClosedLoop(t *testing.T) {
	ipa := []byte("----NEW-WDA-IPA-FOR-DEVICE-5060----")
	srv, meta := newWDAMockCloud(t, ipa)
	defer srv.Close()

	// 1) 上传到 mock 云（模拟签名机 curl 上传）
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "wda.ipa")
	_, _ = fw.Write(ipa)
	_ = mw.WriteField("sign_mode", "personal")
	_ = mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/wda/package", &buf)
	req.Header.Set("Authorization", "Bearer "+wdaTestToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("upload failed: %v status=%d", err, resp.StatusCode)
	}
	var up struct {
		Package gatewayWdaPackage `json:"package"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&up)
	_ = resp.Body.Close()
	if up.Package.Version == "" || up.Package.SHA256 == "" {
		t.Fatalf("upload did not return package: %+v", up.Package)
	}

	// 2) 网关收到 wda:config，下载并替换 state/wda.ipa
	state := t.TempDir()
	cfg, _ := OpenConfig(state)
	gw := New(cfg, nil, nil, nil, nil)
	pkg := gatewayWdaPackage{
		Version: up.Package.Version, SignMode: up.Package.SignMode,
		Download: srv.URL + "/api/wda/package/download", SHA256: up.Package.SHA256,
		DownloadToken: wdaTestToken,
	}
	if err := gw.ApplyWdaPackage(context.Background(), pkg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(state, "wda.ipa"))
	if string(got) != string(ipa) {
		t.Fatalf("state/wda.ipa mismatch")
	}
	// 3) 管理页状态可见
	st := gw.WdaStatus()
	if st["version"] != up.Package.Version || st["sign_mode"] != "personal" || st["installed"] != true {
		t.Fatalf("wda status = %+v", st)
	}
	// 4) udids 接口可拉取（供签名机）
	u := srv.URL + "/api/wda/udids"
	req2, _ := http.NewRequest("GET", u, nil)
	req2.Header.Set("Authorization", "Bearer "+wdaTestToken)
	r2, _ := http.DefaultClient.Do(req2)
	if r2.StatusCode != 200 {
		t.Fatalf("udids status %d", r2.StatusCode)
	}
	_ = r2.Body.Close()
	_ = meta
}
