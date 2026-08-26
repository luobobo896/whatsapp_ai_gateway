package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// gatewayWdaPackage 是云平台 wda:config 的载荷：当前 WDA 控制器包。
type gatewayWdaPackage struct {
	Version  string `json:"version"`
	SignMode string `json:"sign_mode"` // personal | enterprise
	Download string `json:"download"`  // 相对 /api 或绝对 URL
	SHA256   string `json:"sha256"`
}

// ApplyWdaPackage 下载并原子替换 <state>/wda.ipa，记录当前版本，推送给管理页。
func (g *Gateway) ApplyWdaPackage(ctx context.Context, pkg gatewayWdaPackage) error {
	if pkg.Download == "" || pkg.Version == "" {
		return nil
	}
	cur, _ := g.Cfg.ReadExtra("wda_package_version")
	if cur == pkg.Version {
		return nil // 已是最新
	}
	u, err := wdaDownloadURL(g.Cfg.Cloud.WSURL, pkg.Download)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("下载 wda.ipa: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("下载 wda.ipa http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return err
	}
	if pkg.SHA256 != "" {
		if got := hashHex(data); got != pkg.SHA256 {
			return fmt.Errorf("wda.ipa sha256 不匹配: got %s want %s", got, pkg.SHA256)
		}
	}
	state := g.Cfg.Dir()
	dst := filepath.Join(state, "wda.ipa")
	tmp := filepath.Join(state, "wda.ipa.download")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	_ = g.Cfg.WriteExtra("wda_package_version", pkg.Version)
	_ = g.Cfg.WriteExtra("wda_package_sha256", pkg.SHA256)
	_ = g.Cfg.WriteExtra("wda_package_sign_mode", pkg.SignMode)
	g.Hub.Publish("wda:update", map[string]any{"version": pkg.Version, "sign_mode": pkg.SignMode, "path": dst})
	return nil
}

// wdaDownloadURL 把云平台相对 download 拼成完整 URL（从 Cloud.WSURL 推导 scheme+host）。
func wdaDownloadURL(wsURL, download string) (string, error) {
	if strings.HasPrefix(download, "http://") || strings.HasPrefix(download, "https://") {
		return download, nil
	}
	u, err := url.Parse(wsURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("ws url 无效: %s", wsURL)
	}
	scheme := "https"
	if u.Scheme == "ws" || u.Scheme == "http" {
		scheme = "http"
	}
	return scheme + "://" + u.Host + download, nil
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// WdaStatus 返回网关当前 WDA 控制器包状态（供管理页显示"已更新最新包"，不影响运行）。
func (g *Gateway) WdaStatus() map[string]any {
	ver, _ := g.Cfg.ReadExtra("wda_package_version")
	sha, _ := g.Cfg.ReadExtra("wda_package_sha256")
	mode, _ := g.Cfg.ReadExtra("wda_package_sign_mode")
	return map[string]any{
		"version": ver,
		"sha256":  sha,
		"sign_mode": mode,
		"installed": ver != "",
	}
}
