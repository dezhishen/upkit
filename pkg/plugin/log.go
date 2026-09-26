package plugin

import (
	"fmt"
	"os"
	"strings"
)

// stderrLogger 把插件日志写到 stderr。
//
// 宿主通过 go-plugin 的 SyncStderr 接管这条流，统一加上 plugin/<插件ID> 前缀、
// 套用级别与脱敏规则后写入 upkit 的日志。好处是插件日志与宿主日志天然同一条流，
// 不需要额外的 RPC 通道，插件作者也不需要关心日志落盘与轮转。
//
// 输出格式是 logfmt（key=value），便于宿主逐行解析而无需插件侧做结构化编码。
type stderrLogger struct{}

// logfmt 的格式符号。它们只是本文件私有的格式细节，不属于跨进程契约，
// 因此放在这里而不是 constants.go。
const (
	kvSeparator    = "="
	fieldSeparator = " "
)

func newStderrLogger() Logger { return stderrLogger{} }

func (stderrLogger) Debug(msg string, kv ...any) { writeLog(LogLevelDebug, msg, kv...) }
func (stderrLogger) Info(msg string, kv ...any)  { writeLog(LogLevelInfo, msg, kv...) }
func (stderrLogger) Warn(msg string, kv ...any)  { writeLog(LogLevelWarn, msg, kv...) }
func (stderrLogger) Error(msg string, kv ...any) { writeLog(LogLevelError, msg, kv...) }

func writeLog(level, msg string, kv ...any) {
	var b strings.Builder
	b.WriteString(LogKeyLevel)
	b.WriteString(kvSeparator)
	b.WriteString(level)
	b.WriteString(fieldSeparator)
	b.WriteString(LogKeyMessage)
	b.WriteString(kvSeparator)
	b.WriteString(msg)
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString(fieldSeparator)
		fmt.Fprintf(&b, "%v%s", kv[i], kvSeparator)
		fmt.Fprintf(&b, "%v", kv[i+1])
	}
	if len(kv)%2 == 1 {
		b.WriteString(fieldSeparator)
		fmt.Fprintf(&b, "%v", kv[len(kv)-1])
	}
	fmt.Fprintln(os.Stderr, b.String())
}
