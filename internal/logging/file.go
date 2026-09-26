package logging

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// rotatingFile 是带轮转的日志文件写入器。
//
// 文件名：upkit-YYYYMMDD-NN.jsonl（压缩后为 .jsonl.gz）。
// 轮转策略：单文件超过 MaxSizeMB 时换新文件；随后按 MaxFiles / MaxAgeDays /
// MaxTotalMB 清理旧文件。
type rotatingFile struct {
	mu         sync.Mutex
	opts       Options
	dir        string
	file       *os.File
	size       int64
	seq        int
	currentDay string
}

func newRotatingFile(opts Options) (*rotatingFile, error) {
	if opts.MaxSizeMB <= 0 {
		opts.MaxSizeMB = 16
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 20
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	rf := &rotatingFile{opts: opts, dir: opts.Dir}
	if err := rf.open(); err != nil {
		return nil, err
	}
	rf.prune()
	return rf, nil
}

// Write 实现 io.Writer。
func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.shouldRotate(int64(len(p))) {
		_ = r.file.Close()
		r.file = nil
		if err := r.open(); err != nil {
			return 0, err
		}
		r.prune()
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// Close 关闭当前文件。
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// Path 返回当前文件名（供界面上显示）。
func (r *rotatingFile) Path() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return ""
	}
	return r.file.Name()
}

func (r *rotatingFile) shouldRotate(incoming int64) bool {
	day := time.Now().Format("20060102")
	if day != r.currentDay {
		return true
	}
	return r.size+incoming > int64(r.opts.MaxSizeMB)*1024*1024
}

// open 打开（或创建）当前应写入的文件。
func (r *rotatingFile) open() error {
	day := time.Now().Format("20060102")
	r.currentDay = day
	// 找当天的下一个可写序号
	for seq := 1; seq <= r.opts.MaxFiles+1; seq++ {
		name := fmt.Sprintf("upkit-%s-%02d.jsonl", day, seq)
		full := filepath.Join(r.dir, name)
		info, err := os.Stat(full)
		if err != nil {
			f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return err
			}
			r.file = f
			r.size = 0
			r.seq = seq
			return nil
		}
		if info.Size() < int64(r.opts.MaxSizeMB)*1024*1024 {
			f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return err
			}
			r.file = f
			r.size = info.Size()
			r.seq = seq
			return nil
		}
	}
	// 序号用尽：直接追加到最后一个
	name := fmt.Sprintf("upkit-%s-%02d.jsonl", day, r.opts.MaxFiles+1)
	full := filepath.Join(r.dir, name)
	f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	r.file = f
	r.size = 0
	return nil
}

// prune 按文件数、天数与总量清理旧日志。
func (r *rotatingFile) prune() {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		mod  time.Time
		size int64
	}
	var items []item
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !hasLogPrefix(name) || !strings.Contains(name, ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(r.dir, name), info.ModTime(), info.Size()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })

	deadline := time.Now().AddDate(0, 0, -r.opts.MaxAgeDays)
	var total int64
	for i, it := range items {
		tooOld := r.opts.MaxAgeDays > 0 && it.mod.Before(deadline)
		tooMany := i >= r.opts.MaxFiles
		tooBig := r.opts.MaxTotalMB > 0 && total > int64(r.opts.MaxTotalMB)*1024*1024
		if tooOld || tooMany || tooBig {
			_ = os.Remove(it.path)
			continue
		}
		if r.opts.Compress && strings.HasSuffix(it.path, ".jsonl") && i > 0 {
			if gzErr := gzipFile(it.path); gzErr == nil {
				_ = os.Remove(it.path)
				continue
			}
		}
		total += it.size
	}
}

// logPrefix 是日志文件名前缀。
const logPrefix = "upkit-"

// hasLogPrefix 报告文件名是否属于本工具的日志。
func hasLogPrefix(name string) bool {
	return strings.HasPrefix(name, logPrefix)
}

func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(path + ".gz")
	if err != nil {
		return err
	}
	defer out.Close()
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		return err
	}
	return zw.Close()
}
