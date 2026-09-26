package plugin

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// stderrLogger 把插件日志以 JSON 行写到 stderr。
//
// 宿主通过 go-plugin 的 SyncStderr 接管这条流，统一加上 plugin/<插件ID> 前缀、
// 套用级别与脱敏规则后写入 upkit 的日志。好处是插件日志与宿主日志天然同一条流，
// 不需要额外的 RPC 通道，插件作者也不必关心落盘与轮转。
//
// 用 zap 而不是手拼 logfmt：
//   - 手拼时 msg 里的换行会伪造出整行假日志，值里的 "level=error" 能冒充级别，
//     属于日志注入；结构化编码器会自动转义。
//   - 输出格式与宿主日志（JSONL）一致，可复用同一套解析与查看方式。
//
// 插件侧只负责输出，不写文件，所以不需要 lumberjack。
type stderrLogger struct{}

// pluginLog 是插件侧日志的底层 logger。
//
// 用 SugaredLogger：Logger 的方法要求 []zap.Field，而插件 API 传的是 ...any。
var pluginLog = zap.New(zapcore.NewCore(
	zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		MessageKey:     "msg",
		NameKey:        "logger",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.EpochMillisTimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	}),
	zapcore.AddSync(os.Stderr),
	zapcore.DebugLevel,
)).Sugar()

func newStderrLogger() Logger { return stderrLogger{} }

func (stderrLogger) Debug(msg string, kv ...any) { pluginLog.Debugw(msg, kv...) }
func (stderrLogger) Info(msg string, kv ...any)  { pluginLog.Infow(msg, kv...) }
func (stderrLogger) Warn(msg string, kv ...any)  { pluginLog.Warnw(msg, kv...) }
func (stderrLogger) Error(msg string, kv ...any) { pluginLog.Errorw(msg, kv...) }
