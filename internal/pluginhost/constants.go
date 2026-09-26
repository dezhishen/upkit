package pluginhost

import "time"

// 宿主侧的固定字符串。集中定义，避免在实现里散落裸字面量。
//
// 注意：清单里插件来源的 kind 值与限定 ID 分隔符定义在清单层（apps 包），
// 这里只放宿主自己使用的常量，同一份契约不维护两遍。

const (
	// dirPlugins 是插件私有目录在 data/ 与 log/ 下的子目录名：
	// data/plugins/<来源ID>/ 与 log/plugins/<来源ID>/。
	dirPlugins = "plugins"

	// manifestSuffix 是插件描述文件的后缀，完整形式为 <id>.plugin.yaml。
	manifestSuffix = ".plugin.yaml"

	// extExec 是插件可执行文件的后缀。
	//
	// upkit 只发行 Windows 版本，所以它是常量而不是按平台判断 —— 开发机（Linux）
	// 上跑测试时，插件文件名同样是 .exe。
	extExec = ".exe"
)

// full 模式的时间参数。
const (
	// eventPollInterval 是宿主向插件拉取事件的时间间隔。
	//
	// 事件由插件缓冲、宿主轮询，而不是插件反向调用宿主：这样插件的 Apply 无论
	// 跑多久都不会因为上报而阻塞，宿主也不必给插件提供回调地址。
	eventPollInterval = 200 * time.Millisecond

	// eventPollTimeout 是单次拉取与收尾拉取的超时。
	//
	// 收尾拉取必须用独立的短超时：此时调用方的 ctx 往往已经取消，
	// 复用它会把最后一批事件（含失败原因）丢掉。
	eventPollTimeout = 2 * time.Second

	// cancelTimeout 是通知插件停止当前任务时的宽限时间。
	cancelTimeout = 2 * time.Second
)
