package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
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

	// 正确账号密码：200 + 网关会话 cookie。
	resp, err = client.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"email":"admin@whatsapp-ai.local","password":"secret-pass"}`))
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
