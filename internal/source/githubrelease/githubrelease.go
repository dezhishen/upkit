// Package githubrelease 是「从 GitHub Release 取版本与产物」的来源适配器。
//
// 选项：
//
//	repo            必填，owner/name
//	asset           资源名匹配，支持 * 与 {arch}
//	arch            auto（默认）/ x64 / x86 / arm64
//	prerelease      是否接受预发布（默认 false）
//	version         固定版本（等价于 app.pin），非空时不查 latest
package githubrelease

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/github"
	"github.com/dezhishen/upkit/internal/registry"
)

// Name 是注册名。
const Name = "github-release"

// Source 实现 core.SourceResolver（并可选实现 core.Verifier）。
type Source struct {
	app    core.AppRef
	client *github.Client
	deps   registry.Deps
}

// New 构造适配器。
func New(app core.AppRef, deps registry.Deps) (core.SourceResolver, error) {
	repo := strings.TrimSpace(app.SourceOpts["repo"])
	if repo == "" {
		return nil, fmt.Errorf("app %s: github-release 需要 source.repo", app.ID)
	}
	client := github.NewClient(repo, app.SourceOpts["token"], deps.HTTP,
		github.WithUserAgent("upkit/2"))
	return &Source{app: app, client: client, deps: deps}, nil
}

// Name 实现 core.SourceResolver。
func (s *Source) Name() string { return Name }

// Latest 返回目标版本；app.Pin 或 source.version 非空时直接取该版本。
func (s *Source) Latest(ctx context.Context, app core.AppRef) (core.Release, error) {
	pin := firstNonEmpty(app.Pin, s.app.SourceOpts["version"])
	if pin != "" {
		rel, err := s.client.ReleaseByTag(ctx, pin)
		if err != nil {
			return core.Release{}, fmt.Errorf("获取固定版本 %s: %w", pin, err)
		}
		return s.convert(rel)
	}
	if s.app.SourceOpts["prefer_release"] != "" {
		rel, err := s.client.ReleaseByTag(ctx, s.app.SourceOpts["prefer_release"])
		if err == nil {
			return s.convert(rel)
		}
	}
	rel, err := s.client.LatestRelease(ctx, truthy(s.app.SourceOpts["prerelease"]))
	if err != nil {
		return core.Release{}, err
	}
	return s.convert(rel)
}

// Versions 返回最近若干版本。
func (s *Source) Versions(ctx context.Context, app core.AppRef, limit int) ([]core.Release, error) {
	list, err := s.client.ListReleases(ctx, limit, truthy(s.app.SourceOpts["prerelease"]))
	if err != nil {
		return nil, err
	}
	out := make([]core.Release, 0, len(list))
	for i := range list {
		rel, err := s.convert(&list[i])
		if err != nil {
			continue
		}
		out = append(out, rel)
	}
	return out, nil
}

// convert 把 GitHub Release 转成领域对象，并完成资源筛选。
func (s *Source) convert(rel *github.Release) (core.Release, error) {
	pattern := s.pattern()
	asset, err := github.SelectAsset(rel, pattern)
	if err != nil {
		return core.Release{}, err
	}
	out := core.Release{
		Version:     rel.Version(),
		Tag:         rel.TagName,
		Channel:     channel(rel.Prerelease),
		PublishedAt: rel.PublishedAt,
		Notes:       rel.Body,
		Artifacts: []core.Artifact{{
			Name:   asset.Name,
			URL:    asset.DownloadURL,
			Size:   asset.Size,
			Digest: asset.Digest,
		}},
	}
	if out.Version == "" {
		return core.Release{}, fmt.Errorf("无法从 Release %s 解析版本号", rel.TagName)
	}
	return out, nil
}

// ExpectedDigest 实现 core.Verifier：优先用 asset digest，其次找 .sha256 附件。
func (s *Source) ExpectedDigest(ctx context.Context, app core.AppRef, art core.Artifact) (string, error) {
	if d := sumFromDigest(art.Digest); d != "" {
		return d, nil
	}
	rel, err := s.latestRaw(ctx, app)
	if err != nil {
		return "", err
	}
	asset, ok := rel.FindAsset(art.Name)
	if !ok {
		return "", nil
	}
	chk, ok := rel.ChecksumAsset(asset)
	if !ok {
		return "", nil
	}
	return s.client.FetchChecksum(ctx, chk.DownloadURL)
}

// latestRaw 取回原始 Release（用于校验值查询）。
func (s *Source) latestRaw(ctx context.Context, app core.AppRef) (*github.Release, error) {
	pin := firstNonEmpty(app.Pin, s.app.SourceOpts["version"])
	if pin != "" {
		return s.client.ReleaseByTag(ctx, pin)
	}
	return s.client.LatestRelease(ctx, truthy(s.app.SourceOpts["prerelease"]))
}

// pattern 组装资源匹配模式（把 {arch} 换成实际架构）。
func (s *Source) pattern() string {
	p := strings.TrimSpace(s.app.SourceOpts["asset"])
	if p == "" {
		p = "ungoogled-chromium_*_windows_{arch}.zip"
	}
	return strings.ReplaceAll(p, "{arch}", archToken(s.app.SourceOpts["arch"]))
}

func archToken(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "auto":
		switch runtime.GOARCH {
		case "amd64":
			return "x64"
		case "386":
			return "x86"
		case "arm64":
			return "arm64"
		default:
			return runtime.GOARCH
		}
	case "amd64", "x86_64", "x64":
		return "x64"
	case "386", "i386", "x86":
		return "x86"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return v
	}
}

func channel(prerelease bool) string {
	if prerelease {
		return "prerelease"
	}
	return "stable"
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "是":
		return true
	default:
		return false
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func sumFromDigest(d string) string {
	d = strings.TrimSpace(d)
	if i := strings.Index(d, ":"); i >= 0 && !strings.Contains(d[:i], " ") {
		d = d[i+1:]
	}
	return strings.ToLower(strings.TrimSpace(d))
}
