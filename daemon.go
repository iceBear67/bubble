package main

import (
	"bubble/daemon"
	"bubble/daemon/sshd"
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
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
	daemon.SetupNetworkGroup(dockerClient, config.Network)
	ctx := context.Background()
	sshs := sshd.CreateSshServer(ctx, dockerClient, config)

	sigChan := make(chan os.Signal, 1)
	go sshs.Serve(config.Address)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	signalHandler(sshs, sigChan)
}

func signalHandler(sshd *sshd.SshServerContext, sigChan chan os.Signal) {
	sign := <-sigChan
	switch sign {
	case syscall.SIGINT, syscall.SIGTERM:
		log.Println("Shutting down...")
		sshd.StopSshServer()
	default:
		log.Println("Unknown signal ", sign.String())
	}

}
