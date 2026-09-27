package core

import "errors"

// 跨适配器统一的错误语义。适配器必须用 errors.Is/As 包装这些哨兵值，
// engine 与 TUI 依此决定重试、提示或回滚。
var (
	// ErrNotFound 资源不存在（仓库 / 版本 / 文件）。
	ErrNotFound = errors.New("资源不存在")
	// ErrUpToDate 已是最新版本。
	ErrUpToDate = errors.New("已是最新版本")
	// ErrBlocked 被运行中的进程占用。
	ErrBlocked = errors.New("已被运行中的进程占用")
	// ErrChecksum 校验失败。
	ErrChecksum = errors.New("校验失败")
	// ErrVerifyFailed 执行成功但结果复核失败（例如安装器退出码为 0 却没装上）。
	ErrVerifyFailed = errors.New("安装结果复核失败")
	// ErrNetwork 网络错误（可重试）。
	ErrNetwork = errors.New("网络错误")
	// ErrRateLimited 上游限流（可重试，带退避）。
	ErrRateLimited = errors.New("上游限流")
	// ErrPermission 权限不足。
	ErrPermission = errors.New("权限不足")
	// ErrUnsupported 适配器不支持该操作。
	ErrUnsupported = errors.New("适配器不支持该操作")
	// ErrUserAborted 用户取消。
	ErrUserAborted = errors.New("用户取消")
	// ErrRollbackFailed 回滚失败，需要人工介入（错误里必须带备份路径）。
	ErrRollbackFailed = errors.New("回滚失败")
	// ErrDisabled 软件在清单里被停用：不参与检查与更新。
	ErrDisabled = errors.New("软件已停用")
	// ErrConflict 多源冲突（同 ID / 同目标）。
	ErrConflict = errors.New("软件冲突")
)

// Retryable 报告错误是否值得重试。
func Retryable(err error) bool {
	return errors.Is(err, ErrNetwork) || errors.Is(err, ErrRateLimited)
}
