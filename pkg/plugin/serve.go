package plugin

import (
	"fmt"
	"os"

	goplugin "github.com/hashicorp/go-plugin"
)

// Serve 启动插件并把注册表交给宿主，之后阻塞。
//
// 这是插件 main 里唯一必要的语句：
//
//	func main() {
//		plugin.Serve(plugin.Info{ID: "corp-index", Name: "企业源", Version: "1.0.0"},
//			plugin.Register("corp-vpn", NewCorpVPN, plugin.WithName("公司 VPN")),
//			plugin.Register("legacy-crm", NewLegacyCRM),
//		)
//	}
//
// 插件被用户直接双击运行时，握手令牌不存在，go-plugin 会打印
// "This binary is a plugin. These are not meant to be executed directly." 并退出。
func Serve(info Info, regs ...Registration) {
	reg, err := newRegistry(info, regs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "level=ERROR msg=%q\n", "upkit 插件启动失败: "+err.Error())
		os.Exit(exitCodeSetupFailure)
	}
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins:         pluginHandlers(newServePlugin(reg)),
	})
}
