package pluginhost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dezhishen/upkit/internal/fsutil"
)

// Manifest 是插件目录里的 sidecar 描述文件（<id>.plugin.yaml）。
//
// sidecar 只描述「这个可执行文件是什么插件」，不承载信任决定：是否信任由清单里的
// sources[].trust 记录（sha256），见 host.go。
type Manifest struct {
	ID             string `yaml:"id"`
	Name           string `yaml:"name"`
	Description    string `yaml:"description"`
	Exec           string `yaml:"exec"`
	Mode           string `yaml:"mode"`
	Enabled        *bool  `yaml:"enabled"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	Path           string `yaml:"-"`

	// 以下字段由「订阅安装」写入，用于更新检测与审计；手工放置的插件可以留空。
	Version      string `yaml:"version,omitempty"`
	SHA256       string `yaml:"sha256,omitempty"`
	Subscription string `yaml:"subscription,omitempty"`
}

// SaveManifest 把插件描述写到插件目录（<id>.plugin.yaml）。
//
// 订阅安装落盘插件后用它生成 sidecar，这样「订阅装出来的插件」与「手工放进来的
// 插件」在后续的发现 / 信任 / 加载流程里完全一致，不存在第二条代码路径。
func SaveManifest(dir string, m Manifest) (string, error) {
	if strings.TrimSpace(m.ID) == "" {
		return "", fmt.Errorf("插件描述缺少 id")
	}
	path := filepath.Join(dir, m.ID+manifestSuffix)
	data, err := yaml.Marshal(&m)
	if err != nil {
		return "", fmt.Errorf("序列化插件描述: %w", err)
	}
	if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// EnabledValue 报告插件是否启用（缺省启用）。
func (m Manifest) EnabledValue() bool {
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

// Discover 扫描插件目录下的 *.plugin.yaml。
//
// 目录不存在时返回空列表而不是错误：没有插件是完全正常的初始状态。
func Discover(dir string) ([]Manifest, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取插件目录 %s: %w", dir, err)
	}
	var out []Manifest
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), manifestSuffix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取插件描述 %s: %w", path, err)
		}
		var m Manifest
		if err := yaml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("解析插件描述 %s: %w", path, err)
		}
		m.Path = path
		if m.ID == "" {
			m.ID = strings.TrimSuffix(e.Name(), manifestSuffix)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ResolveExec 解析插件可执行文件的绝对路径。
//
// exec 为空时按插件 id 推断文件名：upkit 只发行 Windows 版本，插件可执行文件
// 恒为 <id>.exe，不按运行平台分支。相对路径按插件目录解析。
func ResolveExec(dir, id, exec string) string {
	name := strings.TrimSpace(exec)
	if name == "" {
		name = id + extExec
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Join(dir, name)
}

// HashFile 计算文件的 sha256（小写十六进制），用于信任记录。
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("计算 %s 的哈希: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Trusted 报告记录的哈希是否与当前文件一致。
//
// 记录为空、或与文件实际哈希不同，都视为「未信任」——文件一旦被替换就必须重新信任。
func Trusted(recorded, actual string) bool {
	rec := strings.TrimSpace(recorded)
	if rec == "" || actual == "" {
		return false
	}
	return strings.EqualFold(rec, actual)
}
