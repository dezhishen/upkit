package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
)

// ClientConfig 是宿主启动插件所需的参数。
type ClientConfig struct {
	// Exec 是插件可执行文件的绝对路径。
	Exec string
	// Args 是附加命令行参数（一般留空）。
	Args []string
	// Env 是附加环境变量（默认继承宿主环境）。
	Env []string
	// Dir 是插件子进程的工作目录。
	Dir string
	// Timeout 是启动与首次握手超时，默认 20 秒。
	Timeout time.Duration
	// Stderr 接收插件的 stdout/stderr（即插件日志）。默认 os.Stderr。
	Stderr io.Writer
}

// Client 是宿主侧的一条插件连接。
type Client struct {
	exec string
	gp   *goplugin.Client
	src  Source
	info Info
}

// NewClient 启动插件子进程并完成握手；任何一步失败都不会残留子进程。
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Exec == "" {
		return nil, errors.New("插件可执行文件路径为空")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	out := cfg.Stderr
	if out == nil {
		out = os.Stderr
	}

	cmd := exec.Command(cfg.Exec, cfg.Args...)
	cmd.Dir = cfg.Dir
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}

	gp := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  handshake,
		Plugins:          pluginHandlers(newPlugin()),
		Cmd:              cmd,
		AllowedProtocols: supportedProtocols,
		Managed:          true,
		StartTimeout:     cfg.Timeout,
		SyncStdout:       out,
		SyncStderr:       out,
		Logger: hclog.New(&hclog.LoggerOptions{
			Name:   "upkit-plugin",
			Level:  hclog.Error,
			Output: out,
		}),
	})

	rc, err := gp.Client()
	if err != nil {
		gp.Kill()
		return nil, fmt.Errorf("启动插件 %s: %w", cfg.Exec, err)
	}
	raw, err := rc.Dispense(pluginKey)
	if err != nil {
		gp.Kill()
		return nil, fmt.Errorf("连接插件 %s: %w", cfg.Exec, err)
	}
	src, ok := raw.(Source)
	if !ok {
		gp.Kill()
		return nil, fmt.Errorf("插件 %s 接口不兼容（请升级插件或主程序）", cfg.Exec)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	info, err := src.Info(ctx)
	if err != nil {
		gp.Kill()
		return nil, fmt.Errorf("插件 %s 握手失败: %w", cfg.Exec, err)
	}
	if info.APIVersion != "" && info.APIVersion != APIVersion {
		gp.Kill()
		return nil, fmt.Errorf("插件 %s 的协议版本为 %s，主程序支持 %s", cfg.Exec, info.APIVersion, APIVersion)
	}

	return &Client{exec: cfg.Exec, gp: gp, src: src, info: info}, nil
}

// Info 返回握手时取得的插件信息。
func (c *Client) Info() Info { return c.info }

// Source 返回可直接调用的插件对象。
func (c *Client) Source() Source { return c.src }

// Ping 做一次健康检查。
func (c *Client) Ping(ctx context.Context) error { return c.src.Ping(ctx) }

// Kill 停止插件子进程并释放连接。
func (c *Client) Kill() {
	if c.gp != nil {
		c.gp.Kill()
	}
}

// Exec 返回插件可执行文件路径。
func (c *Client) Exec() string { return c.exec }
