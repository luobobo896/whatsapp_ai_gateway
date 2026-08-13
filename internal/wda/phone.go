package wda

import (
	"fmt"
	"regexp"
	"strings"
)

// chinaMobileRe 是中国大陆手机号的硬性格式（国家码 86），与平台 phoneinfo.NormalizeMobilePhone 对齐。
var chinaMobileRe = regexp.MustCompile(`^(?:\+?86|0086)?(1[0-9]{10})$`)

// normalizeMobilePhone 归一化中国大陆手机号，返回 86+11 位数字；非法报错。
func normalizeMobilePhone(phone string) (string, error) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return "", fmt.Errorf("手机号为空")
	}
	m := chinaMobileRe.FindStringSubmatch(phone)
	if m == nil {
		return "", fmt.Errorf("手机号 %q 不符合规范：仅支持中国大陆手机号（11 位以 1 开头，或 86/+86/0086 前缀）", phone)
	}
	return "86" + m[1], nil
}
