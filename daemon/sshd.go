package daemon

import (
	"bytes"
	"context"
	"fmt"
	"github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"
	"log"
	"net"
	"os"
)

type SshServerContext struct {
	DockerClient *client.Client
	AppConfig    *Config
	Context      context.Context
}

const (
	ConsoleResizeEvent = 114514
)

type ConsoleEvent struct {
	typeIndex int
	data      []byte
}

type SshConnContext struct {
	ServerContext *SshServerContext
	Context       context.Context
	User          string
	Conn          *ssh.Channel
	EventLoop     chan *ConsoleEvent
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
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Failed to accept connection:", err)
			continue
		}
		connCtx := &SshConnContext{
			ServerContext: sctx,
			Conn:          nil,
		}
		go connCtx.handleConnection(conn, sshConfig)
	}
}
