/*
 * proxy.go
 *
 * This file contains the proxyConnect function that will connect to a host
 * via a proxy. It also has resolveProxies function has some very Booking
 * specific proxy logic in it that should be moved to the external target
 * program.
 *
 */

package main

import (
	"errors"
	"time"

	"golang.org/x/crypto/ssh"
)

// The proxyConnect function is run from the remoteHost goroutine that exists
// for every connected SSH session. Any host we SSH into with this program can
// be used to proxy a connection to another host. This is in practice only used
// by bastion hosts though.
func proxyConnect(req proxyRequest, e Env, client *ssh.Client) {
	dest := req.target + ":" + SSHPort
	timeout := make(chan bool, 1)
	proxyclient := make(chan proxyResponse)

	cwi := &waitInfo{req.target, stateDialing, time.Now(), timeout}

	e.s.SetConnWaitInfo(cwi)
	defer e.s.DeleteConnWaitInfo(req.target)

	// We wrap the dial, NewClientConn, and NewClient calls in a goroutine
	// because they can take longer than we want to wait.
	go func(done chan<- proxyResponse) {
		var localConfig = new(ssh.ClientConfig)
		*localConfig = *(e.s.GetSSHConfig())
		if e.c.Password {
			pwClosure := func() (string, error) {
				host := e.s.GetPTR(req.target)
				e.s.SetRequiresPw(host)
				return e.s.GetAuthPass(), nil
			}
			localConfig.Auth = append(
				localConfig.Auth,
				ssh.PasswordCallback(pwClosure),
			)
		}
		conn, err := client.Dial("tcp", dest)
		if err != nil {
			done <- proxyResponse{err: err}
			return
		}
		e.s.SetConnWaitState(req.target, stateEstablishing)
		ncc, chans, reqs, err := ssh.NewClientConn(conn, dest, localConfig)
		if err != nil {
			done <- proxyResponse{err: err}
			return
		}
		e.s.SetConnWaitState(req.target, stateNewClient)
		client := ssh.NewClient(ncc, chans, reqs)
		e.s.SetConnWaitState(req.target, stateDone)
		done <- proxyResponse{client: client}
	}(proxyclient)

	// This goroutine serves as the timeout for the above.
	go func(done chan<- bool) {
		time.Sleep(time.Duration(req.timeout) * time.Second)
		done <- true
	}(timeout)

	select {
	case resp := <-proxyclient:
		req.response <- resp
		// Don't leave that timeout goroutine hangin'
		go func() { <-timeout }()
		return
	case organic := <-timeout:
		var retErr error
		if organic {
			retErr = errors.New("Remote connection timed out.")
		} else {
			retErr = errors.New("Remote connection aborted.")
		}
		req.response <- proxyResponse{err: retErr}
		// No goroutines left behind.
		go func() {
			<-proxyclient
			// resp := <-proxyclient
			// Debug("%s caught a proxy straggler: %v\n", dest, resp)
		}()
		return
	}
}
