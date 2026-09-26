package plugin

import goplugin "github.com/hashicorp/go-plugin"

// handshake 是宿主与插件之间的握手配置。
//
// 宿主启动插件子进程时会写入 magicCookieKey 环境变量；插件侧 go-plugin 在 Serve
// 时校验它，因此插件可执行文件被用户直接双击运行时不会静默挂起，而是打印
// "This binary is a plugin..." 后退出。
//
// 下面三个是 go-plugin 的**字段名**（必须用它的大写形式，不能改），
// 等号右侧才是本包的常量。
var handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  protocolVersion,
	MagicCookieKey:   magicCookieKey,
	MagicCookieValue: magicCookieValue,
}

// supportedProtocols 只启用 net/rpc，不启用 gRPC。
//
// 传输用 net/rpc + gob 信封（载荷是 JSON），插件作者因此不需要 protoc，
// 也不需要生成任何代码。
var supportedProtocols = []goplugin.Protocol{goplugin.ProtocolNetRPC}

// newPlugin 返回一个只实现消息传递、不含实现的 go-plugin 描述（宿主侧使用）。
func newPlugin() *pluginImpl { return &pluginImpl{} }

// newServePlugin 返回插件进程侧使用的 go-plugin 描述。
func newServePlugin(impl Source) *pluginImpl { return &pluginImpl{Impl: impl} }

// pluginHandlers 返回供 go-plugin 使用的插件表。
//
// 这里用函数而不是字面量，是为了让「注册名」只有 pluginKey 一个来源，
// 避免客户端与服务端写得不一致。
func pluginHandlers(p *pluginImpl) map[string]goplugin.Plugin {
	return map[string]goplugin.Plugin{pluginKey: p}
}
