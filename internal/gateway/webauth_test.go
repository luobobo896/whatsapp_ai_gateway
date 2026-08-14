package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newPlatformServer 模拟 HK 平台 /api/auth/login：邮箱+密码匹配返回 200，否则 401。
func newPlatformServer(t *testing.T, email, password string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Email == email && body.Password == password {
			w.WriteHeader(http.StatusNoContent) // 与真实平台一致：成功返回 204
			return
		}
		writeJSONStatus(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]string{"code": "AUTH_INVALID", "message": "邮箱或密码不正确。"},
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

// newAuthTestServer 网关鉴权测试服务器；cloudWSURL 为空 = 开放模式。
func newAuthTestServer(t *testing.T, cloudWSURL string) (*httptest.Server, *webSessions) {
	t.Helper()
	cfg := &Config{Cloud: CloudConfig{WSURL: cloudWSURL, Enabled: cloudWSURL != ""}}
	auth := NewWebAuth(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", auth.HandleLogin)
	mux.HandleFunc("/api/logout", auth.HandleLogout)
	mux.HandleFunc("/api/session", auth.HandleSession)
	mux.HandleFunc("/api/cloud", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"connected": true})
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/login" || r.URL.Path == "/api/session" {
			mux.ServeHTTP(w, r)
			return
		}
		if !auth.Authenticated(r) {
			writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !auth.CSRFValid(r) {
			writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "invalid csrf token"})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, auth.ss
}

// authTestClient 返回带 cookie jar 的客户端（httptest Server.Client 在部分 Go 版本不带 jar）。
func authTestClient(ts *httptest.Server) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Transport: ts.Client().Transport}
}

// TestWebAuthLoginFlow 未登录 401 → 平台校验不过 401 → 正确账号密码 200+会话 → 通过 → 登出后 401。
func TestWebAuthLoginFlow(t *testing.T) {
	platform := newPlatformServer(t, "admin@whatsapp-ai.local", "secret-pass")
	ts, _ := newAuthTestServer(t, platform.URL+"/api/ios-agent/v1/gateway/ws")
	client := authTestClient(ts)

	// 未登录：401。
	resp, err := client.Get(ts.URL + "/api/cloud")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status = %v err=%v, want 401", resp.StatusCode, err)
	}

	// 错误密码：网关转发平台校验，401。
	resp, err = client.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"email":"admin@whatsapp-ai.local","password":"wrong"}`))
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad password status = %v err=%v", resp.StatusCode, err)
	}

	// 正确账号密码：200 + 网关会话 cookie + CSRF token。
	resp, err = client.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"email":"admin@whatsapp-ai.local","password":"secret-pass"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %v err=%v", resp.StatusCode, err)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("login must set session cookie")
	}
	var loginResp struct {
		CsrfToken string `json:"csrfToken"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&loginResp)
	if loginResp.CsrfToken == "" {
		t.Fatal("login must return csrf token")
	}

	// 带 cookie：200。
	resp, err = client.Get(ts.URL + "/api/cloud")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("auth status = %v err=%v", resp.StatusCode, err)
	}

	// 登出（带 CSRF）后：401。
	loReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/logout", nil)
	loReq.Header.Set("X-CSRF-Token", loginResp.CsrfToken)
	if _, err = client.Do(loReq); err != nil {
		t.Fatal(err)
	}
	resp, err = client.Get(ts.URL + "/api/cloud")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout status = %v err=%v, want 401", resp.StatusCode, err)
	}
}

// TestWebAuthSessionReturnsCSRF 登录后 /api/session 必须回传 CSRF token：
// 前端只把 token 存在 JS 变量里，页面刷新即丢，不回传会导致刷新后所有 POST 403。
func TestWebAuthSessionReturnsCSRF(t *testing.T) {
	platform := newPlatformServer(t, "admin@whatsapp-ai.local", "secret-pass")
	ts, _ := newAuthTestServer(t, platform.URL+"/api/ios-agent/v1/gateway/ws")
	client := authTestClient(ts)

	resp, err := client.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"email":"admin@whatsapp-ai.local","password":"secret-pass"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %v err=%v", resp.StatusCode, err)
	}
	var loginResp struct {
		CsrfToken string `json:"csrfToken"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&loginResp)
	if loginResp.CsrfToken == "" {
		t.Fatal("login must return csrf token")
	}

	// 模拟页面刷新：只剩 cookie，从 /api/session 找回 CSRF 后 POST 必须通过。
	sr, err := client.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Authenticated bool   `json:"authenticated"`
		CsrfToken     string `json:"csrfToken"`
	}
	_ = json.NewDecoder(sr.Body).Decode(&s)
	if !s.Authenticated || s.CsrfToken != loginResp.CsrfToken {
		t.Fatalf("session = %+v, want authenticated with same csrf token", s)
	}
	postReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/logout", nil)
	postReq.Header.Set("X-CSRF-Token", s.CsrfToken)
	if resp, err = client.Do(postReq); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("post with session csrf status = %v err=%v", resp.StatusCode, err)
	}
}

// TestWebAuthPlatformUnreachable 平台不可达时登录返回 503，不签发会话。
func TestWebAuthPlatformUnreachable(t *testing.T) {
	platform := newPlatformServer(t, "a@b.c", "pw")
	platformURL := platform.URL
	platform.Close() // 立即关闭，模拟平台不可达
	ts, _ := newAuthTestServer(t, platformURL+"/api/ios-agent/v1/gateway/ws")
	client := authTestClient(ts)

	resp, err := client.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"email":"a@b.c","password":"pw"}`))
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unreachable status = %v err=%v, want 503", resp.StatusCode, err)
	}
}

// TestWebAuthOpenMode 未配置云通道时开放访问，session 返回 passwordRequired=false。
func TestWebAuthOpenMode(t *testing.T) {
	ts, _ := newAuthTestServer(t, "")
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
	platform := newPlatformServer(t, "a@b.c", "pw")
	ts, ss := newAuthTestServer(t, platform.URL+"/api/ios-agent/v1/gateway/ws")
	token, _ := ss.create()
	// 手动把过期时间拨到过去。
	ss.mu.Lock()
	sess := ss.tokens[token]
	sess.exp = time.Now().Add(-24 * time.Hour)
	ss.tokens[token] = sess
	ss.mu.Unlock()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/cloud", nil)
	req.AddCookie(&http.Cookie{Name: webSessionCookie, Value: token})
	resp, err := ts.Client().Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session status = %v err=%v", resp.StatusCode, err)
	}
}

// TestWebAuthLoginAutoProvision 登录成功后自动调用平台签发网关凭证并回传 token。
func TestWebAuthLoginAutoProvision(t *testing.T) {
	var platform *httptest.Server
	platform = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			w.WriteHeader(http.StatusNoContent)
		case "/api/ios-agent/v1/gateway/register":
			var body struct {
				Email    string `json:"email"`
				Password string `json:"password"`
				Name     string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Email != "admin@whatsapp-ai.local" || body.Password != "secret-pass" || body.Name != "macmini-01" {
				writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "bad"})
				return
			}
			writeJSON(w, map[string]any{"token": "auto-token-123"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer platform.Close()

	cfg := &Config{Cloud: CloudConfig{WSURL: platform.URL + "/api/ios-agent/v1/gateway/ws", GatewayName: "macmini-01", Enabled: true}}
	auth := NewWebAuth(cfg)
	var captured string
	auth.onToken = func(token string) error { captured = token; return nil }

	ts := httptest.NewServer(http.HandlerFunc(auth.HandleLogin))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"email":"admin@whatsapp-ai.local","password":"secret-pass"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login status=%v err=%v", resp.StatusCode, err)
	}
	resp.Body.Close()
	if captured != "auto-token-123" {
		t.Fatalf("captured token = %q, want auto-token-123", captured)
	}
}
