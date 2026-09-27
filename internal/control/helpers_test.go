package control

import (
	"github.com/dezhishen/upkit/internal/core"
	"github.com/dezhishen/upkit/internal/pluginfeed"
)

// boolPtr 取一个布尔指针（清单里的开关字段是指针，nil 表示「没写过」）。
func boolPtr(v bool) *bool { return &v }

// newEvent 造一条最小可辨识的事件（只关心它属于哪个软件）。
func newEvent(appID string) core.Event {
	return core.Event{AppID: appID, Kind: core.EventLog, Msg: "事件 " + appID}
}

// pluginEntry 造一条订阅条目，用于「订阅模块未启用」之类的守卫分支。
func pluginEntry(id string) pluginfeed.Entry {
	return pluginfeed.Entry{Plugin: pluginfeed.Plugin{ID: id, Version: "1.0.0"}}
}
