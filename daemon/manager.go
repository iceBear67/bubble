package daemon

import (
	"context"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"log"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	SignalDestroyContainer = []byte{114}
	SignalStopContainer    = []byte{19}
	SignalKillContainer    = []byte{07}
	InContainerSocketName  = "daemon.sock"
	// We won't mount a file into container, we mount the directory instead.
	// When you're editing here, also take a look at sshd.go # handlePtyRequest, where we specified a path to be listened.
	InContainerSocketPath = path.Join(InContainerDataDir, InContainerSocketName)
)

type ManagerContext struct {
	DockerClient *client.Client
	Context      context.Context
	ContainerId  string
	Address      string
	shuttingDown bool
	listener     *net.Listener
}

var runningManagers = sync.Map{}
var managers = atomic.Int32{}

func GetManagerByContainerId(containerId string) *ManagerContext {
	v, _ := runningManagers.Load(containerId)
	if v == nil {
		return nil
	}
	return v.(*ManagerContext)
}

func HasRunningManager() bool {
	return managers.Load() != 0
}

func GetRunningManagers() *sync.Map {
	return &runningManagers
}

func StartManagementServer(docker *client.Client, context context.Context, containerId string, address string) (*ManagerContext, error) {
	ctx := ManagerContext{
		docker,
		context,
		containerId,
		address,
		false,
		nil,
	}

	v, _ := runningManagers.Load(containerId)
	if v != nil {
		return nil, nil
	}
	if ctx.shuttingDown {
		log.Printf("(%v) Attempt to start a shutting down management server!", ctx.ContainerId)
	}
	addr := ctx.Address
	log.Printf("Starting management server on %s", addr)
	var l net.Listener
	ctx.listener = &l
	var err error
	// todo check socket file not exist
	for {
		l, err = net.Listen("unix", addr)
		if err == nil && l != nil {
			break
		}
		if err != nil && strings.Contains(err.Error(), "address already in use") {
			_err := os.Remove(addr)
			if _err != nil {
				log.Printf("Cannot remove old socket: %s", _err)
				return nil, err
			}
		}
	}

	go func() {
		for {
			fd, err := l.Accept()
			if err != nil {
				if ctx.shuttingDown {
					return
				}
				log.Printf("accept error from container %v: %v", ctx.ContainerId, err)
			}
			go ctx.signalServer(fd)
		}
	}()
	runningManagers.Store(ctx.ContainerId, &ctx)
	managers.Add(1)
	log.Printf("Started.")
	return &ctx, nil
}

func (ctx *ManagerContext) IsShuttingDown() bool {
	return ctx.shuttingDown
}

func (ctx *ManagerContext) signalServer(c net.Conn) {
	buf := make([]byte, 1)
	for {
		_, err := c.Read(buf)
		if err != nil {
			return
		}
		switch buf[0] {
		case SignalDestroyContainer[0]:
			log.Printf("Received destroy signal from container %v", ctx.ContainerId)
			go ctx.destroyContainer()
		case SignalStopContainer[0]:
			log.Printf("Received stop signal from container %v", ctx.ContainerId)
			go ctx.stopContainer()
		case SignalKillContainer[0]:
			log.Printf("Received kill signal from container %v", ctx.ContainerId)
			go ctx.killContainer()
		}
		ctx.ShutdownGracefully()
		break // don't receive more signals.
	}
}

func (ctx *ManagerContext) ShutdownGracefully() {
	ctx.shuttingDown = true
	_ = (*ctx.listener).Close()
	_ = os.Remove(ctx.Address)
	runningManagers.Delete(ctx.ContainerId)
	managers.Add(-1)
}

func (ctx *ManagerContext) destroyContainer() {
	err := ctx.DockerClient.ContainerStop(ctx.Context, ctx.ContainerId, container.StopOptions{})
	if err != nil {
		log.Printf("failed to stop container %v: %v", ctx.ContainerId, err)

		err = ctx.DockerClient.ContainerKill(ctx.Context, ctx.ContainerId, "KILL")
		if err != nil {
			log.Printf("failed to kill container %v: %v", ctx.ContainerId, err)
			return
		} else {
			log.Printf("killed container %v", ctx.ContainerId)
		}
	}
	err = ctx.DockerClient.ContainerRemove(ctx.Context, ctx.ContainerId, container.RemoveOptions{Force: true})
	if err != nil {
		log.Printf("failed to remove container %v: %v", ctx.ContainerId, err)
		return
	}
}

func (ctx *ManagerContext) stopContainer() {
	err := ctx.DockerClient.ContainerStop(ctx.Context, ctx.ContainerId, container.StopOptions{})
	if err != nil {
		log.Printf("failed to stop container %v: %v", ctx.ContainerId, err)
	}
}

func (ctx *ManagerContext) killContainer() {
	err := ctx.DockerClient.ContainerKill(ctx.Context, ctx.ContainerId, "KILL")
	if err != nil {
		log.Printf("failed to kill container %v: %v", ctx.ContainerId, err)
		return
	}
}
