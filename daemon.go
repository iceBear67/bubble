package main

import (
	"bubble/daemon"
	"bubble/daemon/sshd"
	"flag"
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

	go func() {
		sshd.StartSshServer(dockerClient, config)
	}()
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	signalHandler(sigChan)
}

func signalHandler(sigChan chan os.Signal) {
	sign := <-sigChan
	switch sign {
	case syscall.SIGINT, syscall.SIGTERM:
		managers := daemon.GetRunningManagers()
		managers.Range(func(key, value interface{}) bool {
			ctx := value.(*daemon.ManagerContext)
			if !ctx.IsShuttingDown() {
				log.Println("Removing manager socket from ", key.(string))
				ctx.ShutdownGracefully()
			}
			return true
		})
		for {
			if !daemon.HasRunningManager() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

}
