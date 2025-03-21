package main

import (
	"bubble/daemon"
	"log"
	"net"
	"os"
)

func main() {
	c, err := net.Dial("unix", daemon.InContainerSocketPath)
	if err != nil {
		log.Println("Can't connect to daemon: ", err)
		return
	}
	defer c.Close()
	if len(os.Args) != 2 {
		log.Printf("Usage: %s <destroy|stop>\n", os.Args[0])
		return
	}
	cmd := os.Args[1]
	switch cmd {
	case "destroy":
		_, err = c.Write(daemon.SignalDestroyContainer)
	case "stop":
		log.Println("Reconnect using the same user name for a restart.")
		_, err = c.Write(daemon.SignalStopContainer)
	} //todo suuuupppppoooorrrrtttt restart without disconnection
	if err != nil {
		log.Fatalf("Signal did not sent successfully: %v", err)
	} else {
		log.Println("Signal sent, action in progress.")
	}
}
