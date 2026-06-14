package sshd

import (
	"bubble/daemon"
	"bubble/daemon/manager"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
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
		ctx, cancel := context.WithCancel(sctx.context)
		connCtx := &SshConnContext{
			ServerContext: sctx,
			context:       ctx,
			cancel:        cancel,
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

type pkcb = func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error)

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
	var sshConfig = &ssh.ServerConfig{}

	var callbacks = make([]pkcb, 0)
	if len(namedKeys) != 0 {
		callbacks = append(callbacks, authConfiguredKeypair(namedKeys))
	}
	if config.AuthServer != "" {
		callbacks = append(callbacks, authRemoteConfiguredKeypair(config.AuthServer))
	}

	finalPkcb := func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if key == nil {
			return nil, fmt.Errorf("unauthorized: key not present")
		}
		var finalErr error = nil
		for _, callback := range callbacks {
			perm, err := callback(conn, key)
			if err == nil {
				return perm, nil
			}
			if finalErr == nil {
				finalErr = err
			} else {
				finalErr = errors.Join(finalErr, err)
			}
		}
		return nil, finalErr
	}

	sshConfig.PublicKeyCallback = finalPkcb

	if len(callbacks) == 0 {
		log.Println("NO CLIENT AUTH IS ENABLED! YOU SHALL ONLY USE THIS IN TEST ENVIRONMENT.")
		sshConfig = &ssh.ServerConfig{
			NoClientAuth: true,
		}
	}
	sshConfig.AddHostKey(private)

	return sshConfig
}

func authRemoteConfiguredKeypair(authServer string) pkcb {
	return func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		payload, err := json.Marshal(map[string]string{
			"key":  base64.StdEncoding.EncodeToString(key.Marshal()),
			"user": conn.User(),
		})
		if err != nil {
			return nil, err
		}
		_resp, err := http.Post(authServer, "application/json", bytes.NewBuffer(payload))
		if err != nil {
			return nil, err
		}
		defer _resp.Body.Close()
		if _resp.StatusCode != 200 {
			return nil, fmt.Errorf("auth server returned non-200 status code: %d", _resp.StatusCode)
		}
		body, err := io.ReadAll(_resp.Body)
		if err != nil {
			return nil, err
		}
		var resp struct {
			User string `json:"user"`
		}
		err = json.Unmarshal(body, &resp)
		if err != nil {
			return nil, err
		}
		if resp.User == "" {
			log.Println("auth server returned empty user for key", base64.StdEncoding.EncodeToString(key.Marshal()))
			return nil, fmt.Errorf("auth server returned empty user")
		}
		return &ssh.Permissions{
			Extensions: map[string]string{
				"user": resp.User,
			},
		}, nil
	}
}

func authConfiguredKeypair(namedKeys map[string][]ssh.PublicKey) pkcb {
	return func(conn ssh.ConnMetadata, incomingKey ssh.PublicKey) (*ssh.Permissions, error) {
		for name, allowedKeys := range namedKeys {
			for i := range allowedKeys {
				key := allowedKeys[i]
				if key == nil {
					println("key is null")
					continue
				}
				if bytes.Equal(key.Marshal(), incomingKey.Marshal()) {
					return &ssh.Permissions{
						Extensions: map[string]string{
							"user": name,
						},
					}, nil
				}
			}
		}
		return nil, fmt.Errorf("unauthorized: incomingKey not enrolled")
	}
}

func (sctx *SshServerContext) PrepareContainer(containerName string, workspaceDir string, labels map[string]string, containerTemplate *daemon.ContainerConfig) (*string, error, bool) {
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
			labels,
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
