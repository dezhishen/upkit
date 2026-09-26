package registry

import (
	"errors"
	"strings"
	"testing"

	"github.com/dezhishen/upkit/internal/core"
)

// 用「工厂返回一个带标记的错误」来断言注册表究竟选中了哪个工厂，
// 这样不必构造真实的适配器实现。
func sourceMarker(tag string) SourceFactory {
	return func(core.AppRef, Deps) (core.SourceResolver, error) {
		return nil, errors.New(tag)
	}
}

func TestSourceExactBeatsPrefix(t *testing.T) {
	r := New()
	r.RegisterSource("plugin:corp", sourceMarker("exact"))
	r.RegisterSourcePrefix("plugin:", sourceMarker("prefix"))

	_, err := r.Source(core.AppRef{Source: "plugin:corp"}, Deps{})
	if err == nil || err.Error() != "exact" {
		t.Fatalf("精确注册应优先于前缀注册，实际 %v", err)
	}
}

func TestSourcePrefixLongestWins(t *testing.T) {
	r := New()
	r.RegisterSourcePrefix("plugin:", sourceMarker("short"))
	r.RegisterSourcePrefix("plugin:corp:", sourceMarker("long"))

	// 两个前缀都匹配 "plugin:corp:x"，必须选更长、更具体的那个。
	_, err := r.Source(core.AppRef{Source: "plugin:corp:x"}, Deps{})
	if err == nil || err.Error() != "long" {
		t.Fatalf("应选最长前缀，实际 %v", err)
	}

	// 只匹配短前缀时回退到它。
	_, err = r.Source(core.AppRef{Source: "plugin:other"}, Deps{})
	if err == nil || err.Error() != "short" {
		t.Fatalf("应回退到短前缀，实际 %v", err)
	}
}

func TestSourceUnknown(t *testing.T) {
	r := New()
	_, err := r.Source(core.AppRef{Source: "nope"}, Deps{})
	if err == nil {
		t.Fatal("未注册的来源应报错")
	}
	// 必须包住 ErrUnsupported：上层会用它区分「配置写错」与「运行期失败」。
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("错误应可被 errors.Is(ErrUnsupported) 命中，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "可选") {
		t.Fatalf("错误应列出可选类型，实际 %v", err)
	}
}

func TestSourceEmptyKind(t *testing.T) {
	r := New()
	// 空 kind 不能碰巧命中空前缀之外的任何注册项。
	r.RegisterSourcePrefix("plugin:", sourceMarker("prefix"))
	if _, err := r.Source(core.AppRef{Source: ""}, Deps{}); err == nil {
		t.Fatal("空来源类型应报错")
	}
}

func TestUnpackerAndMethodUnknown(t *testing.T) {
	r := New()

	if _, err := r.Unpacker(core.AppRef{Unpack: "zip"}, Deps{}); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("未知解包类型应报 ErrUnsupported，实际 %v", err)
	}
	if _, err := r.Method(core.AppRef{Method: "x"}, Deps{}); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("未知安装方式应报 ErrUnsupported，实际 %v", err)
	}

	r.RegisterUnpacker("zip", func(core.AppRef, Deps) (core.Unpacker, error) { return nil, nil })
	r.RegisterMethod("x", func(core.AppRef, Deps) (core.InstallMethod, error) { return nil, nil })
	if _, err := r.Unpacker(core.AppRef{Unpack: "zip"}, Deps{}); err != nil {
		t.Fatalf("已注册的解包类型不应报错: %v", err)
	}
	if _, err := r.Method(core.AppRef{Method: "x"}, Deps{}); err != nil {
		t.Fatalf("已注册的安装方式不应报错: %v", err)
	}
}

func TestDetectors(t *testing.T) {
	r := New()
	r.RegisterDetector("a", func(core.AppRef, Deps) (core.Detector, error) { return nil, nil })
	r.RegisterDetector("b", func(core.AppRef, Deps) (core.Detector, error) { return nil, nil })

	t.Run("空链报错", func(t *testing.T) {
		// 没有探测器就无法判断本机版本，必须显式失败而不是当成「永远最新」。
		if _, err := r.Detectors(core.AppRef{ID: "demo"}, Deps{}); err == nil {
			t.Fatal("未声明探测器时应报错")
		}
	})

	t.Run("保持声明顺序", func(t *testing.T) {
		ref := core.AppRef{ID: "demo", Detect: []string{"b", "a"}}
		got, err := r.Detectors(ref, Deps{})
		if err != nil {
			t.Fatalf("Detectors: %v", err)
		}
		// 顺序即优先级，注册表不得重排。
		if len(got) != 2 {
			t.Fatalf("期望 2 个探测器，得到 %d", len(got))
		}
	})

	t.Run("未知探测器报错", func(t *testing.T) {
		ref := core.AppRef{ID: "demo", Detect: []string{"a", "missing"}}
		if _, err := r.Detectors(ref, Deps{}); !errors.Is(err, core.ErrUnsupported) {
			t.Fatalf("应报 ErrUnsupported，实际 %v", err)
		}
	})

	t.Run("工厂报错向上传递", func(t *testing.T) {
		r2 := New()
		r2.RegisterDetector("bad", func(core.AppRef, Deps) (core.Detector, error) {
			return nil, errors.New("boom")
		})
		_, err := r2.Detectors(core.AppRef{ID: "demo", Detect: []string{"bad"}}, Deps{})
		if err == nil || err.Error() != "boom" {
			t.Fatalf("应原样返回工厂错误，实际 %v", err)
		}
	})
}

func TestKinds(t *testing.T) {
	r := New()
	r.RegisterSource("z", sourceMarker("z"))
	r.RegisterSource("a", sourceMarker("a"))
	r.RegisterSourcePrefix("plugin:", sourceMarker("p"))
	r.RegisterSourcePrefix("hub:", sourceMarker("h"))

	got := r.SourceKinds()
	// 前缀在列表里带 * 后缀，便于错误信息区分「精确」与「前缀」。
	want := []string{"a", "hub:*", "plugin:*", "z"}
	if len(got) != len(want) {
		t.Fatalf("SourceKinds() = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SourceKinds() = %v，期望 %v", got, want)
		}
	}

	if len(r.UnpackerKinds()) != 0 || len(r.MethodKinds()) != 0 || len(r.DetectorKinds()) != 0 {
		t.Fatal("未注册的轴应返回空列表")
	}
	r.RegisterUnpacker("zip", nil)
	r.RegisterMethod("inplace", nil)
	r.RegisterDetector("pe", nil)
	if k := r.UnpackerKinds(); len(k) != 1 || k[0] != "zip" {
		t.Fatalf("UnpackerKinds() = %v", k)
	}
	if k := r.MethodKinds(); len(k) != 1 || k[0] != "inplace" {
		t.Fatalf("MethodKinds() = %v", k)
	}
	if k := r.DetectorKinds(); len(k) != 1 || k[0] != "pe" {
		t.Fatalf("DetectorKinds() = %v", k)
	}
}

func TestRegisterOverwrites(t *testing.T) {
	r := New()
	r.RegisterSource("dup", sourceMarker("first"))
	r.RegisterSource("dup", sourceMarker("second"))

	// 重复注册以最后一次为准：方便测试里覆盖默认实现。
	_, err := r.Source(core.AppRef{Source: "dup"}, Deps{})
	if err == nil || err.Error() != "second" {
		t.Fatalf("重复注册应覆盖，实际 %v", err)
	}
}
