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
	ShuttingDown bool
}

func (ctx *ManagerContext) StartManagementServer() error {
	if ctx.ShuttingDown {
		log.Printf("(%v) Attempt to start a shutting down management server!", ctx.ContainerId)
	}
	addr := ctx.Address
	log.Printf("Starting management server on %s", addr)
	var l net.Listener
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
				return err
			}
		}
	}
	log.Printf("Started.")
	for {
		if ctx.ShuttingDown {
			break
		}
		fd, err := l.Accept()
		if err != nil {
			log.Printf("accept error from container %v: %v", ctx.ContainerId, err)
		}
		go ctx.signalServer(fd)
	}
	return nil
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
		ctx.shutdownGracefully()
		break // don't receive more signals.
	}
}

func (ctx *ManagerContext) shutdownGracefully() {
	ctx.ShuttingDown = true
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
