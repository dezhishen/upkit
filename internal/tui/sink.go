package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dezhishen/upkit/internal/core"
)

// sink 把 engine 的事件投递到 TUI 的消息循环。
type sink struct {
	ch chan core.Event
}

// NewSink 创建事件接收器（缓冲区不足时丢弃事件，绝不阻塞 engine）。
func NewSink(buf int) (*sink, core.EventSink) {
	s := &sink{ch: make(chan core.Event, buf)}
	return s, core.SinkFunc(func(e core.Event) {
		select {
		case s.ch <- e:
		default:
		}
	})
}

// waitEvent 返回等待下一个事件的命令。
func (s *sink) waitEvent() tea.Cmd {
	return func() tea.Msg {
		e, ok := <-s.ch
		if !ok {
			return nil
		}
		return eventMsg(e)
	}
}

// eventMsg 是引擎事件包装。
type eventMsg core.Event
