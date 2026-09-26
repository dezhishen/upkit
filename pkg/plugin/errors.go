package plugin

import "errors"

// 插件可以返回这些哨兵错误，宿主会按语义区别处理（而不是当成普通故障）。
var (
	// ErrNotSupported 表示该能力未实现（full 模式的常见返回值）。
	ErrNotSupported = errors.New("plugin: 不支持该操作")
	// ErrNotFound 表示请求的软件/版本/产物不存在。
	ErrNotFound = errors.New("plugin: 未找到")
	// ErrBadConfig 表示配置不合法，宿主会提示用户修正配置。
	ErrBadConfig = errors.New("plugin: 配置不合法")
	// ErrRateLimited 表示上游限流，宿主会提示配置令牌而不是判为失败。
	ErrRateLimited = errors.New("plugin: 上游限流")
)

// errorKind 是跨进程的错误分类。net/rpc 只能传字符串，所以错误按类别还原。
type errorKind string

// 错误分类取值。
const (
	kindOK           errorKind = ""
	kindOther        errorKind = "other"
	kindNotSupported errorKind = "not_supported"
	kindNotFound     errorKind = "not_found"
	kindBadConfig    errorKind = "bad_config"
	kindRateLimited  errorKind = "rate_limited"
	kindCanceled     errorKind = "canceled"
)

// classifyError 把错误映射成可跨进程传输的分类。
func classifyError(err error) errorKind {
	switch {
	case err == nil:
		return kindOK
	case errors.Is(err, ErrNotSupported):
		return kindNotSupported
	case errors.Is(err, ErrNotFound):
		return kindNotFound
	case errors.Is(err, ErrBadConfig):
		return kindBadConfig
	case errors.Is(err, ErrRateLimited):
		return kindRateLimited
	default:
		return kindOther
	}
}
