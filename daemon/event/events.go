package event

import (
	"encoding/binary"
)

const (
	ClientResizeEvent           = 114514
	ClientExecEvent             = 1551
	ClientPipeBrokenEvent       = 1919810
	ClientSubsystemRequestEvent = 943
	ContainerManagerEnableEvent = 0721
)

type ConsoleEvent struct {
	typeIndex int
	flag      int
	data      interface{}
}

func (c *ConsoleEvent) Type() int {
	return c.typeIndex
}

var (
	enableManagerEvent = &ConsoleEvent{
		typeIndex: ContainerManagerEnableEvent,
	}
)

func NewSubsystemRequest(name string) *ConsoleEvent {
	return &ConsoleEvent{
		typeIndex: ClientSubsystemRequestEvent,
		data:      name,
	}
}

func (e *ConsoleEvent) SubsystemRequest() string {
	return e.data.(string)
}

func NewBrokenPipeEvent(execId string) *ConsoleEvent {
	return &ConsoleEvent{
		typeIndex: ClientPipeBrokenEvent,
		data:      execId,
	}
}

func (e *ConsoleEvent) BrokenPipeEvent() string {
	return e.data.(string)
}

func NewEnableManager() *ConsoleEvent {
	return enableManagerEvent
}

func NewExecEvent(silent bool, exec []string) *ConsoleEvent {
	flag := 0
	if silent {
		flag = 1
	}
	return &ConsoleEvent{
		typeIndex: ClientExecEvent,
		flag:      flag,
		data:      exec,
	}
}

func (e *ConsoleEvent) ExecEvent() (quiet bool, exec []string) {
	return e.flag == 1, e.data.([]string)
}

func NewResizeEvent(dims []byte) *ConsoleEvent {
	return &ConsoleEvent{
		typeIndex: ClientResizeEvent,
		data:      dims,
	}
}

// algo taken from https://gist.github.com/jpillora/b480fde82bff51a06238.
func (e *ConsoleEvent) ResizeEvent() (w uint, h uint) {
	data := e.data.([]byte)
	return uint(binary.BigEndian.Uint32(data)), uint(binary.BigEndian.Uint32(data[4:]))
}
