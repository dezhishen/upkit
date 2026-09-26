package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/rpc"

	goplugin "github.com/hashicorp/go-plugin"
)

// Empty 用于无参数或无返回值的 RPC 调用。
type Empty struct{}

// JobID 是宿主为一次长任务分配的标识（full 模式使用）。
type JobID struct {
	ID string
}

// RPCRequest 是跨进程调用的统一信封。
//
// 采用「方法名 + JSON 载荷」而不是为每个 API 生成一对类型，原因是：插件 API 会演进，
// 信封式分发让新增方法不需要改动 RPC 签名，旧宿主遇到未知方法会得到清晰的
// ErrNotSupported，而不是编解码失败。
type RPCRequest struct {
	Method  string
	AppID   string
	JobID   string
	Payload []byte
}

// RPCResponse 是跨进程调用的统一应答。
type RPCResponse struct {
	Payload []byte
	ErrKind errorKind
	ErrMsg  string
}

// rpcTarget 是 net/rpc 要求的方法集（参数与返回值都必须是指针）。
type rpcTarget interface {
	Call(req *RPCRequest, resp *RPCResponse) error
	Ping(_ *Empty, _ *Empty) error
	Cancel(id *JobID, _ *Empty) error
}

// pluginImpl 是 SDK 交给 hashicorp/go-plugin 的插件描述。
//
// 插件进程侧填 Impl（真实实现），宿主侧留空（只要它的 Client 方法）。
// 它只属于 SDK 与宿主之间的接线细节，插件作者不会接触到。
type pluginImpl struct {
	Impl Source
}

// Server 由插件进程调用，返回真正对外提供 RPC 服务的对象。
func (p *pluginImpl) Server(_ *goplugin.MuxBroker) (interface{}, error) {
	return &rpcServer{src: p.Impl}, nil
}

// Client 由宿主调用，返回可以在本进程直接当作 Source 使用的 stub。
func (p *pluginImpl) Client(_ *goplugin.MuxBroker, conn *rpc.Client) (interface{}, error) {
	return &rpcClient{conn: conn}, nil
}

// ── 服务端（插件进程内）────────────────────────────────────────

type rpcServer struct {
	src Source
}

func (s *rpcServer) Call(req *RPCRequest, resp *RPCResponse) error {
	if req == nil {
		return errors.New("插件收到的请求为空")
	}
	out, err := serveCall(s.src, req)
	if err != nil {
		resp.ErrKind = classifyError(err)
		resp.ErrMsg = err.Error()
		return nil // 业务错误通过信封回传，不占用 RPC 错误通道
	}
	resp.Payload = out
	return nil
}

func (s *rpcServer) Ping(_ *Empty, _ *Empty) error { return nil }

func (s *rpcServer) Cancel(id *JobID, _ *Empty) error {
	if s.src == nil || id == nil {
		return nil
	}
	return s.src.Cancel(context.Background(), id.ID)
}

// serveCall 把一次信封调用分发到 Source 的具体方法。
func serveCall(src Source, req *RPCRequest) ([]byte, error) {
	if src == nil {
		return nil, fmt.Errorf("%w: 插件未提供任何实现", ErrNotSupported)
	}
	ctx := context.Background()

	switch req.Method {
	case methodInfo:
		v, err := src.Info(ctx)
		return marshal(v, err)
	case methodList:
		v, err := src.List(ctx)
		return marshal(v, err)
	case methodVersions:
		var a SourceVersionsRequest
		if err := unmarshal(req.Payload, &a); err != nil {
			return nil, err
		}
		if a.AppID == "" {
			a.AppID = req.AppID
		}
		v, err := src.Versions(ctx, a)
		return marshal(v, err)
	case methodStatus:
		var a SourceAppRequest
		if err := unmarshal(req.Payload, &a); err != nil {
			return nil, err
		}
		if a.AppID == "" {
			a.AppID = req.AppID
		}
		v, err := src.Status(ctx, a)
		return marshal(v, err)
	case methodPlan:
		var a SourcePlanRequest
		if err := unmarshal(req.Payload, &a); err != nil {
			return nil, err
		}
		a.JobID = req.JobID
		if a.AppID == "" {
			a.AppID = req.AppID
		}
		v, err := src.Plan(ctx, a)
		return marshal(v, err)
	case methodApply:
		var a SourcePlanRequest
		if err := unmarshal(req.Payload, &a); err != nil {
			return nil, err
		}
		a.JobID = req.JobID
		if a.AppID == "" {
			a.AppID = req.AppID
		}
		v, err := src.Apply(ctx, a)
		return marshal(v, err)
	case methodRollback:
		var a SourceRollbackRequest
		if err := unmarshal(req.Payload, &a); err != nil {
			return nil, err
		}
		if a.AppID == "" {
			a.AppID = req.AppID
		}
		return nil, src.Rollback(ctx, a)
	case methodUninst:
		var a SourceUninstallRequest
		if err := unmarshal(req.Payload, &a); err != nil {
			return nil, err
		}
		if a.AppID == "" {
			a.AppID = req.AppID
		}
		return nil, src.Uninstall(ctx, a)
	case methodEvents:
		v, err := src.PollEvents(ctx, req.JobID)
		return marshal(v, err)
	case methodSchema:
		v, err := src.ConfigSchema(ctx)
		return marshal(v, err)
	case methodValidate:
		var a Config
		if err := unmarshal(req.Payload, &a); err != nil {
			return nil, err
		}
		v, err := src.ValidateConfig(ctx, a)
		return marshal(v, err)
	case methodConfig:
		var a Config
		if err := unmarshal(req.Payload, &a); err != nil {
			return nil, err
		}
		return nil, src.Configure(ctx, a)
	case methodPing:
		return nil, src.Ping(ctx)
	case methodCancel:
		return nil, src.Cancel(ctx, req.JobID)
	default:
		return nil, fmt.Errorf("%w: 未知方法 %q", ErrNotSupported, req.Method)
	}
}

func marshal(v any, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	b, merr := json.Marshal(v)
	if merr != nil {
		return nil, fmt.Errorf("编码应答: %w", merr)
	}
	return b, nil
}

func unmarshal(data []byte, v any) error {
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: 请求载荷无法解析: %v", ErrBadConfig, err)
	}
	return nil
}

// ── 客户端（宿主进程内）──────────────────────────────────────

type rpcClient struct {
	conn *rpc.Client
}

func (c *rpcClient) invoke(ctx context.Context, method, appID, jobID string, in any, out any) error {
	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("编码请求 %s: %w", method, err)
		}
		payload = b
	}
	req := &RPCRequest{Method: method, AppID: appID, JobID: jobID, Payload: payload}
	resp := &RPCResponse{}

	done := make(chan error, 1)
	go func() { done <- c.conn.Call(rpcServerName+".Call", req, resp) }()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("调用插件方法 %s: %w", method, err)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	if resp.ErrKind != kindOK {
		return newRemoteError(resp.ErrKind, resp.ErrMsg)
	}
	if out != nil && len(resp.Payload) > 0 {
		if err := json.Unmarshal(resp.Payload, out); err != nil {
			return fmt.Errorf("解码应答 %s: %w", method, err)
		}
	}
	return nil
}

// remoteError 把插件进程的错误还原成本进程的语义错误。
type remoteError struct {
	kind errorKind
	msg  string
}

func (e *remoteError) Error() string { return e.msg }

// Is 让 errors.Is(err, ErrNotFound) 这类判断跨进程依然成立。
func (e *remoteError) Is(target error) bool {
	switch e.kind {
	case kindNotSupported:
		return target == ErrNotSupported
	case kindNotFound:
		return target == ErrNotFound
	case kindBadConfig:
		return target == ErrBadConfig
	case kindRateLimited:
		return target == ErrRateLimited
	case kindCanceled:
		return target == context.Canceled
	default:
		return false
	}
}

func newRemoteError(kind errorKind, msg string) error {
	if kind == kindOK {
		return nil
	}
	if msg == "" {
		msg = "插件调用失败"
	}
	return &remoteError{kind: kind, msg: msg}
}

// ── rpcClient 实现 Source ────────────────────────────────────

func (c *rpcClient) Info(ctx context.Context) (Info, error) {
	var v Info
	err := c.invoke(ctx, methodInfo, "", "", nil, &v)
	return v, err
}

func (c *rpcClient) List(ctx context.Context) ([]Software, error) {
	var v []Software
	err := c.invoke(ctx, methodList, "", "", nil, &v)
	return v, err
}

func (c *rpcClient) Versions(ctx context.Context, req SourceVersionsRequest) ([]Release, error) {
	var v []Release
	err := c.invoke(ctx, methodVersions, req.AppID, "", req, &v)
	return v, err
}

func (c *rpcClient) Status(ctx context.Context, req SourceAppRequest) (Status, error) {
	var v Status
	err := c.invoke(ctx, methodStatus, req.AppID, "", req, &v)
	return v, err
}

func (c *rpcClient) Plan(ctx context.Context, req SourcePlanRequest) (PlanResult, error) {
	var v PlanResult
	err := c.invoke(ctx, methodPlan, req.AppID, req.JobID, req, &v)
	return v, err
}

func (c *rpcClient) Apply(ctx context.Context, req SourcePlanRequest) (Result, error) {
	var v Result
	err := c.invoke(ctx, methodApply, req.AppID, req.JobID, req, &v)
	return v, err
}

func (c *rpcClient) Rollback(ctx context.Context, req SourceRollbackRequest) error {
	return c.invoke(ctx, methodRollback, req.AppID, "", req, nil)
}

func (c *rpcClient) Uninstall(ctx context.Context, req SourceUninstallRequest) error {
	return c.invoke(ctx, methodUninst, req.AppID, "", req, nil)
}

func (c *rpcClient) PollEvents(ctx context.Context, jobID string) ([]Event, error) {
	var v []Event
	err := c.invoke(ctx, methodEvents, "", jobID, nil, &v)
	return v, err
}

func (c *rpcClient) ConfigSchema(ctx context.Context) (ConfigSchema, error) {
	var v ConfigSchema
	err := c.invoke(ctx, methodSchema, "", "", nil, &v)
	return v, err
}

func (c *rpcClient) ValidateConfig(ctx context.Context, cfg Config) (ValidationResult, error) {
	var v ValidationResult
	err := c.invoke(ctx, methodValidate, "", "", cfg, &v)
	return v, err
}

func (c *rpcClient) Configure(ctx context.Context, cfg Config) error {
	return c.invoke(ctx, methodConfig, "", "", cfg, nil)
}

func (c *rpcClient) Ping(ctx context.Context) error {
	return c.invoke(ctx, methodPing, "", "", nil, nil)
}

func (c *rpcClient) Cancel(ctx context.Context, jobID string) error {
	return c.invoke(ctx, methodCancel, "", jobID, nil, nil)
}
