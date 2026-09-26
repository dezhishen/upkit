// Package manifest 负责把「设置 + 清单 + 已装版本」导出成单文件，便于换机重装。
//
// 导出文件是 JSON，包含完整的 apps.yaml 内容；导入时只合并软件清单（并把新增
// 与更新分开报告），不会覆盖本机 settings.yaml。
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/settings"
)

// FileName 是默认导出文件名。
const FileName = "upkit-manifest.json"

// Version 是导出文件格式版本。
const Version = 1

// File 是导出文件结构。
type File struct {
	Format     int                `json:"format"`
	Version    int                `json:"version"`
	ExportedAt time.Time          `json:"exported_at"`
	Tool       string             `json:"tool"`
	Settings   *settings.Settings `json:"settings,omitempty"`
	Apps       *apps.File         `json:"apps"`
	Installed  map[string]string  `json:"installed,omitempty"`
}

// DefaultPath 返回默认导出路径。
func DefaultPath(dataDir string) string {
	return filepath.Join(dataDir, FileName)
}

// Export 写出清单文件；path 为空时落到 <根目录>/config/upkit-manifest.json。
func Export(path string, s *settings.Settings, a *apps.File, installed map[string]string) (string, error) {
	if path == "" {
		if s != nil {
			path = s.ManifestPath()
		} else {
			path = DefaultPath(".")
		}
	}
	exported := *s
	exported.Path = ""
	f := File{
		Format:     Version,
		Version:    a.Version,
		ExportedAt: time.Now().UTC(),
		Tool:       "upkit",
		Settings:   &exported,
		Apps:       a,
		Installed:  installed,
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Load 读取导出文件。
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	if f.Apps == nil {
		return nil, fmt.Errorf("%s 不含软件清单", path)
	}
	return &f, nil
}

// MergeApps 把导出文件中的软件合并进当前清单（按 id 覆盖，保留本机已有条目）。
func (f *File) MergeApps(cur *apps.File) (added, updated []string) {
	index := map[string]int{}
	for i, a := range cur.Apps {
		index[a.ID] = i
	}
	for _, spec := range f.Apps.Apps {
		if i, ok := index[spec.ID]; ok {
			cur.Apps[i] = spec
			updated = append(updated, spec.ID)
			continue
		}
		cur.Apps = append(cur.Apps, spec)
		index[spec.ID] = len(cur.Apps) - 1
		added = append(added, spec.ID)
	}
	if len(f.Apps.Equivalents) > 0 {
		cur.Equivalents = append(cur.Equivalents, f.Apps.Equivalents...)
	}
	return added, updated
}
