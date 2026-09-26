package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// logFileName 是当前日志文件的固定名。
//
// 轮转后的历史文件由 lumberjack 追加时间戳后缀。不再按天命名：
// 按天切成多份会让「找最近一次运行的日志」变成先猜日期。
const logFileName = "upkit.jsonl"

// newLumberjack 构造带轮转的文件写入端。
//
// 轮转交给 lumberjack：单文件超限换新、按份数/天数清理、gzip 压缩这些边界，
// 自己实现容易留下「日志悄悄堆满磁盘」或「该留的被删了」这类只有长期运行
// 才暴露的问题，而且很难测。
func newLumberjack(opts Options) *lumberjack.Logger {
	return &lumberjack.Logger{
		Filename:   filepath.Join(opts.Dir, logFileName),
		MaxSize:    opts.MaxSizeMB,
		MaxBackups: opts.MaxFiles,
		MaxAge:     opts.MaxAgeDays,
		Compress:   opts.Compress,
		LocalTime:  true,
	}
}

// redactCore 在编码写出之前抹掉凭据。
//
// zap 没有内置脱敏，而 Core 正好是「编码前最后一站」：包一层就能同时覆盖
// 消息与字段，且不影响下面的 encoder、采样与轮转。
type redactCore struct{ zapcore.Core }

func (c redactCore) With(fs []zapcore.Field) zapcore.Core {
	// 必须在 With 阶段就脱敏：这些字段会被固化进内层 Core，
	// 到 Write 时已不再出现在本次调用的 fields 参数里。
	out := make([]zapcore.Field, 0, len(fs))
	for _, f := range fs {
		out = append(out, redactField(f))
	}
	return redactCore{c.Core.With(out)}
}

// Check 必须覆盖。
//
// zap 的写入路径是 Core.Check → CheckedEntry → Write。若只嵌核心 Core 而不覆盖 Check，
// CheckedEntry 里注册的会是内层 Core，本类型的 Write 永远不会被调用 —— 脱敏看
// 起来接上了，实际一条都没生效。
func (c redactCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c redactCore) Write(ent zapcore.Entry, fs []zapcore.Field) error {
	ent.Message = redactText(ent.Message)
	out := make([]zapcore.Field, 0, len(fs))
	for _, f := range fs {
		out = append(out, redactField(f))
	}
	return c.Core.Write(ent, out)
}

// redactField 抹掉单个字段里的凭据。
func redactField(f zapcore.Field) zapcore.Field {
	if isSensitiveKey(f.Key) {
		return zap.String(f.Key, redacted)
	}
	if f.Type == zapcore.StringType {
		f.String = redactText(f.String)
	}
	return f
}

// ringCore 把记录同时投进内存环形缓冲，供 TUI 的日志面板展示。
//
// 做成 Core 而不是 zap.Hook：Hook 在 Core 之后触发，拿不到已被脱敏的消息。
type ringCore struct {
	zapcore.Core
	ring  *Ring
	runID string
}

func (c ringCore) With(fs []zapcore.Field) zapcore.Core {
	// 附加字段直接透传：环形缓冲只展示消息与级别，不重建完整上下文。
	return ringCore{Core: c.Core.With(fs), ring: c.ring, runID: c.runID}
}

// Check 同样要覆盖：否则记录进不了环形缓冲（原因见 redactCore.Check）。
func (c ringCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c ringCore) Write(ent zapcore.Entry, fs []zapcore.Field) error {
	kv := make(map[string]any, len(fs))
	for _, f := range fs {
		if f.Key == "" {
			continue
		}
		kv[f.Key] = fieldValue(f)
	}
	c.ring.Add(Record{
		At:    ent.Time,
		Level: ent.Level.String(),
		App:   stringField(fs, "app"),
		Phase: stringField(fs, "phase"),
		Msg:   ent.Message,
		KV:    kv,
	})
	return c.Core.Write(ent, fs)
}

// fieldValue 取字段的可读值。
func fieldValue(f zapcore.Field) any {
	if f.Type == zapcore.StringType {
		return f.String
	}
	return f.Interface
}

// stringField 取出名为 key 的字符串字段（不存在时返回空串）。
func stringField(fs []zapcore.Field, key string) string {
	for _, f := range fs {
		if f.Key == key && f.Type == zapcore.StringType {
			return f.String
		}
	}
	return ""
}

// newCore 组装日志输出链路，并返回需要关闭的轮转器（无文件输出时为 nil）。
//
// 用 zapcore.NewTee 组合多个输出端 —— 这是 zap 原生能力，
// 取代原先手写的 fanout handler。
func newCore(opts Options, ring *Ring, runID string) (zapcore.Core, io.Closer) {
	level := levelOf(opts.Level)

	encoderCfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.RFC3339NanoTimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	}

	var closer io.Closer
	cores := make([]zapcore.Core, 0, 2)
	if strings.TrimSpace(opts.Dir) != "" {
		lj := newLumberjack(opts)
		closer = lj
		cores = append(cores, zapcore.NewCore(
			zapcore.NewJSONEncoder(encoderCfg), zapcore.AddSync(lj), level))
	}
	if opts.Console {
		consoleCfg := encoderCfg
		// zap 的彩色 encoder 不认 NO_COLOR，这里自己判一次：
		// 尊重该变量的终端与用户不在少数，日志里混进转义序列会很难看。
		if os.Getenv("NO_COLOR") != "" {
			consoleCfg.EncodeLevel = zapcore.CapitalLevelEncoder
		} else {
			consoleCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		}
		cores = append(cores, zapcore.NewCore(
			zapcore.NewConsoleEncoder(consoleCfg), zapcore.AddSync(os.Stderr), level))
	}

	var base zapcore.Core
	switch len(cores) {
	case 0:
		base = zapcore.NewNopCore()
	case 1:
		base = cores[0]
	default:
		base = zapcore.NewTee(cores...)
	}

	// ringCore 放在 redactCore 之内，这样进环形缓冲的内容已经是脱敏过的：
	// 面板上看到的与落盘的一致，不会因为“只是展示”而漏。
	inner := ringCore{Core: base, ring: ring, runID: runID}
	if opts.Redact {
		return redactCore{inner}, closer
	}
	return inner, closer
}

// newZapLogger 按选项构造 zap logger，并返回需要关闭的轮转器。
func newZapLogger(opts Options, ring *Ring, runID string) (*zap.Logger, io.Closer) {
	core, closer := newCore(opts, ring, runID)
	return zap.New(core), closer
}

// toFields 把 core.Logger 的 kv 展开成 zap 字段（奇数个时补占位）。
func toFields(kv []any) []zapcore.Field {
	out := make([]zapcore.Field, 0, (len(kv)+1)/2)
	for i := 0; i < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			key = fmt.Sprintf("key%d", i)
		}
		if i+1 < len(kv) {
			out = append(out, zap.Any(key, kv[i+1]))
			continue
		}
		out = append(out, zap.String(key, "<missing>"))
	}
	return out
}

// levelOf 把配置里的级别名映射成 zap 级别。
func levelOf(level string) zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace", "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
