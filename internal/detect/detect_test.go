package detect

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/version"
)

// 四个探测器都必须满足同一条契约：没装时返回 Installed=false 且 err=nil。
//
// 探测链是「按顺序试，第一个说装了就停」，把「没装」写成错误会让链条在第二环就断掉。
func TestDetectorsReturnNotInstalledWithoutError(t *testing.T) {
	ctx := context.Background()
	missing := core.AppRef{ID: "demo", InstallPath: filepath.Join(t.TempDir(), "nope")}

	cases := []struct {
		name string
		new  func(core.AppRef, registry.Deps) (core.Detector, error)
	}{
		{KindStateFile, NewStateFile},
		{KindPEResource, NewPEResource},
		{KindDirName, NewDirName},
		{KindCLIVersion, NewCLIVersion},
	}
	for _, c := range cases {
		d, err := c.new(missing, registry.Deps{})
		if err != nil {
			t.Fatalf("%s: New: %v", c.name, err)
		}
		if d.Name() != c.name {
			t.Fatalf("Name() 应为 %q，实际 %q", c.name, d.Name())
		}
		st, err := d.Detect(ctx, missing)
		if err != nil {
			t.Fatalf("%s: 没装时不该报错: %v", c.name, err)
		}
		if st.Installed {
			t.Fatalf("%s: 目录不存在却报告已安装: %+v", c.name, st)
		}
	}
}

// 状态文件探测器：读的是 upkit 自己写的记录，版本与安装时间都要透出来。
func TestStateFileDetector(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.exe"), []byte("bin"), 0o755); err != nil {
		t.Fatalf("写入: %v", err)
	}
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := version.WriteRecord(dir, &version.Record{Version: "1.2.3", InstalledAt: at}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	d, _ := NewStateFile(core.AppRef{}, registry.Deps{})
	st, err := d.Detect(ctx, core.AppRef{ID: "demo", InstallPath: dir})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !st.Installed || st.Version != "1.2.3" || st.Source != KindStateFile {
		t.Fatalf("状态不对: %+v", st)
	}
	if st.Size <= 0 {
		t.Fatalf("应统计出安装体积，实际 %d", st.Size)
	}

	// 路径为空、或记录里没版本，都算没装（记录文件可能是别的工具写的）。
	if st, _ := d.Detect(ctx, core.AppRef{ID: "demo"}); st.Installed {
		t.Fatalf("安装路径为空时不该报告已安装")
	}
	empty := t.TempDir()
	if err := version.WriteRecord(empty, &version.Record{Version: ""}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	if st, _ := d.Detect(ctx, core.AppRef{ID: "demo", InstallPath: empty}); st.Installed {
		t.Fatalf("记录里没有版本时不该报告已安装")
	}
}

// 目录名探测器：目录名本身就是版本号（便携软件常见做法）。
func TestDirNameDetector(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	d, _ := NewDirName(core.AppRef{}, registry.Deps{})

	withVersion := filepath.Join(root, "1.2.3")
	if err := os.MkdirAll(withVersion, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	st, err := d.Detect(ctx, core.AppRef{ID: "demo", InstallPath: withVersion})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !st.Installed || st.Version != "1.2.3" || st.Source != KindDirName {
		t.Fatalf("应从目录名读出 %q，实际 %+v", "1.2.3", st)
	}

	plain := filepath.Join(root, "demo")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if st, _ := d.Detect(ctx, core.AppRef{ID: "demo", InstallPath: plain}); st.Installed {
		t.Fatalf("目录名不是版本号时不该报告已安装: %+v", st)
	}
}

// CLI 探测器：跑一次 `--version` 之类，从输出里正则取版本。
func TestCLIVersionDetector(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("脚本形式的假 CLI 只在类 Unix 上可执行")
	}
	ctx := context.Background()
	dir := t.TempDir()
	writeScript(t, filepath.Join(dir, "demo.sh"), "echo 'demo 2.5.1'")

	d, _ := NewCLIVersion(core.AppRef{}, registry.Deps{})
	ref := core.AppRef{
		ID: "demo", InstallPath: dir,
		MethodOpts: map[string]string{"version_cmd": "demo.sh"},
	}
	st, err := d.Detect(ctx, ref)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !st.Installed || st.Version != "2.5.1" || st.Source != KindCLIVersion {
		t.Fatalf("应从输出里取到 2.5.1，实际 %+v", st)
	}

	// 自定义正则：取的是整段匹配。
	writeScript(t, filepath.Join(dir, "build.sh"), "echo 'build 20240927'")
	ref.MethodOpts = map[string]string{"version_cmd": "build.sh", "version_regex": `\d{8}`}
	if st, err := d.Detect(ctx, ref); err != nil || st.Version != "20240927" {
		t.Fatalf("自定义正则没生效: %+v err=%v", st, err)
	}

	// 非法正则要说清楚是哪一项配错了。
	ref.MethodOpts = map[string]string{"version_cmd": "build.sh", "version_regex": `(`}
	if _, err := d.Detect(ctx, ref); err == nil {
		t.Fatalf("非法正则应报错")
	}

	// 输出里没有版本号、命令没配、可执行文件不存在：都算没装。
	writeScript(t, filepath.Join(dir, "noversion.sh"), "echo 'no numbers here'")
	ref.MethodOpts = map[string]string{"version_cmd": "noversion.sh"}
	if st, _ := d.Detect(ctx, ref); st.Installed {
		t.Fatalf("取不到版本号时不该报告已安装: %+v", st)
	}
	ref.MethodOpts = nil
	if st, _ := d.Detect(ctx, ref); st.Installed {
		t.Fatalf("没配 version_cmd 时不该报告已安装")
	}
	ref.MethodOpts = map[string]string{"version_cmd": "missing.sh"}
	if st, _ := d.Detect(ctx, ref); st.Installed {
		t.Fatalf("命令不存在时不该报告已安装")
	}
}

// version_cmd 也可以写在来源选项里（历史写法），两处都认。
func TestCLIVersionReadsSourceOpts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("脚本形式的假 CLI 只在类 Unix 上可执行")
	}
	dir := t.TempDir()
	writeScript(t, filepath.Join(dir, "demo.sh"), "echo '3.1.4'")

	d, _ := NewCLIVersion(core.AppRef{}, registry.Deps{})
	ref := core.AppRef{
		ID: "demo", InstallPath: dir,
		SourceOpts: map[string]string{"version_cmd": "demo.sh"},
	}
	st, err := d.Detect(context.Background(), ref)
	if err != nil || !st.Installed || st.Version != "3.1.4" {
		t.Fatalf("来源选项里的 version_cmd 没生效: %+v err=%v", st, err)
	}
}

// PE 资源探测器在没有 PE 文件（或文件里没有版本资源）时也算没装。
//
// 这里不构造真的 PE：Linux 开发机上没有，而 version.ProductVersion 对非 PE 文件
// 返回错误，正是「继续试下一个入口文件」的那条分支。
func TestPEResourceDetectorSkipsNonPE(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.exe"), []byte("not a PE"), 0o755); err != nil {
		t.Fatalf("写入: %v", err)
	}

	d, _ := NewPEResource(core.AppRef{}, registry.Deps{})
	st, err := d.Detect(context.Background(), core.AppRef{ID: "demo", InstallPath: dir, Entrypoints: []string{"demo.exe"}})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if st.Installed {
		t.Fatalf("非 PE 文件不该报告已安装: %+v", st)
	}

	// 没声明入口文件时会去试常见候选（chrome.exe / chrome），同样不该报错。
	if st, err := d.Detect(context.Background(), core.AppRef{ID: "demo", InstallPath: dir}); err != nil || st.Installed {
		t.Fatalf("候选入口不存在时应算没装: %+v err=%v", st, err)
	}
}

// writeScript 写一个可执行的 shell 脚本，当作假的 CLI。
func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("写入脚本: %v", err)
	}
}
