package main

import (
	"bubble/daemon"
	"bubble/daemon/sshd"
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
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
	ctx, cancel := context.WithCancel(context.Background())

	waitGroup := &sync.WaitGroup{}
	go sshd.StartSshServer(waitGroup, ctx, dockerClient, config)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	signalHandler(waitGroup, cancel, sigChan)
}

func signalHandler(wg *sync.WaitGroup, cancel func(), sigChan chan os.Signal) {
	sign := <-sigChan
	switch sign {
	case syscall.SIGINT, syscall.SIGTERM:
		log.Println("Shutting down...")
		cancel()
		wg.Wait()
		return
	default:
		log.Println("Unknown signal ", sign.String())
	}

}
