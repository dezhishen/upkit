package unpacking

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
)

func zipWith(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 zip: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("写入条目: %v", err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("写入内容: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip: %v", err)
	}
}

func tarGzWith(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 tar.gz: %v", err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatalf("写 tar 头: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("写 tar 内容: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("关闭 gzip: %v", err)
	}
}

// find_root 应把根定位到包含标记文件的那一层。
func TestZipFindRoot(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "demo.zip")
	zipWith(t, archive, map[string]string{
		"chrome-wrapper/chrome.exe":      "bin",
		"chrome-wrapper/resources/x.txt": "data",
	})

	app := core.AppRef{ID: "demo", Unpack: KindZip}
	u, err := NewZip(app, registryDeps())
	if err != nil {
		t.Fatalf("NewZip: %v", err)
	}
	res, err := u.Unpack(context.Background(), core.UnpackRequest{
		ArchivePath: archive,
		DestDir:     filepath.Join(dir, "out"),
		Opts:        map[string]string{"find_root": "chrome.exe"},
	}, core.NopSink{})
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if filepath.Base(res.Root) != "chrome-wrapper" {
		t.Fatalf("根目录应为 chrome-wrapper，得到 %s", res.Root)
	}
	if res.Files != 2 {
		t.Fatalf("期望 2 个文件，得到 %d", res.Files)
	}
}

// strip=1 应去掉一层包裹目录。
func TestZipStrip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "demo.zip")
	zipWith(t, archive, map[string]string{"pkg/app.exe": "bin", "pkg/readme.md": "doc"})

	u, err := NewZip(core.AppRef{ID: "demo"}, registryDeps())
	if err != nil {
		t.Fatalf("NewZip: %v", err)
	}
	dest := filepath.Join(dir, "out")
	res, err := u.Unpack(context.Background(), core.UnpackRequest{
		ArchivePath: archive,
		DestDir:     dest,
		Opts:        map[string]string{"strip": "1"},
	}, core.NopSink{})
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if res.Root != filepath.Join(dest, "pkg") {
		t.Fatalf("strip=1 后根目录应为 %s，得到 %s", filepath.Join(dest, "pkg"), res.Root)
	}
	if _, err := os.Stat(filepath.Join(res.Root, "app.exe")); err != nil {
		t.Fatalf("strip 后文件位置不对: %v", err)
	}
}

// zip-slip：含 .. 的条目必须被拒绝。
func TestZipRejectsSlip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.zip")
	zipWith(t, archive, map[string]string{"../evil.txt": "boom"})

	u, err := NewZip(core.AppRef{ID: "demo"}, registryDeps())
	if err != nil {
		t.Fatalf("NewZip: %v", err)
	}
	if _, err := u.Unpack(context.Background(), core.UnpackRequest{
		ArchivePath: archive,
		DestDir:     filepath.Join(dir, "out"),
	}, core.NopSink{}); err == nil {
		t.Fatalf("期望拒绝含 .. 的压缩包")
	}
}

// tar.gz 应能解压并通过 find_root 定位根目录。
func TestTarGzFindRoot(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "demo.tar.gz")
	tarGzWith(t, archive, map[string]string{"bundle/tool": "bin", "bundle/LICENSE": "text"})

	u, err := NewTarGz(core.AppRef{ID: "demo"}, registryDeps())
	if err != nil {
		t.Fatalf("NewTarGz: %v", err)
	}
	res, err := u.Unpack(context.Background(), core.UnpackRequest{
		ArchivePath: archive,
		DestDir:     filepath.Join(dir, "out"),
		Opts:        map[string]string{"find_root": "tool"},
	}, core.NopSink{})
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if filepath.Base(res.Root) != "bundle" {
		t.Fatalf("根目录应为 bundle，得到 %s", res.Root)
	}
}

// raw 解包器把产物文件本身当作安装源。
func TestRawRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "setup.exe")
	if err := os.WriteFile(file, []byte("bin"), 0o644); err != nil {
		t.Fatalf("写入: %v", err)
	}
	u, err := NewRaw(core.AppRef{ID: "demo"}, registryDeps())
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}
	res, err := u.Unpack(context.Background(), core.UnpackRequest{ArchivePath: file}, core.NopSink{})
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if res.Root != file {
		t.Fatalf("raw 根应为产物本身，得到 %s", res.Root)
	}
}

// registryDeps 返回解包器需要的最小依赖（解包不需要网络）。
func registryDeps() registry.Deps {
	return registry.Deps{Clock: time.Now}
}
