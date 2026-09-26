package pluginfeed

import (
	"strings"
	"testing"
)

// mkFeed 造一个最小可用订阅，用于单点校验规则的测试。
func mkFeed(id, version string) *Feed {
	return &Feed{
		Schema: SchemaVersion,
		Plugins: []Plugin{{
			ID:      id,
			Name:    "Demo",
			Version: version,
			Packages: NewPackages(map[string]Package{
				Platform(): {
					URL:    "https://example.com/demo.exe",
					SHA256: strings.Repeat("a", 64),
					Size:   1024,
				},
			}),
		}},
	}
}

func TestValidateAcceptsSafeVersion(t *testing.T) {
	// 空 version 表示未声明，应放行；其余是实际会出现的写法。
	for _, v := range []string{"", "1.2.3", "v1.2.3", "1.0.0-rc1", "1.0.0+build.5", "2026.09.26"} {
		if err := mkFeed("demo", v).Validate("1.0.0", Platform()); err != nil {
			t.Errorf("version %q 应被接受，得到 %v", v, err)
		}
	}
}

// TestValidateRejectsUnsafeVersion 是路径穿越的回归用例。
//
// version 会被拼进缓存文件名（install.go 的 cacheName），
// 放任任意字符等于把 filepath.Join 的越界能力交给订阅方。
func TestValidateRejectsUnsafeVersion(t *testing.T) {
	bad := []string{
		"../../../etc/passwd",
		"..",
		"a/b",
		`a\b`,
		"1.0.0/x",
		".hidden",
		"-1.0.0",
		"_x",
		"a b",
		"a:b",
		"a*b",
		strings.Repeat("v", 100),
	}
	for _, v := range bad {
		err := mkFeed("demo", v).Validate("1.0.0", Platform())
		if err == nil {
			t.Errorf("version %q 应被拒绝", v)
			continue
		}
		if !strings.Contains(err.Error(), "version") {
			t.Errorf("version %q 的报错应点名 version，实际 %v", v, err)
		}
	}
}

// TestCacheNameHasNoSeparators 守住第二道防线：
// 即使以后有人放宽 schema 校验，缓存文件名也不能带出路径分隔符。
func TestCacheNameHasNoSeparators(t *testing.T) {
	e := Entry{Plugin: Plugin{ID: "demo", Version: "../../../evil"}}
	got := cacheName(e)
	for _, bad := range []string{"/", `\`, ".."} {
		if strings.Contains(got, bad) {
			t.Errorf("cacheName(%q) = %q，不应含 %q", e.Plugin.Version, got, bad)
		}
	}
}
