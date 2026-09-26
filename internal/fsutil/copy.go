// Package fsutil 提供文件系统相关的基础能力：复制、移动、清理与跳过规则。
//
// 这些函数被 updater 用于「备份 -> 替换 -> 回滚」流程，因此对错误的
// 返回比较严格：任何一步失败都会中断并向上抛出，由调用方决定是否回滚。
package fsutil

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/util"
)

// copyBufferSize 是文件复制时使用的缓冲区大小。
const copyBufferSize = 512 * 1024

// SkipFunc 判断某个相对路径是否应被跳过。
//
// rel 为相对根目录的斜杠分隔路径，isDir 表示是否为目录。
type SkipFunc func(rel string, isDir bool) bool

// SkipMatcher 依据一组名字/glob 模式生成 SkipFunc。
//
// 匹配规则（全部大小写不敏感）：
//   - 模式命中相对路径本身，例如 "User Data" 命中 "User Data"；
//   - 模式命中路径中的任意一层目录名，例如 "User Data" 能命中
//     "User Data/Default/Bookmarks"，从而整棵子树被跳过；
//   - 支持 filepath.Match 通配符，例如 "*.log"。
//
// 这保证了用户数据目录既不会被删除，也不会被新版覆盖。
func SkipMatcher(patterns []string) SkipFunc {
	clean := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.Trim(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "/")
		if p != "" {
			clean = append(clean, strings.ToLower(p))
		}
	}
	if len(clean) == 0 {
		return func(string, bool) bool { return false }
	}
	return func(rel string, _ bool) bool {
		rel = strings.ToLower(strings.ReplaceAll(filepath.ToSlash(rel), `\`, "/"))
		if rel == "" || rel == "." {
			return false
		}
		segments := strings.Split(rel, "/")
		for _, pattern := range clean {
			if rel == pattern {
				return true
			}
			if ok, err := filepath.Match(pattern, rel); err == nil && ok {
				return true
			}
			if !strings.ContainsAny(pattern, "*?[") {
				for _, seg := range segments {
					if seg == pattern {
						return true
					}
				}
				continue
			}
			// 含通配符时逐层匹配，用于 "*.log" 这类规则。
			for _, seg := range segments {
				if ok, err := filepath.Match(pattern, seg); err == nil && ok {
					return true
				}
			}
		}
		return false
	}
}

// CopyFile 复制单个文件并保留权限位，返回写入的字节数。
func CopyFile(src, dst string) (int64, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return 0, fmt.Errorf("读取源文件 %s: %w", src, err)
	}
	if srcInfo.IsDir() {
		return 0, fmt.Errorf("源路径 %s 是目录", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, fmt.Errorf("创建目标目录: %w", err)
	}

	in, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("打开源文件 %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode().Perm())
	if err != nil {
		return 0, fmt.Errorf("创建目标文件 %s: %w", dst, err)
	}

	buf := make([]byte, copyBufferSize)
	n, copyErr := io.CopyBuffer(out, in, buf)
	closeErr := out.Close()
	if copyErr != nil {
		return n, fmt.Errorf("复制 %s -> %s: %w", src, dst, copyErr)
	}
	if closeErr != nil {
		return n, fmt.Errorf("写入 %s: %w", dst, closeErr)
	}
	// 显式设置一次权限，避免 umask 干扰（Windows 上为空操作）。
	if err := os.Chmod(dst, srcInfo.Mode().Perm()); err != nil && !errors.Is(err, fs.ErrInvalid) {
		return n, fmt.Errorf("设置权限 %s: %w", dst, err)
	}
	return n, nil
}

// CopyDir 递归复制目录树，skip 命中的路径会被整体跳过。
//
// dst 会被创建；符号链接按照其指向的内容复制（保持简单与可预期）。
func CopyDir(src, dst string, skip SkipFunc) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	if !util.DirExists(src) {
		return fmt.Errorf("源目录不存在: %s", src)
	}
	if skip == nil {
		skip = func(string, bool) bool { return false }
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("创建目标目录 %s: %w", dst, err)
	}

	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("遍历 %s: %w", path, err)
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return fmt.Errorf("计算相对路径 %s: %w", path, relErr)
		}
		if rel == "." {
			return nil
		}
		if skip(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("创建目录 %s: %w", target, err)
			}
			return nil
		}
		// WalkDir 不跟随符号链接，目录型链接会走到这里被当成文件：
		// CopyFile 里 os.Stat 跟随链接后判定为目录并报错，一个链接就让整次
		// 备份失败 —— 而备份失败等于更新中止。悬空链接同理。
		if d.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(path); err != nil {
				// 悬空链接：跳过而不是中断，它不是本次更新关心的内容。
				return nil
			} else if st.IsDir() {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return fmt.Errorf("创建目录 %s: %w", target, err)
				}
				return nil
			}
		}
		if _, err := CopyFile(path, target); err != nil {
			return err
		}
		return nil
	})
}

// RemoveContents 删除 dir 下的一级条目，keep 中命名的条目会被保留。
//
// 用于「替换」阶段清空旧版本文件，同时保护用户数据目录。
func RemoveContents(dir string, keep []string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取目录 %s: %w", dir, err)
	}
	skip := SkipMatcher(keep)
	var errs []error
	for _, e := range entries {
		if skip(e.Name(), e.IsDir()) {
			continue
		}
		target := filepath.Join(dir, e.Name())
		if err := RemoveAll(target); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeAttempts/removeBackoff 控制删除的重试节奏。
//
// Windows 上删除失败大多是瞬时占用：杀毒扫描、搜索索引器、资源管理器缩略图
// 都会短时间持有文件句柄。直接上报会把「本可避免的失败」变成一次回滚，
// 而回滚走的是同一个实现，可能再次失败退化成回滚失败。
const (
	removeAttempts = 3
	removeBackoff  = 150 * time.Millisecond
)

// RemoveAll 删除文件或目录，目标不存在时视为成功。
//
// 与 os.RemoveAll 的区别：遇失败会退避重试并清掉只读位
// （os.Chmod 在 Windows 上会清除 FILE_ATTRIBUTE_READONLY），
// 仍失败则返回带路径的可读错误，而不是静默成功。
func RemoveAll(path string) error {
	if path == "" {
		return nil
	}
	var err error
	for attempt := 0; attempt < removeAttempts; attempt++ {
		if err = os.RemoveAll(path); err == nil {
			return nil
		}
		// 已经不存在（比如别的重试删掉了）就不要当成失败。
		if _, statErr := os.Lstat(path); statErr != nil {
			return nil
		}
		_ = os.Chmod(path, 0o666)
		time.Sleep(time.Duration(attempt+1) * removeBackoff)
	}
	return fmt.Errorf("删除 %s: %w", path, err)
}

// Move 优先使用 rename 移动路径；跨卷失败时回退为复制后删除。
func Move(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("创建目标父目录: %w", err)
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", src, err)
	}
	if info.IsDir() {
		if err := CopyDir(src, dst, nil); err != nil {
			return err
		}
	} else if _, err := CopyFile(src, dst); err != nil {
		return err
	}
	return RemoveAll(src)
}

// DirSize 递归统计目录占用字节数（不跟随符号链接指向的内容）。
func DirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("统计目录大小 %s: %w", dir, err)
	}
	return total, nil
}

// WriteFileAtomic 先写入临时文件再改名，避免半写文件被发现。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建目录: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("写入 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换 %s: %w", path, err)
	}
	return nil
}

// IsEmptyDir 报告目录是否存在且不含任何条目。
func IsEmptyDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return len(entries) == 0, nil
}

// DiskFree 返回 dir 所在卷的可用字节数（单位：字节）。
func DiskFree(dir string) (uint64, error) {
	return diskFree(dir)
}
