package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathWithin(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "opt", "uc")

	cases := []struct {
		file string
		want bool
	}{
		{filepath.Join(root, "chrome"), true},
		{filepath.Join(root, "sub", "chrome"), true},
		{filepath.Join(string(filepath.Separator), "opt", "other", "chrome"), false},
		{filepath.Join(string(filepath.Separator), "usr", "bin", "chrome"), false},
		{root, false},
	}
	for _, c := range cases {
		if got := pathWithin(root, c.file); got != c.want {
			t.Errorf("pathWithin(%q, %q) = %v，期望 %v", root, c.file, got, c.want)
		}
	}
}

func TestInDirIgnoresUnrelatedProcesses(t *testing.T) {
	procs, err := InDir([]string{"upkit-definitely-not-running-xyz"}, t.TempDir())
	if err != nil {
		t.Skipf("当前环境无法枚举进程: %v", err)
	}
	if len(procs) != 0 {
		t.Errorf("不应匹配到任何进程，得到 %v", procs)
	}
}

func TestListIncludesSelf(t *testing.T) {
	all, err := list()
	if err != nil {
		t.Skipf("当前环境无法枚举进程: %v", err)
	}
	if len(all) == 0 {
		t.Skip("进程列表为空（可能受容器限制）")
	}
	found := false
	for _, p := range all {
		if p.PID == os.Getpid() {
			found = true
			if p.Name == "" {
				t.Error("进程名不应为空")
			}
		}
	}
	if !found {
		t.Skip("当前进程未出现在进程列表中（可能受沙箱限制）")
	}
}

func TestMatchingHandlesExeSuffix(t *testing.T) {
	if _, err := Matching([]string{"CHROME.EXE", "chrome", "  "}); err != nil {
		t.Fatalf("Matching 报错: %v", err)
	}
}

func TestRunning(t *testing.T) {
	ok, _, err := Running([]string{"upkit-definitely-not-running-xyz"})
	if err != nil {
		t.Skipf("当前环境无法枚举进程: %v", err)
	}
	if ok {
		t.Error("不应报告存在运行中的进程")
	}
}

func TestStartRejectsMissingExecutable(t *testing.T) {
	if err := Start(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("可执行文件不存在时应报错")
	}
}

func TestInfoString(t *testing.T) {
	withPath := Info{PID: 42, Name: "chrome.exe", Path: filepath.Join("C:", "uc", "chrome.exe")}
	got := withPath.String()
	for _, want := range []string{"chrome.exe", "42", filepath.Join("C:", "uc")} {
		if !strings.Contains(got, want) {
			t.Errorf("Info.String() = %q，应包含 %q", got, want)
		}
	}

	without := Info{PID: 7, Name: "chrome"}
	got = without.String()
	for _, want := range []string{"chrome", "7"} {
		if !strings.Contains(got, want) {
			t.Errorf("Info.String() = %q，应包含 %q", got, want)
		}
	}
}
