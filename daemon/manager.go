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
	// When you're editing here, also take a look at sshd.go # handleSession, where we specified a path to be listened.
	InContainerSocketPath = path.Join(InContainerWorkspaceDir, InContainerSocketName)
)

type ManagerContext struct {
	SshContext    *SshConnContext
	ContainerName string
	Address       string
	shuttingDown  bool
}

func (ctx *ManagerContext) StartManagementServer() error {
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
		if ctx.shuttingDown {
			break
		}
		fd, err := l.Accept()
		if err != nil {
			log.Printf("(%v) accept error: %v", ctx.ContainerName, err)
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
			log.Printf("Received destroy signal from container %v", ctx.ContainerName)
			go ctx.destroyContainer()
		case SignalStopContainer[0]:
			log.Printf("Received stop signal from container %v", ctx.ContainerName)
			go ctx.stopContainer()
		case SignalKillContainer[0]:
			log.Printf("Received kill signal from container %v", ctx.ContainerName)
			go ctx.killContainer()
		}
		ctx.shutdownGracefully()
		break // don't receive more signals.
	}
}

func (ctx *ManagerContext) shutdownGracefully() {
	ctx.shuttingDown = true
	_ = os.Remove(ctx.Address)
}

func (ctx *ManagerContext) dockerClient() *client.Client {
	return ctx.SshContext.ServerContext.DockerClient
}

func (ctx *ManagerContext) runContext() context.Context {
	return ctx.SshContext.ServerContext.Context
}

func (ctx *ManagerContext) destroyContainer() {
	err := ctx.dockerClient().ContainerStop(ctx.runContext(), ctx.ContainerName, container.StopOptions{})
	if err != nil {
		log.Printf("(%v) failed to stop container: %v", ctx.ContainerName, err)

		err = ctx.dockerClient().ContainerKill(ctx.runContext(), ctx.ContainerName, "KILL")
		if err != nil {
			log.Printf("(%v) failed to kill container: %v", ctx.ContainerName, err)
			return
		} else {
			log.Printf("(%v) killed container", ctx.ContainerName)
		}
	}
	err = ctx.dockerClient().ContainerRemove(ctx.runContext(), ctx.ContainerName, container.RemoveOptions{Force: true})
	if err != nil {
		log.Printf("(%v) failed to remove container: %v", ctx.ContainerName, err)
		return
	}
}

func (ctx *ManagerContext) stopContainer() {
	err := ctx.dockerClient().ContainerStop(ctx.runContext(), ctx.ContainerName, container.StopOptions{})
	if err != nil {
		log.Printf("(%v) failed to stop container: %v", ctx.ContainerName, err)
	}
}

func (ctx *ManagerContext) killContainer() {
	err := ctx.dockerClient().ContainerKill(ctx.runContext(), ctx.ContainerName, "KILL")
	if err != nil {
		log.Printf("(%v) failed to kill container: %v", ctx.ContainerName, err)
		return
	}
}
