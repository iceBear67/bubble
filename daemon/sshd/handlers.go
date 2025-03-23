package sshd

import (
	"bubble/daemon"
	"bubble/daemon/event"
	"encoding/binary"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/ssh"
	"log"
	"net"
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
	exitHandle := func() {
		err := (conn).Close()
		if err != nil {
			log.Printf("Failed to close connection: %v", err)
		}
	}
	go ssh.DiscardRequests(_requests)
	for newChannel := range channels {
		// todo support env passthru
		if newChannel.ChannelType() == "session" {
			channel, reqs, err := newChannel.Accept()
			if err != nil {
				log.Println("Failed to accept channel:", err)
				continue
			}
			connCtx.Conn = &channel
			connCtx.User = sshConn.User()
			connCtx.EventChannel = make(chan *event.ConsoleEvent, 4)
			containerId, containerTemplate, err := connCtx.prepareSession()
			if err != nil || containerId == nil {
				connCtx.logToBoth(fmt.Sprintf("Failed to handle session:", err))
				return
			}

			go connCtx.handleRequests(reqs)
			connCtx.eventLoop(exitHandle, containerTemplate, *containerId)
		}
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
	log.Printf("Preparing container for %v...", connCtx.User)
	containerId, erro := connCtx.ServerContext.PrepareContainer(
		containerName,
		connCtx.ServerContext.GetHostWorkspaceDir(connCtx.User),
		containerTemplate)
	if containerTemplate.EnableManager {
		connCtx.EventChannel <- event.NewEnableManager()
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
				connCtx.EventChannel <- event.NewExecEvent(false, nil)
			}
			termLen := req.Payload[3]
			connCtx.EventChannel <- event.NewResizeEvent(req.Payload[termLen+4:])
			_ = req.Reply(true, nil)
		case "window-change":
			connCtx.EventChannel <- event.NewResizeEvent(req.Payload)
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
			connCtx.EventChannel <- event.NewExecEvent(true, cmd)
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
	connCtx.EventChannel <- event.NewSubsystemRequest(name)
	return nil
}

type PtySession struct {
	connCtx         *SshConnContext
	containerId     string
	lastCloseHandle func()
	lastExecId      *string
}

func (connCtx *SshConnContext) eventLoop(exitHandle func(), containerTemplate *daemon.ContainerConfig, containerId string) {
	defer exitHandle()
	pty := &PtySession{
		connCtx:         connCtx,
		containerId:     containerId,
		lastCloseHandle: nil,
		lastExecId:      nil,
	}
	for evt := range connCtx.EventChannel {
		switch evt.Type() {
		case event.ClientExecEvent, event.ClientResizeEvent, event.ClientPipeBrokenEvent:
			err := pty.onPtyEvent(evt, containerTemplate)
			if err != nil {
				log.Printf("(%v) Connection closed, message: %v", connCtx.User, err)
				return
			}
		case event.ClientSubsystemRequestEvent:
			service := evt.SubsystemRequest()
			var err error
			//switch service {
			//case "sftp":
			//	log.Printf("Starting SFTP session for container %v", containerId)
			//	err = initSftp(connCtx.ServerContext.GetHostWorkspaceDir(connCtx.User), connCtx)
			//}
			if err != nil {
				log.Printf("Cannot initialize %v service for %v: %v", service, connCtx.User, err)
			}
		case event.ContainerManagerEnableEvent:
			workspaceDataDir := connCtx.ServerContext.AppConfig.WorkspaceParent
			if workspaceDataDir == "" {
				log.Printf("(%v) Error: management socket depends on the workspace volume, which isn't mounted.")
				break
			}
			connCtx.createManagerSocket(containerId,
				filepath.Join(
					connCtx.ServerContext.GetHostWorkspaceDir(connCtx.User),
					daemon.InContainerSocketName),
			)
		}
	}
}

func (ptys *PtySession) onPtyEvent(evt *event.ConsoleEvent, containerTemplate *daemon.ContainerConfig) error {
	connCtx := ptys.connCtx
	switch evt.Type() {
	case event.ClientPipeBrokenEvent:
		execId := evt.BrokenPipeEvent()
		if execId == *ptys.lastExecId {
			return fmt.Errorf("pipe is broken: %v", execId)
		} else {
			log.Printf("(%v) Pty exec switch detected.", connCtx.User)
		}
	case event.ClientExecEvent:
		silent, exec := evt.ExecEvent()
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
	case event.ClientResizeEvent:
		if ptys.lastExecId == nil {
			log.Printf("Cannot perform pty resize for %v since no pty is attached.", connCtx.User)
			return nil
		}
		w, h := evt.ResizeEvent()
		err := connCtx.ServerContext.DockerClient.ContainerExecResize(connCtx.Context, *ptys.lastExecId, container.ResizeOptions{
			Height: h,
			Width:  w,
		})
		if err != nil {
			log.Printf("Failed to resize exec session: %v", err)
		}
	}
	return nil
}
