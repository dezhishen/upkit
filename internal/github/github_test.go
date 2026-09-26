package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func releaseJSON(tag string, assets ...Asset) []byte {
	rel := Release{
		TagName:    tag,
		Name:       tag,
		HTMLURL:    "https://example.com/release",
		Assets:     assets,
		Prerelease: false,
	}
	data, _ := json.Marshal(rel)
	return data
}

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient("owner/repo", "", srv.Client(), WithBaseURL(srv.URL)), srv
}

func TestLatestRelease(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/repos/owner/repo/releases/latest"; got != want {
			t.Errorf("请求路径 = %q，期望 %q", got, want)
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Error("缺少 User-Agent")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(releaseJSON("131.0.6778.86-1.1",
			Asset{Name: "ungoogled-chromium_131.0.6778.86-1.1_windows_x64.zip", Size: 100, DownloadURL: "http://x/1.zip"},
			Asset{Name: "checksums.txt", Size: 10},
		))
	})

	rel, err := client.LatestRelease(context.Background(), false)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if rel.Version() != "131.0.6778.86-1.1" {
		t.Errorf("Version() = %q", rel.Version())
	}
	if len(rel.Zips()) != 1 {
		t.Errorf("Zips() 数量 = %d，期望 1", len(rel.Zips()))
	}
}

func TestLatestReleaseWithPrereleaseFallsBackToList(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/repos/owner/repo/releases"; got != want {
			t.Errorf("请求路径 = %q，期望 %q", got, want)
		}
		if got := r.URL.Query().Get("per_page"); got == "" {
			t.Error("应带上 per_page 参数")
		}
		w.Header().Set("Content-Type", "application/json")
		draft := Release{TagName: "draft", Draft: true}
		ok := Release{TagName: "132.0.0.1-1", Prerelease: true}
		data, _ := json.Marshal([]Release{draft, ok})
		_, _ = w.Write(data)
	})

	rel, err := client.LatestRelease(context.Background(), true)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if rel.TagName != "132.0.0.1-1" {
		t.Errorf("应跳过 draft，得到 %q", rel.TagName)
	}
}

func TestRateLimitError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := client.LatestRelease(context.Background(), false)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("期望 ErrRateLimited，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "github_token") {
		t.Errorf("错误信息应提示配置 Token: %v", err)
	}
}

func TestNotFoundError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := client.LatestRelease(context.Background(), false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，得到 %v", err)
	}
}

func TestSelectAsset(t *testing.T) {
	rel := &Release{
		TagName: "131.0.6778.86-1.1",
		Assets: []Asset{
			{Name: "ungoogled-chromium_131.0.6778.86-1.1_windows_x64.zip", Size: 200},
			{Name: "ungoogled-chromium_131.0.6778.86-1.1_windows_arm64.zip", Size: 210},
			{Name: "SHA256SUMS", Size: 5},
		},
	}

	t.Run("glob 匹配", func(t *testing.T) {
		got, err := SelectAsset(rel, "ungoogled-chromium_*_windows_x64.zip")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.Name, "x64") {
			t.Errorf("选中了 %q", got.Name)
		}
	})

	t.Run("精确匹配", func(t *testing.T) {
		got, err := SelectAsset(rel, "ungoogled-chromium_131.0.6778.86-1.1_windows_arm64.zip")
		if err != nil {
			t.Fatal(err)
		}
		if got.Size != 210 {
			t.Errorf("选中版本错误: %+v", got)
		}
	})

	t.Run("无匹配时报错并列出候选", func(t *testing.T) {
		_, err := SelectAsset(rel, "*_windows_x86.zip")
		if err == nil {
			t.Fatal("应返回错误")
		}
		if !strings.Contains(err.Error(), "x64") {
			t.Errorf("错误信息应列出候选资源: %v", err)
		}
	})

	t.Run("唯一 zip 时兜底", func(t *testing.T) {
		single := &Release{TagName: "1.0.0", Assets: []Asset{{Name: "weird-name.zip", Size: 1}}}
		got, err := SelectAsset(single, "*_windows_x64.zip")
		if err != nil {
			t.Fatalf("应兜底选中唯一 zip: %v", err)
		}
		if got.Name != "weird-name.zip" {
			t.Errorf("选中 %q", got.Name)
		}
	})

	t.Run("没有 zip 时报错", func(t *testing.T) {
		if _, err := SelectAsset(&Release{TagName: "1.0.0"}, "*"); err == nil {
			t.Fatal("应返回错误")
		}
	})
}

func TestChecksumAsset(t *testing.T) {
	main := Asset{Name: "build_windows_x64.zip"}
	rel := &Release{
		Assets: []Asset{main, {Name: "build_windows_x64.zip.sha256"}},
	}
	got, ok := rel.ChecksumAsset(main)
	if !ok || got.Name != "build_windows_x64.zip.sha256" {
		t.Fatalf("ChecksumAsset = %+v, %v", got, ok)
	}

	rel2 := &Release{Assets: []Asset{main, {Name: "build_windows_x64.sha256"}}}
	if got, ok := rel2.ChecksumAsset(main); !ok || got.Name != "build_windows_x64.sha256" {
		t.Fatalf("ChecksumAsset 未识别省略 .zip 的写法: %+v", got)
	}

	if _, ok := (&Release{}).ChecksumAsset(main); ok {
		t.Error("无校验文件时应返回 false")
	}
}

func TestReleaseFindAsset(t *testing.T) {
	rel := &Release{Assets: []Asset{{Name: "A.ZIP"}}}
	if _, ok := rel.FindAsset("a.zip"); !ok {
		t.Error("FindAsset 应大小写不敏感")
	}
	if _, ok := rel.FindAsset("b.zip"); ok {
		t.Error("不应找到不存在的资源")
	}
}

func TestVersionFromAssetNameWhenTagIsNotVersioned(t *testing.T) {
	rel := &Release{
		TagName: "latest",
		Assets:  []Asset{{Name: "ungoogled-chromium_131.0.6778.86-1.1_windows_x64.zip"}},
	}
	if got := rel.Version(); got != "131.0.6778.86-1.1" {
		t.Errorf("Version() = %q", got)
	}
}
