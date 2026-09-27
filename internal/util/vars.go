package util

import "strings"

// ExpandVars 把 s 里的 ${NAME} / $NAME 按 vars 替换，未列出的保持原样。
//
// 与 ExpandPath 的分工：ExpandPath 处理环境变量、%VAR% 与 ~，这里的 vars 是宿主自己
// 的变量（如 ${ROOT} 指安装根目录、${ARCH} 指平台架构）。先过这一层再过 ExpandPath，
// 于是「插件声明的 ${ROOT}/fzf」不会被当成环境变量而展开成空串 —— 那正是之前把软件
// 装到当前盘根目录的原因。
//
// 只认「变量名字符」组成的名字（字母、数字、下划线）：`${ROOT}` 与 `$ROOT/` 都能替
// 换，而 `$ROOTDIR` 不会被误当成 `$ROOT` 后面跟个 DIR。
func ExpandVars(s string, vars map[string]string) string {
	if s == "" || len(vars) == 0 || !strings.ContainsRune(s, '$') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// ${NAME}
		if i+1 < len(s) && s[i+1] == '{' {
			if end := strings.IndexByte(s[i+2:], '}'); end >= 0 {
				name := s[i+2 : i+2+end]
				if v, ok := vars[name]; ok {
					b.WriteString(v)
					i += 2 + end + 1
					continue
				}
			}
			b.WriteByte(s[i])
			i++
			continue
		}
		// $NAME
		j := i + 1
		for j < len(s) && isVarChar(s[j]) {
			j++
		}
		if j > i+1 {
			if v, ok := vars[s[i+1:j]]; ok {
				b.WriteString(v)
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// isVarChar 报告 c 是否可以作为变量名的一部分。
func isVarChar(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}
