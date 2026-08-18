package gateway

import (
	"bytes"
	"fmt"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无 CGO）
)

// Web 管理页登录鉴权：网关本地不存任何账号密码，
// 登录时把邮箱/密码转发到 HK 平台的 /api/auth/login 校验（与平台同一账号体系），
// 校验通过后签发网关自己的会话 cookie（12 小时有效，SQLite 落盘，重启不丢）。
//
// CSRF：签发会话时同步生成一个 CSRF token，前端在 state-changing 请求里带
// X-CSRF-Token 头，服务端与会话内 token 比对，防止跨站请求伪造。

const (
	webSessionCookie = "wda_gateway_session"
	webSessionTTL    = 12 * time.Hour
	csrfHeader       = "X-CSRF-Token"
)

// webSessions SQLite 会话表（网关单实例进程；重启后会话仍在）。
type webSessions struct {
	db *sql.DB
}

// openWebSessions 打开（必要时创建）会话库并建表。
func openWebSessions(dbPath string) (*webSessions, error) {
	if dir := filepath.Dir(dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
	token      TEXT PRIMARY KEY,
	csrf       TEXT NOT NULL,
	expires_at INTEGER NOT NULL
);`); err != nil {
	_ = db.Close()
		return nil, err
	}
	return &webSessions{db: db}, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// create 生成会话 token 与 CSRF token 并落盘（会话 token 发 cookie，CSRF 只回传响应体）；
// 顺手清理过期会话。落盘失败返回错误，调用方不得向用户签发未持久化的会话。
func (ws *webSessions) create() (token, csrf string, err error) {
	token, csrf = randHex(24), randHex(24)
	if _, err = ws.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix()); err != nil {
		return "", "", err
	}
	_, err = ws.db.Exec(`INSERT INTO sessions(token, csrf, expires_at) VALUES(?, ?, ?)`,
		token, csrf, time.Now().Add(webSessionTTL).Unix())
	if err != nil {
		return "", "", err
	}
	return token, csrf, nil
}

// row 读取会话；不存在或已过期返回 !ok（过期行顺手删除）。
func (ws *webSessions) row(token string) (csrf string, ok bool) {
	var exp int64
	err := ws.db.QueryRow(`SELECT csrf, expires_at FROM sessions WHERE token = ?`, token).Scan(&csrf, &exp)
	if err != nil {
		return "", false
	}
	if time.Now().After(time.Unix(exp, 0)) {
		_, _ = ws.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
		return "", false
	}
	return csrf, true
}

func (ws *webSessions) valid(token string) bool {
	if token == "" {
		return false
	}
	_, ok := ws.row(token)
	return ok
}

func (ws *webSessions) csrfFor(token string) string {
	csrf, ok := ws.row(token)
	if !ok {
		return ""
	}
	return csrf
}

func (ws *webSessions) revoke(token string) {
	if token == "" {
		return
	}
	_, _ = ws.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
}

// WebAuth 包装管理页 HTTP 鉴权（凭证校验转发给平台）。
type WebAuth struct {
	cfg    *Config
	ss     *webSessions
	client *http.Client
	// onToken 登录成功后回传平台自动签发的网关凭证与租户 ID（生产环境由 Gateway 注入；测试/开放模式为 nil）。
	onToken func(token, tenantID string) error
}

// NewWebAuth 构造鉴权器；会话存 SQLite（dbPath 为库文件路径，目录不存在时自动创建）。
// 打开失败返回错误——会话持久化是硬要求，不静默退回内存。
func NewWebAuth(cfg *Config, dbPath string) (*WebAuth, error) {
	ss, err := openWebSessions(dbPath)
	if err != nil {
		return nil, err
	}
	return &WebAuth{
		cfg: cfg,
		ss:  ss,
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // 不回跟随平台重定向
			},
		},
	}, nil
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

// platformGatewayRegisterURL 由云通道 ws_url 推导平台自动签发网关凭证接口。
func (a *WebAuth) platformGatewayRegisterURL() string {
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
	return scheme + "://" + u.Host + "/api/ios-agent/v1/gateway/register"
}

// TenantOption 多租户账号注册网关时的候选租户（平台 422 TENANT_AMBIGUOUS 返回）。
type TenantOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TenantChoiceError 平台要求先选择租户：Candidates 为该账号可选租户列表，
// 登录页展示选择后携带 tenant_id 重新登录即可。
type TenantChoiceError struct {
	Candidates []TenantOption
}

func (e *TenantChoiceError) Error() string {
	return "账号属于多个租户，请选择网关所属租户"
}

// provisionGatewayToken 登录成功后调用平台自动签发/轮换网关凭证，并通过 onToken 回传落盘。
// tenantID 优先取本次登录请求携带值（用户刚选择），否则用配置保存值（多租户账号轮换沿用）。
func (a *WebAuth) provisionGatewayToken(ctx context.Context, email, password, tenantID string) error {
	if tenantID == "" {
		tenantID = a.cfg.Cloud.TenantID
	}
	if a.onToken == nil {
		return nil
	}
	regURL := a.platformGatewayRegisterURL()
	if regURL == "" {
		return errors.New("gateway register url invalid")
	}
	// name 空时回退主机名（去掉 .local 等域后缀——平台对网关名有格式/唯一性校验，
	// 实测带域后缀的主机名会被 422 拒绝）。
	name := a.cfg.Cloud.GatewayName
	if name == "" {
		if host, err := os.Hostname(); err == nil {
			name = strings.SplitN(host, ".", 2)[0]
		}
	}
	payload, _ := json.Marshal(map[string]string{
		"email": email, "password": password, "name": name, "tenant_id": tenantID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, regURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		// 多租户账号未指定租户：平台返回候选列表，交由登录页引导用户选择后带 tenant_id 重试
		var amb struct {
			Error struct {
				Code       string `json:"code"`
				Message    string `json:"message"`
			} `json:"error"`
			Candidates []TenantOption `json:"candidates"`
		}
		if resp.StatusCode == http.StatusUnprocessableEntity && json.Unmarshal(body, &amb) == nil &&
			amb.Error.Code == "TENANT_AMBIGUOUS" && len(amb.Candidates) > 0 {
			return &TenantChoiceError{Candidates: amb.Candidates}
		}
		// 其他错误：透传平台拒绝原因，只留关键内容方便排障
		detail := ""
		var e struct {
			Detail any `json:"detail"`
		}
		if json.Unmarshal(body, &e) == nil && e.Detail != nil {
			if b, err := json.Marshal(e.Detail); err == nil {
				detail = strings.TrimSpace(string(b))
			}
		}
		if detail == "" && len(body) > 0 {
			detail = strings.TrimSpace(string(body))
		}
		if len(detail) > 300 {
			detail = detail[:300]
		}
		err := errors.New("gateway register failed: HTTP " + strings.TrimSpace(http.StatusText(resp.StatusCode)))
		if detail != "" {
			err = fmt.Errorf("%w (%s)", err, detail)
		}
		return err
	}
	var out struct {
		Token    string `json:"token"`
		TenantID string `json:"tenantId"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return errors.New("gateway register response missing token")
	}
	return a.onToken(out.Token, out.TenantID)
}

// cookieToken 读取会话 cookie。
func (a *WebAuth) cookieToken(r *http.Request) string {
	c, err := r.Cookie(webSessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// Authenticated 会话是否有效（GET 鉴权用）。
func (a *WebAuth) Authenticated(r *http.Request) bool {
	return !a.PasswordRequired() || a.ss.valid(a.cookieToken(r))
}

// CSRFValid 校验 state-changing 请求的 CSRF token。
func (a *WebAuth) CSRFValid(r *http.Request) bool {
	if !a.PasswordRequired() {
		return true
	}
	token := a.cookieToken(r)
	if !a.ss.valid(token) {
		return false
	}
	csrf := r.Header.Get(csrfHeader)
	return csrf != "" && csrf == a.ss.csrfFor(token)
}

// HandleLogin POST /api/login {email, password} → 转发平台校验 → 签发网关会话 cookie + CSRF token。
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
		token, csrf, err := a.ss.create()
		if err != nil {
			slog.Error("session store create failed", "error", err)
			writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "会话存储失败，请重试"})
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: webSessionCookie, Value: token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(webSessionTTL.Seconds()),
			Secure: a.cfg.Web.CookieSecure,
		})
		// 登录成功后自动签发/轮换网关云凭证，避免管理员手动在平台复制 token。
		var creds struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			TenantID string `json:"tenant_id"` // 多租户账号：用户选择租户后重试登录携带
		}
		provisionErr := ""
		var tenantChoices []TenantOption
		if json.Unmarshal(body, &creds) == nil && creds.Email != "" && creds.Password != "" {
			if err := a.provisionGatewayToken(r.Context(), creds.Email, creds.Password, creds.TenantID); err != nil {
				var choice *TenantChoiceError
				if errors.As(err, &choice) {
					// 平台要求选择租户：前端弹出候选列表，选择后带 tenant_id 重新登录
					tenantChoices = choice.Candidates
				} else {
					slog.Warn("auto-provision gateway token failed", "error", err)
					provisionErr = err.Error() // 透传给前端：凭证签发失败时用户能立即看到平台拒绝原因
				}
			}
		}
		resp := map[string]any{"ok": true, "passwordRequired": true, "csrfToken": csrf, "provision_error": provisionErr}
		if len(tenantChoices) > 0 {
			resp["provision_tenants"] = tenantChoices
		}
		writeJSON(w, resp)
	case resp.StatusCode == http.StatusUnauthorized:
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
// 已登录会话同步回传 CSRF token：前端只把它存在 JS 变量里，页面刷新即丢失，
// 不回传的话刷新后所有 POST（激活/停止）都会 403。
func (a *WebAuth) HandleSession(w http.ResponseWriter, r *http.Request) {
	valid := a.Authenticated(r)
	resp := map[string]any{"authenticated": valid, "passwordRequired": a.PasswordRequired()}
	if valid {
		if csrf := a.ss.csrfFor(a.cookieToken(r)); csrf != "" {
			resp["csrfToken"] = csrf
		}
	}
	writeJSON(w, resp)
}

// splitBearer 兼容网关凭证头解析（保持与 cloud 一致的小工具）。
func splitBearer(header string) (string, bool) {
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	return strings.TrimPrefix(header, "Bearer "), true
}
