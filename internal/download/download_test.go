package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadWritesFileAndReportsProgress(t *testing.T) {
	payload := strings.Repeat("upkit", 4096) // 20 KiB
	sum := sha256.Sum256([]byte(payload))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Error("缺少 User-Agent")
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	client, err := New(30*time.Second, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "pkg.zip")
	var called atomic.Int32
	res, err := client.Download(context.Background(), srv.URL+"/pkg.zip", dest, nil, func(done, total int64, _ float64, _ time.Duration) {
		called.Add(1)
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Size != int64(len(payload)) {
		t.Errorf("Size = %d，期望 %d", res.Size, len(payload))
	}
	if res.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("SHA256 = %s", res.SHA256)
	}
	if res.Skipped {
		t.Error("首次下载不应标记为 Skipped")
	}
	if called.Load() == 0 {
		t.Error("进度回调未被调用")
	}
	if _, err := os.Stat(dest + PartSuffix); !os.IsNotExist(err) {
		t.Error("临时 .part 文件应已被清理")
	}
	if err := VerifySHA256(dest, res.SHA256); err != nil {
		t.Errorf("VerifySHA256: %v", err)
	}
}

func TestDownloadMissingFileFailsFast(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := New(10*time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "missing.zip")
	if _, err := client.Download(context.Background(), srv.URL, dest, nil, nil); err == nil {
		t.Fatal("404 应返回错误")
	}
	if hits.Load() != 1 {
		t.Errorf("HTTP 404 不应重试，实际请求 %d 次", hits.Load())
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("失败时不应留下目标文件")
	}
}

func TestDownloadRetriesTransientFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过重试测试")
	}
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client, err := New(10*time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "retry.txt")
	res, err := client.Download(context.Background(), srv.URL, dest, nil, nil)
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if res.Size != 2 {
		t.Errorf("Size = %d，期望 2", res.Size)
	}
	if attempts.Load() != 2 {
		t.Errorf("请求次数 = %d，期望 2", attempts.Load())
	}
}

func TestDownloadCanceledContext(t *testing.T) {
	client, err := New(0, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Download(ctx, "http://127.0.0.1:1/x", filepath.Join(t.TempDir(), "x"), nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，得到 %v", err)
	}
}

func TestNewRejectsBadProxy(t *testing.T) {
	if _, err := New(0, "://bad"); err == nil {
		t.Error("非法代理应报错")
	}
}

func TestParseSHA256(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	cases := []struct {
		in   string
		want string
	}{
		{hash + "  pkg.zip\n", hash},
		{"sha256:" + hash, hash},
		{strings.ToUpper(hash), hash},
		{"not-a-hash", ""},
		{"", ""},
		{hash + "\n" + strings.Repeat("cd", 32) + "  other.zip", hash},
	}
	for _, c := range cases {
		if got := ParseSHA256(c.in); got != c.want {
			t.Errorf("ParseSHA256(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestVerifySHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("hello"))
	want := hex.EncodeToString(sum[:])

	if err := VerifySHA256(path, want); err != nil {
		t.Errorf("校验应通过: %v", err)
	}
	if err := VerifySHA256(path, strings.Repeat("00", 32)); err == nil {
		t.Error("摘要不一致应报错")
	}
	if err := VerifySHA256(path, ""); err != nil {
		t.Errorf("空摘要应跳过校验: %v", err)
	}
	if _, err := SHA256File(filepath.Join(filepath.Dir(path), "nope")); err == nil {
		t.Error("文件不存在应报错")
	}
}

func TestIsRetryable(t *testing.T) {
	if isRetryable(nil) {
		t.Error("nil 不应重试")
	}
	if isRetryable(context.Canceled) {
		t.Error("取消不应重试")
	}
	if isRetryable(errors.New("下载失败：HTTP 404")) {
		t.Error("4xx 不应重试")
	}
	if !isRetryable(errors.New("连接下载地址: EOF")) {
		t.Error("网络错误应重试")
	}
}
