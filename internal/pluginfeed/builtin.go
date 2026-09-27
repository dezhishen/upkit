package pluginfeed

import "strings"

// 内置订阅：upkit 自带一条官方订阅地址，用户可以在「来源」面板里一键添加。
//
// 它**不会被自动启用**，也不绕过任何授权步骤 —— 内置只是省掉手输地址：
// 订阅功能本身默认关闭，添加时仍然要确认信任该域名。
//
// 清单与插件产物都由 dezhishen/upkit-hub 托管，本仓库只做平台、不产清单。这样
// 分开的原因很实在：清单里写死的产物地址与 sha256 必须与某一次插件发布严格对应，
// 留在主仓库会跟代码提交搅在一起被顺手改掉，让已发布版本的行为跟着漂移。
//
// 地址取 release 附件的直链而不是 raw 分支链接：
//   - release 附件是不可变的，upkit-hub 主分支上任何半成品提交都不会影响已发布版本；
//   - raw 链接紧跟分支，改一行即刻对所有版本的 upkit 生效，且带 CDN 缓存。
//
// 代价是 upkit-hub 每更新一次清单就要发一个 release。
const (
	// DefaultBuiltinFeedURL 是官方订阅的默认地址。
	DefaultBuiltinFeedURL = "https://github.com/dezhishen/upkit-hub/releases/latest/download/feed.yaml"

	// BuiltinFeedName 是内置订阅的展示名。
	BuiltinFeedName = "upkit 官方源"
)

// builtinFeedURLSymbol 是 -ldflags -X 使用的变量全路径。
//
// 单独定义一份供测试核对：-X 写错路径不会报错，只会静默保留默认值，属于最难查的
// 那类失效。builtin_test.go 会用这个值真的构建一次探针；构建脚本里也必须出现同一
// 字符串，否则 --feed-url 只是个摆设。
const builtinFeedURLSymbol = "github.com/dezhishen/upkit/internal/pluginfeed.BuiltinFeedURL"

// BuiltinFeedURL 是内置订阅实际使用的地址。
//
// 它在构建时可以用 -ldflags 覆盖，便于自建分发、内网镜像或指向测试源：
//
//	go build -ldflags "-X github.com/dezhishen/upkit/internal/pluginfeed.BuiltinFeedURL=https://example.com/feed.yaml"
//	bash scripts/build.sh --feed-url https://example.com/feed.yaml     # 等价写法
//
// 用变量而不是常量就是为了留这个口子。**运行时不要改它** —— IsBuiltinFeed 与界面
// 提示都读这个值，中途改会让「哪个才算内置源」变得难以推理。
var BuiltinFeedURL = DefaultBuiltinFeedURL

// IsBuiltinFeed 报告某个地址是否是内置订阅。
func IsBuiltinFeed(rawURL string) bool {
	return strings.TrimSpace(rawURL) == BuiltinFeedURL
}
