package pluginfeed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/pluginhost"
	"github.com/dezhishen/upkit/internal/util"
)

// MaxPackageBytes 是单个插件包的大小上限。
const MaxPackageBytes = 256 << 20

// InstallRequest 是一次安装（或更新）请求。
type InstallRequest struct {
	FeedURL   string
	Entry     Entry
	PluginDir string
	CacheDir  string
	// AllowDowngrade 允许安装比本机更旧的版本。默认拒绝：降级是典型的供应链攻击
	// 手法（用有已知漏洞的旧版本覆盖新版本）。
	AllowDowngrade bool
	// Authorize 在需要「跨域下载」授权时被调用；返回 false 表示用户拒绝。
	// 由界面实现成弹窗；不设置时一律拒绝跨域下载。
	Authorize func(host string) (bool, error)
	// Progress 可选：报告下载进度。
	Progress func(done, total int64)
	// BeforeWrite 在覆盖可执行文件之前被调用，返回错误即中止安装。
	//
	// 给宿主留的钩子：Windows 上正在运行的插件不能被改名覆盖（会拿到 Access is
	// denied），必须先把进程停掉再写盘。放在写盘前而不是开工前，是为了不让下载那段
	// 时间白白把插件停着。
	BeforeWrite func() error
}

// Installed 是一次成功安装的结果。
type Installed struct {
	ID       string
	Version  string
	Path     string
	SHA256   string
	Manifest string
	// Cached 为 true 表示没有再下载，而是复用了缓存里的包。
	Cached bool
}

// Install 下载、校验并落盘插件。
//
// 顺序是刻意的：先判授权、再判降级、然后才下载 —— 越早失败越省事，也避免把
// 未授权的数据落到磁盘上。
func Install(ctx context.Context, client *http.Client, req InstallRequest) (*Installed, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}

	// 1) 跨域下载必须单独授权：订阅里的非相对地址意味着下载可能被指向别处。
	if req.Entry.Location.NeedsAuthorization() {
		allowed := false
		if req.Authorize != nil {
			ok, err := req.Authorize(req.Entry.Location.Host)
			if err != nil {
				return nil, err
			}
			allowed = ok
		}
		if !allowed {
			return nil, fmt.Errorf("未授权从 %s 下载插件：该地址与订阅不同源，需要单独确认", req.Entry.Location.Host)
		}
	}

	// 2) 降级保护。
	if !req.AllowDowngrade && IsDowngrade(req.Entry.Installed, req.Entry.Plugin.Version) {
		return nil, fmt.Errorf("拒绝降级：本机已安装 %s，订阅提供 %s", req.Entry.Installed, req.Entry.Plugin.Version)
	}

	// 3) 下载（命中缓存则跳过）并强制校验摘要。
	cachePath := filepath.Join(req.CacheDir, "plugins", cacheName(req.Entry))
	data, cached, err := fetchPackage(ctx, client, req, cachePath)
	if err != nil {
		return nil, err
	}
	digest := sha256Hex(data)

	// 4) 落盘到插件目录。
	if err := os.MkdirAll(req.PluginDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建插件目录 %s: %w", req.PluginDir, err)
	}
	execPath := pluginhost.ResolveExec(req.PluginDir, req.Entry.Plugin.ID, "")
	// 盖掉旧文件之前先让宿主停掉旧进程：它开着这个文件的话，改名会以
	// 「Access is denied」失败（Windows 上必现，更新插件时最典型）。
	if req.BeforeWrite != nil {
		if err := req.BeforeWrite(); err != nil {
			return nil, fmt.Errorf("替换 %s 前准备失败: %w", execPath, err)
		}
	}
	if err := fsutil.WriteFileAtomic(execPath, data, 0o755); err != nil {
		return nil, fmt.Errorf("写入插件 %s: %w", execPath, err)
	}

	// 5) 生成 sidecar，使订阅装出来的插件与手工放置的插件完全同构。
	manifestPath, err := pluginhost.SaveManifest(req.PluginDir, pluginhost.Manifest{
		ID:           req.Entry.Plugin.ID,
		Name:         firstNonEmpty(req.Entry.Plugin.Name, req.Entry.Plugin.ID),
		Description:  req.Entry.Plugin.Description,
		Exec:         filepath.Base(execPath),
		Mode:         req.Entry.Plugin.Mode,
		Version:      req.Entry.Plugin.Version,
		SHA256:       digest,
		Subscription: req.FeedURL,
	})
	if err != nil {
		return nil, err
	}

	return &Installed{
		ID:       req.Entry.Plugin.ID,
		Version:  req.Entry.Plugin.Version,
		Path:     execPath,
		SHA256:   digest,
		Manifest: manifestPath,
		Cached:   cached,
	}, nil
}

func (r InstallRequest) validate() error {
	if strings.TrimSpace(r.FeedURL) == "" {
		return fmt.Errorf("缺少订阅地址")
	}
	if strings.TrimSpace(r.Entry.Plugin.ID) == "" {
		return fmt.Errorf("插件条目缺少 id")
	}
	if strings.TrimSpace(r.PluginDir) == "" {
		return fmt.Errorf("缺少插件目录")
	}
	if strings.TrimSpace(r.Entry.Package.URL) == "" {
		return fmt.Errorf("插件 %s 缺少下载地址", r.Entry.Plugin.ID)
	}
	if !ValidSHA256(r.Entry.Package.SHA256) {
		return fmt.Errorf("插件 %s 缺少合法的 sha256，拒绝安装", r.Entry.Plugin.ID)
	}
	return nil
}

// fetchPackage 返回包的字节内容；命中缓存时不再下载。
func fetchPackage(ctx context.Context, client *http.Client, req InstallRequest, cachePath string) ([]byte, bool, error) {
	want := NormalizeSHA256(req.Entry.Package.SHA256)
	if data, err := os.ReadFile(cachePath); err == nil && sha256Hex(data) == want {
		return data, true, nil
	}

	data, err := download(ctx, client, req)
	if err != nil {
		return nil, false, err
	}
	if got := sha256Hex(data); got != want {
		return nil, false, fmt.Errorf("插件包校验失败：期望 sha256 %s，实际 %s（下载被篡改或中断）", want, got)
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return nil, false, fmt.Errorf("创建缓存目录: %w", err)
	}
	if err := fsutil.WriteFileAtomic(cachePath, data, 0o644); err != nil {
		return nil, false, fmt.Errorf("写入缓存 %s: %w", cachePath, err)
	}
	return data, false, nil
}

func download(ctx context.Context, client *http.Client, req InstallRequest) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.Entry.Location.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造下载请求: %w", err)
	}
	httpReq.Header.Set("User-Agent", "upkit/2 pluginfeed")

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("下载插件 %s: %w", req.Entry.Plugin.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载插件 %s: HTTP %d", req.Entry.Plugin.ID, resp.StatusCode)
	}

	total := resp.ContentLength
	if total <= 0 {
		total = req.Entry.Package.Size
	}
	if total > MaxPackageBytes {
		return nil, fmt.Errorf("插件包 %d 字节超过上限 %d", total, MaxPackageBytes)
	}

	var reader io.Reader = io.LimitReader(resp.Body, MaxPackageBytes+1)
	if req.Progress != nil {
		reader = &progressReader{r: reader, total: total, report: req.Progress}
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("读取插件包: %w", err)
	}
	if len(data) > MaxPackageBytes {
		return nil, fmt.Errorf("插件包超过 %d 字节上限", MaxPackageBytes)
	}
	return data, nil
}

// progressReader 在读取过程中报告进度。
type progressReader struct {
	r      io.Reader
	total  int64
	done   int64
	report func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		p.report(p.done, p.total)
	}
	return n, err
}

// cacheName 是包在缓存里的文件名（带版本与平台，避免不同版本互相覆盖）。
//
// 版本号在 schema 层已限制字符集；这里再过一道 SanitizeFileName，
// 保证即使以后有人放宽校验，也不会把路径分隔符带进文件名。
func cacheName(e Entry) string {
	v := util.SanitizeFileName(firstNonEmpty(e.Plugin.Version, "unknown"))
	return fmt.Sprintf("%s-%s-%s", e.Plugin.ID, v, strings.ReplaceAll(Platform(), "/", "-"))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
