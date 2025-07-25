package main

import (
	"bubble/daemon"
	"bubble/daemon/sshd"
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
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
	go signalHandler(sshs, sigChan)
	handleCommand()
}

func handleCommand() {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		scanner.Scan()
		prompt := scanner.Text()
		if err := scanner.Err(); err != nil {
			log.Printf("Error reading input: %v", err)
			continue
		}
		switch prompt {
		case "stop":
			pid := os.Getpid()
			_ = syscall.Kill(pid, syscall.SIGTERM)
			log.Println("Signal sent!")
		}
	}
}

func signalHandler(sshd *sshd.SshServerContext, sigChan chan os.Signal) {
	sign := <-sigChan
	switch sign {
	case syscall.SIGINT, syscall.SIGTERM:
		log.Println("Shutting down...")
		go func() {
			time.Sleep(8 * time.Second)
			// The program isn't terminated.
			_ = sshd
			fmt.Println("Program isn't stopped!")
		}()
		sshd.StopSshServer()
		os.Exit(0)
	default:
		log.Println("Unknown signal ", sign.String())
	}

}
