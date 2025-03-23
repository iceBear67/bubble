package daemon

import (
	"encoding/hex"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/ssh"
	"io"
	"log"
	"net"
	"path/filepath"
)

func (connCtx *SshConnContext) handleConnection(conn net.Conn, sshConfig *ssh.ServerConfig) {
	sshConn, channels, requests, err := ssh.NewServerConn(conn, sshConfig)
	if err != nil {
		log.Println("SSH handshake failed:", err)
		return
	}
	log.Printf("New connection from %s as %s\n", sshConn.RemoteAddr(), sshConn.User())
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		log.Printf("Channel type: %v", newChannel.ChannelType())
		log.Printf("Extra dump: %v", hex.Dump(newChannel.ExtraData()))
		if newChannel.ChannelType() == "session" {
			channel, reqs, err := newChannel.Accept()
			if err != nil {
				log.Println("Failed to accept channel:", err)
				continue
			}
			connCtx.Conn = &channel
			connCtx.EventLoop = make(chan *ConsoleEvent)
			connCtx.User = sshConn.User()
			go connCtx.handleSession(channel)
			go connCtx.handleRequests(reqs)
		}
	}
}

func (connCtx *SshConnContext) handleRequests(requests <-chan *ssh.Request) {
	for req := range requests {
		// taken from https://gist.github.com/jpillora/b480fde82bff51a06238
		switch req.Type {
		case "shell":
			// We only accept the default shell
			// (i.e. no command in the Payload)
			if len(req.Payload) == 0 {
				_ = req.Reply(true, nil)
			}
		case "pty-req":
			termLen := req.Payload[3]
			connCtx.EventLoop <- &ConsoleEvent{
				typeIndex: ConsoleResizeEvent, data: req.Payload[termLen+4:],
			}
			// Responding true (OK) here will let the client
			// know we have a pty ready for input
			_ = req.Reply(true, nil)
		case "window-change":
			connCtx.EventLoop <- &ConsoleEvent{
				typeIndex: ConsoleResizeEvent, data: req.Payload,
			}
		}
	}
}

func (connCtx *SshConnContext) handleSession(conn ssh.Channel) {
	sctx := connCtx.ServerContext
	user := connCtx.User
	containerName := "workspace-" + user //todo refactor
	containerTemplate, err := sctx.AppConfig.GetTemplateByUser(user)
	cleanUp := func() {
		(*connCtx.Conn).Close()
		conn.Close()
	}
	defer cleanUp()

	if err != nil {
		connCtx.logToBoth(err.Error())
		return
	}
	exists, status, containerID := ContainerExists(sctx.DockerClient, containerName)
	if sctx.AppConfig.WorkspaceData != "" && containerTemplate.EnableManager { // todo refactor
		connCtx.initManagerSocket(containerName, filepath.Join(sctx.AppConfig.WorkspaceData, user, InContainerSocketName))
	}
	if !exists {
		_containerID, fail := connCtx.handleCreateContainer(containerTemplate, containerName)
		if fail {
			return
		}
		containerID = _containerID
	}
	if status != "" {
		switch status {
		case ContainerStatusCreated, ContainerStatusPaused, ContainerStatusRunning, ContainerStatusUp:
			break
		case ContainerStatusExited:
			// Workaround from issue: https://github.com/docker/cli/issues/1891#issuecomment-581486695
			// This issue also occurs when you are using normal `docker stop` commands.
			// so let's disconnect it first.
			_ = sctx.DockerClient.NetworkDisconnect(sctx.Context, sctx.AppConfig.Network, containerID, true)
			err = sctx.DockerClient.ContainerStart(sctx.Context, containerID, container.StartOptions{})
			if err != nil {
				connCtx.logToBoth(fmt.Sprintf("Failed to start container: %v", err))
				return
			}
			break
		default:
			connCtx.logToBoth(fmt.Sprintf("Unexpected container status: %s", status))
			return
		}
	}
	connCtx.printTextLn("Redirecting to the container...")
	connCtx.redirectToContainer(containerID, containerTemplate.EnableManager, containerTemplate.Exec)
}

func (connCtx *SshConnContext) initManagerSocket(containerName string, addr string) *ManagerContext {
	mctx := ManagerContext{
		connCtx,
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

func (connCtx *SshConnContext) handleCreateContainer(containerTemplate *ContainerConfig, containerName string) (string, bool) {
	connCtx.logToBoth("Creating a new container, please wait...")
	sctx := connCtx.ServerContext
	containerID, err := CreateContainerFromTemplate(
		sctx.DockerClient,
		connCtx.User,
		filepath.Join(sctx.AppConfig.WorkspaceData, connCtx.User),
		sctx.AppConfig.GlobalShareDir,
		sctx.AppConfig.Network,
		containerTemplate,
	)
	if err != nil {
		connCtx.logToBoth(fmt.Sprintf("Failed to create container: %v", err))
		// close container
		log.Println("Removing error container..")
		err = sctx.DockerClient.ContainerRemove(connCtx.Context, containerName, container.RemoveOptions{})
		if err != nil {
			log.Println("Failed to remove container:", err)
		}
		return "", true
	}
	return containerID, false
}

func (connCtx *SshConnContext) redirectToContainer(
	containerID string,
	provideSocketEnv bool,
	cmd []string,
) {
	env := make([]string, 0)
	if provideSocketEnv {
		env = append(env, "BUBBLE_SOCK="+InContainerSocketPath)
	}
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
	execResp, err := dockerClient.ContainerExecCreate(sctx.Context, containerID, execConfig)
	if err != nil {
		connCtx.logToBoth(fmt.Sprintf("Failed to create instance! %v\n", err))
		return
	}

	hijackedResp, err := dockerClient.ContainerExecAttach(sctx.Context, execResp.ID, container.ExecStartOptions{Tty: true})
	if err != nil {
		connCtx.logToBoth(fmt.Sprintf("Failed to attach instance! %v\n", err))
		return
	}
	defer hijackedResp.Close()

	// these io.Copy are expected to close at the same time.
	go func() {
		_, _ = io.Copy(hijackedResp.Conn, *connCtx.Conn)
		_ = hijackedResp.CloseWrite()
	}()
	go func() {
		_, _ = io.Copy(*connCtx.Conn, hijackedResp.Reader)
		connCtx.EventLoop <- &ConsoleEvent{
			typeIndex: ConsolePipeBroken,
		}
	}()
	connCtx.handleEvents(execResp.ID)
}

func (connCtx *SshConnContext) handleEvents(execId string) {
	for event := range connCtx.EventLoop {
		switch event.typeIndex {
		case ConsoleResizeEvent:
			w, h := parseDims(event.data)
			err := connCtx.ServerContext.DockerClient.ContainerExecResize(connCtx.Context, execId, container.ResizeOptions{
				Height: uint(h),
				Width:  uint(w),
			})
			if err != nil {
				log.Printf("Failed to resize container: %v", err)
			}
		case ConsolePipeBroken:
			log.Printf("(%v) Connection closed, execId: %v", connCtx.User, execId)
			return // clean up connection resources.
		}
	}
}

func (connCtx *SshConnContext) logToBoth(msg string) {
	connCtx.printTextLn(msg)
	log.Println(msg)
}

func (connCtx *SshConnContext) printTextLn(text string) {
	_, _ = (*connCtx.Conn).Write([]byte(text + "\r\n"))
}
