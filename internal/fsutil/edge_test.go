package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// WriteFileAtomic 是设置与清单落盘的唯一通道：先写 .tmp 再改名。
// 这里钉住三件事：内容与权限对、不留 .tmp 残留、目标目录能自动建出来。
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "settings.yaml")

	if err := WriteFileAtomic(path, []byte("version: 2\n"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if string(data) != "version: 2\n" {
		t.Fatalf("内容不对: %q", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("权限应为 0600，实际 %o", perm)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("临时文件应被改名掉: %v", err)
	}

	// 覆盖已有文件：不能留下旧内容，也不能因为 .tmp 已存在而失败。
	if err := WriteFileAtomic(path, []byte("version: 3\n"), 0o644); err != nil {
		t.Fatalf("覆盖写入: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "version: 3\n" {
		t.Fatalf("覆盖后内容不对: %q", data)
	}
}

// 目标目录建不出来时要报错，不能假装写成功。
func TestWriteFileAtomicError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("写占位文件: %v", err)
	}
	if err := WriteFileAtomic(filepath.Join(blocker, "a.yaml"), []byte("x"), 0o644); err == nil {
		t.Fatal("父路径是文件时应报错")
	}
	// 目标路径是个目录：改名会失败，报错里要带上路径，并且清掉临时文件。
	if err := WriteFileAtomic(t.TempDir(), []byte("x"), 0o644); err == nil {
		t.Fatal("目标为目录时应报错")
	}
}

// CopyFile 的失败路径：源不存在、源是目录、目标目录建不出来。
// 这些在备份/替换流程里都意味着「必须中断」，不能退化成静默跳过。
func TestCopyFileErrors(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "srcdir")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if _, err := CopyFile(filepath.Join(dir, "missing.txt"), filepath.Join(dir, "dst.txt")); err == nil {
		t.Fatal("源不存在时应报错")
	}
	if _, err := CopyFile(srcDir, filepath.Join(dir, "dst.txt")); err == nil ||
		!strings.Contains(err.Error(), "是目录") {
		t.Fatalf("源是目录时应报错，实际 %v", err)
	}

	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("写占位文件: %v", err)
	}
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("写源文件: %v", err)
	}
	if _, err := CopyFile(src, filepath.Join(blocker, "sub", "b.txt")); err == nil {
		t.Fatal("目标目录建不出来时应报错")
	}
	// 目标父目录是文件时，打开目标也会失败。
	if _, err := CopyFile(src, filepath.Join(blocker, "b.txt")); err == nil {
		t.Fatal("目标不可写时应报错")
	}
}

// CopyDir 遇到符号链接时既不能整次失败，也不能漏掉真实目录。
//
// 更新时把「新版覆盖旧版」做成一次目录复制，链接处理错了就是一次失败的回滚。
func TestCopyDirSymlinksAndSkipFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("在 Windows 上创建符号链接需要额外权限")
	}
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "real.txt"), "real")
	mustWrite(t, filepath.Join(src, "skip.log"), "log")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	mustWrite(t, filepath.Join(src, "sub", "inner.txt"), "inner")

	// 指向真实目录的链接：应当被当成目录建出来（内容不重要，关键是不报错）。
	if err := os.Symlink(filepath.Join(src, "sub"), filepath.Join(src, "linkdir")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	// 悬空链接：跳过而不是让整次备份失败。
	if err := os.Symlink(filepath.Join(src, "nowhere"), filepath.Join(src, "dangling")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out")
	// 跳过规则同时命中文件（*.log）与目录（sub）。
	if err := CopyDir(src, dst, SkipMatcher([]string{"*.log", "sub"})); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "real.txt")); err != nil {
		t.Fatalf("普通文件应被复制: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "skip.log")); !os.IsNotExist(err) {
		t.Fatalf("被跳过规则命中的文件不该出现: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sub")); !os.IsNotExist(err) {
		t.Fatalf("被跳过的目录不该出现: %v", err)
	}
	if info, err := os.Stat(filepath.Join(dst, "linkdir")); err != nil || !info.IsDir() {
		t.Fatalf("指向目录的链接应被当成目录建出来: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "dangling")); !os.IsNotExist(err) {
		t.Fatalf("悬空链接应被跳过: %v", err)
	}

	// skip 为 nil 时全量复制；目标不存在时要自己建出来。
	dst2 := filepath.Join(t.TempDir(), "nested", "out")
	if err := CopyDir(src, dst2, nil); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst2, "sub", "inner.txt")); err != nil {
		t.Fatalf("子目录内容应被复制: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst2, "skip.log")); err != nil {
		t.Fatalf("无跳过规则时日志文件也该复制: %v", err)
	}
}

// DirSize 统计的是所有普通文件的字节数之和；空目录为 0，路径不存在时报错。
func TestDirSizeEdge(t *testing.T) {
	dir := t.TempDir()
	if n, err := DirSize(dir); err != nil || n != 0 {
		t.Fatalf("空目录应为 0: %d %v", n, err)
	}
	mustWrite(t, filepath.Join(dir, "a.txt"), "12345")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	mustWrite(t, filepath.Join(dir, "sub", "b.txt"), "123")
	n, err := DirSize(dir)
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if n != 8 {
		t.Fatalf("目录大小应为 8 字节，实际 %d", n)
	}
	if _, err := DirSize(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("路径不存在时应报错")
	}
}

// IsEmptyDir：不存在视为空（可安全当目标用），不可读时报错。
func TestIsEmptyDirEdge(t *testing.T) {
	dir := t.TempDir()
	if ok, err := IsEmptyDir(filepath.Join(dir, "missing")); err != nil || !ok {
		t.Fatalf("不存在的目录应视为空: %v %v", ok, err)
	}
	if ok, err := IsEmptyDir(dir); err != nil || !ok {
		t.Fatalf("空目录应为空: %v %v", ok, err)
	}
	mustWrite(t, filepath.Join(dir, "f.txt"), "x")
	if ok, err := IsEmptyDir(dir); err != nil || ok {
		t.Fatalf("有内容时不该为空: %v %v", ok, err)
	}
	// 路径是文件：不是「空目录」，要报错让调用方看清楚。
	if ok, err := IsEmptyDir(filepath.Join(dir, "f.txt")); err == nil || ok {
		t.Fatalf("路径是文件时应报错: %v %v", ok, err)
	}
}

// RemoveContents：目录不存在视为成功（幂等），路径是文件时报错，
// 保留列表里的目录连内容一起留下。
func TestRemoveContentsEdge(t *testing.T) {
	if err := RemoveContents(filepath.Join(t.TempDir(), "missing"), nil); err != nil {
		t.Fatalf("目录不存在时应返回 nil: %v", err)
	}

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "keep.txt"), "x")
	if err := RemoveContents(dir, []string{"keep.txt"}); err != nil {
		t.Fatalf("RemoveContents: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatalf("保留项不该被删: %v", err)
	}

	file := filepath.Join(dir, "not-a-dir")
	mustWrite(t, file, "x")
	if err := RemoveContents(file, nil); err == nil {
		t.Fatal("路径是文件时应报错")
	}
}

// RemoveAll：空路径与不存在都视为成功，这是「清理一次可能已经清过的东西」的前提。
func TestRemoveAllEdge(t *testing.T) {
	if err := RemoveAll(""); err != nil {
		t.Fatalf("空路径应返回 nil: %v", err)
	}
	if err := RemoveAll(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("路径不存在应返回 nil: %v", err)
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	mustWrite(t, file, "x")
	if err := RemoveAll(file); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("文件应被删除: %v", err)
	}
}

// 删除失败要退避重试并最终报错，而不是静默成功 —— 备份流程靠这个信号决定是否中断。
//
// 用一个没有写权限的目录制造失败：里面的文件删不掉。非 root 才构造得出来。
func TestRemoveAllRetriesThenFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("目录权限语义不同，构造不出「删不掉」的目录")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	mustWrite(t, filepath.Join(dir, "f.txt"), "x")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("置只读: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o755)
		_ = os.RemoveAll(dir)
	})

	err := RemoveAll(dir)
	if err == nil {
		t.Fatal("删不掉时应报错（静默成功会让备份流程以为已经清空）")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("错误信息应带上路径: %v", err)
	}
}

// Move：同卷走 rename，路径不存在时报错；跨卷时回退成「复制 + 删除」。
func TestMoveEdge(t *testing.T) {
	dir := t.TempDir()
	if err := Move(filepath.Join(dir, "missing"), filepath.Join(dir, "dst")); err == nil {
		t.Fatal("源不存在时应报错")
	}

	src := filepath.Join(dir, "src.txt")
	mustWrite(t, src, "payload")
	dst := filepath.Join(dir, "deep", "dst.txt")
	if err := Move(src, dst); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "payload" {
		t.Fatalf("目标内容不对: %q %v", data, err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("源应被移走: %v", err)
	}

	// 目录移动。
	srcDir := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	mustWrite(t, filepath.Join(srcDir, "sub", "a.txt"), "a")
	dstDir := filepath.Join(dir, "moved-tree")
	if err := Move(srcDir, dstDir); err != nil {
		t.Fatalf("Move 目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "sub", "a.txt")); err != nil {
		t.Fatalf("目录内容应完整移动: %v", err)
	}
}

// 跨卷移动：rename 会失败，必须回退成「复制 + 删除」而不是丢掉文件。
//
// 用 /dev/shm（tmpfs）当另一块卷；拿不到就跳过，别把环境差异当成功能问题。
func TestMoveAcrossVolumes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("只在 Linux 上用 /dev/shm 构造跨卷场景")
	}
	other := "/dev/shm"
	info, err := os.Stat(other)
	if err != nil || !info.IsDir() {
		t.Skipf("%s 不可用: %v", other, err)
	}
	probeSrc := filepath.Join(other, "upkit-crossvol-probe")
	probe := filepath.Join(t.TempDir(), "probe")
	mustWrite(t, probeSrc, "x")
	renameErr := os.Rename(probeSrc, probe)
	_ = os.Remove(probeSrc)
	if renameErr == nil {
		t.Skip("rename 成功了，说明这里不是跨卷场景")
	}

	dir, err := os.MkdirTemp(other, "upkit-crossvol-")
	if err != nil {
		t.Skipf("无法在 %s 建临时目录: %v", other, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// 文件跨卷。
	srcFile := filepath.Join(dir, "f.txt")
	mustWrite(t, srcFile, "payload")
	dstFile := filepath.Join(t.TempDir(), "out", "f.txt")
	if err := Move(srcFile, dstFile); err != nil {
		t.Fatalf("Move 跨卷文件: %v", err)
	}
	if data, err := os.ReadFile(dstFile); err != nil || string(data) != "payload" {
		t.Fatalf("跨卷复制内容不对: %q %v", data, err)
	}
	if _, err := os.Lstat(srcFile); !os.IsNotExist(err) {
		t.Fatalf("跨卷移动后源应被删除: %v", err)
	}

	// 目录跨卷。
	srcDir := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	mustWrite(t, filepath.Join(srcDir, "sub", "a.txt"), "a")
	dstDir := filepath.Join(t.TempDir(), "tree")
	if err := Move(srcDir, dstDir); err != nil {
		t.Fatalf("Move 跨卷目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "sub", "a.txt")); err != nil {
		t.Fatalf("跨卷移动的目录应完整: %v", err)
	}
	if _, err := os.Lstat(srcDir); !os.IsNotExist(err) {
		t.Fatalf("跨卷移动后源目录应被删除: %v", err)
	}
}

// DiskFree：拿不到卷信息时要报错，而不是返回 0 让「空间检查」误判为磁盘满。
func TestDiskFreeEdge(t *testing.T) {
	n, err := DiskFree(t.TempDir())
	if err != nil {
		t.Fatalf("DiskFree: %v", err)
	}
	if n == 0 {
		t.Fatal("临时目录所在卷的可用空间不该是 0")
	}
	if _, err := DiskFree(filepath.Join(t.TempDir(), "missing", "deeper")); err == nil {
		t.Fatal("路径不存在时应报错")
	}
}
