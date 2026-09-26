package method

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/util"
)

// backupPrefix 是备份目录的统一前缀。
const backupPrefix = "backup_"

// createBackup 把安装目录复制到备份目录（跳过 preserve 命中的路径）。
func createBackup(installPath, backupRoot, ver string, skip fsutil.SkipFunc, log func(string, ...any)) (*core.Backup, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	if !util.DirExists(installPath) {
		return nil, fmt.Errorf("安装目录不存在，无法备份: %s", installPath)
	}
	if err := util.EnsureDir(backupRoot); err != nil {
		return nil, fmt.Errorf("创建备份根目录: %w", err)
	}

	now := time.Now()
	name := backupPrefix + util.Timestamp(now)
	if v := util.SanitizeFileName(ver); v != "" {
		name += "_" + v
	}
	dest := filepath.Join(backupRoot, name)
	if util.Exists(dest) {
		dest += fmt.Sprintf("_%d", now.UnixNano()%1_000_000)
	}

	log("备份 %s -> %s", installPath, dest)
	if err := fsutil.CopyDir(installPath, dest, skip); err != nil {
		_ = fsutil.RemoveAll(dest)
		return nil, fmt.Errorf("备份 %s: %w", installPath, err)
	}
	size, err := fsutil.DirSize(dest)
	if err != nil {
		return nil, err
	}
	return &core.Backup{Path: dest, Version: ver, CreatedAt: now, Size: size}, nil
}

// listBackups 列出备份根目录下的备份（按时间升序）。
func listBackups(backupRoot string) ([]core.Backup, error) {
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]core.Backup, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), backupPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(backupRoot, e.Name())
		b := core.Backup{Path: path, Version: parseVersionFromName(e.Name()), CreatedAt: info.ModTime()}
		if size, err := fsutil.DirSize(path); err == nil {
			b.Size = size
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// pruneBackups 只保留最新 max 份备份。
func pruneBackups(backupRoot string, max int, log func(string, ...any)) {
	if max <= 0 {
		return
	}
	backups, err := listBackups(backupRoot)
	if err != nil || len(backups) <= max {
		return
	}
	for _, b := range backups[:len(backups)-max] {
		if log != nil {
			log("清理过期备份 %s", b.Path)
		}
		_ = fsutil.RemoveAll(b.Path)
	}
}

func parseVersionFromName(name string) string {
	parts := strings.Split(strings.TrimPrefix(name, backupPrefix), "_")
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts[1:], "_")
}

// runStep 执行一个 Run 步骤并等待结束。
func runStep(ctx context.Context, step core.Step, dir string, sink core.EventSink) error {
	if len(step.Command) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, step.Command[0], step.Command[1:]...)
	cmd.Dir = dir
	hideWindow(cmd)
	sink.Emit(core.Event{Kind: core.EventLog, Level: core.LevelInfo,
		Msg: "执行: " + strings.Join(step.Command, " ")})
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 800 {
			msg = msg[:800]
		}
		if msg != "" {
			return fmt.Errorf("%s 失败: %w（输出: %s）", step.Desc, err, msg)
		}
		return fmt.Errorf("%s 失败: %w", step.Desc, err)
	}
	return nil
}

// expandTemplate 替换 {artifact} / {target} / {version} / {name} 占位符。
func expandTemplate(s string, app core.AppRef, req core.Request, version string) string {
	repl := strings.NewReplacer(
		"{artifact}", req.ArtifactPath,
		"{target}", app.InstallPath,
		"{version}", version,
		"{name}", app.ID,
		"{workdir}", req.WorkDir,
	)
	return repl.Replace(s)
}

// parseArgs 把类似 ["/S", "/DIR={target}"] 的配置展开成命令参数。
func parseArgs(list []string, app core.AppRef, req core.Request, version string) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, expandTemplate(a, app, req, version))
	}
	return out
}

// verifyEntrypoints 复核安装结果：声明的入口文件必须存在。
func verifyEntrypoints(app core.AppRef) error {
	if len(app.Entrypoints) == 0 {
		return nil
	}
	for _, e := range app.Entrypoints {
		if util.FileExists(filepath.Join(app.InstallPath, e)) {
			return nil
		}
	}
	return fmt.Errorf("安装后复核失败：%s 下未找到 %s", app.InstallPath, strings.Join(app.Entrypoints, "/"))
}

// truthy 解析配置里的布尔字符串。
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "是":
		return true
	default:
		return false
	}
}

// splitList 把 "a,b,c" 或 "a;b" 拆成切片。
func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// atoiSafe 把字符串转成 int；解析失败时返回 fallback（未提供则为 0）。
func atoiSafe(s string, fallback ...int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	if len(fallback) > 0 {
		return fallback[0]
	}
	return 0
}
