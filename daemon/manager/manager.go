package manager

import (
	"bubble/daemon"
	"context"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
)

const (
	containerMethodKill       = "KILL"
	containerMethodStop       = "STOP"
	containerMethodDestroy    = "DESTROY"
	containerMethodExposePort = "PORT"
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
	var server *http.Server
	go func() {
		server = &http.Server{
			Handler: &ctx,
		}
		err = server.Serve(l)
		if err != nil && !ctx.shuttingDown {
			log.Printf("Cannot start management server on %s: %v", ctx.Address, err)
		}
		eventChannel <- NewManagerSocketCloseEvent(&ctx)
	}()
	if eventChannel != nil {
		eventChannel <- NewManagerSocketOpenEvent(&ctx)
	}
	log.Printf("Started.")
	go func() {
		select {
		case <-context.Done():
			if ctx.shuttingDown {
				return
			}
			ctx.shuttingDown = true
			if err := server.Shutdown(ctx.Context); err != nil {
				log.Printf("Cannot shutdown management server gracefully: %v", err)
			}
			_ = os.Remove(ctx.Address)
		}
	}()
	return &ctx, nil
}

func (ctx *ManagerContext) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "STOP":
		ctx.stopContainer()
	case "DESTROY":
		ctx.destroyContainer()
	case "KILL":
		ctx.killContainer()
	}
}

func (ctx *ManagerContext) IsShuttingDown() bool {
	return ctx.shuttingDown
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
