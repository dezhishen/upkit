package logging

import (
	"time"
)

// Elapsed 便于日志里统一打印耗时。
func Elapsed(start time.Time) time.Duration { return time.Since(start).Truncate(time.Millisecond) }
