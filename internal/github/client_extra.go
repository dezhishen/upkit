package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dezhishen/upkit/internal/util"
)

// ListReleases 返回最近若干 Release（按发布时间倒序，跳过 draft）。
func (c *Client) ListReleases(ctx context.Context, limit int, includePrerelease bool) ([]Release, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	var list []Release
	path := fmt.Sprintf("/repos/%s/releases?per_page=%d", c.repo, limit)
	if err := c.get(ctx, path, &list); err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(list))
	for _, r := range list {
		if r.Draft {
			continue
		}
		if r.Prerelease && !includePrerelease {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// FetchChecksum 下载并解析校验文件，返回小写十六进制摘要（解析不出时返回空串）。
func (c *Client) FetchChecksum(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载校验文件: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载校验文件失败：HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return util.ParseSHA256(string(data)), nil
}

// ReleaseAge 返回 Release 的发布时间到现在的间隔（未发布时为 0）。
func (r *Release) ReleaseAge(now time.Time) time.Duration {
	if r.PublishedAt.IsZero() {
		return 0
	}
	// 上游时钟偏差会让发布时间落在「未来」，此时按 0 处理：
	// 界面上显示「-1h 前」比显示「刚刚」更像故障。
	if d := now.Sub(r.PublishedAt); d > 0 {
		return d
	}
	return 0
}
