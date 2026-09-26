package pluginfeed

import "testing"

func TestCompareVersionsSemantics(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.3", "1.2.4", -1},
		{"1.2.4", "1.2.3", 1},
		{"1.2", "1.2.0", -1},
		{"2.0.0", "1.9.9", 1},
		{"", "1.0.0", -1},

		// 预发布段必须按数值比较。旧实现用 strings.Compare，
		// "rc9" > "rc10"（'9' > '1'）,会让降级保护在它要防的场景下失效。
		{"1.0.0-rc9", "1.0.0-rc10", -1},
		{"1.0.0-rc10", "1.0.0-rc9", 1},
		{"1.0.0-rc1", "1.0.0-rc2", -1},
		{"1.0.0-rc2", "1.0.0-rc10", -1},
		{"1.0.0-beta", "1.0.0-rc1", -1},
		{"1.0.0-rc", "1.0.0-rc1", -1},
		{"1.0.0-alpha1", "1.0.0-alpha1", 0},

		// v 前缀必须剥掉，否则 "v1" 会被当成非数字段而得出错误结论。
		{"v1.2.0", "1.2.0", 0},
		{"V1.2.0", "1.2.0", 0},
		{"v1.2.0", "v1.2.1", -1},
	}
	for _, tc := range cases {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d，期望 %d", tc.a, tc.b, got, tc.want)
		}
		// 反对称性：调换参数必须得到相反结果。
		if got := CompareVersions(tc.b, tc.a); got != -tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d，期望 %d（对称性）", tc.b, tc.a, got, -tc.want)
		}
	}
}

// TestIsDowngradeCoversPrerelease 是降级保护的回归用例。
func TestIsDowngradeCoversPrerelease(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		// 本机 rc10，订阅给 rc9 → 这是降级，必须拦住。
		{"1.0.0-rc10", "1.0.0-rc9", true},
		{"1.0.0-rc9", "1.0.0-rc10", false},
		{"1.2.0", "1.1.9", true},
		{"1.1.9", "1.2.0", false},
		{"v1.2.0", "1.1.0", true},
		// 未安装时不算降级。
		{"", "0.0.1", false},
		{"1.0.0", "", false},
	}
	for _, tc := range cases {
		if got := IsDowngrade(tc.from, tc.to); got != tc.want {
			t.Errorf("IsDowngrade(%q, %q) = %v，期望 %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestSplitTrailingNumber(t *testing.T) {
	cases := []struct {
		in  string
		pre string
		num int
		ok  bool
	}{
		{"rc12", "rc", 12, true},
		{"rc", "rc", 0, false},
		{"1", "", 1, true},
		{"", "", 0, false},
		{"alpha0", "alpha", 0, true},
		{"x9y", "x9y", 0, false},
	}
	for _, tc := range cases {
		pre, num, ok := splitTrailingNumber(tc.in)
		if pre != tc.pre || num != tc.num || ok != tc.ok {
			t.Errorf("splitTrailingNumber(%q) = (%q, %d, %v)，期望 (%q, %d, %v)",
				tc.in, pre, num, ok, tc.pre, tc.num, tc.ok)
		}
	}
}
