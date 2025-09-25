package sshd

import (
	"bubble/daemon"
	"encoding/binary"
)

const (
	ClientResizeEvent           = "ClientResizeEvent"
	ClientExecEvent             = "ClientExecEvent"
	ClientPipeBrokenEvent       = "ClientPipeBrokenEvent"
	ClientSubsystemRequestEvent = "ClientSubsystemRequestEvent"

	ConnectionCloseEvent       = "ConnectionCloseEvent"
	ConnectionEstablishedEvent = "ConnectionEstablishedEvent"
)

func NewConnectionEstablishedEvent(conn *SshConnContext) *daemon.ServerEvent {
	return daemon.CreateEventRaw(ConnectionEstablishedEvent, 0, conn)
}

func NewConnectionLostEvent(conn *SshConnContext) *daemon.ServerEvent {
	return daemon.CreateEventRaw(ConnectionCloseEvent, 0, conn)
}

func ConnectionEvent(c *daemon.ServerEvent) *SshConnContext {
	return c.DataRaw().(*SshConnContext)
}

func NewSubsystemRequest(name string) *daemon.ServerEvent {
	return daemon.CreateEventRaw(ClientSubsystemRequestEvent, 0, name)
}

func SubsystemRequest(c *daemon.ServerEvent) string {
	return c.DataRaw().(string)
}

func NewBrokenPipeEvent(execId string) *daemon.ServerEvent {
	return daemon.CreateEventRaw(ClientPipeBrokenEvent, 0, execId)
}

func BrokenPipeEvent(c *daemon.ServerEvent) string {
	return c.DataRaw().(string)
}

func NewExecEvent(silent bool, exec []string) *daemon.ServerEvent {
	flag := 0
	if silent {
		flag = 1
	}
	return daemon.CreateEventRaw(ClientExecEvent, flag, exec)
}

func ExecEvent(c *daemon.ServerEvent) (quiet bool, exec []string) {
	return c.Flag() == 1, c.DataRaw().([]string)
}

func NewResizeEvent(dims []byte) *daemon.ServerEvent {
	return daemon.CreateEventRaw(ClientResizeEvent, 0, dims)
}

// algo taken from https://gist.github.com/jpillora/b480fde82bff51a06238.
func ResizeEvent(c *daemon.ServerEvent) (w uint, h uint) {
	data := c.DataRaw().([]byte)
	return uint(binary.BigEndian.Uint32(data)), uint(binary.BigEndian.Uint32(data[4:]))
}
