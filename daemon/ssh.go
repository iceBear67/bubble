package daemon

import (
	"bytes"
	"context"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
)

type SshServerContext struct {
	DockerClient *client.Client
	AppConfig    *Config
	Context      context.Context
	Conn         *net.Conn
}

func StartSshServer(client *client.Client, config *Config) {
	allowedKeys := parseAuthorizedKeys(config.Keys)
	setupNetworkGroup(client, config.Network)
	privateKey := loadPrivateKey(config.ServerKey)
	sshConfig := setupSSHConfig(privateKey, &allowedKeys)
	sctx := SshServerContext{
		DockerClient: client,
		AppConfig:    config,
		Context:      context.Background(),
	}
	sctx.startSSHServer(sshConfig, config.Address)
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

func (sctx *SshServerContext) startSSHServer(sshConfig *ssh.ServerConfig, address string) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to listen on address %s: %v", address, err)
	}
	log.Printf("Listening on %s...\n", address)
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Failed to accept connection:", err)
			continue
		}
		sctx.Conn = &conn
		go sctx.handleConnection(conn, sshConfig)
	}
}
func (sctx *SshServerContext) handleConnection(conn net.Conn, sshConfig *ssh.ServerConfig) {
	sshConn, channels, _, err := ssh.NewServerConn(conn, sshConfig)
	if err != nil {
		log.Println("SSH handshake failed:", err)
		return
	}
	log.Printf("New connection from %s as %s\n", sshConn.RemoteAddr(), sshConn.User())
	for newChannel := range channels {
		if newChannel.ChannelType() == "session" {
			channel, _, err := newChannel.Accept()
			if err != nil {
				log.Println("Failed to accept channel:", err)
				continue
			}
			go sctx.handleSession(channel, sshConn.User())
		}
	}
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

func (sctx *SshServerContext) handleSession(nc io.ReadWriteCloser, user string) {
	ctx := sctx.Context
	containerName := "workspace-" + user
	containerTemplate, err := sctx.AppConfig.GetTemplateByUser(user)
	defer nc.Close()
	if err != nil {
		logToBoth(&nc, err.Error())
		return
	}
	exists, status, containerID := ContainerExists(sctx.DockerClient, containerName)
	if sctx.AppConfig.WorkspaceData != "" { // todo refactor
		sctx.initManagerSocket(containerName, filepath.Join(sctx.AppConfig.WorkspaceData, user, InContainerSocketName))
	}
	if !exists {
		_containerID, fail := sctx.handleCreateContainer(nc, user, containerID, err, containerTemplate, ctx, containerName)
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
				logToBoth(&nc, fmt.Sprintf("Failed to start container: %v", err))
				return
			}
			break
		default:
			logToBoth(&nc, fmt.Sprintf("Unexpected container status: %s", status))
			return
		}
	}
	printTextLn(&nc, "Redirecting to container...")
	sctx.redirectToContainer(nc, ctx, containerID, containerTemplate.Exec)
}

func (sctx *SshServerContext) initManagerSocket(containerName string, addr string) *ManagerContext {
	mctx := ManagerContext{
		sctx,
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

func (sctx *SshServerContext) handleCreateContainer(nc io.ReadWriteCloser, user string, containerID string, err error, containerTemplate *ContainerConfig, ctx context.Context, containerName string) (string, bool) {
	logToBoth(&nc, "Creating new container, please wait...")
	containerID, err = sctx.createContainerFromTemplate(user, containerTemplate)
	if err != nil {
		logToBoth(&nc, fmt.Sprintf("Failed to create container: %v", err))
		_ = nc.Close()
		// close container
		log.Println("Removing error container..")
		err = sctx.DockerClient.ContainerRemove(ctx, containerName, container.RemoveOptions{})
		if err != nil {
			log.Println("Failed to remove container:", err)
		}
		return "", true
	}
	return containerID, false
}

func (sctx *SshServerContext) createContainerFromTemplate(user string, template *ContainerConfig) (id string, erro error) {
	appConfig := sctx.AppConfig
	workspaceDir := filepath.Join(appConfig.WorkspaceData, user)
	return CreateContainer(
		sctx.DockerClient,
		user,
		workspaceDir,
		appConfig.Network,
		template,
	)
}

func (sctx *SshServerContext) redirectToContainer(
	nc io.ReadWriteCloser,
	ctx context.Context,
	containerID string,
	cmd []string,
) {
	execConfig := container.ExecOptions{
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	}
	dockerClient := sctx.DockerClient
	execResp, err := dockerClient.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		logToBoth(&nc, fmt.Sprintf("Failed to create instance! %v\n", err))
		return
	}

	hijackedResp, err := dockerClient.ContainerExecAttach(ctx, execResp.ID, container.ExecStartOptions{Tty: true})
	if err != nil {
		logToBoth(&nc, fmt.Sprintf("Failed to attach instance! %v\n", err))
		return
	}
	defer hijackedResp.Close()

	go func() {
		_, _ = io.Copy(hijackedResp.Conn, nc)
		_ = hijackedResp.CloseWrite()
	}()

	_, _ = io.Copy(nc, hijackedResp.Reader) // these io.Copy are expected to close at the same time.
}

func logToBoth(nc *io.ReadWriteCloser, msg string) {
	printTextLn(nc, msg)
	log.Println(msg)
}

func printTextLn(nc *io.ReadWriteCloser, text string) {
	_, _ = (*nc).Write([]byte(text + "\r\n"))
}
