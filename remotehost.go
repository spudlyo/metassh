/*
 * remotehost.go
 *
 * This file contains the remoteHost function, which is the goroutine that runs
 * for each connected host. This goroutine listens for requests to proxy, run
 * commands, or clean itself up.
 *
 */

package main

import (
	"math/rand"
	"time"

	"golang.org/x/crypto/ssh"
)

type proxyResponse struct {
	client *ssh.Client
	err    error
}

type proxyRequest struct {
	target   string
	response chan<- proxyResponse
	timeout  int
}

type runRequest struct {
	cmd      string
	response chan<- runResponse
	timeout  int
}

type runResponse struct {
	stdOut   string
	stdErr   string
	exitCode int
	err      error
}

type cleanupResponse struct {
	allGood bool
}

type cleanupRequest struct {
	response chan<- cleanupResponse
}

// The remoteHost function runs once for every SSH connection, all it does is
// listen on the request channel and dispatches commands.
//
// For bastion hosts, we can have a number of these goroutines all listening
// on one common channel, but each having an independent SSH client connection
// to the bastion host, as to not spam a single bastion connection too hard.
func remoteHost(me string, id int, listen <-chan interface{}, e Env, client *ssh.Client, isProxy bool) {
	var m *Mux
	var nmErr error

	if host := e.s.GetPTR(me); host != "" {
		me = host
	}
	e.o.Debug("remoteHost(): running for %s\n", me)

	// We keep a session around just for keep-alives.
	session, err := client.NewSession()
	if err != nil {
		e.o.Debug("%s: NewSession() failed: %s\n", me, err)
		go func() {
			err := disconnectHost(e, me)
			if err != nil {
				e.o.Debug("disconnectHost: %s\n", err)
			}
		}()
	}
	keepAliveChan := make(chan bool)
	if e.c.KeepAlive > 0 {
		go func() {
			splay := rand.Intn(59) + 1
			time.Sleep(time.Duration(splay) * time.Second)
			for {
				time.Sleep(time.Duration(e.c.KeepAlive) * time.Second)
				keepAliveChan <- true
			}
		}()
	}
	// Proxies don't get ControlMaster sockets.
	// Only create ControlMaster sockets if we're in server mode.
	if !isProxy && e.c.Server {
		m, nmErr = NewMux(me, client, e)
		if nmErr != nil {
			e.o.Debug("NewMux() failed: %s\n", nmErr)
		}
	}
	for {
		select {
		case req := <-listen:
			switch req.(type) {
			case proxyRequest:
				pReq := req.(proxyRequest)
				go proxyConnect(pReq, e, client)
			case runRequest:
				rReq := req.(runRequest)
				go runCmd(me, rReq, client, e)
			case cleanupRequest:
				allGood := true
				cReq := req.(cleanupRequest)
				if session != nil {
					err := session.Close()
					if err != nil {
						e.o.Debug("session.Close(): %s\n", err)
					}
				}
				err := client.Close()
				if err != nil {
					e.o.Debug("%s: client.Close(): %s\n", me, err)
					allGood = false
				}
				if !isProxy && nmErr == nil {
					m.Close()
				}
				cReq.response <- cleanupResponse{allGood}
				return
			default:
				e.o.Debug("Unknown request type: %v\n", req)
			}
		case <-keepAliveChan:
			if session != nil {
				_, err := session.SendRequest(KeepAlive, true, nil)
				if err != nil {
					e.o.Debug("%s Keep alive failed: %s\n", err)
					go func() {
						err := disconnectHost(e, me)
						if err != nil {
							e.o.Debug("disconnectHost: %s\n", err)
						}
					}()
				}
			}
		}
	}
}
