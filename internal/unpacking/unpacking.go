// Package unpacking 提供「下载物 → 文件树」的解包适配器。
//
// 体积考虑：zip / tar.gz / raw 用标准库；7z 与 zstd 需要第三方纯 Go 库，暂不引入
// （见 docs/design-multi-app.md §12.7 的裁剪开关规划）。
package unpacking

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dezhishen/upkit/internal/archive"
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/util"
)

// 已注册的解包类型。
const (
	KindZip   = "zip"
	KindTarGz = "tar.gz"
	KindRaw   = "raw"
)

// 选项名。
const (
	optFindRoot = "find_root" // 在解压结果里定位包含该文件的目录
	optStrip    = "strip"     // 跳过前 N 层目录（1 = 去掉单层包裹目录）
)

// NewZip 构造 zip 解包器。
func NewZip(app core.AppRef, _ registry.Deps) (core.Unpacker, error) {
	return &zipUnpacker{opts: app.UnpackOpts}, nil
}

// NewTarGz 构造 tar.gz 解包器。
func NewTarGz(app core.AppRef, _ registry.Deps) (core.Unpacker, error) {
	return &tarGzUnpacker{opts: app.UnpackOpts}, nil
}

// NewRaw 构造「不解包」适配器：产物本身就是安装器（exe/msi）。
func NewRaw(_ core.AppRef, _ registry.Deps) (core.Unpacker, error) {
	return rawUnpacker{}, nil
}

type zipUnpacker struct{ opts map[string]string }

func (z *zipUnpacker) Name() string { return KindZip }

func (z *zipUnpacker) Unpack(ctx context.Context, req core.UnpackRequest, sink core.EventSink) (core.UnpackResult, error) {
	sink.Emit(core.Event{Kind: core.EventPhase, Phase: "解压", Msg: "解压 zip 归档"})
	stats, err := archive.ExtractZip(ctx, req.ArchivePath, req.DestDir, func(done, total int, _ string) {
		sink.Emit(core.Event{Kind: core.EventProgress, Phase: "解压", Done: int64(done), Total: int64(total)})
	})
	if err != nil {
		return core.UnpackResult{}, err
	}
	root, err := resolveRoot(req.DestDir, mergeOpts(z.opts, req.Opts))
	if err != nil {
		return core.UnpackResult{}, err
	}
	return core.UnpackResult{Root: root, Files: stats.Files, Bytes: stats.Bytes}, nil
}

type tarGzUnpacker struct{ opts map[string]string }

func (t *tarGzUnpacker) Name() string { return KindTarGz }

func (t *tarGzUnpacker) Unpack(ctx context.Context, req core.UnpackRequest, sink core.EventSink) (core.UnpackResult, error) {
	sink.Emit(core.Event{Kind: core.EventPhase, Phase: "解压", Msg: "解压 tar.gz 归档"})
	res, err := extractTarGz(ctx, req.ArchivePath, req.DestDir)
	if err != nil {
		return core.UnpackResult{}, err
	}
	root, err := resolveRoot(req.DestDir, mergeOpts(t.opts, req.Opts))
	if err != nil {
		return core.UnpackResult{}, err
	}
	res.Root = root
	return res, nil
}

type rawUnpacker struct{}

func (rawUnpacker) Name() string { return KindRaw }

// Unpack 不解包：Root 直接指向下载下来的安装器文件。
func (rawUnpacker) Unpack(_ context.Context, req core.UnpackRequest, sink core.EventSink) (core.UnpackResult, error) {
	sink.Emit(core.Event{Kind: core.EventPhase, Phase: "解压", Msg: "无需解包（安装器类产物）"})
	info, err := os.Stat(req.ArchivePath)
	if err != nil {
		return core.UnpackResult{}, fmt.Errorf("读取产物: %w", err)
	}
	return core.UnpackResult{Root: req.ArchivePath, Files: 1, Bytes: info.Size()}, nil
}

// mergeOpts 合并选项：请求里的同名键优先（便于测试与临时覆盖）。
func mergeOpts(base, over map[string]string) map[string]string {
	if len(over) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// resolveRoot 决定「安装源根目录」：优先 find_root，其次 strip，最后取解压目录。
func resolveRoot(destDir string, opts map[string]string) (string, error) {
	if marker := strings.TrimSpace(opts[optFindRoot]); marker != "" {
		root, err := findRootWithMarker(destDir, marker)
		if err != nil {
			return "", err
		}
		return root, nil
	}
	if strip := atoiSafe(opts[optStrip]); strip > 0 {
		return stripLayers(destDir, strip)
	}
	return destDir, nil
}

// findRootWithMarker 在解压结果中定位包含 marker 文件的目录。
func findRootWithMarker(destDir, marker string) (string, error) {
	if util.FileExists(filepath.Join(destDir, marker)) {
		return destDir, nil
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return "", fmt.Errorf("读取解压目录: %w", err)
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		child := filepath.Join(destDir, e.Name())
		if util.FileExists(filepath.Join(child, marker)) {
			found = append(found, child)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("解压结果中未找到 %s（目录: %s）", marker, destDir)
	case 1:
		return found[0], nil
	default:
		return found[0], nil
	}
}

// stripLayers 跳过前 n 层目录。
func stripLayers(destDir string, n int) (string, error) {
	cur := destDir
	for i := 0; i < n; i++ {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", fmt.Errorf("读取解压目录: %w", err)
		}
		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(cur, e.Name()))
			}
		}
		if len(dirs) != 1 {
			return "", fmt.Errorf("strip=%d 失败：%s 下有 %d 个目录", n, cur, len(dirs))
		}
		cur = dirs[0]
	}
	return cur, nil
}

// extractTarGz 解压 tar.gz（含路径逃逸防护）。
func extractTarGz(ctx context.Context, archivePath, destDir string) (core.UnpackResult, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return core.UnpackResult{}, fmt.Errorf("打开归档 %s: %w", archivePath, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return core.UnpackResult{}, fmt.Errorf("读取 gzip: %w", err)
	}
	defer gz.Close()

	if err := util.EnsureDir(destDir); err != nil {
		return core.UnpackResult{}, err
	}
	root, err := filepath.Abs(destDir)
	if err != nil {
		return core.UnpackResult{}, err
	}

	var res core.UnpackResult
	var written int64
	entries := 0
	tr := tar.NewReader(gz)
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, fmt.Errorf("读取 tar: %w", err)
		}
		// 与 zip 分支一致：条目数与写出体积都要封顶，
		// 否则一个小体积高压缩比的包能把磁盘写满。
		entries++
		if entries > archive.MaxZipEntries {
			return res, fmt.Errorf("压缩包条目数 %d 超过上限 %d", entries, archive.MaxZipEntries)
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			continue
		}
		target, err := secureJoin(root, hdr.Name)
		if err != nil {
			return res, err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return res, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return res, err
			}
			n, copyErr := io.Copy(out, io.LimitReader(tr, archive.MaxExtractedBytes-written+1))
			closeErr := out.Close()
			if copyErr != nil {
				return res, fmt.Errorf("解压 %s: %w", hdr.Name, copyErr)
			}
			if closeErr != nil {
				return res, closeErr
			}
			written += n
			if written > archive.MaxExtractedBytes {
				_ = os.Remove(target)
				return res, fmt.Errorf("解压 %s: 解压后总体积超过上限 %d 字节（疑似压缩炸弹）", hdr.Name, archive.MaxExtractedBytes)
			}
			res.Files++
			res.Bytes += n
		}
	}
	return res, nil
}

// secureJoin 阻止 tar 条目的路径逃逸。
func secureJoin(root, name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("归档条目使用绝对路径，已拒绝: %q", name)
	}
	target := filepath.Join(root, cleaned)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("归档条目试图逃逸目标目录，已拒绝: %q", name)
	}
	return target, nil
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
