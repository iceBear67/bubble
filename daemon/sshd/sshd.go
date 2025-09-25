package sshd

import (
	"bubble/daemon"
	"bubble/daemon/forwarder"
	"bubble/daemon/manager"
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/werbenhu/eventbus"
	"golang.org/x/crypto/ssh"
)

type SshServerContext struct {
	context      context.Context
	wg           *sync.WaitGroup
	shuttingDown bool
	cancel       func()
	serverConfig *ssh.ServerConfig
	DockerClient *client.Client
	AppConfig    *daemon.Config
	EventBus     *eventbus.EventBus
}

func CreateSshServer(parent context.Context, client *client.Client, config *daemon.Config) *SshServerContext {
	privateKey := loadPrivateKey(config.ServerKey)
	sshConfig := setupSSHConfig(privateKey, config)
	ctx, cancel := context.WithCancel(parent)
	sctx := SshServerContext{
		DockerClient: client,
		AppConfig:    config,
		EventBus:     eventbus.New(),
		cancel:       cancel,
		context:      ctx,
		wg:           &sync.WaitGroup{},
		shuttingDown: false,
		serverConfig: sshConfig,
	}
	return &sctx
}

func (sctx *SshServerContext) Serve(address string) {
	sshConfig := sctx.serverConfig
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to listen on address %s: %v", address, err)
	}
	log.Printf("Listening on %s...\n", address)
	go sctx.signalListener(listener)
	go sctx.eventHandler()
	_, err = manager.StartManagementServer(
		sctx.DockerClient,
		sctx.AppConfig.Manager,
		sctx.EventBus,
		sctx.context)
	if err != nil {
		log.Fatalf("Failed to start manager server: %v", err)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			if sctx.shuttingDown {
				return
			}
			log.Println("Failed to accept connection:", err)
			continue
		}
		connCtx := &SshConnContext{
			ServerContext: sctx,
			context:       sctx.context,
			Conn:          nil,
			EventBus:      eventbus.New(),
		}
		go connCtx.handleConnection(conn, sshConfig)
	}
}

func (sctx *SshServerContext) signalListener(listener net.Listener) {
	ctx := sctx.context
	select {
	case <-ctx.Done():
		log.Println("Shutting down ssh server...")
		sctx.shuttingDown = true
		_ = listener.Close()
	}
}

func (sctx *SshServerContext) eventHandler() {
	openedPorts := make(map[int]*struct{})
	err := sctx.EventBus.Subscribe(ConnectionEstablishedEvent, func(_ string, _ *daemon.ServerEvent) {
		sctx.wg.Add(1)
	})
	if err != nil {
		panic(err)
	}
	err = sctx.EventBus.Subscribe(ConnectionCloseEvent, func(_ string, _ *daemon.ServerEvent) {
		sctx.wg.Add(-1)
	})
	if err != nil {
		panic(err)
	}
	err = sctx.EventBus.Subscribe(forwarder.PortForwardRequestEvent, func(_ string, ev *daemon.ServerEvent) {
		from, to, dst := forwarder.ForwardRequest(ev)
		log.Println("Received port forwarding request from ", dst, ": (host)", from, " -> (guest)", to)
		if _, ok := openedPorts[from]; ok {
			log.Println("Port conflict: ", from, " -> ", to)
			return
		}
		openedPorts[from] = &struct{}{}
		go forwarder.PortForward(sctx.context, dst, from, to)
	})
	if err != nil {
		panic(err)
	}
}

func (sctx *SshServerContext) StopSshServer() {
	sctx.cancel()
	sctx.wg.Wait()
}

func (sctx *SshServerContext) GetHostWorkspaceDir(user string) string {
	return filepath.Join(sctx.AppConfig.WorkspaceParent, user)
}

func loadPrivateKey(path string) ssh.Signer {
	privateBytes, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("Failed to load private key: %v", err)
	}
	private, err := ssh.ParsePrivateKey(privateBytes)
	if err != nil {
		log.Fatalf("Failed to parse private key: %v", err)
	}
	return private
}

func setupSSHConfig(private ssh.Signer, config *daemon.Config) *ssh.ServerConfig {
	namedKeys := make(map[string][]ssh.PublicKey)
	for k, v := range config.Keys {
		keys := make([]ssh.PublicKey, 0)
		for i := range v {
			result, _, _, _, err := ssh.ParseAuthorizedKey([]byte(v[i]))
			if err != nil {
				log.Printf("Failed to parse public key: %v", err)
				continue
			}
			keys = append(keys, result)
		}
		namedKeys[k] = keys
	}
	var sshConfig *ssh.ServerConfig
	if len(namedKeys) != 0 {
		sshConfig = &ssh.ServerConfig{PublicKeyCallback: func(conn ssh.ConnMetadata, incomingKey ssh.PublicKey) (*ssh.Permissions, error) {
			if incomingKey == nil {
				return nil, fmt.Errorf("unauthorized: key not present")
			}
			for name, allowedKeys := range namedKeys {
				for i := range allowedKeys {
					key := allowedKeys[i]
					if key == nil {
						println("key is null")
						continue
					}
					if bytes.Equal(key.Marshal(), incomingKey.Marshal()) {
						access, exists := config.AccessControl[name]
						if !exists {
							return nil, fmt.Errorf("unauthorized: acl not set")
						}
						if access.CanAccess(conn.User()) {
							return nil, nil
						}
						return nil, fmt.Errorf("unauthorized: access not granted")
					}
				}
			}
			return nil, fmt.Errorf("unauthorized: incomingKey not enrolled.")
		}}
	} else {
		log.Println("NO CLIENT AUTH IS ENABLED! YOU SHALL ONLY USE THIS IN TEST ENVIRONMENT.")
		sshConfig = &ssh.ServerConfig{
			NoClientAuth: true,
		}
	}
	sshConfig.AddHostKey(private)

	return sshConfig
}

func (sctx *SshServerContext) PrepareContainer(containerName string, workspaceDir string, containerTemplate *daemon.ContainerConfig) (*string, error, bool) {
	dockerClient := sctx.DockerClient
	exists, status, containerID := daemon.ContainerExists(dockerClient, containerName)
	isNew := false
	if !exists {
		_containerID, err := daemon.CreateContainerFromTemplate(
			dockerClient,
			containerName,
			workspaceDir,
			sctx.AppConfig.GlobalShareDir,
			sctx.AppConfig.Network,
			sctx.AppConfig.Runtime,
			containerTemplate,
		)
		if err != nil {
			log.Println("Failed to create container: ", err)
			_ = sctx.DockerClient.ContainerRemove(sctx.context, containerName, container.RemoveOptions{})
			return nil, fmt.Errorf("failed to create container: %v", err), false
		}
		containerID = _containerID
		isNew = true
	}
	if status != "" {
		switch status {
		case daemon.ContainerStatusCreated, daemon.ContainerStatusPaused, daemon.ContainerStatusRunning, daemon.ContainerStatusUp:
			break
		case daemon.ContainerStatusExited:
			// Workaround from issue: https://github.com/docker/cli/issues/1891#issuecomment-581486695
			// This issue also occurs when you are using normal `docker stop` commands.
			// so let's disconnect it first.
			_ = sctx.DockerClient.NetworkDisconnect(sctx.context, sctx.AppConfig.Network, containerID, true)
			err := sctx.DockerClient.ContainerStart(sctx.context, containerID, container.StartOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to start container: %v", err), false
			}
			break
		default:
			return nil, fmt.Errorf("unexpected container status: %v", status), false
		}
	}
	return &containerID, nil, isNew
}
