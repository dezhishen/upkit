#!/usr/bin/env bash
# 分层检查：界面层不得持有领域服务。
#
# 界面只做两件事 —— 把状态画出来、把输入转成意图；服务句柄与多步流程都在
# internal/control。这条约束靠代码评审守不住：随手挂个字段、随手 import 一个包都很
# 方便，而缺的往往不是显示而是流程里的一步（插件装完忘了记信任、更新前忘了停进程、
# 启停只改了文件没改行为）。用脚本钉住，顺手也说明当前边界在哪。
#
# 用法：bash scripts/check-layering.sh

set -euo pipefail

target="internal/tui"

# ① 服务句柄的具体类型不得出现。
#
# 设置（*settings.Settings）也在内：字段、取值范围、可选项、脏标记全在控制层，界面只按
# Kind 渲染并提交意图。将来换 GUI 也是同一套接口。
banned=(
  '*engine.Engine'
  '*apps.File'
  '*pluginhost.Manager'
  '*pluginfeed.Store'
  '*logging.Manager'
  '*settings.Settings'
)

# ② 界面可以依赖的领域包白名单：只有值类型与工具。
#
# 值类型（core.AppRef、engine.App、pluginhost.State、pluginfeed.Entry）是数据，界面
# 拿它选颜色、算宽度都合理；不能依赖的是「拥有状态的那些包」。控制层是唯一入口。
allowed='^(control|core|engine|pluginfeed|pluginhost|util)$'

status=0

for pat in "${banned[@]}"; do
  # 排除测试：测试要往控制层里注入宿主与订阅仓库，那是装配，不是界面逻辑。
  if hits=$(grep -rnF --include='*.go' -e "$pat" "$target" | grep -v '_test\.go' || true); then
    if [ -n "$hits" ]; then
      echo "界面层不应持有领域服务 $pat：" >&2
      echo "$hits" >&2
      status=1
    fi
  fi
done

while IFS= read -r line; do
  file=${line%%:*}
  dep=${line#*:}
  dep=${dep#*\"}
  dep=${dep%\"*}
  name=${dep##*/}
  if ! printf '%s' "$name" | grep -Eq "$allowed"; then
    echo "$file 不该 import internal/$name：界面只用值类型与工具，服务走 internal/control" >&2
    status=1
  fi
done < <(grep -rn --include='*.go' -e '"github.com/dezhishen/upkit/internal/' "$target" \
  | grep -v '_test\.go' || true)

if [ "$status" -ne 0 ]; then
  exit 1
fi
echo "分层检查通过：$target 只依赖值类型与 internal/control"
