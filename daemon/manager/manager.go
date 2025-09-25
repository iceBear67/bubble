package manager

import (
	"bubble/daemon"
	"bubble/daemon/forwarder"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/werbenhu/eventbus"
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
	bus *eventbus.EventBus,
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
	bus.Subscribe(ManagerContainerRegisteredEvent, func(_ string, ev *daemon.ServerEvent) {
		event := ContainerRegisterEvent(ev)
		log.Println("(manager) Allowed ACCESS from container ", event.containerId, " at address ", event.bridgeIp)
		ctx.IpToContainer[event.bridgeIp] = event.containerId
	})
	l, err := net.Listen("tcp", config.Address)
	if err != nil {
		return nil, err
	}
	go func() {
		ctx.listener = &l
		err = http.Serve(l, &ctx)
		if err != nil && !ctx.shuttingDown {
			log.Println("Manager server has been abnormally shut down: ", err)
		}
	}()
	go func() {
		select {
		case <-context.Done():
			ctx.shuttingDown = true
			(*ctx.listener).Close()
		}
	}()
	return &ctx, nil
}

func (ctx *ManagerContext) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := strings.Split(r.RemoteAddr, ":")[0]
	containerId, ok := ctx.IpToContainer[ip]
	if !ok {
		log.Println("Denied access to management server from ", r.RemoteAddr)
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
