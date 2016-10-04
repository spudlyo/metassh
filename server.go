/*
 * server.go
 *
 * This file houses the code that deals with setting up the SSH server
 * component of MetaSSH.
 *
 */

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/kr/pty"
	"golang.org/x/crypto/ssh"
)

// SSHServer is an object that keeps track of our server's state. Most notably
// if we've already used up our one GNU Readline session.
type SSHServer struct {
	readlineSession bool
	sshConfig       *ssh.ServerConfig
	e               Env
}

// NewSSHServer initializes the SSHServer state.
func NewSSHServer(fp *os.File, e Env) (*SSHServer, error) {
	var err error
	s := &SSHServer{
		e: e,
	}
	s.sshConfig, err = getSSHServerConfig(fp, e)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Start starts up the server once you've initialized the object.
func (s *SSHServer) Start() {
	s.e.o.Debug("Listening on the server port.\n")
	listener, err := net.Listen("tcp", "0.0.0.0:"+ServerPort)
	if err != nil {
		s.e.o.ErrExit("Failed to listen on port %s: \n", ServerPort, err)
	}
	for {
		tcpConn, err := listener.Accept()
		if err != nil {
			s.e.o.Err("Failed to accept incoming connection: %s\n", err)
			continue
		}
		sshConn, chans, reqs, err := ssh.NewServerConn(tcpConn, s.sshConfig)
		if err != nil {
			s.e.o.Err("Failed to handshake: %s\n", err)
			continue
		}
		s.e.o.Debug("Client connection from: %s\n", sshConn.RemoteAddr())
		go s.discardRequests(reqs)
		go s.handleChannels(chans)
	}
}

func (s *SSHServer) discardRequests(in <-chan *ssh.Request) {
	for req := range in {
		s.e.o.Debug("Discarding req: %v\n", req)
		if req.WantReply {
			if err := req.Reply(false, nil); err != nil {
				s.e.o.Debug("req.Reply() failed: %s\n", err)
			}
		}
	}
}

func (s *SSHServer) handleChannels(chans <-chan ssh.NewChannel) {
	for newChannel := range chans {
		go s.handleChannel(newChannel)
	}
}

func (s *SSHServer) handleChannel(newChannel ssh.NewChannel) {
	var tty, xpty *os.File
	var err error
	var con ssh.Channel
	var requests <-chan *ssh.Request

	if t := newChannel.ChannelType(); t != "session" {
		msg := fmt.Sprintf("Unknown channel request: '%s'", t)
		s.e.o.Debug("%s\n", msg)
		err = newChannel.Reject(ssh.UnknownChannelType, msg)
		if err != nil {
			s.e.o.Debug("newChannel.Reject() failed: %s\n", err)
		}
		return
	}
	con, requests, err = newChannel.Accept()
	if err != nil {
		s.e.o.Err("Could not accept channel (%s)\n", err)
		return
	}
	newOut := NewOutput(con, con, true, s.e.c.Debug)
	newE := Env{s.e.s, newOut, s.e.c}
	for req := range requests {
		switch req.Type {
		case "exec":
			payload, _ := pullUint32(req.Payload)
			cmdline := string(payload)
			chunks := strings.Fields(cmdline)
			cmd := strings.ToLower(chunks[0])
			runCliCmd(newE, cmd, chunks[1:])
			if err = con.Close(); err != nil {
				s.e.o.Debug("con.Close() faied: %s\n", err)
			}
		case "shell":
			s.e.o.Debug("CLI connection established.\n")
			if !s.readlineSession {
				s.readlineSession = true
				go func() {
					defer func() { s.readlineSession = false }()
					cli(con, newE, true)
				}()
			} else {
				cli(con, newE, false)
			}
		case "pty-req":
			termLen := req.Payload[3]
			w, h := parseDims(req.Payload[termLen+4:])

			xpty, tty, err = pty.Open()
			if err != nil {
				s.e.o.Debug("pty.Open() failed: %s\n", err)
				return
			}
			if err := setWinSize(xpty.Fd(), w, h); err != nil {
				s.e.o.Debug("setWinSize(xpty) failed: %s\n", err)
			}
			if !s.readlineSession {
				if err := rlInstream(tty); err != nil {
					s.e.o.Debug("rlInstream() failed: %s\n", err)
				}
				if err := rlOutstream(tty); err != nil {
					s.e.o.Debug("rlOutstream() failed: %s\n", err)
				}
				go func() {
					if _, err := io.Copy(newOut, xpty); err != nil {
						s.e.o.Debug("io.Copy(newOut, xpty) failed: %s\n", err)
					}
				}()
				go func() {
					if _, err := io.Copy(xpty, con); err != nil {
						s.e.o.Debug("io.Copy(xpty, con) failed: %s\n", err)
					}
				}()
			}
		case "window-change":
			w, h := parseDims(req.Payload)
			if err := setWinSize(xpty.Fd(), w, h); err != nil {
				s.e.o.Debug("setWinSize failed: %s\n", err)
			}

		default:
			s.e.o.Debug("unhandled request: %s\n", req.Type)
		}
	}
	s.e.o.Debug("I'm done with the requests loop.\n")
}

func parseDims(b []byte) (uint32, uint32) {
	w := binary.BigEndian.Uint32(b)
	h := binary.BigEndian.Uint32(b[4:])
	return w, h
}

type winSize struct {
	Height uint16
	Width  uint16
	x      uint16
	y      uint16
}

func setWinSize(fd uintptr, w, h uint32) error {
	ws := &winSize{Width: uint16(w), Height: uint16(h)}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(syscall.TIOCSWINSZ),
		uintptr(unsafe.Pointer(ws)),
	)
	if errno != 0 {
		msg := fmt.Sprintf("SYS_IOCTL failed with code: %d", errno)
		return errors.New(msg)
	}
	return nil
}

func getWinSize(fd uintptr) (winSize, error) {
	ws := new(winSize)
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(ws)),
	)
	if errno != 0 {
		msg := fmt.Sprintf("SYS_IOCTL failed with code: %d", errno)
		return *ws, errors.New(msg)
	}
	return *ws, nil
}
