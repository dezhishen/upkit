// Package github 封装了 upkit 需要的 GitHub Releases API。
//
// 只依赖标准库：这样交叉编译到 Windows 时不会引入额外模块，
// 也便于在没有 GitHub CLI 的环境中运行。
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/util"
)

// DefaultAPIBase 是 GitHub 公共 API 的地址（企业版可替换）。
const DefaultAPIBase = "https://api.github.com"

// apiVersion 固定 API 版本，避免上游默认行为变化影响解析。
const apiVersion = "2022-11-28"

// ErrNotFound 表示仓库或 Release 不存在。
var ErrNotFound = errors.New("资源不存在")

// ErrRateLimited 表示触发了 API 限流（通常是未配置 Token）。
var ErrRateLimited = errors.New("GitHub API 限流")

// versionRe 用于从 tag 或资源名中提取形如 131.0.6778.86-1.1 的版本号。
var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+(\.\d+)?(-\d+(\.\d+)*)?`)

// Asset 是 Release 中的一个附件。
type Asset struct {
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	DownloadURL string    `json:"browser_download_url"`
	ContentType string    `json:"content_type"`
	Downloads   int       `json:"download_count"`
	CreatedAt   time.Time `json:"created_at"`
	// Digest 是 GitHub 自 2025 年起提供的内容摘要，形如 "sha256:abcdef..."。
	Digest string `json:"digest"`
}

// Release 是 GitHub Release 的精简表示。
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Version 返回该 Release 对应的版本号。
//
// 优先解析 tag；tag 不含版本号时退回到资源名（例如
// ungoogled-chromium_131.0.6778.86-1.1_windows_x64.zip）。
func (r *Release) Version() string {
	if v := versionRe.FindString(r.TagName); v != "" {
		return v
	}
	for _, a := range r.Assets {
		if v := versionRe.FindString(a.Name); v != "" {
			return v
		}
	}
	return strings.TrimPrefix(strings.TrimSpace(r.TagName), "v")
}

// Zips 返回所有看起来是 zip 的附件（按名称排序，结果稳定）。
func (r *Release) Zips() []Asset {
	var out []Asset
	for _, a := range r.Assets {
		name := strings.ToLower(a.Name)
		if !strings.HasSuffix(name, ".zip") {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FindAsset 按名称精确（大小写不敏感）查找附件。
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}

// Client 是 GitHub API 客户端。
type Client struct {
	repo      string
	token     string
	baseURL   string
	userAgent string
	hc        *http.Client
}

// Option 用于定制 Client。
type Option func(*Client)

// WithBaseURL 覆盖 API 地址（例如 GitHub Enterprise）。
func WithBaseURL(base string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(base, "/") }
}

// WithUserAgent 覆盖 User-Agent。
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// NewClient 创建客户端。hc 为 nil 时使用 http.DefaultClient。
func NewClient(repo, token string, hc *http.Client, opts ...Option) *Client {
	c := &Client{
		repo:      strings.Trim(strings.TrimSpace(repo), "/"),
		token:     strings.TrimSpace(token),
		baseURL:   DefaultAPIBase,
		userAgent: "upkit",
		hc:        hc,
	}
	if c.hc == nil {
		c.hc = http.DefaultClient
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Repo 返回目标仓库。
func (c *Client) Repo() string { return c.repo }

// LatestRelease 获取最新 Release。
//
// includePrerelease 为 true 时，会从最近若干个 Release 中挑选第一个
// 未标记为 draft 的版本（含预发布），否则使用 latest 接口。
func (c *Client) LatestRelease(ctx context.Context, includePrerelease bool) (*Release, error) {
	if !includePrerelease {
		var rel Release
		if err := c.get(ctx, fmt.Sprintf("/repos/%s/releases/latest", c.repo), &rel); err != nil {
			return nil, err
		}
		return &rel, nil
	}

	var list []Release
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/releases?per_page=20", c.repo), &list); err != nil {
		return nil, err
	}
	for i := range list {
		if !list[i].Draft {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("%w: 仓库 %s 暂无可用 Release", ErrNotFound, c.repo)
}

// ReleaseByTag 按 tag 获取指定 Release。
func (c *Client) ReleaseByTag(ctx context.Context, tag string) (*Release, error) {
	var rel Release
	path := fmt.Sprintf("/repos/%s/releases/tags/%s", c.repo, url.PathEscape(tag))
	if err := c.get(ctx, path, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// get 执行一次 GET 请求并把 JSON 解码到 out。
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("构造请求: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", c.userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("请求 GitHub API: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrNotFound, c.repo)
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining == "0" {
			return fmt.Errorf("%w: %s，请在配置中设置 github_token 以提升配额",
				ErrRateLimited, rateLimitHint(resp))
		}
		return fmt.Errorf("GitHub API 拒绝访问（HTTP %d）: %s", resp.StatusCode, strings.TrimSpace(c.tokenHint(resp)))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API 返回 HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("解析 GitHub 响应: %w", err)
	}
	return nil
}

func (c *Client) tokenHint(resp *http.Response) string {
	if c.token == "" {
		return "未配置 github_token，API 限流为 60 次/小时"
	}
	return resp.Header.Get("X-GitHub-SSO")
}

func rateLimitHint(resp *http.Response) string {
	reset := resp.Header.Get("X-RateLimit-Reset")
	if ts, err := strconv.ParseInt(reset, 10, 64); err == nil && ts > 0 {
		return "限流将于 " + time.Unix(ts, 0).Format("15:04:05") + " 重置"
	}
	return "配额已用尽"
}

// SelectAsset 依据模式挑选需要下载的资源。
//
// 匹配顺序：
//  1. 名称完全相等（大小写不敏感）；
//  2. glob 通配符匹配（path.Match 语义，大小写不敏感）；
//  3. 若仓库只有一个 zip，则直接使用；
//  4. 否则报错并列出候选，避免静默下错包。
func SelectAsset(rel *Release, pattern string) (Asset, error) {
	zips := rel.Zips()
	if len(zips) == 0 {
		return Asset{}, fmt.Errorf("%w: Release %s 中没有 zip 资源", ErrNotFound, rel.TagName)
	}
	pattern = strings.TrimSpace(pattern)
	if pattern != "" {
		for _, a := range zips {
			if strings.EqualFold(a.Name, pattern) {
				return a, nil
			}
		}
		for _, a := range zips {
			if util.MatchFold(pattern, a.Name) {
				return a, nil
			}
		}
	}
	if len(zips) == 1 {
		return zips[0], nil
	}
	names := make([]string, 0, len(zips))
	for _, a := range zips {
		names = append(names, a.Name)
	}
	return Asset{}, fmt.Errorf("未找到匹配 %q 的资源，候选为: %s", pattern, strings.Join(names, ", "))
}

// ChecksumAsset 查找与 asset 对应的校验文件（.sha256 / .sha256sum）。
func (r *Release) ChecksumAsset(asset Asset) (Asset, bool) {
	candidates := []string{
		asset.Name + ".sha256",
		asset.Name + ".sha256sum",
		strings.TrimSuffix(asset.Name, ".zip") + ".sha256",
	}
	lower := make(map[string]Asset, len(r.Assets))
	for _, a := range r.Assets {
		lower[strings.ToLower(a.Name)] = a
	}
	for _, c := range candidates {
		if a, ok := lower[strings.ToLower(c)]; ok {
			return a, true
		}
	}
	return Asset{}, false
}
