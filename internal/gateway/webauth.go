package gateway

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Web 管理页登录鉴权：网关本地不存任何账号密码，
// 登录时把邮箱/密码转发到 HK 平台的 /api/auth/login 校验（与平台同一账号体系），
// 校验通过后签发网关自己的会话 cookie（12 小时有效，内存保存）。

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

// WebAuth 包装管理页 HTTP 鉴权（凭证校验转发给平台）。
type WebAuth struct {
	cfg    *Config
	ss     *webSessions
	client *http.Client
}

// NewWebAuth 构造鉴权器。
func NewWebAuth(cfg *Config) *WebAuth {
	return &WebAuth{
		cfg: cfg,
		ss:  newWebSessions(),
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // 不回跟随平台重定向
			},
		},
	}
}

// PasswordRequired 是否要求登录：云通道已配置即要求（与平台同一账号体系）。
func (a *WebAuth) PasswordRequired() bool {
	return a.cfg != nil && a.cfg.Cloud.Enabled && a.cfg.Cloud.WSURL != ""
}

// platformLoginURL 由云通道 ws_url 推导平台登录接口（wss://host/... → https://host/api/auth/login）。
func (a *WebAuth) platformLoginURL() string {
	u, err := url.Parse(a.cfg.Cloud.WSURL)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := "https"
	switch u.Scheme {
	case "http", "ws":
		scheme = "http"
	case "https", "wss":
		scheme = "https"
	}
	return scheme + "://" + u.Host + "/api/auth/login"
}

// wrap 给 /api/* 处理器套鉴权（未配置云通道时透传）。
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

// HandleLogin POST /api/login {email, password} → 转发平台校验 → 签发网关会话 cookie。
// 网关不保存、不记录任何凭证。
func (a *WebAuth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.PasswordRequired() {
		writeJSON(w, map[string]any{"ok": true, "passwordRequired": false})
		return
	}
	loginURL := a.platformLoginURL()
	if loginURL == "" {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "云通道 ws_url 无效，无法定位平台登录服务"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "请求体无效"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, loginURL, bytes.NewReader(body))
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "构造登录请求失败"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "平台登录服务不可达，请稍后重试"})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent:
		// 平台登录成功（200 或 204），签发网关自己的会话。
		token := a.ss.create()
		http.SetCookie(w, &http.Cookie{
			Name: webSessionCookie, Value: token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(webSessionTTL.Seconds()),
		})
		writeJSON(w, map[string]any{"ok": true, "passwordRequired": true})
	case resp.StatusCode == http.StatusUnauthorized:
		// 透传平台的错误文案（邮箱或密码不正确 / 触发限流等）。
		msg := "邮箱或密码不正确"
		var pe struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &pe) == nil && pe.Error.Message != "" {
			msg = pe.Error.Message
		}
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": msg})
	default:
		writeJSONStatus(w, http.StatusServiceUnavailable,
			map[string]string{"error": "平台登录服务异常（HTTP " + strings.TrimSpace(http.StatusText(resp.StatusCode)) + "），请稍后重试"})
	}
}

// HandleLogout POST /api/logout → 撤销网关会话。
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
