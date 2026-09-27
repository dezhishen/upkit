package util

import "testing"

// 宿主变量替换：插件声明的 ${ROOT} 必须变成真正的安装根目录。
//
// 这是真实缺陷的回归：宿主此前只调 ExpandPath（os.ExpandEnv），而 ROOT 不是环境变量，
// 于是 "${ROOT}/fzf" 被展开成空串拼出来的 "/fzf" —— 软件装到了当前盘根目录。
func TestExpandVars(t *testing.T) {
	vars := map[string]string{"ROOT": `D:\Apps`, "ARCH": "amd64"}
	cases := []struct{ in, want string }{
		{`${ROOT}/fzf`, `D:\Apps/fzf`},
		{`$ROOT/fzf`, `D:\Apps/fzf`},
		{`${ROOT}\7-Zip`, `D:\Apps\7-Zip`},
		{`${ROOT}`, `D:\Apps`},
		{`${ARCH}/tool`, `amd64/tool`},
		{`$ROOTDIR/x`, `$ROOTDIR/x`},             // 名字更长，不该被当成 $ROOT
		{`${ROOT_EXTRA}/x`, `${ROOT_EXTRA}/x`},   // 未知变量保持原样，交给 ExpandPath 处理
		{`%LOCALAPPDATA%/x`, `%LOCALAPPDATA%/x`}, // %VAR% 不归这里管
		{``, ``},
	}
	for _, c := range cases {
		if got := ExpandVars(c.in, vars); got != c.want {
			t.Fatalf("ExpandVars(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}

	// vars 为空时原样返回（省掉一次扫描）。
	if got := ExpandVars(`${ROOT}/x`, nil); got != `${ROOT}/x` {
		t.Fatalf("无变量表时应原样返回，实际 %q", got)
	}
}
