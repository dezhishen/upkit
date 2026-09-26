// Package archive 负责安全地解压 zip 归档。
//
// 安全性：所有条目都会经过路径归并校验，杜绝 zip-slip（../../ 逃逸）攻击。
package archive

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dezhishen/upkit/internal/util"
)

// ProgressFunc 在解压过程中被调用（done/total 为条目数）。
type ProgressFunc func(done, total int, current string)

// 解压配额：挡住「一个几百 KB 的压缩包解出几十 GB」的压缩炸弹。
//
// 磁盘预检用的是压缩包体积，对小体积高压缩比的包给不出任何保护；
// 而写满盘时旧版本已经被删，回滚同样需要空间，会连锁失败。
const (
	// MaxZipEntries 是单个压缩包允许的条目数上限。
	MaxZipEntries = 200_000
	// MaxExtractedBytes 是单次解压允许写出的总字节上限。
	MaxExtractedBytes = 32 << 30 // 32 GiB
)

// Stats 汇总一次解压的结果。
type Stats struct {
	Files int
	Dirs  int
	Bytes int64
}

// ExtractZip 把 zipPath 解压到 destDir，已存在文件会被覆盖。
func ExtractZip(ctx context.Context, zipPath, destDir string, progress ProgressFunc) (Stats, error) {
	var stats Stats

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return stats, fmt.Errorf("打开压缩包 %s: %w", zipPath, err)
	}
	defer r.Close()

	if err := util.EnsureDir(destDir); err != nil {
		return stats, fmt.Errorf("创建解压目录 %s: %w", destDir, err)
	}
	root, err := filepath.Abs(destDir)
	if err != nil {
		return stats, fmt.Errorf("解析解压目录: %w", err)
	}

	total := len(r.File)
	// 条目数也要设上限：几万个小文件同样能把磁盘写满，
	// 而磁盘预检用的是压缩包体积，拦不住这种放大。
	if total > MaxZipEntries {
		return stats, fmt.Errorf("压缩包条目数 %d 超过上限 %d", total, MaxZipEntries)
	}

	var written int64
	for i, f := range r.File {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		target, err := secureJoin(root, f.Name)
		if err != nil {
			return stats, err
		}
		if progress != nil {
			progress(i, total, f.Name)
		}

		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			// 跳过符号链接：绿色版不需要它们，且链接目标可能逃逸出目标目录。
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return stats, fmt.Errorf("创建目录 %s: %w", target, err)
			}
			stats.Dirs++
			continue
		}
		if !mode.IsRegular() {
			continue
		}

		n, err := extractFile(f, target, mode, MaxExtractedBytes-written)
		if err != nil {
			return stats, err
		}
		if err := os.Chtimes(target, f.Modified, f.Modified); err != nil && !os.IsNotExist(err) {
			// 时间戳失败不影响正确性，忽略。
			_ = err
		}
		stats.Files++
		stats.Bytes += n
		written += n
	}
	if progress != nil {
		progress(total, total, "")
	}
	return stats, nil
}

// extractFile 写出单个条目并返回字节数。
//
// quota 是本次写入允许占用的最大字节数，用于挡住「小压缩包解出巨量体积」。
func extractFile(f *zip.File, target string, mode os.FileMode, quota int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, fmt.Errorf("创建目录 %s: %w", filepath.Dir(target), err)
	}
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return 0, fmt.Errorf("创建文件 %s: %w", target, err)
	}
	rc, err := f.Open()
	if err != nil {
		_ = out.Close()
		return 0, fmt.Errorf("读取压缩条目 %s: %w", f.Name, err)
	}
	// 多读 1 字节用于判断越界，避免把「刚好等于配额」误判为超限。
	n, copyErr := io.Copy(out, io.LimitReader(rc, quota+1))
	closeErr := out.Close()
	_ = rc.Close()
	if copyErr != nil {
		return n, fmt.Errorf("解压 %s: %w", f.Name, copyErr)
	}
	if closeErr != nil {
		return n, fmt.Errorf("写入 %s: %w", target, closeErr)
	}
	if n > quota {
		// 不要把写了一半的内容留在盘上。
		_ = os.Remove(target)
		return n, fmt.Errorf("解压 %s: 解压后体积超过上限 %d 字节（疑似压缩炸弹）", f.Name, MaxExtractedBytes)
	}
	return n, nil
}

// secureJoin 把 zip 内的名字安全地拼接到 root 下，阻止路径逃逸。
func secureJoin(root, name string) (string, error) {
	if strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("压缩包条目名包含非法字符: %q", name)
	}
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if cleaned == "." {
		return root, nil
	}
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("压缩包条目使用绝对路径，已拒绝: %q", name)
	}
	target := filepath.Join(root, cleaned)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("校验条目路径 %q: %w", name, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("压缩包条目试图逃逸目标目录，已拒绝: %q", name)
	}
	return target, nil
}

// FindAppRoot 在解压结果中定位「包含 chrome.exe 的目录」。
//
// 上游压缩包有时会多包一层目录，因此这里做有限深度搜索。
func FindAppRoot(dir string) (string, error) {
	const maxDepth = 3

	if util.FileExists(filepath.Join(dir, "chrome.exe")) {
		return dir, nil
	}
	type candidate struct {
		path  string
		depth int
	}
	queue := []candidate{{path: dir, depth: 0}}
	var found []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth > maxDepth {
			continue
		}
		entries, err := os.ReadDir(cur.path)
		if err != nil {
			return "", fmt.Errorf("读取目录 %s: %w", cur.path, err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			p := filepath.Join(cur.path, e.Name())
			if e.IsDir() {
				if util.FileExists(filepath.Join(p, "chrome.exe")) {
					found = append(found, p)
					continue
				}
				queue = append(queue, candidate{path: p, depth: cur.depth + 1})
				continue
			}
			if strings.EqualFold(e.Name(), "chrome.exe") {
				found = append(found, cur.path)
			}
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("解压结果中未找到 chrome.exe（目录: %s）", dir)
	case 1:
		return found[0], nil
	default:
		sort.Strings(found)
		return found[0], nil
	}
}
