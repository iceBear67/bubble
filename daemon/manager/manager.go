package manager

import (
	"bubble/daemon"
	"context"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"log"
	"net"
	"os"
	"path"
	"strings"
)

var (
	InContainerSocketName = "daemon.sock"
	InContainerSocketPath = path.Join(daemon.InContainerDataDir, InContainerSocketName)
)

type ManagerContext struct {
	DockerClient *client.Client
	Context      context.Context
	ContainerId  string
	Address      string
	shuttingDown bool
	listener     *net.Listener
}

func StartManagementServer(
	docker *client.Client,
	context context.Context,
	eventChannel chan *daemon.ServerEvent,
	containerId string,
	address string) (*ManagerContext, error) {
	ctx := ManagerContext{
		docker,
		context,
		containerId,
		address,
		false,
		nil,
	}
	addr := ctx.Address
	log.Printf("Starting management server on %s", addr)
	var l net.Listener
	ctx.listener = &l
	var err error
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

	go acceptLoop(l, &ctx, address, eventChannel)
	if eventChannel != nil {
		eventChannel <- NewManagerSocketOpenEvent(&ctx)
	}
	log.Printf("Started.")
	go func() {
		select {
		case <-context.Done():
			ctx.shutdownGracefully()
		}
	}()
	return &ctx, nil
}

func acceptLoop(l net.Listener, ctx *ManagerContext, address string, eventChannel chan *daemon.ServerEvent) {
	for {
		fd, err := l.Accept()

		if ctx.shuttingDown {
			log.Printf("Shutting down management server at %v", address)
			if eventChannel != nil {
				eventChannel <- NewManagerSocketCloseEvent(ctx)
			}
			return
		}
		if err != nil {
			log.Printf("accept error from container %v: %v", ctx.ContainerId, err)
			continue
		}
		go ctx.signalServer(fd)
	}
}

func (ctx *ManagerContext) IsShuttingDown() bool {
	return ctx.shuttingDown
}

func (ctx *ManagerContext) signalServer(c net.Conn) {
	for {
		buf := make([]byte, 1024)
		daemon.Unmarshal()
	}

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
		ctx.shutdownGracefully()
		break // don't receive more signals.
	}
}

func (ctx *ManagerContext) shutdownGracefully() {
	if ctx.shuttingDown {
		return
	}
	ctx.shuttingDown = true
	_ = (*ctx.listener).Close()
	_ = os.Remove(ctx.Address)
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
