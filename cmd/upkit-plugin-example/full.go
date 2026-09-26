package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dezhishen/upkit/pkg/plugin"
)

// 本文件演示 full 模式：插件自己接管状态探测、计划与安装。
//
// 与 catalog 模式的区别只在于「谁动手」：这里的取包、落地、回滚全在本进程内完成
// （示例里简化成写两个文件），宿主不再下载、不解包，只提供安装路径、工作目录与
// 事件通道。想走这条路，把软件构造器返回的实例实现 plugin.Method 即可。

// full 模式示例的配置键与固定值。
const (
	cfgStubVersion = "version"
	cfgStubFail    = "fail"
	cfgStubDelayMS = "delay_ms"

	stubDefaultVersion = "1.0.0"
	stubStateFile      = ".upkit-stub-version"
	stubPayloadFile    = "stub-payload.txt"

	// phaseStubInstall 是上报事件时使用的阶段名。
	phaseStubInstall = "安装"
)

// stubConfig 是 full 模式示例软件的配置。
type stubConfig struct {
	// Version 是「上游」提供的版本；改它就能演示更新。
	Version string `json:"version"`
	// Fail 为真时 Apply 会失败，用来演示错误如何跨进程传回宿主。
	Fail bool `json:"fail"`
	// DelayMS 让 Apply 慢一点，用来演示进度事件。
	DelayMS int `json:"delay_ms"`
}

// newLocalStub 是「本地存根」的构造器。
func newLocalStub(cfg plugin.AppConfig) (plugin.App, error) {
	var c stubConfig
	if err := plugin.DecodeConfig(cfg.Config, &c); err != nil {
		return nil, err
	}
	if c.Version == "" {
		c.Version = stubDefaultVersion
	}
	cfg.Log.Info("构造本地存根", "version", c.Version, "data", cfg.DataDir)
	return &localStub{cfg: c, dataDir: cfg.DataDir, log: cfg.Log}, nil
}

// localStub 是 full 模式的示例实现。
type localStub struct {
	cfg     stubConfig
	dataDir string
	log     plugin.Logger
}

// 编译期确认它真的实现了 full 模式所需的方法集。
var _ plugin.Method = (*localStub)(nil)

// installPath 优先用宿主给的清单路径；没配就落在自己的私有目录里。
func (s *localStub) installPath(fromHost string) string {
	if p := strings.TrimSpace(fromHost); p != "" {
		return p
	}
	return filepath.Join(s.dataDir, "install")
}

// payload 是「下载物」的内容：示例不联网，直接生成。
func (s *localStub) payload() []byte {
	return []byte("upkit local stub " + s.cfg.Version + "\n")
}

// Versions 报告「上游」版本。
//
// full 模式下宿主不会去下载 Artifacts 里的地址，它只用于界面展示。
func (s *localStub) Versions(_ context.Context, _ plugin.VersionsRequest) ([]plugin.Release, error) {
	return []plugin.Release{{
		Version:     s.cfg.Version,
		Tag:         "v" + s.cfg.Version,
		Channel:     channelStable,
		PublishedAt: time.Now(),
		Notes:       "本地存根 " + s.cfg.Version,
		Artifacts: []plugin.Artifact{{
			Name: stubPayloadFile,
			URL:  "stub://" + stubPayloadFile,
			Size: int64(len(s.payload())),
		}},
	}}, nil
}

// Status 由插件自己判断本机装了什么：版本记录文件在，就算装上了。
//
// 必须能做到「没装时 Installed=false 且 err=nil」——这是探测链的约定。
func (s *localStub) Status(_ context.Context, req plugin.StatusRequest) (plugin.Status, error) {
	path := s.installPath(req.InstallPath)
	b, err := os.ReadFile(filepath.Join(path, stubStateFile))
	if err != nil {
		return plugin.Status{}, nil
	}
	return plugin.Status{Installed: true, Version: strings.TrimSpace(string(b)), Path: path}, nil
}

// Plan 给出可展示的步骤；宿主会把它渲染成计划清单。
func (s *localStub) Plan(_ context.Context, req plugin.PlanRequest) (plugin.PlanResult, error) {
	path := s.installPath(req.InstallPath)
	return plugin.PlanResult{
		Action: plugin.ActionInstall,
		From:   req.From,
		To:     req.To,
		Steps: []plugin.Step{
			{Kind: plugin.StepDownload, Desc: "生成载荷（本地，不联网）"},
			{Kind: plugin.StepCopy, Desc: "写入 " + path},
			{Kind: plugin.StepVerify, Desc: "回读版本记录"},
		},
		Note: "由插件自行安装",
	}, nil
}

// Apply 真正动手。
//
// send 可能为 nil（宿主不需要进度时），plugin.EventSender 的 Send 已经处理了这点。
func (s *localStub) Apply(ctx context.Context, req plugin.PlanRequest, send plugin.EventSender) (plugin.Result, error) {
	start := time.Now()
	path := s.installPath(req.InstallPath)

	send.Send(plugin.Event{Kind: plugin.EventStarted, Phase: phaseStubInstall, Msg: "开始安装 " + s.cfg.Version})

	if s.cfg.DelayMS > 0 {
		send.Send(plugin.Event{Kind: plugin.EventProgress, Phase: phaseStubInstall, Done: 1, Total: 2,
			Msg: "准备中"})
		select {
		case <-time.After(time.Duration(s.cfg.DelayMS) * time.Millisecond):
		case <-ctx.Done():
			return plugin.Result{}, ctx.Err()
		}
	}

	if err := os.MkdirAll(path, 0o755); err != nil {
		return plugin.Result{}, fmt.Errorf("创建安装目录 %s: %w", path, err)
	}
	if s.cfg.Fail {
		return plugin.Result{}, fmt.Errorf("%w: 配置要求本次安装失败", plugin.ErrBadConfig)
	}
	if err := os.WriteFile(filepath.Join(path, stubPayloadFile), s.payload(), 0o644); err != nil {
		return plugin.Result{}, err
	}
	if err := os.WriteFile(filepath.Join(path, stubStateFile), []byte(s.cfg.Version+"\n"), 0o644); err != nil {
		return plugin.Result{}, err
	}

	send.Send(plugin.Event{Kind: plugin.EventProgress, Phase: phaseStubInstall, Done: 2, Total: 2})
	send.Send(plugin.Event{Kind: plugin.EventLog, Level: plugin.LogLevelInfo,
		Msg: "已写入 " + filepath.Join(path, stubPayloadFile)})
	s.log.Info("安装完成", "version", s.cfg.Version, "path", path)

	return plugin.Result{
		Action:      plugin.ActionInstall,
		From:        req.From,
		To:          s.cfg.Version,
		InstallPath: path,
		ElapsedMS:   time.Since(start).Milliseconds(),
	}, nil
}

// Rollback 把备份目录搬回去。
func (s *localStub) Rollback(_ context.Context, req plugin.RollbackRequest) error {
	if strings.TrimSpace(req.BackupPath) == "" {
		return fmt.Errorf("%w: 缺少备份路径", plugin.ErrBadConfig)
	}
	return fmt.Errorf("%w: 示例插件没有实现回滚", plugin.ErrNotSupported)
}

// Uninstall 删除安装目录与自己的状态记录。
func (s *localStub) Uninstall(_ context.Context, req plugin.UninstallRequest) error {
	path := s.installPath(req.InstallPath)
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if !req.KeepUserData && req.DataDir != "" {
		// 私有目录由宿主创建，这里只清掉自己写进去的东西。
		return os.RemoveAll(filepath.Join(req.DataDir, "install"))
	}
	return nil
}
