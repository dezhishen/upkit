package plugin

// 握手常量。宿主启动插件子进程时写入 magicCookieKey，插件侧 Serve 会校验它：
// 直接双击运行插件 exe 时拿不到正确的 cookie，插件会打印提示后退出，
// 而不是傻等 —— 这是「插件不是独立程序」的第一道提醒。
const (
	// magicCookieKey 是传递握手令牌的环境变量名。
	magicCookieKey = "UPKIT_PLUGIN_COOKIE"
	// magicCookieValue 是当前协议对应的令牌值。
	magicCookieValue = "upkit-plugin-1"
	// protocolVersion 是传输层协议版本，宿主与插件必须一致。
	protocolVersion = 1
	// APIVersion 是当前 SDK 的接口版本，宿主据此判断插件是否需要升级。
	APIVersion = "1"
	// rpcServerName 是 net/rpc 实际注册的服务名。
	//
	// 注意：它**不是** Plugins map 的 key。go-plugin 在分发之后，会在子通道上
	// 固定以 "Plugin" 注册插件实现（见 go-plugin 的 rpc_server.go：
	// serve(conn, "Plugin", impl)），写错就会得到 "rpc: can't find service"。
	rpcServerName = "Plugin"
	// pluginKey 是 go-plugin 里的插件名。一个插件 = 一个 Source = 多个软件。
	pluginKey = "source"
)

// 插件进程的专用退出码。
const (
	// exitCodeBadCookie 是插件在缺少正确握手令牌时使用的退出码。
	exitCodeBadCookie = 9
	// exitCodeSetupFailure 是插件注册表校验失败（插件自身写错）时的退出码。
	exitCodeSetupFailure = 10
)

// methodInfo / methodList / ... 是 RPC 分发用的方法名。
const (
	methodInfo     = "info"
	methodList     = "list"
	methodVersions = "versions"
	methodStatus   = "status"
	methodPlan     = "plan"
	methodApply    = "apply"
	methodRollback = "rollback"
	methodUninst   = "uninstall"
	methodSchema   = "schema"
	methodValidate = "validate"
	methodConfig   = "configure"
	methodPing     = "ping"
	methodCancel   = "cancel"
	methodEvents   = "events"
)
