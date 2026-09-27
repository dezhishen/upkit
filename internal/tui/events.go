package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/dezhishen/upkit/internal/core"
)

// eventMsg 是引擎事件的包装：事件由控制层投递（control.Controller.Events），
// 界面只负责把事件渲染成任务进度与日志。
type eventMsg core.Event

// waitEvent 返回等待下一个引擎事件的命令。
//
// 通道的拥有者在控制层，界面不再自己建通道 —— 否则「谁负责关闭、谁负责丢弃」
// 会散在两边。
func (m Model) waitEvent() tea.Cmd {
	if m.ctrl == nil {
		return nil
	}
	ch := m.ctrl.Events()
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg(e)
	}
}
