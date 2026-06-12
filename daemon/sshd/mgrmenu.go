package sshd

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"golang.org/x/crypto/ssh"
)

type sshTTY struct {
	io.Reader
	io.Writer
	windowSize tcell.WindowSize
}

func (connCtx *SshConnContext) initMenu(reqs chan *ssh.Request) error {
	conn := *connCtx.Conn
	var windowSize tcell.WindowSize
	var term string
	var reqToReplay = make([]*ssh.Request, 0)
	for req := range reqs {
		reqToReplay = append(reqToReplay, req)
		if req.Type != "pty-req" {
			continue
		}
		n := binary.BigEndian.Uint32(req.Payload)
		start := 4
		end := start + int(n)
		term = string(req.Payload[start:end])

		windowSize = tcell.WindowSize{
			Width:       int(binary.BigEndian.Uint32(req.Payload[end : end+4])),
			Height:      int(binary.BigEndian.Uint32(req.Payload[end+4 : end+8])),
			PixelWidth:  int(binary.BigEndian.Uint32(req.Payload[end+8 : end+12])),
			PixelHeight: int(binary.BigEndian.Uint32(req.Payload[end+12 : end+16])),
		}
		break
	}
	tty := sshTTY{
		conn, conn, windowSize,
	}
	ti, err := tcell.LookupTerminfo(term)
	if err != nil {
		return err
	}
	screen, err := tcell.NewTerminfoScreenFromTtyTerminfo(tty, ti)
	if err != nil {
		return err
	}
	app := tview.NewApplication()
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	box := connCtx.prepareMenu(app)
	// https://github.com/rivo/tview/wiki/Modal
	// Returns a new primitive which puts the provided primitive in the center and
	// sets its size to the given width and height.
	modal := func(p tview.Primitive, width, height int) tview.Primitive {
		return tview.NewFlex().
			AddItem(nil, 0, 1, false).
			AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
				AddItem(nil, 0, 1, false).
				AddItem(p, height, 1, true).
				AddItem(nil, 0, 1, false), width, 1, true).
			AddItem(nil, 0, 1, false)
	}

	app.SetScreen(screen).SetRoot(modal(box, int(float32(windowSize.Width)*0.75), int(float32(windowSize.Height)*0.75)), true)
	connCtx.User = ""
	if err := app.Run(); err != nil {
		return err
	}
	if connCtx.User == "" {
		return errors.New("user quit selection")
	}
	go func() {
		for _, request := range reqToReplay {
			reqs <- request
		}
	}()
	return nil
}

func (connCtx *SshConnContext) prepareMenu(app *tview.Application) tview.Primitive {
	providedUser := connCtx.User
	containers, err := connCtx.ServerContext.DockerClient.ContainerList(connCtx.context, container.ListOptions{All: true})
	if err != nil {
		log.Println(err)
		return tview.NewTextView().SetText("This service is currently unavailable, please try again :(").SetTextColor(tcell.ColorRed)
	}
	l := tview.NewList().
		AddItem("Quit", "Do nothing.", 'q', func() {
			app.Stop()
		}).
		AddItem("Create \""+connCtx.User+"\"", "Create a new bubble named "+connCtx.User, 'c', func() {
			connCtx.User = providedUser
			app.Stop()
		})
	for i, summary := range containers {
		if user, ok := summary.Labels["bubble_user"]; ok {
			if aclUser, ok := summary.Labels["bubble_acl_user"]; ok {
				if aclUser != connCtx.ACLUser {
					continue
				}
			}
			description := "Created at " + time.UnixMilli(summary.Created).String() + ", " + summary.Status
			l.AddItem("Enter "+user+" ("+summary.Image+") instead", description, rune(i), func() {
				connCtx.User = user
				app.Stop()
			})
		}
	}
	l.SetSelectedBackgroundColor(tcell.ColorDefault).SetSelectedTextColor(tcell.ColorDodgerBlue)
	return tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(tview.NewTextView().
			SetText("Container for "+connCtx.User+" doesn't exist. May you want to create one?").
			SetTextAlign(tview.AlignCenter), 0, 1, false).
		AddItem(l, 0, 1, true)
}

func (s sshTTY) Start() error {
	return nil
}

func (s sshTTY) Stop() error {
	return nil
}

func (s sshTTY) Drain() error {
	return nil
}

func (s sshTTY) NotifyResize(cb func()) {
}

func (s sshTTY) WindowSize() (tcell.WindowSize, error) {
	return s.windowSize, nil
}

func (s sshTTY) Close() error {
	return nil
}
