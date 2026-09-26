// Package detect 提供「本机装的是哪个版本、装在哪」的探测适配器。
package detect

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/util"
	"github.com/dezhishen/upkit/internal/version"
)

// 已注册的探测器类型。
const (
	KindStateFile  = "state-file"
	KindPEResource = "pe-resource"
	KindDirName    = "dir-name"
	KindCLIVersion = "cli-version"
)

// NewStateFile 读取 upkit 自己写的状态文件。
func NewStateFile(app core.AppRef, _ registry.Deps) (core.Detector, error) {
	return &stateFile{app: app}, nil
}

// NewPEResource 读取入口可执行文件的 PE 版本资源（Windows 首选）。
func NewPEResource(app core.AppRef, _ registry.Deps) (core.Detector, error) {
	return &peResource{app: app}, nil
}

// NewDirName 从安装目录名里提取版本号（手工解压场景的兜底）。
func NewDirName(app core.AppRef, _ registry.Deps) (core.Detector, error) {
	return &dirName{app: app}, nil
}

// NewCLIVersion 执行命令并解析版本（需 opts: cmd="tool --version"、regex="(\\d+\\.\\d+)"）。
func NewCLIVersion(app core.AppRef, _ registry.Deps) (core.Detector, error) {
	return &cliVersion{app: app}, nil
}

type stateFile struct{ app core.AppRef }

func (d *stateFile) Name() string { return KindStateFile }

func (d *stateFile) Detect(_ context.Context, app core.AppRef) (core.Status, error) {
	path := app.InstallPath
	if path == "" {
		return core.Status{}, nil
	}
	rec, err := version.ReadRecord(path)
	if err != nil || rec == nil || rec.Version == "" {
		return core.Status{}, nil
	}
	return core.Status{
		Installed:   true,
		Version:     rec.Version,
		Path:        path,
		Source:      KindStateFile,
		InstalledAt: rec.InstalledAt,
		Size:        dirSize(path),
	}, nil
}

type peResource struct{ app core.AppRef }

func (d *peResource) Name() string { return KindPEResource }

func (d *peResource) Detect(_ context.Context, app core.AppRef) (core.Status, error) {
	for _, exe := range candidateEntrypoints(app) {
		full := filepath.Join(app.InstallPath, exe)
		if !util.FileExists(full) {
			continue
		}
		v, err := version.ProductVersion(full)
		if err != nil || v == "" {
			continue
		}
		return core.Status{
			Installed: true,
			Version:   v,
			Path:      app.InstallPath,
			Source:    KindPEResource + ":" + exe,
			Size:      dirSize(app.InstallPath),
		}, nil
	}
	return core.Status{}, nil
}

type dirName struct{ app core.AppRef }

func (d *dirName) Name() string { return KindDirName }

func (d *dirName) Detect(_ context.Context, app core.AppRef) (core.Status, error) {
	base := filepath.Base(filepath.Clean(app.InstallPath))
	v := version.MustParse(base)
	if v.IsZero() {
		return core.Status{}, nil
	}
	return core.Status{
		Installed: true,
		Version:   v.NumbersString(),
		Path:      app.InstallPath,
		Source:    KindDirName,
		Size:      dirSize(app.InstallPath),
	}, nil
}

type cliVersion struct{ app core.AppRef }

func (d *cliVersion) Name() string { return KindCLIVersion }

func (d *cliVersion) Detect(ctx context.Context, app core.AppRef) (core.Status, error) {
	cmdline := strings.TrimSpace(app.MethodOpts["version_cmd"])
	if cmdline == "" {
		cmdline = strings.TrimSpace(app.SourceOpts["version_cmd"])
	}
	if cmdline == "" {
		return core.Status{}, nil
	}
	fields := strings.Fields(cmdline)
	exe := fields[0]
	if !filepath.IsAbs(exe) {
		if local := filepath.Join(app.InstallPath, exe); util.FileExists(local) {
			exe = local
		}
	}
	if !util.FileExists(exe) {
		return core.Status{}, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, exe, fields[1:]...)
	cmd.Dir = app.InstallPath
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return core.Status{}, nil
	}
	text := string(out)
	pattern := strings.TrimSpace(app.MethodOpts["version_regex"])
	if pattern == "" {
		pattern = `\d+(?:\.\d+){1,3}`
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return core.Status{}, fmt.Errorf("version_regex 非法: %w", err)
	}
	v := re.FindString(text)
	if v == "" {
		return core.Status{}, nil
	}
	return core.Status{
		Installed: true,
		Version:   v,
		Path:      app.InstallPath,
		Source:    KindCLIVersion,
		Size:      dirSize(app.InstallPath),
	}, nil
}

// candidateEntrypoints 返回声明的入口文件；未声明时给出常见候选。
func candidateEntrypoints(app core.AppRef) []string {
	if len(app.Entrypoints) > 0 {
		return app.Entrypoints
	}
	return []string{"chrome.exe", "chrome"}
}

func dirSize(path string) int64 {
	if path == "" || !util.DirExists(path) {
		return 0
	}
	size, err := dirSizeWalk(path)
	if err != nil {
		return 0
	}
	return size
}

func dirSizeWalk(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total, err
}
