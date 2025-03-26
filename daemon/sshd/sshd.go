package sshd

import (
	"bubble/daemon"
	"bubble/daemon/manager"
	"bytes"
	"context"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
)

type SshServerContext struct {
	context      context.Context
	wg           *sync.WaitGroup
	shuttingDown bool
	DockerClient *client.Client
	AppConfig    *daemon.Config
	EventChannel chan *daemon.ServerEvent
}

func StartSshServer(waitGroup *sync.WaitGroup, context context.Context, client *client.Client, config *daemon.Config) {
	allowedKeys := parseAuthorizedKeys(config.Keys)
	daemon.SetupNetworkGroup(client, config.Network)
	privateKey := loadPrivateKey(config.ServerKey)
	sshConfig := setupSSHConfig(privateKey, &allowedKeys)
	sctx := SshServerContext{
		DockerClient: client,
		AppConfig:    config,
		EventChannel: make(chan *daemon.ServerEvent, 4),
		context:      context,
		wg:           waitGroup,
		shuttingDown: false,
	}
	sctx.startSSHServer(sshConfig, config.Address)
}

func (sctx *SshServerContext) GetHostWorkspaceDir(user string) string {
	return filepath.Join(sctx.AppConfig.WorkspaceParent, user)
}

func parseAuthorizedKeys(keys []string) []ssh.PublicKey {
	allowedKeys := make([]ssh.PublicKey, 0)
	for _, key := range keys {
		result, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
		if err != nil {
			log.Printf("Failed to parse public key: %v", err)
			continue
		}
		allowedKeys = append(allowedKeys, result)
	}
	return allowedKeys
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

func setupSSHConfig(private ssh.Signer, authorizedKeys *[]ssh.PublicKey) *ssh.ServerConfig {
	var sshConfig *ssh.ServerConfig
	if len(*authorizedKeys) != 0 {
		sshConfig = &ssh.ServerConfig{PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return publicKeyAuth(authorizedKeys, key)
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

func publicKeyAuth(authorizedKeys *[]ssh.PublicKey, key ssh.PublicKey) (*ssh.Permissions, error) {
	if len(*authorizedKeys) == 0 {
		return nil, nil
	}
	for _, allowedKey := range *authorizedKeys {
		if bytes.Equal(allowedKey.Marshal(), key.Marshal()) {
			return nil, nil
		}
	}
	return nil, fmt.Errorf("unauthorized")
}

func (sctx *SshServerContext) startSSHServer(sshConfig *ssh.ServerConfig, address string) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to listen on address %s: %v", address, err)
	}
	log.Printf("Listening on %s...\n", address)
	go sctx.signalListener(listener)
	go sctx.channelListener()
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
		}
		go connCtx.handleConnection(conn, sshConfig)
	}
}

func (sctx *SshServerContext) signalListener(listener net.Listener) {
	select {
	case <-sctx.context.Done():
		sctx.shuttingDown = true
		_ = listener.Close()
		sctx.wg.Wait()
		close(sctx.EventChannel)
		return
	}
}

func (sctx *SshServerContext) channelListener() {
	for event := range sctx.EventChannel {
		switch event.Type() {
		case ConnectionEstablishedEvent, manager.ManagerSocketOpenEvent:
			sctx.wg.Add(1)
		case ConnectionCloseEvent, manager.ManagerSocketCloseEvent:
			sctx.wg.Add(-1)
		}
	}
}

func (sctx *SshServerContext) PrepareContainer(containerName string, workspaceDir string, containerTemplate *daemon.ContainerConfig) (*string, error) {
	dockerClient := sctx.DockerClient
	exists, status, containerID := daemon.ContainerExists(dockerClient, containerName)
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
			return nil, fmt.Errorf("failed to create container: %v", err)
		}
		containerID = _containerID
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
				return nil, fmt.Errorf("failed to start container: %v", err)
			}
			break
		default:
			return nil, fmt.Errorf("unexpected container status: %v", status)
		}
	}
	return &containerID, nil
}
