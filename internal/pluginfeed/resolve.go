package pluginfeed

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// Location 描述一个解析后的下载地址，以及它是否需要额外授权。
type Location struct {
	// URL 是解析后的绝对地址。
	URL string
	// Host 是主机名（含端口，若有）。
	Host string
	// SameOrigin 为 true 表示它与订阅同源 —— 相对路径必然同源。
	SameOrigin bool
	// Relative 为 true 表示订阅里写的是相对路径。
	Relative bool
}

// NeedsAuthorization 报告这个地址是否需要在订阅授权之外再单独授权。
//
// 相对路径必然与订阅同源，不需要；跨域的绝对地址需要 —— 否则一个被篡改的订阅
// 就能把插件指向任意第三方域名。
func (l Location) NeedsAuthorization() bool { return !l.SameOrigin }

// ResolveLocation 解析订阅里的包地址。
//
//	https://...            绝对地址；与订阅不同源时标记为需要额外授权
//	/dist/x.exe            以 / 开头：相对订阅的 origin
//	./dist/x.exe x.exe     相对订阅 URL 解析；解析后必须与订阅同源
//
// 同源强制是关键：相对路径一旦被允许指向其它域名，订阅就成了任意域名下载器。
func ResolveLocation(feedURL, raw string) (Location, error) {
	feed, err := parseHTTPURL(feedURL)
	if err != nil {
		return Location{}, fmt.Errorf("订阅地址无效: %w", err)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Location{}, fmt.Errorf("包地址为空")
	}

	// 绝对地址：带 scheme。
	if u, err := parseHTTPURL(raw); err == nil {
		return Location{
			URL:        u.String(),
			Host:       hostOf(u),
			SameOrigin: sameOrigin(feed, u),
		}, nil
	}

	// 看起像绝对地址但 scheme 不被支持（如 ftp://、file://）。
	if hasScheme(raw) {
		return Location{}, fmt.Errorf("不支持的地址协议: %s（只允许 http/https）", schemeOf(raw))
	}

	ref, err := url.Parse(raw)
	if err != nil {
		return Location{}, fmt.Errorf("解析相对路径 %q: %w", raw, err)
	}
	joined := feed.ResolveReference(ref)
	// 相对路径禁止跨域：ResolveReference 对 //host/x 这种"协议相对地址"会换域，
	// 对 ../.. 也可能越出，因此统一再判一次同源。
	if !sameOrigin(feed, joined) {
		return Location{}, fmt.Errorf("相对路径 %q 解析后跨域（%s），订阅里的包地址必须与订阅同源", raw, hostOf(joined))
	}
	joined.Fragment = ""
	return Location{
		URL:        joined.String(),
		Host:       hostOf(joined),
		SameOrigin: true,
		Relative:   true,
	}, nil
}

// FeedHost 返回订阅地址所在的主机名，供"授权信任该域名"使用。
func FeedHost(feedURL string) (string, error) {
	u, err := parseHTTPURL(feedURL)
	if err != nil {
		return "", fmt.Errorf("订阅地址无效: %w", err)
	}
	host := hostOf(u)
	if host == "" {
		return "", fmt.Errorf("订阅地址缺少主机名")
	}
	return host, nil
}

// ── 内部工具 ─────────────────────────────────────────────────

func parseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme == "http" {
		// 明文 HTTP 下链路中间任何人都能整份替换订阅，而清单自带的 sha256
		// 对「清单本身被换掉」毫无帮助 —— 供应链防线的根在清单来源，
		// 摘要只保护了清单以下的那一层。
		return nil, fmt.Errorf("订阅地址必须使用 https（收到 http，明文链路无法保证清单未被篡改）")
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("只允许 https，收到 %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("缺少主机名")
	}
	return u, nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(hostOf(a), hostOf(b)) && strings.EqualFold(a.Scheme, b.Scheme)
}

func hostOf(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	return host
}

func hasScheme(raw string) bool {
	i := strings.Index(raw, "://")
	if i <= 0 {
		return false
	}
	return path.Clean(raw[:i]) != "" && !strings.ContainsAny(raw[:i], "/\\ ")
}

func schemeOf(raw string) string {
	if i := strings.Index(raw, "://"); i > 0 {
		return raw[:i]
	}
	return raw
}
