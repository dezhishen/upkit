package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 列版本：草稿要跳过，预发布按开关决定，其余原样返回。
func TestListReleasesFiltersDraftsAndPrereleases(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/owner/repo/releases") {
			t.Errorf("请求路径不对: %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("per_page"); got != "3" {
			t.Errorf("per_page 应透传 3，实际 %q", got)
		}
		list := []Release{
			{TagName: "v2.0.0"},
			{TagName: "v1.9.0-rc.1", Prerelease: true},
			{TagName: "v1.8.0", Draft: true},
			{TagName: "v1.7.0"},
		}
		_ = json.NewEncoder(w).Encode(list)
	})

	ctx := context.Background()
	got, err := client.ListReleases(ctx, 3, false)
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(got) != 2 || got[0].TagName != "v2.0.0" || got[1].TagName != "v1.7.0" {
		t.Fatalf("默认应跳过草稿与预发布: %+v", got)
	}

	withPre, err := client.ListReleases(ctx, 3, true)
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(withPre) != 3 {
		t.Fatalf("带上预发布应有 3 条，实际 %d", len(withPre))
	}
	for _, r := range withPre {
		if r.Draft {
			t.Fatalf("草稿永远不该出现: %+v", r)
		}
	}
}

// limit 的边界：<=0 取默认 10、>100 压到 100（API 的上限）。
func TestListReleasesClampsLimit(t *testing.T) {
	var seen []string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query().Get("per_page"))
		_, _ = w.Write([]byte("[]"))
	})

	if _, err := client.ListReleases(context.Background(), 0, false); err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if _, err := client.ListReleases(context.Background(), 500, false); err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(seen) != 2 || seen[0] != "10" || seen[1] != "100" {
		t.Fatalf("per_page 夹取不对: %v", seen)
	}
}

// 上游出错时要透出错误，而不是返回空列表（否则界面会显示「没有版本」）。
func TestListReleasesPropagatesError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})
	if _, err := client.ListReleases(context.Background(), 5, false); err == nil {
		t.Fatalf("5xx 应报错")
	}
}

// 校验文件解析：认 sha256sum 的常见写法（含 Docker 的 `sha256:` 前缀），
// 大写摘要统一转小写；解析不出来时返回空串而不是报错 —— 调用方据此退化为
// 「不校验」，而不是把整次更新判死。
func TestFetchChecksum(t *testing.T) {
	const digest = "3A7BD3E2360A3D29EEA436FCFB7E44C735D117C42D1C1835420B6B9942DD4E1B"

	cases := []struct {
		name string
		body string
		want string
	}{
		{"sha256sum 格式", digest + "  demo.zip\n", strings.ToLower(digest)},
		{"两栏无文件名", digest + "\n", strings.ToLower(digest)},
		{"docker 前缀", "sha256:" + digest + "\n", strings.ToLower(digest)},
		{"只认第一行", "no digest here\n" + digest + "  demo.zip\n", ""},
		{"长度不够不认", "abc123  demo.zip\n", ""},
		{"非十六进制不认", strings.Repeat("z", 64) + "  demo.zip\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, srv := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := client.FetchChecksum(context.Background(), srv.URL+"/sums.txt")
			if err != nil {
				t.Fatalf("FetchChecksum: %v", err)
			}
			if got != tc.want {
				t.Fatalf("摘要 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// 私有仓库的校验文件也要带 token 才拿得到。
func TestFetchChecksumSendsToken(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(strings.Repeat("a", 64) + "  demo.zip\n"))
	}))
	t.Cleanup(srv.Close)

	client := NewClient("owner/repo", "tok123", srv.Client(), WithBaseURL(srv.URL))
	if _, err := client.FetchChecksum(context.Background(), srv.URL+"/sums.txt"); err != nil {
		t.Fatalf("FetchChecksum: %v", err)
	}
	if auth != "Bearer tok123" {
		t.Fatalf("Authorization = %q", auth)
	}
}

// 取不到校验文件（网络/404）必须报错，让上层决定是否降级 —— 静默返回空串会把
// 「下载失败」伪装成「这份清单没有摘要」。
func TestFetchChecksumError(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := client.FetchChecksum(context.Background(), srv.URL+"/missing.txt"); err == nil {
		t.Fatalf("404 应报错")
	}
	if _, err := client.FetchChecksum(context.Background(), "://bad-url"); err == nil {
		t.Fatalf("非法 URL 应报错")
	}
}

// ReleaseAge：未发布为 0，过去的发布时间给出正值。
func TestReleaseAge(t *testing.T) {
	now := time.Now()
	if got := (&Release{}).ReleaseAge(now); got != 0 {
		t.Fatalf("未发布时年龄应为 0，实际 %v", got)
	}
	r := Release{PublishedAt: now.Add(-48 * time.Hour)}
	if got := r.ReleaseAge(now); got != 48*time.Hour {
		t.Fatalf("年龄应为 48h，实际 %v", got)
	}
	// 时钟偏差（发布时间在未来）不该返回负数。
	future := Release{PublishedAt: now.Add(time.Hour)}
	if got := future.ReleaseAge(now); got < 0 {
		t.Fatalf("未来时间不该给出负年龄: %v", got)
	}
}
