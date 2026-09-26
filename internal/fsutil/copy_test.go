package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkipMatcher(t *testing.T) {
	skip := SkipMatcher([]string{"User Data", "*.log", "Cache"})
	cases := []struct {
		rel  string
		want bool
	}{
		{"User Data", true},
		{"User Data/Default/Bookmarks", true},
		{"user data/default", true},
		{"chrome.exe", false},
		{"chrome_debug.log", true},
		{"logs/chrome_debug.log", true},
		{"Cache", true},
		{"Default/Cache/index", true},
		{"resources.pak", false},
	}
	for _, c := range cases {
		if got := skip(c.rel, false); got != c.want {
			t.Errorf("skip(%q) = %v，期望 %v", c.rel, got, c.want)
		}
	}

	empty := SkipMatcher(nil)
	if empty("anything", false) {
		t.Error("空规则不应跳过任何路径")
	}
}

func TestCopyDirSkipsProtectedPaths(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")

	mustWrite(t, filepath.Join(src, "chrome.exe"), "binary")
	mustWrite(t, filepath.Join(src, "locales", "zh-CN.pak"), "pak")
	mustWrite(t, filepath.Join(src, "User Data", "Default", "Bookmarks"), "bookmarks")

	if err := CopyDir(src, dst, SkipMatcher([]string{"User Data"})); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}

	if got, err := os.ReadFile(filepath.Join(dst, "chrome.exe")); err != nil || string(got) != "binary" {
		t.Fatalf("chrome.exe 未正确复制: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "locales", "zh-CN.pak")); err != nil {
		t.Fatalf("嵌套文件未复制: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "User Data")); !os.IsNotExist(err) {
		t.Fatal("User Data 不应被复制")
	}
}

func TestCopyDirRejectsMissingSource(t *testing.T) {
	if err := CopyDir(filepath.Join(t.TempDir(), "nope"), t.TempDir(), nil); err == nil {
		t.Fatal("源目录不存在时应报错")
	}
}

func TestRemoveContentsKeepsProtectedPaths(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "chrome.exe"), "old")
	mustWrite(t, filepath.Join(dir, "locales", "en-US.pak"), "old")
	mustWrite(t, filepath.Join(dir, "User Data", "Profile", "Preferences"), "keep-me")

	if err := RemoveContents(dir, []string{"User Data"}); err != nil {
		t.Fatalf("RemoveContents: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "User Data" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("清理后剩余条目 = %v，期望仅 User Data", names)
	}
	body, err := os.ReadFile(filepath.Join(dir, "User Data", "Profile", "Preferences"))
	if err != nil || string(body) != "keep-me" {
		t.Fatalf("受保护文件被破坏: %v", err)
	}
}

func TestCopyFileAndDirSize(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.bin")
	mustWrite(t, src, "0123456789")
	dst := filepath.Join(t.TempDir(), "sub", "b.bin")

	n, err := CopyFile(src, dst)
	if err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	if n != 10 {
		t.Errorf("复制字节数 = %d，期望 10", n)
	}

	size, err := DirSize(filepath.Dir(dst))
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if size != 10 {
		t.Errorf("DirSize = %d，期望 10", size)
	}

	if _, err := CopyFile(t.TempDir(), dst); err == nil {
		t.Error("源为目录时应报错")
	}
}

func TestMoveAndRemoveAll(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustWrite(t, filepath.Join(src, "file.txt"), "x")
	dst := filepath.Join(root, "dst", "moved")

	if err := Move(src, dst); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "file.txt")); err != nil {
		t.Fatalf("移动后文件不存在: %v", err)
	}
	if err := RemoveAll(dst); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("目录应已被删除")
	}
}

func TestIsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	empty, err := IsEmptyDir(dir)
	if err != nil || !empty {
		t.Fatalf("IsEmptyDir = %v, %v", empty, err)
	}
	mustWrite(t, filepath.Join(dir, "x"), "1")
	empty, err = IsEmptyDir(dir)
	if err != nil || empty {
		t.Fatalf("IsEmptyDir = %v, %v", empty, err)
	}
	missing, err := IsEmptyDir(filepath.Join(dir, "nope"))
	if err != nil || !missing {
		t.Fatalf("不存在的目录应视为空: %v, %v", missing, err)
	}
}

func TestDiskFree(t *testing.T) {
	free, err := DiskFree(t.TempDir())
	if err != nil {
		t.Skipf("当前平台不支持查询磁盘空间: %v", err)
	}
	if free == 0 {
		t.Skip("磁盘可用空间返回 0，跳过断言")
	}
}
