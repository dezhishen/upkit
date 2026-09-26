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
	s := strings.TrimSpace(v)
	// 统一剥掉 v 前缀：v1.2.0 与 1.2.0 是同一个版本。
	// 不剥的话 "v1" 会被当成非数字段，与数字段 "1" 比较时得出错误结论，
	// 进而把新版本判成降级而拒装。
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	return strings.FieldsFunc(s, func(r rune) bool {
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
		return comparePrerelease(x, y)
	}
}

// comparePrerelease 比较两个非数字段（如 rc1 与 rc10）。
//
// 直接字典序会得出 rc9 > rc10（'9' > '1'），于是把降级判成升级、
// 使降级保护正好在它要防的场景下失效。这里按「公共前缀 + 末尾数字的数值」比较，
// 因此 rc1 < rc2 < rc10，而 alpha < beta。
func comparePrerelease(x, y string) int {
	xp, xn, xok := splitTrailingNumber(x)
	yp, yn, yok := splitTrailingNumber(y)
	if c := strings.Compare(xp, yp); c != 0 {
		return c
	}
	switch {
	case !xok && !yok:
		return 0
	case !xok:
		return -1 // 无数字后缀 < 有数字后缀（rc < rc1）
	case !yok:
		return 1
	case xn < yn:
		return -1
	case xn > yn:
		return 1
	default:
		return 0
	}
}

// splitTrailingNumber 把 "rc12" 拆成 ("rc", 12, true)；没有数字后缀时第三位为 false。
func splitTrailingNumber(s string) (string, int, bool) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	if i == len(s) {
		return s, 0, false
	}
	n, err := strconv.Atoi(s[i:])
	if err != nil {
		return s, 0, false
	}
	return s[:i], n, true
}
