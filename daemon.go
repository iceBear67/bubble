package main

import (
	"bubble/daemon"
	"bubble/daemon/sshd"
	"flag"
	"fmt"
	"log"
	"syscall"
)

func main() {
	configPath := flag.String("config", "config.yml", "Path to config file")
	needHelp := flag.Bool("help", false, "Show help")
	flag.Parse()
	if *needHelp {
		flag.Usage()
		return
	}
	config, err := daemon.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to open config file: %v", err)
	}

	dockerClient, err := daemon.SetupDockerClient()
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}
	if config.WorkspaceData != "" {
		log.Printf("Chroot-ing to workspace data dir")
		err := syscall.Chroot(config.WorkspaceData)
		if err != nil {
			log.Fatalf("Failed to chroot: %v", err)
		}
	}

	go func() {
		sshd.StartSshServer(dockerClient, config)
	}()
	for {
		_, _ = fmt.Scanln()
	}
}
