// Package all 把内置适配器注册进 Registry。
//
// 这是「知道所有具体适配器」的唯一位置：新增一个安装方式或解包器，只需要在
// 这里多写一行 Register，engine 与 TUI 都不用改。
package all

import (
	"github.com/dezhishen/upkit/internal/apps"
	"github.com/dezhishen/upkit/internal/detect"
	plugindetect "github.com/dezhishen/upkit/internal/detect/plugin"
	"github.com/dezhishen/upkit/internal/method"
	pluginmethod "github.com/dezhishen/upkit/internal/method/plugin"
	"github.com/dezhishen/upkit/internal/registry"
	"github.com/dezhishen/upkit/internal/source/githubrelease"
	pluginsource "github.com/dezhishen/upkit/internal/source/plugin"
	"github.com/dezhishen/upkit/internal/unpacking"
)

// Registry 返回注册了全部内置适配器的注册表。
func Registry() *registry.Registry {
	r := registry.New()

	// 来源
	r.RegisterSource(githubrelease.Name, githubrelease.New)
	// 插件来源：kind 形如 "plugin:<来源ID>"，一个前缀匹配全部插件。
	r.RegisterSourcePrefix(apps.SourceKindPluginPrefix, pluginsource.New)

	// 解包
	r.RegisterUnpacker(unpacking.KindZip, unpacking.NewZip)
	r.RegisterUnpacker(unpacking.KindTarGz, unpacking.NewTarGz)
	r.RegisterUnpacker(unpacking.KindRaw, unpacking.NewRaw)

	// 安装方式
	r.RegisterMethod(method.KindPortableInPlace, method.NewPortableInPlace)
	r.RegisterMethod(method.KindExeInstaller, method.NewExeInstaller)
	r.RegisterMethod(method.KindMSIExec, method.NewMSIExec)
	// 插件接管安装（full 模式）：插件是「一种安装方式」，不是一处特殊逻辑。
	r.RegisterMethod(pluginmethod.Kind, pluginmethod.New)

	// 本地版本探测
	r.RegisterDetector(detect.KindStateFile, detect.NewStateFile)
	r.RegisterDetector(detect.KindPEResource, detect.NewPEResource)
	r.RegisterDetector(detect.KindDirName, detect.NewDirName)
	r.RegisterDetector(detect.KindCLIVersion, detect.NewCLIVersion)
	// full 模式下只有插件自己知道装在哪、装的哪个版本。
	r.RegisterDetector(plugindetect.Kind, plugindetect.New)

	return r
}
