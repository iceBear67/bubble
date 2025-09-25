package manager

import (
	"bubble/daemon"
	"bubble/daemon/forwarder"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/asaskevich/EventBus"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

const (
	containerMethodKill       = "KILL"
	containerMethodStop       = "STOP"
	containerMethodDestroy    = "DESTROY"
	containerMethodExposePort = "PORT"
)

type ManagerContext struct {
	DockerClient  *client.Client
	Context       context.Context
	IpToContainer map[string]string
	shuttingDown  bool
	listener      *net.Listener
	forwarder     *forwarder.PortForwarderConfig
}

func (mctx *ManagerContext) AllowPortForwarding(forwarder *forwarder.PortForwarderConfig) {
	mctx.forwarder = forwarder
}

func StartManagementServer(
	docker *client.Client,
	config daemon.ManagerServer,
	bus EventBus.Bus,
	context context.Context) (*ManagerContext, error) {
	ctx := ManagerContext{
		docker,
		context,
		make(map[string]string, 16),
		false,
		nil,
		nil,
	}
	log.Printf("Starting management server")
	bus.Subscribe(ManagerContainerRegisteredEvent, func(event ContainerJoinedEvent) {
		ctx.IpToContainer[event.bridgeIp] = event.containerId
	})
	go func() {
		err := http.ListenAndServe(config.Address, &ctx)
		if err != nil && !ctx.shuttingDown {
			log.Println("Manager server has been abnormally shut down: ", err)
		}
	}()
	return &ctx, nil
}

func (ctx *ManagerContext) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	containerId, ok := ctx.IpToContainer[r.RemoteAddr]
	if !ok {
		w.WriteHeader(403)
		w.Write([]byte(""))
		return
	}
	switch r.Method {
	case containerMethodStop:
		log.Printf("Received STOP signal from container %v", containerId)
		ctx.stopContainer(containerId)
	case containerMethodDestroy:
		log.Printf("Received DESTROY signal from container %v", containerId)
		ctx.destroyContainer(containerId)
	case containerMethodKill:
		log.Printf("Received KILL signal from container %v", containerId)
		ctx.killContainer(containerId)
	case containerMethodExposePort:
		log.Printf("Receive PORT forwarding request from container %v", containerId)
		if ctx.forwarder == nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		err := r.ParseForm()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("Invalid body"))
			return
		}
		from := r.Form.Get("from")
		fromPort, err := strconv.Atoi(from)
		if err != nil || fromPort < ctx.forwarder.AllowLowest || fromPort > ctx.forwarder.AllowHighest {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(fmt.Sprintf("From port %v isn't integer. Min: %v, max: %v", fromPort, ctx.forwarder.AllowLowest, ctx.forwarder.AllowHighest)))
			return
		}
		to := r.Form.Get("to")
		toPort, err := strconv.Atoi(to)
		if err != nil || (toPort < 1 || toPort > 65535) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("Invalid destination port"))
			return
		}
		ctx.forwarder.Start(fromPort, toPort, r.RemoteAddr)
	}
	w.WriteHeader(http.StatusOK)
}

func (ctx *ManagerContext) getIpOfContainer(containerId string) (string, error) {
	info, err := ctx.DockerClient.ContainerInspect(ctx.Context, containerId)
	if err != nil {
		return "", err
	}
	networks := info.NetworkSettings.Networks
	network, contains := networks["network"]
	if !contains {
		return "", errors.New("no network found")
	}
	return network.IPAddress, nil
}

func (ctx *ManagerContext) IsShuttingDown() bool {
	return ctx.shuttingDown
}

func (ctx *ManagerContext) destroyContainer(containerId string) {
	err := ctx.DockerClient.ContainerStop(ctx.Context, containerId, container.StopOptions{})
	if err != nil {
		log.Printf("failed to stop container %v: %v", containerId, err)

		err = ctx.DockerClient.ContainerKill(ctx.Context, containerId, "KILL")
		if err != nil {
			log.Printf("failed to kill container %v: %v", containerId, err)
			return
		} else {
			log.Printf("killed container %v", containerId)
		}
	}
	err = ctx.DockerClient.ContainerRemove(ctx.Context, containerId, container.RemoveOptions{Force: true})
	if err != nil {
		log.Printf("failed to remove container %v: %v", containerId, err)
		return
	}
}

func (ctx *ManagerContext) stopContainer(containerId string) {
	err := ctx.DockerClient.ContainerStop(ctx.Context, containerId, container.StopOptions{})
	if err != nil {
		log.Printf("failed to stop container %v: %v", containerId, err)
	}
}

func (ctx *ManagerContext) killContainer(containerId string) {
	err := ctx.DockerClient.ContainerKill(ctx.Context, containerId, "KILL")
	if err != nil {
		log.Printf("failed to kill container %v: %v", containerId, err)
		return
	}
}
