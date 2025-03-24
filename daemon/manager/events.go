package manager

import "bubble/daemon"

const (
	ManagerSocketOpenEvent      = 233
	ManagerSocketCloseEvent     = 234
	ContainerManagerEnableEvent = 0721
)

var (
	enableManagerEvent = daemon.CreateEventRaw(ContainerManagerEnableEvent, 0, nil)
)

func NewEnableManagerEvent() *daemon.ServerEvent {
	return enableManagerEvent
}

func NewManagerSocketOpenEvent(ctx *ManagerContext) *daemon.ServerEvent {
	return daemon.CreateEventRaw(ManagerSocketOpenEvent, 0, ctx)
}

func NewManagerSocketCloseEvent(ctx *ManagerContext) *daemon.ServerEvent {
	return daemon.CreateEventRaw(ManagerSocketCloseEvent, 0, ctx)
}

func ManagerSocketEvent(evt *daemon.ServerEvent) *ManagerContext {
	return evt.DataRaw().(*ManagerContext)
}
