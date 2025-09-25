package manager

import (
	"bubble/daemon"
)

const (
	ManagerContainerRegisteredEvent = "ManagerContainerRegistered"
)

type ContainerJoinedEvent struct {
	containerId string
	bridgeIp    string
}

func NewContainerRegisterEvent(containerId string, bridgeIp string) *daemon.ServerEvent {
	return daemon.CreateEventRaw(ManagerContainerRegisteredEvent, 0, ContainerJoinedEvent{
		containerId: containerId,
		bridgeIp:    bridgeIp,
	})
}

func ContainerRegisterEvent(event *daemon.ServerEvent) ContainerJoinedEvent {
	if event.Type() != ManagerContainerRegisteredEvent {
		panic(event.Type() + " is not a ContainerJoinedEvent")
	}
	return event.DataRaw().(ContainerJoinedEvent)
}
