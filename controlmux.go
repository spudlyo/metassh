/*
 * controlmux.go
 *
 * Herein lies mhamrick's half-ass implementation of the OpenSSH MUX protocol.
 * (https://github.com/openssh/openssh-portable/blob/master/PROTOCOL.mux)
 *
 * It's works well enough to run commands over the ControlMaster provided
 * you don't try anything fancy like port/stdio forwarding, terminate, etc.
 *
 * It creates the ControlMaster UNIX domain socket, listens for new session
 * requests, sets up a new session, snarfs file descriptors from the
 * ControlMaster UNIX domain socket, shuffles data around, sends the right
 * exit code back to the client, and tries hard to clean up after itself.
 *
 */

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
)

// Grabbed from OpenSSH's mux.c
const (
	MuxMsgHello       = 0x00000001
	MuxCNewSession    = 0x10000002
	MuxCAliveCheck    = 0x10000004
	MuxSExitMessage   = 0x80000004
	MuxSAlive         = 0x80000005
	MuxSSessionOpened = 0x80000006
)

type controlMsg struct {
	MsgType   uint32
	data      *bytes.Buffer
	bytesRead int
	conn      *net.UnixConn
}

type controlKill struct {
	respChan chan<- bool
}

type helloMsg struct {
	MsgType uint32
	Version uint32
}

type aliveCheckMsg struct {
	MsgType   uint32
	RequestID uint32
}

type aliveResponseMsg struct {
	MsgType   uint32
	RequestID uint32
	ServerPid uint32
}

type sessionOpenedMsg struct {
	MsgType     uint32
	ClientReqID uint32
	SessionID   uint32
}

type exitMsg struct {
	MsgType   uint32
	SessionID uint32
	ExitCode  uint32
}

type newSessionMsg struct {
	MsgType      uint32
	RequestID    uint32
	Reserved     string
	WantTTY      bool
	WantX11      bool
	WantAgent    bool
	Subsystem    bool
	EscapeChar   uint32
	TerminalType string
	Command      string
}

// Mux is an object that implements the SSH ControlMaster socket protocol.
type Mux struct {
	me          string
	cc          chan interface{}
	client      *ssh.Client
	l           *net.UnixListener
	e           Env
	sesscounter uint32
}

// NewMux initializes the Mux type, as well as creating the ControlMaster
// UNIX domain socket, and firing off goroutines to listen on the socket
// and to handle mux requests.
func NewMux(me string, client *ssh.Client, e Env) (*Mux, error) {
	var err error
	m := &Mux{
		me:     me,
		client: client,
		e:      e,
	}
	sockName := e.c.ControlPath + "/" + me + "_" + SSHPort
	if _, err = os.Stat(sockName); os.IsExist(err) {
		msg := fmt.Sprintf("Socket %s already exists.", sockName)
		return nil, errors.New(msg)
	}
	addr := &net.UnixAddr{Name: sockName, Net: "unix"}
	m.l, err = net.ListenUnix("unix", addr)
	if err != nil {
		msg := fmt.Sprintf("%s: net.Listen failed: %s", me, err)
		return nil, errors.New(msg)
	}
	m.cc = make(chan interface{})
	go m.acceptControlMaster()
	go m.handleControlMaster()
	return m, nil
}

// Close cleans up the ControlMaster UNIX domain socket.
func (m *Mux) Close() {
	respChan := make(chan bool)
	m.cc <- controlKill{respChan}
	<-respChan
	return
}

func (m *Mux) acceptControlMaster() {
	for {
		conn, err := m.l.AcceptUnix()
		if err != nil {
			// Connection closed while we're listening. This happens
			// when we clean up during a controlKill message, which is
			// why we don't debug log it.
			return
		}
		go m.readMsg(conn)
	}
}

func (m *Mux) readMsg(conn *net.UnixConn) {
	// The header tells us how many more bytes are coming.
	headerBuf := make([]byte, 4)
	n, err := conn.Read(headerBuf)
	if err != nil {
		m.e.o.Debug("%s: Read() failed: %s\n", m.me, err)
		return
	}
	if n != 4 {
		m.e.o.Debug("Read %d bytes, expected 4.\n", n)
		return
	}
	moreBytes := binary.BigEndian.Uint32(headerBuf)
	buf := make([]byte, moreBytes)
	n, err = conn.Read(buf)
	if err != nil {
		m.e.o.Debug("%s: Read() failed: %s\n", m.me, err)
		return
	}
	msgType := binary.BigEndian.Uint32(buf[:4])
	m.cc <- controlMsg{msgType, bytes.NewBuffer(buf), n, conn}
}

func (m *Mux) sendMsg(conn *net.UnixConn, data []byte) {
	// first we send the header with the size.
	headerBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(headerBuf, uint32(len(data)))
	_, err := conn.Write(headerBuf)
	if err != nil {
		m.e.o.Debug("%s: Write() failed: %s\n", m.me, err)
		return
	}
	_, err = conn.Write(data)
	if err != nil {
		m.e.o.Debug("%s: Write() failed: %s\n", m.me, err)
		return
	}
	return
}

func (m *Mux) handleControlMaster() {
	for {
		req := <-m.cc
		switch req.(type) {
		case controlMsg:
			msg := req.(controlMsg)
			switch msg.MsgType {
			case MuxMsgHello:
				hm := helloMsg{}
				err := binary.Read(msg.data, binary.BigEndian, &hm)
				if err != nil {
					m.e.o.Debug("Couldn't read message: %s\n", err)
					if err = msg.conn.Close(); err != nil {
						m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
					}
					continue
				}
				// Send their hello back to them. I should probably
				// check version numbers or something. FIXME
				err = m.sendStruct(msg.conn, hm)
				if err != nil {
					m.e.o.Debug("%s: m.sendStruct(): %s\n", m.me, err)
					if err := msg.conn.Close(); err != nil {
						m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
					}
					continue
				}
				go m.readMsg(msg.conn)
			case MuxCAliveCheck:
				// FIXME: Actually verify backend is alive.
				acm := aliveCheckMsg{}
				err := binary.Read(msg.data, binary.BigEndian, &acm)
				if err != nil {
					m.e.o.Debug("Couldn't read message: %s\n", err)
					if err = msg.conn.Close(); err != nil {
						m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
					}
					continue
				}
				arm := aliveResponseMsg{
					MsgType:   MuxSAlive,
					RequestID: acm.RequestID,
					ServerPid: uint32(os.Getpid()),
				}
				err = m.sendStruct(msg.conn, arm)
				if err != nil {
					m.e.o.Debug("%s: m.sendStruct(): %s\n", m.me, err)
					if err := msg.conn.Close(); err != nil {
						m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
					}
					continue
				}
				go m.readMsg(msg.conn)
			case MuxCNewSession:
				nsm := m.parseNewSession(msg.data.Bytes())
				session, err := m.client.NewSession()
				if err != nil {
					m.e.o.Debug("ssh.NewSession() failed: %s\n", err)
					if err = msg.conn.Close(); err != nil {
						m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
					}
					continue
				}
				som := sessionOpenedMsg{
					MsgType:     MuxSSessionOpened,
					ClientReqID: nsm.RequestID,
					SessionID:   m.sesscounter,
				}
				m.sesscounter++
				err = m.sendStruct(msg.conn, som)
				if err != nil {
					m.e.o.Debug("%s: m.sendStruct(): %s\n", m.me, err)
					if err = msg.conn.Close(); err != nil {
						m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
					}
					continue
				}
				var inOutErr [3]int
				for i := 0; i < 3; i++ {
					var fds []int
					fds, err = m.getFd(msg.conn)
					if err != nil {
						m.e.o.Debug("getFds failed: %s\n", err)
						if err = msg.conn.Close(); err != nil {
							m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
						}
						continue
					}
					inOutErr[i] = fds[0]
					syscall.CloseOnExec(inOutErr[i])
				}

				if nsm.WantTTY {
					var ws winSize
					modes := ssh.TerminalModes{
						ssh.ECHO:          0,
						ssh.TTY_OP_ISPEED: 14400,
						ssh.TTY_OP_OSPEED: 14400,
					}
					ws, err = getWinSize(uintptr(inOutErr[1]))
					rows := 24
					cols := 80
					if err != nil {
						m.e.o.Debug("getWinSize: %s\n", err)
					} else {
						rows = int(ws.Height)
						cols = int(ws.Width)
					}
					err = session.RequestPty(nsm.TerminalType, rows, cols, modes)
					if err != nil {
						m.e.o.Debug("ssh.RequestPty() failed: %s\n", err)
						if err = msg.conn.Close(); err != nil {
							m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
						}
						continue
					}
				}
				localStdin := os.NewFile(uintptr(inOutErr[0]), "/dev/stdin")
				localStdout := os.NewFile(uintptr(inOutErr[1]), "/dev/stdout")
				localStderr := os.NewFile(uintptr(inOutErr[2]), "/dev/stderr")
				remoteStdin, err := session.StdinPipe()
				if err != nil {
					m.e.o.Debug("session.StdinPipe() failed: %s\n", err)
					if err = msg.conn.Close(); err != nil {
						m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
					}
					continue
				}
				remoteStdout, err := session.StdoutPipe()
				if err != nil {
					m.e.o.Debug("session.StdoutPipe() failed: %s\n", err)
					if err = msg.conn.Close(); err != nil {
						m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
					}
					continue
				}
				remoteStderr, err := session.StderrPipe()
				if err != nil {
					m.e.o.Debug("session.StderrPipe() failed: %s\n", err)
					if err = msg.conn.Close(); err != nil {
						m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
					}
					continue
				}
				// Because we have a bug where our io.Copy() eats the first
				// character of STDIN after our SSH client drops back to the
				// shell, let's only do the copy if we have to.
				if nsm.WantTTY || nsm.Command == "" ||
					strings.Contains(nsm.Command, "scp -t") {
					// I will eat a character from your shell, sorry.
					go m.copy(remoteStdin, localStdin, "stdin")
				}
				go m.copy(localStdout, remoteStdout, "stdout")
				go m.copy(localStderr, remoteStderr, "stderr")

				if nsm.Command != "" {
					err = session.Start(nsm.Command)
				} else {

					err = session.Shell()
				}
				if err != nil {
					m.e.o.Debug("session.Start/Shell() failed: %s\n", err)
					if err = msg.conn.Close(); err != nil {
						m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
					}
					continue
				}
				go m.waiter(msg.conn, session, som.SessionID)

			default:
				m.e.o.Debug("%s: Unhandled message: %x\n", m.me, msg.MsgType)
			}
		case controlKill:
			kill := req.(controlKill)
			if err := m.l.Close(); err != nil {
				m.e.o.Debug("%s: m.l.Close() failed: %s\n", m.me, err)
			}
			kill.respChan <- true
			return
		}
	}
}

func (m *Mux) waiter(c *net.UnixConn, s *ssh.Session, sid uint32) {
	var exitCode uint32
	err := s.Wait()

	if err != nil {
		ee, ok := err.(*ssh.ExitError)
		if ok {
			exitCode = uint32(ee.Waitmsg.ExitStatus())
		} else {
			m.e.o.Debug("Unknown exit code, faking it.\n")
			exitCode = 255
		}
	}
	em := exitMsg{
		MsgType:   MuxSExitMessage,
		SessionID: sid,
		ExitCode:  exitCode,
	}
	err = m.sendStruct(c, em)
	if err != nil {
		m.e.o.Debug("%s: m.sendStruct(): %s\n", m.me, err)
		if err = c.Close(); err != nil {
			m.e.o.Debug("%s: Close() failed: %s\n", m.me, err)
		}
	}
	if err = c.Close(); err != nil {
		m.e.o.Debug("%s: c.Close() failed: %s\n", m.me, err)
	}
}

func (m *Mux) copy(dest io.Writer, src io.Reader, what string) {
	_, err := io.Copy(dest, src)
	if err != nil {
		// m.e.o.Debug("%s(copied %d bytes): io.Copy(): %s\n", what, n, err)
	}
}

func (m *Mux) sendStruct(conn *net.UnixConn, data interface{}) error {
	resp := bytes.NewBuffer(nil)
	err := binary.Write(resp, binary.BigEndian, data)
	if err != nil {
		return err
	}
	m.sendMsg(conn, resp.Bytes())
	return nil
}

func (m *Mux) getFd(conn *net.UnixConn) ([]int, error) {
	var ret []int

	connFile, err := conn.File()
	if err != nil {
		return ret, err
	}
	sock := int(connFile.Fd())
	buf := make([]byte, syscall.CmsgSpace(4))
	_, _, _, _, err = syscall.Recvmsg(sock, nil, buf, 0)
	if err != nil {
		return ret, err
	}
	if err = connFile.Close(); err != nil {
		return ret, err
	}
	controlMsgs, err := syscall.ParseSocketControlMessage(buf)
	if err != nil {
		return ret, err
	}
	return syscall.ParseUnixRights(&controlMsgs[0])
}

func (m *Mux) parseNewSession(stream []byte) newSessionMsg {
	ret := newSessionMsg{}

	stream, ret.MsgType = pullUint32(stream)
	stream, ret.RequestID = pullUint32(stream)
	stream, ret.Reserved = pullString(stream)
	stream, ret.WantTTY = pullBool(stream)
	stream, ret.WantX11 = pullBool(stream)
	stream, ret.WantAgent = pullBool(stream)
	stream, ret.Subsystem = pullBool(stream)
	stream, ret.EscapeChar = pullUint32(stream)
	stream, ret.TerminalType = pullString(stream)
	_, ret.Command = pullString(stream)
	return ret
}

func pullUint32(stream []byte) ([]byte, uint32) {
	ret := binary.BigEndian.Uint32(stream[:4])
	return stream[4:], ret
}

func pullString(stream []byte) ([]byte, string) {
	stream, length := pullUint32(stream)
	if length == 0 {
		return stream, ""
	}
	ret := string(stream[:length])
	return stream[length:], ret
}

func pullBool(stream []byte) ([]byte, bool) {
	ret := binary.BigEndian.Uint32(stream[:4]) != 0
	return stream[4:], ret
}
