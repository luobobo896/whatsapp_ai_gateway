package gateway

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Web 管理页登录鉴权（会话 cookie）。
// 密码为空 = 开放模式（不要求登录）；设置后 /api/* 需有效会话，登录签发 12 小时会话。

const (
	webSessionCookie = "wda_gateway_session"
	webSessionTTL    = 12 * time.Hour
)

// webSessions 内存会话表（网关单实例；重启后需重新登录）。
type webSessions struct {
	mu     sync.Mutex
	tokens map[string]time.Time
}

func newWebSessions() *webSessions {
	return &webSessions{tokens: map[string]time.Time{}}
}

func (ws *webSessions) create() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	ws.mu.Lock()
	ws.tokens[token] = time.Now().Add(webSessionTTL)
	ws.mu.Unlock()
	return token
}

func (ws *webSessions) valid(token string) bool {
	if token == "" {
		return false
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	exp, ok := ws.tokens[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(ws.tokens, token)
		return false
	}
	return true
}

func (ws *webSessions) revoke(token string) {
	ws.mu.Lock()
	delete(ws.tokens, token)
	ws.mu.Unlock()
}

// WebAuth 包装管理页 HTTP 鉴权。
type WebAuth struct {
	cfg *Config
	ss  *webSessions
}

// NewWebAuth 构造鉴权器。
func NewWebAuth(cfg *Config) *WebAuth {
	return &WebAuth{cfg: cfg, ss: newWebSessions()}
}

// PasswordRequired 是否要求登录。
func (a *WebAuth) PasswordRequired() bool {
	return a.cfg != nil && a.cfg.Web.Password != ""
}

// wrap 给 /api/* 处理器套鉴权（未启用密码时透传）。
func (a *WebAuth) wrap(next http.HandlerFunc) http.HandlerFunc {
	if !a.PasswordRequired() {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if a.ss.valid(a.cookieToken(r)) {
			next(w, r)
			return
		}
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
}

func (a *WebAuth) cookieToken(r *http.Request) string {
	c, err := r.Cookie(webSessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// HandleLogin POST /api/login {username, password} → 签发会话 cookie。
// 账号与 HK 平台保持一致（默认 admin@whatsapp-ai.local），只是登录入口不同。
func (a *WebAuth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.PasswordRequired() {
		writeJSON(w, map[string]any{"ok": true, "passwordRequired": false})
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	userOK := subtle.ConstantTimeCompare([]byte(body.Username), []byte(a.cfg.Web.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(body.Password), []byte(a.cfg.Web.Password)) == 1
	if !userOK || !passOK {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "用户名或密码错误"})
		return
	}
	token := a.ss.create()
	http.SetCookie(w, &http.Cookie{
		Name: webSessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(webSessionTTL.Seconds()),
	})
	writeJSON(w, map[string]any{"ok": true, "passwordRequired": true})
}

// HandleLogout POST /api/logout → 撤销会话。
func (a *WebAuth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	a.ss.revoke(a.cookieToken(r))
	http.SetCookie(w, &http.Cookie{Name: webSessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"ok": true})
}

// HandleSession GET /api/session → 会话状态（前端 401 跳登录用）。
func (a *WebAuth) HandleSession(w http.ResponseWriter, r *http.Request) {
	valid := !a.PasswordRequired() || a.ss.valid(a.cookieToken(r))
	writeJSON(w, map[string]any{"authenticated": valid, "passwordRequired": a.PasswordRequired()})
}

// splitBearer 兼容网关凭证头解析（保持与 cloud 一致的小工具）。
func splitBearer(header string) (string, bool) {
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	return strings.TrimPrefix(header, "Bearer "), true
}
