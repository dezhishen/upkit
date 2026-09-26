package archive

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeZip 在临时目录中生成一个 zip 文件。
func writeZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pkg.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractZip(t *testing.T) {
	zipPath := writeZip(t, map[string]string{
		"chrome.exe":                     "binary",
		"resources.pak":                  "pak",
		"locales/zh-CN.pak":              "locale",
		"ungoogled-chromium/README.txt":  "readme",
		"ungoogled-chromium/chrome.exe2": "not-the-binary",
	})
	dest := t.TempDir()

	var lastDone, lastTotal int
	stats, err := ExtractZip(context.Background(), zipPath, dest, func(done, total int, _ string) {
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatalf("ExtractZip: %v", err)
	}
	if stats.Files != 5 {
		t.Errorf("Files = %d，期望 5", stats.Files)
	}
	if lastDone != lastTotal || lastTotal != 5 {
		t.Errorf("进度回调结束值 = %d/%d，期望 5/5", lastDone, lastTotal)
	}

	data, err := os.ReadFile(filepath.Join(dest, "locales", "zh-CN.pak"))
	if err != nil || string(data) != "locale" {
		t.Fatalf("嵌套文件解压失败: %v (%q)", err, data)
	}
}

func TestExtractZipRejectsPathTraversal(t *testing.T) {
	cases := []string{"../evil.txt", "a/../../evil.txt"}
	if runtime.GOOS != "windows" {
		// Windows 下 "/absolute.txt" 会退化为相对路径，不构成逃逸。
		cases = append(cases, "/absolute.txt")
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			zipPath := writeZip(t, map[string]string{name: "evil"})
			dest := t.TempDir()
			if _, err := ExtractZip(context.Background(), zipPath, dest, nil); err == nil {
				t.Fatalf("条目 %q 应被拒绝", name)
			}
		})
	}
}

func TestExtractZipHonorsContextCancel(t *testing.T) {
	zipPath := writeZip(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ExtractZip(ctx, zipPath, t.TempDir(), nil); err == nil {
		t.Fatal("已取消的 context 应返回错误")
	}
}

func TestFindAppRoot(t *testing.T) {
	t.Run("直接位于根目录", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "chrome.exe"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := FindAppRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != dir {
			t.Errorf("FindAppRoot = %q，期望 %q", got, dir)
		}
	})

	t.Run("多包一层目录", func(t *testing.T) {
		dir := t.TempDir()
		nested := filepath.Join(dir, "ungoogled-chromium_131.0.6778.86-1.1_windows_x64")
		if err := os.MkdirAll(filepath.Join(nested, "locales"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nested, "chrome.exe"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nested, "locales", "zh-CN.pak"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := FindAppRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != nested {
			t.Errorf("FindAppRoot = %q，期望 %q", got, nested)
		}
	})

	t.Run("找不到 chrome.exe", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := FindAppRoot(dir)
		if err == nil {
			t.Fatal("应返回错误")
		}
		if !strings.Contains(err.Error(), "chrome.exe") {
			t.Errorf("错误信息应提到 chrome.exe，得到 %v", err)
		}
	})
}

func TestSecureJoin(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "root")
	ok, err := secureJoin(root, "a/b.txt")
	if err != nil {
		t.Fatalf("secureJoin 合法路径报错: %v", err)
	}
	if ok != filepath.Join(root, "a", "b.txt") {
		t.Errorf("secureJoin = %q", ok)
	}
	if _, err := secureJoin(root, "../x"); err == nil {
		t.Error("越界路径应报错")
	}
}
