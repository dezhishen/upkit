//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package fsutil

// diskFree 在不支持查询可用空间的平台上返回未知（0 表示不做空间校验）。
//
// 它存在的唯一理由是让 go build / go test 在任何 GOOS 下都跑得起来：upkit 只发行
// Windows 版本，这个文件不是「多平台支持」。
func diskFree(string) (uint64, error) {
	return 0, nil
}
