package pluginsource

import (
	"context"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/core"
)

// 插件来源必须把插件给的摘要交给引擎校验：不实现 Verifier 时，插件来源的产物
// 只显示摘要、下载后不校验 —— 那是「装了但没验」。
func TestExpectedDigestFeedsPluginDigestToVerifier(t *testing.T) {
	r := &resolver{sourceID: "upkit-hub", appID: "ungoogled-chromium"}
	ctx := context.Background()

	cases := []struct {
		name   string
		digest string
		want   string
	}{
		{"带 sha256: 前缀", "sha256:824857DCD68BCA34FF21FFD06F55610FDEA98BE91B6328A826F3497E4881EA4A",
			"824857dcd68bca34ff21ffd06f55610fdea98be91b6328a826f3497e4881ea4a"},
		{"裸十六进制", "824857dcd68bca34ff21ffd06f55610fdea98be91b6328a826f3497e4881ea4a",
			"824857dcd68bca34ff21ffd06f55610fdea98be91b6328a826f3497e4881ea4a"},
		{"带空白", "  sha256:abc  ", "abc"},
		{"没有摘要", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := r.ExpectedDigest(ctx, core.AppRef{}, core.Artifact{Digest: c.digest})
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if got != c.want {
				t.Fatalf("得到 %q，期望 %q", got, c.want)
			}
		})
	}

	// 引擎把它直接交给 download.VerifySHA256，所以必须是**不带前缀的小写十六进制**。
	got, err := r.ExpectedDigest(ctx, core.AppRef{}, core.Artifact{Digest: "SHA256:" + strings.ToUpper("abcdef")})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if strings.Contains(got, ":") || got != strings.ToLower(got) {
		t.Fatalf("返回给引擎的摘要应当是无前缀的小写十六进制，实际 %q", got)
	}
}
