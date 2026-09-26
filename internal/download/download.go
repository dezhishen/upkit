// Package download 提供带进度显示、重试与校验的 HTTP 下载能力。
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dezhishen/upkit/internal/fsutil"
	"github.com/dezhishen/upkit/internal/util"
)

const (
	// PartSuffix 是下载中的临时文件后缀，下载完成后才改名。
	PartSuffix = ".part"
	// progressInterval 是进度回调的最小间隔。
	progressInterval = 200 * time.Millisecond
	// maxAttempts 是单个文件的下载尝试次数。
	maxAttempts = 3
	// readBufferSize 是读取响应体使用的缓冲区大小。
	readBufferSize = 512 * 1024
)

// ProgressFunc 在下载过程中被周期调用。
//
// done/total 为字节数（total <= 0 表示未知），speed 为字节/秒，
// elapsed 为本次下载已用时间。
type ProgressFunc func(done, total int64, speed float64, elapsed time.Duration)

// Result 描述一次下载的结果。
type Result struct {
	// Path 是最终文件路径。
	Path string
	// Size 是文件字节数。
	Size int64
	// SHA256 是小写十六进制的文件摘要。
	SHA256 string
	// Skipped 为 true 表示目标文件已存在且尺寸正确，未重复下载。
	Skipped bool
}

// Client 是下载客户端。
type Client struct {
	hc *http.Client
	ua string
}

// New 创建下载客户端。
//
// timeout 为 0 时不做整体超时（由 context 控制），更适合大文件下载；
// proxy 支持 http/https/socks5，为空则走环境变量。
func New(timeout time.Duration, proxy string) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 8
	transport.MaxIdleConnsPerHost = 8
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 30 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second

	if proxy != "" {
		pu, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("解析代理 %q: %w", proxy, err)
		}
		transport.Proxy = http.ProxyURL(pu)
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}

	return &Client{
		hc: &http.Client{Transport: transport, Timeout: timeout},
		ua: "upkit",
	}, nil
}

// HTTP 返回底层 http.Client，供 GitHub API 复用同一套代理与连接池配置。
func (c *Client) HTTP() *http.Client { return c.hc }

// SetUserAgent 设置请求使用的 User-Agent。
func (c *Client) SetUserAgent(ua string) {
	if strings.TrimSpace(ua) != "" {
		c.ua = ua
	}
}

// Download 把 url 下载到 dest。
//
//   - 先写入 dest+".part"，成功后再改名，避免中断留下半个可用文件；
//   - 失败时按指数退避重试，最多 maxAttempts 次；
//   - 始终计算 SHA256，供状态文件与校验使用。
func (c *Client) Download(ctx context.Context, rawURL, dest string, headers map[string]string, progress ProgressFunc) (Result, error) {
	if err := util.EnsureDir(filepath.Dir(dest)); err != nil {
		return Result{}, fmt.Errorf("创建下载目录: %w", err)
	}
	part := dest + PartSuffix

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		res, err := c.downloadOnce(ctx, rawURL, dest, part, headers, progress)
		if err == nil {
			return res, nil
		}
		lastErr = err
		_ = fsutil.RemoveAll(part)
		if attempt < maxAttempts && isRetryable(err) {
			wait := time.Duration(attempt) * 2 * time.Second
			if progress != nil {
				progress(0, 0, 0, 0)
			}
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		break
	}
	return Result{}, lastErr
}

func (c *Client) downloadOnce(ctx context.Context, rawURL, dest, part string, headers map[string]string, progress ProgressFunc) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("构造下载请求: %w", err)
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/octet-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("连接下载地址: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return Result{}, fmt.Errorf("下载被拒绝（HTTP %d），可尝试配置 github_token 或代理", resp.StatusCode)
	default:
		return Result{}, fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}

	total := resp.ContentLength
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Result{}, fmt.Errorf("创建临时文件 %s: %w", part, err)
	}

	hasher := sha256.New()
	pr := &progressReader{
		r:        resp.Body,
		total:    total,
		start:    time.Now(),
		lastTick: time.Now(),
		onTick:   progress,
	}
	written, copyErr := io.CopyBuffer(io.MultiWriter(f, hasher), pr, make([]byte, readBufferSize))
	syncErr := f.Sync()
	closeErr := f.Close()

	if copyErr != nil {
		_ = fsutil.RemoveAll(part)
		return Result{}, fmt.Errorf("写入 %s: %w", filepath.Base(dest), copyErr)
	}
	if syncErr != nil {
		_ = fsutil.RemoveAll(part)
		return Result{}, fmt.Errorf("刷盘 %s: %w", part, syncErr)
	}
	if closeErr != nil {
		_ = fsutil.RemoveAll(part)
		return Result{}, fmt.Errorf("关闭 %s: %w", part, closeErr)
	}
	if total > 0 && written != total {
		_ = fsutil.RemoveAll(part)
		return Result{}, fmt.Errorf("下载不完整：期望 %d 字节，实际 %d 字节", total, written)
	}

	if err := fsutil.RemoveAll(dest); err != nil {
		return Result{}, err
	}
	if err := os.Rename(part, dest); err != nil {
		return Result{}, fmt.Errorf("重命名 %s: %w", part, err)
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	if progress != nil {
		elapsed := pr.elapsed()
		speed := speedOf(written, elapsed)
		progress(written, written, speed, elapsed)
	}
	return Result{Path: dest, Size: written, SHA256: sum}, nil
}

// progressReader 统计读取进度并按固定间隔回调。
type progressReader struct {
	r        io.Reader
	total    int64
	done     int64
	start    time.Time
	lastTick time.Time
	onTick   ProgressFunc
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		if p.onTick != nil && time.Since(p.lastTick) >= progressInterval {
			p.lastTick = time.Now()
			elapsed := p.elapsed()
			p.onTick(p.done, p.total, speedOf(p.done, elapsed), elapsed)
		}
	}
	if errors.Is(err, io.EOF) && p.onTick != nil && p.total > 0 {
		elapsed := p.elapsed()
		p.onTick(p.done, p.total, speedOf(p.done, elapsed), elapsed)
	}
	return n, err
}

func (p *progressReader) elapsed() time.Duration { return time.Since(p.start) }

func speedOf(done int64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(done) / elapsed.Seconds()
}

// isRetryable 判断错误是否值得重试（网络类错误重试，HTTP 4xx 不重试）。
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"下载被拒绝", "下载失败：HTTP 4", "不完整"} {
		if strings.Contains(msg, s) {
			return false
		}
	}
	return true
}

// SHA256File 计算文件的 SHA256（小写十六进制）。
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开 %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, readBufferSize)); err != nil {
		return "", fmt.Errorf("计算 %s 摘要: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ParseSHA256 从校验文件内容中提取摘要（实现见 util）。
func ParseSHA256(content string) string {
	return util.ParseSHA256(content)
}

// VerifySHA256 校验文件摘要，expected 为空时跳过。
func VerifySHA256(path, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return nil
	}
	got, err := SHA256File(path)
	if err != nil {
		return err
	}
	if got != expected {
		return fmt.Errorf("校验失败：期望 %s，实际 %s", expected, got)
	}
	return nil
}

func isHex(s string) bool {
	return util.IsHex(s)
}
