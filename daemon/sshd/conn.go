package sshd

import (
	"bubble/daemon"
	"bubble/daemon/event"
	"context"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/ssh"
	"io"
	"log"
)

type SshConnContext struct {
	ServerContext *SshServerContext
	Context       context.Context
	User          string
	Conn          *ssh.Channel
	EventChannel  chan *event.ConsoleEvent
}

func (connCtx *SshConnContext) createManagerSocket(containerName string, addr string) *daemon.ManagerContext {
	mctx := daemon.ManagerContext{
		connCtx.ServerContext.DockerClient,
		connCtx.Context,
		containerName,
		addr,
		false,
	}
	go func() {
		err := mctx.StartManagementServer()
		if err != nil {
			log.Printf("Failed to start manager socket: %v", err)
		}
	}()
	return &mctx
}

func (c *SshConnContext) redirectToContainer(
	containerID string,
	cmd []string,
) (closeHandle func(), execId *string, err error) {
	env := make([]string, 0)
	env = append(env, "BUBBLE_SOCK="+daemon.InContainerSocketPath)
	//todo more env
	execConfig := container.ExecOptions{
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
		Env:          env,
	}
	sctx := c.ServerContext
	dockerClient := sctx.DockerClient
	execResp, err := dockerClient.ContainerExecCreate(sctx.Context, containerID, execConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("error occurred while exec-ing! %v\n", err)
	}
	id := execResp.ID

	hijackedResp, err := dockerClient.ContainerExecAttach(sctx.Context, id, container.ExecStartOptions{Tty: true})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to attach instance! %v\n", err)
	}

	// these io.Copy are expected to close at the same time.
	conn := c.Conn
	go func() {
		_, _ = io.Copy(hijackedResp.Conn, *conn)
		_ = hijackedResp.CloseWrite()
	}()
	go func() {
		_, _ = io.Copy(*conn, hijackedResp.Reader)
		c.EventChannel <- event.NewBrokenPipeEvent(id)
	}()
	return func() {
		hijackedResp.Close()
	}, &id, nil
}

func (connCtx *SshConnContext) logToBoth(msg string) {
	connCtx.printTextLn(msg)
	log.Println(msg)
}

func (connCtx *SshConnContext) printTextLn(text string) {
	_, _ = (*connCtx.Conn).Write([]byte(text + "\r\n"))
}
