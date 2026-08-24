package ipasign

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ExtractEmbeddedProvision 从已签名 IPA 取出 embedded.mobileprovision 原文（CMS）。
func ExtractEmbeddedProvision(ipaPath string) ([]byte, error) {
	r, err := zip.OpenReader(ipaPath)
	if err != nil {
		return nil, fmt.Errorf("打开 IPA: %w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if !strings.HasSuffix(name, ".app/embedded.mobileprovision") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("IPA 里的描述文件是空的")
		}
		return data, nil
	}
	return nil, fmt.Errorf("IPA 里没有 embedded.mobileprovision")
}

// ParseProvisionBytes 解析描述文件：已是 plist 就直接读，否则走 macOS security cms。
func ParseProvisionBytes(raw []byte) (Profile, error) {
	if p, err := ParseProfile(raw); err == nil && (p.UUID != "" || p.Team != "" || p.Name != "") {
		return p, nil
	}
	decoded, err := decodeCMS(raw)
	if err != nil {
		return Profile{}, err
	}
	return ParseProfile(decoded)
}

func decodeCMS(raw []byte) ([]byte, error) {
	f, err := os.CreateTemp("", "wda-prov-*.mobileprovision")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	_, werr := f.Write(raw)
	_ = f.Close()
	defer os.Remove(path)
	if werr != nil {
		return nil, werr
	}
	out, err := exec.Command("security", "cms", "-D", "-i", path).Output()
	if err != nil {
		return nil, fmt.Errorf("解码描述文件: %w", err)
	}
	return out, nil
}
