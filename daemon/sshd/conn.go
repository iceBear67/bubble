package sshd

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/docker/docker/api/types/container"
	"github.com/werbenhu/eventbus"
	"golang.org/x/crypto/ssh"
)

type SshConnContext struct {
	ServerContext *SshServerContext
	EventBus      *eventbus.EventBus
	context       context.Context
	User          string
	Conn          *ssh.Channel
	Interactive   bool
}

func (connCtx *SshConnContext) RedirectToContainer(
	containerID string,
	cmd []string,
) (closeHandle func(), execId *string, err error) {
	env := make([]string, 0)
	//todo more env
	execConfig := container.ExecOptions{
		Tty:          connCtx.Interactive,
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
		connCtx.EventBus.Publish(ClientPipeBrokenEvent, NewBrokenPipeEvent(id))
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
	if connCtx.Interactive {
		_, _ = (*connCtx.Conn).Write([]byte(text + "\r\n"))
	}
}
