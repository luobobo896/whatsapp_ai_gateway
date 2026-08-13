package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

func newAuthTestServer(t *testing.T, password string) (*httptest.Server, *webSessions, *Config) {
	t.Helper()
	cfg := &Config{Web: WebConfig{Password: password}}
	auth := NewWebAuth(cfg)
	_ = auth
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", auth.HandleLogin)
	mux.HandleFunc("/api/logout", auth.HandleLogout)
	mux.HandleFunc("/api/session", auth.HandleSession)
	mux.HandleFunc("/api/cloud", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"connected": true})
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") &&
			r.URL.Path != "/api/login" && r.URL.Path != "/api/logout" && r.URL.Path != "/api/session" {
			if auth.PasswordRequired() && !auth.ss.valid(auth.cookieToken(r)) {
				writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, auth.ss, cfg
}

// authTestClient 返回带 cookie jar 的客户端（httptest Server.Client 在部分 Go 版本不带 jar）。
func authTestClient(ts *httptest.Server) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Transport: ts.Client().Transport}
}

// TestWebAuthLoginFlow 未登录 401 → 登录拿 cookie → 通过 → 登出后 401。
func TestWebAuthLoginFlow(t *testing.T) {
	ts, _, _ := newAuthTestServer(t, "secret-pass")
	client := authTestClient(ts)

	// 未登录：401。
	resp, err := client.Get(ts.URL + "/api/cloud")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status = %v err=%v, want 401", resp.StatusCode, err)
	}

	// 错误密码：401。
	resp, err = client.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"password":"wrong"}`))
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad password status = %v err=%v", resp.StatusCode, err)
	}

	// 正确密码：200 + cookie。
	resp, err = client.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"password":"secret-pass"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %v err=%v", resp.StatusCode, err)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("login must set session cookie")
	}

	// 带 cookie：200。
	resp, err = client.Get(ts.URL + "/api/cloud")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("auth status = %v err=%v", resp.StatusCode, err)
	}

	// 登出后：401。
	if _, err = client.Post(ts.URL+"/api/logout", "application/json", nil); err != nil {
		t.Fatal(err)
	}
	resp, err = client.Get(ts.URL + "/api/cloud")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout status = %v err=%v, want 401", resp.StatusCode, err)
	}
}

// TestWebAuthOpenMode 未配置密码时开放访问，session 返回 passwordRequired=false。
func TestWebAuthOpenMode(t *testing.T) {
	ts, _, _ := newAuthTestServer(t, "")
	client := authTestClient(ts)
	resp, err := client.Get(ts.URL + "/api/cloud")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("open mode status = %v err=%v", resp.StatusCode, err)
	}
	sr, err := client.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	_ = json.NewDecoder(sr.Body).Decode(&s)
	if s["passwordRequired"] != false {
		t.Fatalf("session = %v", s)
	}
}

// TestWebSessionExpiry 会话过期失效。
func TestWebSessionExpiry(t *testing.T) {
	ts, ss, _ := newAuthTestServer(t, "pw")
	token := ss.create()
	// 手动把过期时间拨到过去。
	ss.mu.Lock()
	ss.tokens[token] = ss.tokens[token].Add(-24 * 3600e9)
	ss.mu.Unlock()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/cloud", nil)
	req.AddCookie(&http.Cookie{Name: webSessionCookie, Value: token})
	resp, err := ts.Client().Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session status = %v err=%v", resp.StatusCode, err)
	}
}
