package plugin

import "testing"

// 这些常量是对外契约：它们会出现在清单、插件描述文件、插件日志、事件流与配置
// 表单里，改动等于不兼容变更（旧插件或旧宿主会读不懂）。
//
// 本测试把取值钉死，避免在重构时被无意改动 —— 需要改值时必须同时升级
// APIVersion 并同步宿主。
func TestConstantValuesAreStable(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		// 日志
		{"LogKeyLevel", LogKeyLevel, "level"},
		{"LogKeyMessage", LogKeyMessage, "msg"},
		{"LogLevelDebug", LogLevelDebug, "debug"},
		{"LogLevelInfo", LogLevelInfo, "info"},
		{"LogLevelWarn", LogLevelWarn, "warn"},
		{"LogLevelError", LogLevelError, "error"},

		// 事件
		{"EventStarted", EventStarted, "started"},
		{"EventPhase", EventPhase, "phase"},
		{"EventProgress", EventProgress, "progress"},
		{"EventLog", EventLog, "log"},
		{"EventBlocked", EventBlocked, "blocked"},
		{"EventFinished", EventFinished, "finished"},
		{"EventFailed", EventFailed, "failed"},

		// 动作
		{"ActionInstall", ActionInstall, "install"},
		{"ActionUpdate", ActionUpdate, "update"},
		{"ActionReinstall", ActionReinstall, "reinstall"},
		{"ActionUninstall", ActionUninstall, "uninstall"},
		{"ActionNoop", ActionNoop, "noop"},

		// 计划步骤
		{"StepDownload", StepDownload, "download"},
		{"StepExtract", StepExtract, "extract"},
		{"StepCopy", StepCopy, "copy"},
		{"StepRemove", StepRemove, "remove"},
		{"StepRun", StepRun, "run"},
		{"StepVerify", StepVerify, "verify"},
		{"StepSwitch", StepSwitch, "switch"},

		// 配置字段类型
		{"FieldString", FieldString, "string"},
		{"FieldInt", FieldInt, "int"},
		{"FieldBool", FieldBool, "bool"},
		{"FieldEnum", FieldEnum, "enum"},
		{"FieldPath", FieldPath, "path"},
		{"FieldDuration", FieldDuration, "duration"},
		{"FieldSecret", FieldSecret, "secret"},

		// 模式与能力
		{"ModeCatalog", ModeCatalog, "catalog"},
		{"ModeFull", ModeFull, "full"},
		{"CapabilityCatalog", CapabilityCatalog, "catalog"},
		{"CapabilityFull", CapabilityFull, "full"},
		{"CapabilityConfigurable", CapabilityConfigurable, "configurable"},

		// 握手与协议
		{"magicCookieKey", magicCookieKey, "UPKIT_PLUGIN_COOKIE"},
		{"magicCookieValue", magicCookieValue, "upkit-plugin-1"},
		{"pluginKey", pluginKey, "source"},
		{"rpcServerName", rpcServerName, "Plugin"},

		// RPC 方法名
		{"methodInfo", methodInfo, "info"},
		{"methodList", methodList, "list"},
		{"methodVersions", methodVersions, "versions"},
		{"methodStatus", methodStatus, "status"},
		{"methodPlan", methodPlan, "plan"},
		{"methodApply", methodApply, "apply"},
		{"methodRollback", methodRollback, "rollback"},
		{"methodUninst", methodUninst, "uninstall"},
		{"methodSchema", methodSchema, "schema"},
		{"methodValidate", methodValidate, "validate"},
		{"methodConfig", methodConfig, "configure"},
		{"methodPing", methodPing, "ping"},
		{"methodCancel", methodCancel, "cancel"},
		{"methodEvents", methodEvents, "events"},

		// 错误分类
		{"kindOK", string(kindOK), ""},
		{"kindOther", string(kindOther), "other"},
		{"kindNotSupported", string(kindNotSupported), "not_supported"},
		{"kindNotFound", string(kindNotFound), "not_found"},
		{"kindBadConfig", string(kindBadConfig), "bad_config"},
		{"kindRateLimited", string(kindRateLimited), "rate_limited"},
		{"kindCanceled", string(kindCanceled), "canceled"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q，期望 %q；改动即协议不兼容，需同步升级宿主与插件", tc.name, tc.got, tc.want)
		}
	}
}

// 能力声明必须与模式取值一致，否则「描述文件里的 mode」与「插件自报的能力」会对不上。
func TestCapabilityMatchesMode(t *testing.T) {
	if CapabilityCatalog != ModeCatalog {
		t.Errorf("CapabilityCatalog(%q) 必须等于 ModeCatalog(%q)", CapabilityCatalog, ModeCatalog)
	}
	if CapabilityFull != ModeFull {
		t.Errorf("CapabilityFull(%q) 必须等于 ModeFull(%q)", CapabilityFull, ModeFull)
	}
}
