package sshd

import (
	"github.com/pkg/sftp"
	"log"
)

func initSftp(cwd string, ctx *SshConnContext) error {

	server, err := sftp.NewServer(*ctx.Conn, sftp.WithServerWorkingDirectory(cwd))

	if err != nil {
		log.Printf("(%v) SFTP server error: %v", ctx.User, err)
		return err
	} else {
		go func() {
			err := server.Serve()
			if err != nil {
				log.Printf("(%v) SFTP server error: %v", ctx.User, err)
			}
		}()
	}
	return nil
}
