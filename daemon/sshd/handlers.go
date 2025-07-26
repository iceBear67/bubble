package sshd

import (
	"bubble/daemon"
	"bubble/daemon/forwarder"
	"bubble/daemon/manager"
	"encoding/binary"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/ssh"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func (connCtx *SshConnContext) handleConnection(conn net.Conn, sshConfig *ssh.ServerConfig) {
	sshConn, channels, _requests, err := ssh.NewServerConn(conn, sshConfig)
	if err != nil {
		log.Println("SSH handshake failed:", err)
		return
	}
	log.Printf("New connection from %s as %s\n", sshConn.RemoteAddr(), sshConn.User())
	connCtx.ServerContext.EventChannel <- NewConnectionEstablishedEvent(connCtx)
	go connCtx.signalHandler(conn)
	exitHandle := func() {
		err := (conn).Close()
		if err != nil && !connCtx.ServerContext.shuttingDown {
			log.Printf("Failed to close connection: %v", err)
		}
		connCtx.ServerContext.EventChannel <- NewConnectionLostEvent(connCtx)
	}
	defer exitHandle()
	go ssh.DiscardRequests(_requests)
	newChannel := <-channels
	// todo support env passthru
	if newChannel.ChannelType() == "session" {
		channel, reqs, err := newChannel.Accept()
		if err != nil {
			if !connCtx.ServerContext.shuttingDown {
				log.Println("Failed to accept channel:", err)
			}
			return
		}
		connCtx.Conn = &channel
		connCtx.User = sshConn.User()
		connCtx.EventChannel = make(chan *daemon.ServerEvent, 4)
		containerId, containerTemplate, err := connCtx.prepareSession()
		if err != nil || containerId == nil {
			connCtx.logToBoth(fmt.Sprintf("Failed to handle session: %v", err))
			exitHandle()
			return
		}

		go connCtx.handleRequests(reqs)
		connCtx.eventLoop(containerTemplate, *containerId)
		return
	}
}

func (connCtx *SshConnContext) signalHandler(listener net.Conn) {
	select {
	case <-connCtx.context.Done():
		_ = listener.Close()
	}
}

func (connCtx *SshConnContext) prepareSession() (id *string, config *daemon.ContainerConfig, err error) {
	sctx := connCtx.ServerContext
	user := connCtx.User
	containerName := "workspace-" + user //todo refactor
	containerTemplate, err := sctx.AppConfig.GetTemplateByUser(user)
	if err != nil {
		log.Printf("Cannot find template for channel issued by %v: %v\n", connCtx.User, err)
		return
	}
	connCtx.logToBoth(fmt.Sprintf("Preparing container for %v...", connCtx.User))
	containerId, erro, isNew := connCtx.ServerContext.PrepareContainer(
		containerName,
		connCtx.ServerContext.GetHostWorkspaceDir(connCtx.User),
		containerTemplate)
	if isNew && containerTemplate.EnableManager {
		connCtx.EventChannel <- manager.NewEnableManagerEvent()
	}
	if erro != nil {
		erro = fmt.Errorf("error while preparing container: %v", erro)
	}
	return containerId, containerTemplate, erro
}

func (connCtx *SshConnContext) handleRequests(requests <-chan *ssh.Request) {
	hasPty := false
	for req := range requests {
		switch req.Type {
		case "shell":
			if len(req.Payload) == 0 {
				_ = req.Reply(true, nil)
			}
		case "pty-req":
			if !hasPty {
				hasPty = true // then create for it!
				connCtx.EventChannel <- NewExecEvent(false, nil)
			}
			termLen := req.Payload[3]
			connCtx.EventChannel <- NewResizeEvent(req.Payload[termLen+4:])
			_ = req.Reply(true, nil)
		case "window-change":
			connCtx.EventChannel <- NewResizeEvent(req.Payload)
		case "subsystem":
			_ = req.Reply(true, nil)
			err := connCtx.handleSubsystemRequest(req)
			if err != nil {
				log.Println("Failed to handle subsystem request:", err)
			}
		case "exec":
			command_len := binary.BigEndian.Uint32(req.Payload[0:4])
			if int(command_len) > len(req.Payload) || command_len > 1024 {
				log.Printf("Illegal packet length found from user %v, conn %v", connCtx.User, connCtx.Conn)
				return
			}
			cmd_s := string(req.Payload[4 : 4+command_len])
			cmd := strings.Split(cmd_s, " ")
			connCtx.EventChannel <- NewExecEvent(true, cmd)
		default:
			log.Printf("(%v) Unknown request type: %v", connCtx.User, req.Type)
			_ = req.Reply(false, nil)
		}
	}
}

func (connCtx *SshConnContext) handleSubsystemRequest(req *ssh.Request) error {
	nameLen := binary.BigEndian.Uint32(req.Payload[0:4])
	if nameLen > 32 {
		return fmt.Errorf("illegal packet length found from user %v, conn %v", connCtx.User, connCtx.Conn)
	}
	name := string(req.Payload[4 : 4+nameLen])
	connCtx.EventChannel <- NewSubsystemRequest(name)
	return nil
}

type PtySession struct {
	connCtx         *SshConnContext
	containerId     string
	lastCloseHandle func()
	lastExecId      *string
}

func (connCtx *SshConnContext) eventLoop(containerTemplate *daemon.ContainerConfig, containerId string) {
	pty := &PtySession{
		connCtx:         connCtx,
		containerId:     containerId,
		lastCloseHandle: nil,
		lastExecId:      nil,
	}
	for evt := range connCtx.EventChannel {
		switch evt.Type() {
		case ClientExecEvent, ClientResizeEvent, ClientPipeBrokenEvent:
			err := pty.onPtyEvent(evt, containerTemplate)
			if err != nil {
				if !connCtx.ServerContext.shuttingDown {
					log.Printf("(%v) Connection closed, message: %v", connCtx.User, err)
				}
				return
			}
		case ClientSubsystemRequestEvent:
			service := SubsystemRequest(evt)
			log.Printf("(%v) Received a subsystem request for %v, but unsupported yet :(", connCtx.User, service)
		case manager.ContainerManagerEnableEvent:
			workspaceDataDir := connCtx.ServerContext.AppConfig.WorkspaceParent
			if workspaceDataDir == "" {
				log.Printf("(%v) Error: management socket depends on the workspace volume, which isn't mounted.", connCtx.User)
				break
			}
			// remove the previous socket.
			socketPath := filepath.Join(
				connCtx.ServerContext.GetHostWorkspaceDir(connCtx.User),
				manager.InContainerSocketName)
			_ = os.Remove(socketPath)
			mctx := connCtx.createManagerSocket(containerId, socketPath)
			forwarderCfg := containerTemplate.PortForwarding
			if forwarderCfg != nil {
				mctx.AllowPortForwarding(&forwarder.PortForwarderConfig{
					AllowLowest:  forwarderCfg.MinPort,
					AllowHighest: forwarderCfg.MaxPort,
					EventChan:    connCtx.EventChannel,
				})
			}
		case manager.ManagerSocketCloseEvent, manager.ManagerSocketOpenEvent:
			// forward to upstream.
			connCtx.ServerContext.EventChannel <- evt
		case forwarder.PortForwardRequestEvent:
			connCtx.ServerContext.EventChannel <- evt
		}
	}
}

func (ptys *PtySession) onPtyEvent(evt *daemon.ServerEvent, containerTemplate *daemon.ContainerConfig) error {
	connCtx := ptys.connCtx
	switch evt.Type() {
	case ClientPipeBrokenEvent:
		execId := BrokenPipeEvent(evt)
		if execId == *ptys.lastExecId {
			return fmt.Errorf("pipe is broken: %v", execId)
		} else {
			log.Printf("(%v) Pty exec switch detected.", connCtx.User)
		}
	case ClientExecEvent:
		silent, exec := ExecEvent(evt)
		if !silent {
			connCtx.PrintTextLn("Redirecting to the container...")
		}
		if exec == nil {
			exec = containerTemplate.Exec
		}
		ptys.lastExecId = nil // avoid closing connection
		if ptys.lastCloseHandle != nil {
			ptys.lastCloseHandle()
		}
		closeHandle, execId, err := connCtx.RedirectToContainer(ptys.containerId, exec)
		if err != nil {
			connCtx.logToBoth(fmt.Sprintf("(%v) Failed to redirect to container: %v", ptys.containerId, err))
			return err
		}
		ptys.lastExecId = execId
		ptys.lastCloseHandle = closeHandle
	case ClientResizeEvent:
		if ptys.lastExecId == nil {
			log.Printf("Cannot perform pty resize for %v since no pty is attached.", connCtx.User)
			return nil
		}
		w, h := ResizeEvent(evt)
		err := connCtx.ServerContext.DockerClient.ContainerExecResize(connCtx.context, *ptys.lastExecId, container.ResizeOptions{
			Height: h,
			Width:  w,
		})
		if err != nil {
			log.Printf("Failed to resize exec session: %v", err)
		}
	}
	return nil
}
