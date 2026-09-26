// Package plugin 是 upkit 的插件开发 SDK。
//
// 插件作者只需要三件事：
//
//		import "github.com/dezhishen/upkit/pkg/plugin"
//
//		func main() {
//			plugin.Serve(plugin.Info{ID: "corp-index", Name: "企业源", Version: "1.0.0"},
//				plugin.Register("corp-vpn", NewCorpVPN),        // 每个软件一个构造器
//				plugin.Register("legacy-crm", NewLegacyCRM),
//			)
//		}
//
//	 1. 引入本包（SDK）；
//	 2. 用 Register 注册它管理的每个软件，并给出该软件的构造器；
//	 3. 用 Serve 把注册表交给宿主进程。
//
// 一个插件 = 一个可执行文件 = 一个「来源」= 多个软件。
//
// # 两种模式
//
//	catalog（轻）：插件只回答「有哪些软件、有哪些版本、下载什么」，
//	               下载 / 解包 / 落地 / 探测全部复用宿主内置的四条轴。
//	full（重）：   插件额外实现 Method 接口，自己接管 Plan / Apply / 回滚。
//
// 写一个内部软件源通常只需要实现 App.Versions 一个方法。
//
// # 本包的约束
//
// 本包**不 import upkit 的任何 internal 包**，因此可以被第三方模块独立引入；
// 跨进程传输的数据全部是纯数据类型，不含宿主内部类型，保证插件 API 可长期稳定。
package plugin
