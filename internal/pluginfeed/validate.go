package pluginfeed

import (
	"encoding/hex"
	"strconv"
	"strings"

	upkitplugin "github.com/dezhishen/upkit/pkg/plugin"
)

// ValidID 报告插件 id 是否合法（与插件 SDK 同一套规则）。
func ValidID(id string) bool { return upkitplugin.ValidID(id) }

// ValidSHA256 报告摘要是否是合法的 sha256（允许 "sha256:" 前缀）。
//
// 订阅里的包必须提供摘要：没有摘要就无法判断下载到的究竟是不是发布者给的文件。
func ValidSHA256(digest string) bool {
	d := NormalizeSHA256(digest)
	if len(d) != 64 {
		return false
	}
	_, err := hex.DecodeString(d)
	return err == nil
}

// NormalizeSHA256 去掉可选的 "sha256:" 前缀并统一为小写。
func NormalizeSHA256(digest string) string {
	d := strings.ToLower(strings.TrimSpace(digest))
	return strings.TrimPrefix(d, "sha256:")
}

// CompareVersions 比较两个版本号，返回 -1 / 0 / 1。
//
// 支持 "1.2.3"、"1.2.3-rc1"、"2.0" 这类常见写法：按 . - + _ 切段，数字段按数值
// 比较，数字段大于非数字段（所以 1.0 > 1.0-rc1），其余按字典序。它不是完整的
// semver 实现，但对「是否需要升级/是否降级」足够，且不会因为奇怪版本号而失败。
func CompareVersions(a, b string) int {
	as, bs := splitVersion(a), splitVersion(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if c := compareSegment(x, y); c != 0 {
			return c
		}
	}
	return 0
}

// IsDowngrade 报告 to 是否比 from 更旧（from 为空表示未安装）。
func IsDowngrade(from, to string) bool {
	if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
		return false
	}
	return CompareVersions(to, from) < 0
}

func splitVersion(v string) []string {
	return strings.FieldsFunc(strings.TrimSpace(v), func(r rune) bool {
		return r == '.' || r == '-' || r == '+' || r == '_'
	})
}

func compareSegment(x, y string) int {
	xn, xerr := strconv.Atoi(x)
	yn, yerr := strconv.Atoi(y)
	switch {
	case x == "" && y == "":
		return 0
	case x == "":
		// 段用尽：对方是非数字段（预发布标识）时我们用尽的一方更大
		//（1.0.0 > 1.0.0-rc1），对方是数字段时更小（1.0 < 1.0.0）。
		if yerr != nil {
			return 1
		}
		return -1
	case y == "":
		if xerr != nil {
			return -1
		}
		return 1
	case xerr == nil && yerr == nil:
		switch {
		case xn < yn:
			return -1
		case xn > yn:
			return 1
		default:
			return 0
		}
	case xerr == nil:
		return 1 // 数字段 > 非数字段
	case yerr == nil:
		return -1
	default:
		return strings.Compare(x, y)
	}
}
