package sshd

import (
	"bubble/daemon"
	"bubble/daemon/manager"
	"context"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/ssh"
	"io"
	"log"
)

type SshConnContext struct {
	ServerContext *SshServerContext
	context       context.Context
	User          string
	Conn          *ssh.Channel
	EventChannel  chan *daemon.ServerEvent
}

func (connCtx *SshConnContext) createManagerSocket(containerId string, addr string) *manager.ManagerContext {
	ctx, err := manager.StartManagementServer(
		connCtx.ServerContext.DockerClient,
		connCtx.context,
		connCtx.EventChannel,
		containerId,
		addr)
	if err != nil {
		log.Printf("Failed to start manager socket: %v", err)
	}
	return ctx
}

func (connCtx *SshConnContext) RedirectToContainer(
	containerID string,
	cmd []string,
) (closeHandle func(), execId *string, err error) {
	env := make([]string, 0)
	env = append(env, "BUBBLE_SOCK="+manager.InContainerSocketPath)
	//todo more env
	execConfig := container.ExecOptions{
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
		Env:          env,
	}
	sctx := connCtx.ServerContext
	dockerClient := sctx.DockerClient
	execResp, err := dockerClient.ContainerExecCreate(sctx.context, containerID, execConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("error occurred while exec-ing! %v\n", err)
	}
	id := execResp.ID

	hijackedResp, err := dockerClient.ContainerExecAttach(sctx.context, id, container.ExecStartOptions{Tty: true})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to attach instance! %v\n", err)
	}

	// these io.Copy are expected to close at the same time.
	conn := connCtx.Conn
	go func() {
		_, _ = io.Copy(hijackedResp.Conn, *conn)
		_ = hijackedResp.CloseWrite()
	}()
	go func() {
		_, _ = io.Copy(*conn, hijackedResp.Reader)
		connCtx.EventChannel <- NewBrokenPipeEvent(id)
	}()
	return func() {
		hijackedResp.Close()
	}, &id, nil
}

func (connCtx *SshConnContext) logToBoth(msg string) {
	connCtx.PrintTextLn(msg)
	log.Println(msg)
}

func (connCtx *SshConnContext) PrintTextLn(text string) {
	_, _ = (*connCtx.Conn).Write([]byte(text + "\r\n"))
}
