package pluginfeed

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxFeedBytes 是订阅文件的大小上限（防止意外拉到超大文件）。
const MaxFeedBytes = 4 << 20

// Format 是订阅内容的格式。
type Format string

// 支持的订阅格式。
const (
	FormatAuto Format = "auto"
	FormatJSON Format = "json"
	FormatYAML Format = "yaml"
)

// Extensions 返回支持的订阅文件扩展名，供界面提示用户。
func Extensions() []string { return []string{".json", ".yaml", ".yml"} }

// FormatOf 按地址的扩展名推断格式；判断不出时返回 FormatAuto。
func FormatOf(rawURL string) Format {
	trimmed := strings.SplitN(strings.TrimSpace(rawURL), "?", 2)[0]
	switch strings.ToLower(path.Ext(trimmed)) {
	case ".json":
		return FormatJSON
	case ".yaml", ".yml":
		return FormatYAML
	default:
		return FormatAuto
	}
}

// Parse 解析订阅内容。
//
// `.json` 与 `.yaml/.yml` 走**同一个解析器**：JSON 是 YAML 的子集，所以一份 schema
// 定义就能吃两种格式，调用方不需要分支。扩展名只用来让报错更准确。
//
// 解析是严格模式（KnownFields）：字段名写错会直接报错，而不是被静默忽略 —— 订阅
// 等于远程代码执行授权，拼错的限制项如果被忽略，会让人误以为它生效了。
func Parse(data []byte, format Format) (*Feed, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("订阅内容为空")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f Feed
	if err := dec.Decode(&f); err != nil {
		return nil, parseError(err, format)
	}
	return &f, nil
}

func parseError(err error, format Format) error {
	switch format {
	case FormatJSON:
		return fmt.Errorf("JSON 解析失败（JSON 里不能有注释与尾随逗号；想要注释请改用 .yaml）: %w", err)
	case FormatYAML:
		return fmt.Errorf("YAML 解析失败: %w", err)
	default:
		return fmt.Errorf("解析失败（内容需是合法 JSON 或 YAML）: %w", err)
	}
}

// Fetch 拉取并解析订阅。
func Fetch(ctx context.Context, client *http.Client, rawURL string) (*Feed, error) {
	u, err := parseHTTPURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("订阅地址无效: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("构造订阅请求: %w", err)
	}
	req.Header.Set("Accept", "application/yaml, application/json, text/yaml, */*")
	req.Header.Set("User-Agent", "upkit/2 pluginfeed")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取订阅 %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("拉取订阅 %s: HTTP %d", rawURL, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxFeedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取订阅内容: %w", err)
	}
	if len(data) > MaxFeedBytes {
		return nil, fmt.Errorf("订阅文件超过 %d 字节上限", MaxFeedBytes)
	}

	format := FormatOf(u.String())
	if format == FormatAuto {
		format = formatFromContentType(resp.Header.Get("Content-Type"))
	}
	return Parse(data, format)
}

func formatFromContentType(ct string) Format {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "json"):
		return FormatJSON
	case strings.Contains(ct, "yaml"), strings.Contains(ct, "yml"):
		return FormatYAML
	default:
		return FormatAuto
	}
}

// Entry 是订阅里某个插件在**当前平台**上的可用信息。
type Entry struct {
	Plugin   Plugin
	Package  Package
	Location Location
	// Installed / InstalledSHA256 由调用方从本机已安装的插件描述填入。
	Installed       string
	InstalledSHA256 string
}

// Action 描述该条目相对本机状态应该做什么。
type Action string

// 动作取值。
const (
	ActionInstall   Action = "install"   // 本机未安装
	ActionUpdate    Action = "update"    // 有更新
	ActionCurrent   Action = "current"   // 已是最新
	ActionDowngrade Action = "downgrade" // 订阅提供的版本比本机更旧
)

// Action 返回该条目应当执行的动作。
func (e Entry) Action() Action {
	switch {
	case strings.TrimSpace(e.Installed) == "":
		return ActionInstall
	case IsDowngrade(e.Installed, e.Plugin.Version):
		return ActionDowngrade
	case CompareVersions(e.Installed, e.Plugin.Version) < 0:
		return ActionUpdate
	default:
		return ActionCurrent
	}
}

// Plan 把订阅展开成当前平台可用的条目（尚未与本机已装状态对比）。
//
// feedURL 用于解析相对路径的包地址：相对地址一律相对订阅地址解析。
func Plan(f *Feed, feedURL string) ([]Entry, error) {
	if f == nil {
		return nil, fmt.Errorf("订阅为空")
	}
	out := make([]Entry, 0, len(f.Plugins))
	for _, p := range f.Plugins {
		pkg, ok := p.Packages.For(Platform())
		if !ok {
			return nil, fmt.Errorf("插件 %s 没有 %s 平台的包（可选：%v）", p.ID, Platform(), p.Packages.Platforms())
		}
		loc, err := ResolveLocation(feedURL, pkg.URL)
		if err != nil {
			return nil, fmt.Errorf("插件 %s: %w", p.ID, err)
		}
		out = append(out, Entry{Plugin: p, Package: pkg, Location: loc})
	}
	return out, nil
}
