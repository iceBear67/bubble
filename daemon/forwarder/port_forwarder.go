package forwarder

import (
	"bubble/daemon"
	"context"
	"encoding/binary"
	"fmt"
	"log"

	"io"
	"net"
)

const (
	PortForwardRequestEvent = "PortForwardRequestEvent"
)

func NewForwardRequest(fromPort int, toPort int, dst string) *daemon.ServerEvent {
	buf := make([]byte, 0)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(fromPort))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(toPort))
	buf = append(buf, []byte(dst)...)
	return daemon.CreateEventRaw(PortForwardRequestEvent, 0, buf)
}

func ForwardRequest(event *daemon.ServerEvent) (from int, to int, dst string) {
	data := event.DataRaw().([]byte)
	from = int(binary.LittleEndian.Uint16(data[:2]))
	to = int(binary.LittleEndian.Uint16(data[2:4]))
	dst = string(data[4:])
	return from, to, dst
}

type PortForwarderConfig struct {
	AllowLowest  int
	AllowHighest int
	EventChan    chan *daemon.ServerEvent
}

func (cfg *PortForwarderConfig) Start(from int, to int, dst string) {
	cfg.EventChan <- NewForwardRequest(from, to, dst)
}

func PortForward(ctx context.Context, dst string, port int, toPort int) {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Println("port forwarder failed to listen: ", err)
		return
	}
	closed := false
	defer l.Close()
	go func() {
		select {
		case <-ctx.Done():
			closed = true
			_ = l.Close()
		}
	}()
	log.Println("Port forwarder listening on: ", port, " to ", dst, ":", toPort)
	for {
		conn, err := l.Accept()
		if err != nil {
			log.Println("port forwarder failed to accept: ", err)
			if closed {
				return
			}
			continue
		}
		go func() {
			defer conn.Close()
			to, err := net.Dial("tcp", fmt.Sprintf("%s:%d", dst, toPort))
			if err != nil {
				log.Println("port forwarder failed to connect: ", err)
				return
			}
			defer to.Close()
			go func() {
				io.Copy(conn, to)
			}()
			io.Copy(to, conn)
		}()
	}
}
