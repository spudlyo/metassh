/*
 * run.go
 *
 * This file has functions that run commands on single or multiple targets.
 *
 */

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// The runCmd function is run from the remoteHost goroutine that exists for
// every connected SSH session. Any host we SSH into with this program can
// be used to run an arbritrary command. This is in practice only used by non
// by bastion hosts though.
func runCmd(me string, req runRequest, client *ssh.Client, e Env) {
	var stdOut, stdErr bytes.Buffer
	var fpStdOut, fpStdErr, fpRetCode *os.File
	var exitCode uint32
	var session *ssh.Session
	var err error

	timeoutChan := make(chan bool, 1)
	runResp := make(chan runResponse)

	if e.c.Spool {
		fpStdOut, fpStdErr, fpRetCode, err = spoolHandles(e, e.s.GetPTR(me))
		if err != nil {
			e.o.Err("spoolHandles: %s\n", err)
			e.o.Err("Spooling is turned OFF, correct and re-enable.\n")
			e.c.Spool = false
		} else {
			defer func() {
				err = fpStdOut.Close()
				if err != nil {
					e.o.Debug("fpStdOut.Close(): %s\n", err)
				}
				err = fpStdErr.Close()
				if err != nil {
					e.o.Debug("fpStdErr.Close(): %s\n", err)
				}
				err = fpRetCode.Close()
				if err != nil {
					e.o.Debug("fpRetCode.Close(): %s\n", err)
				}
			}()
		}
	}
	rwi := &waitInfo{me, stateNewSession, time.Now(), timeoutChan}
	e.s.SetRunWaitInfo(rwi)
	defer e.s.DeleteRunWaitInfo(me)

	session, err = client.NewSession()
	if err != nil {
		runResp <- runResponse{err: err}
		return
	}
	go func(done chan<- runResponse, session *ssh.Session) {
		var err error
		if e.c.Spool {
			var stdOutPipe, stdErrPipe io.Reader
			stdOutPipe, err = session.StdoutPipe()
			if err != nil {
				e.o.Debug("StdoutPipe(): %s\n", err)
			} else {
				go func() {
					_, err = io.Copy(fpStdOut, io.TeeReader(stdOutPipe, &stdOut))
					if err != nil {
						e.o.Debug("fpStdOut io.Copy: %s\n", err)
					}
				}()
			}
			stdErrPipe, err = session.StderrPipe()
			if err != nil {
				e.o.Debug("StderrPipe(): %s\n", err)
			} else {
				go func() {
					_, err = io.Copy(fpStdErr, io.TeeReader(stdErrPipe, &stdErr))
					if err != nil {
						e.o.Debug("fpStdErr io.Copy: %s\n", err)
					}
				}()
			}
		} else {
			session.Stdout = &stdOut
			session.Stderr = &stdErr
		}
		e.s.SetRunWaitState(me, stateStartSession)
		if err = session.Start(req.cmd); err != nil {
			done <- runResponse{err: err}
			return
		}
		e.s.SetRunWaitState(me, stateRunning)
		if err = session.Wait(); err != nil {
			ee, ok := err.(*ssh.ExitError)
			if ok {
				exitCode = uint32(ee.Waitmsg.ExitStatus())
			} else {
				e.o.Debug("Unknown exit code, faking it.\n")
				exitCode = 255
			}
		}
		if e.c.Spool {
			_, err = fmt.Fprintf(fpRetCode, "%d\n", exitCode)
			if err != nil {
				e.o.Debug("fmt.Fprintf: %s\n", err)
			}
		}
		e.s.SetRunWaitState(me, stateDone)
		done <- runResponse{
			stdOut:   stdOut.String(),
			stdErr:   stdErr.String(),
			err:      err,
			exitCode: int(exitCode),
		}
	}(runResp, session)
	go sleep(timeoutChan, req.timeout)

	select {
	case resp := <-runResp:
		req.response <- resp
		go func() { <-timeoutChan }()
		return
	case organic := <-timeoutChan:
		var retErr error
		if organic {
			retErr = errors.New("Remote run timed out.")
		} else {
			retErr = errors.New("Remote run aborted.")
		}
		// At this point we need to do something to terminate the running
		// program.
		//
		// session.Signal() seems promising, but is not actually implemented
		// in OpenSSH.
		//
		err = session.Close() // Does not actually terminate the program.
		if err != nil {
			e.o.Debug("%s: session.Close(): %s\n", me, err)
		}
		req.response <- runResponse{
			stdOut: stdOut.String(),
			stdErr: stdErr.String(),
			err:    retErr,
		}
		go func() {
			resp := <-runResp
			e.o.Debug("%s caught a run straggler: %v\n", me, resp)
		}()
		return
	}
}

// If the -e option is used, this gets called to run the test command after
// a successful connection.
func runOnce(host string, cmd string, e Env, timeout int) {
	startTime := time.Now()
	mychan := make(chan runResponse)
	req := runRequest{cmd, mychan, timeout}
	ci, err := e.s.GetConnInfo(host)
	if err != nil {
		e.o.Debug("GetConnInfo(): %s\n", err)
		return
	}
	ci.reqChan <- req
	resp := <-mychan
	elapsedTime := time.Since(startTime)
	if resp.err != nil {
		e.s.SetRunStatus(setRunStatus{host, false, true, elapsedTime, resp.err, ""})
	} else {
		srs := setRunStatus{host, true, true, elapsedTime, nil, resp.stdOut}
		e.s.SetRunStatus(srs)
	}
}

// The runEverywhere function runs a command on every connected target.
func runEverywhere(cmd string, e Env, timeout int) {
	var wg sync.WaitGroup
	limiter := make(chan struct{}, e.c.Concurrency)
	var hk = e.s.GetConnKeys()
	for j := range hk {
		host := hk[j]
		ci, err := e.s.GetConnInfo(host)
		if err != nil {
			e.o.Debug("GetConnInfo(): %s\n", err)
			continue
		}
		if ci.isProxy {
			continue
		}
		wg.Add(1)
		limiter <- struct{}{}
		go func(host string, command string) {
			defer func() { wg.Done(); <-limiter }()
			startTime := time.Now()
			mychan := make(chan runResponse)
			req := runRequest{command, mychan, timeout}
			ci, err := e.s.GetConnInfo(host)
			if err != nil {
				e.o.Debug("GetConnInfo(): %s\n", err)
				return
			}
			ci.reqChan <- req
			resp := <-mychan
			elapsedTime := time.Since(startTime)
			e.s.SetRunStatus(setRunStatus{
				host,
				resp.err == nil,
				true,
				elapsedTime,
				resp.err,
				resp.stdOut,
			})
			if resp.stdOut != "" {
				f := "***** Host: %s, Time: %.2fs, Exit: %d, Err: %v *****\n%s"
				e.o.Out(f, host, elapsedTime.Seconds(), resp.exitCode, resp.err, resp.stdOut)
			}
		}(host, cmd)
	}
	wg.Wait()
}

// Return open file handles for the spool files.
func spoolHandles(e Env, hostName string) (*os.File, *os.File, *os.File, error) {
	fpStdOut, err := os.Create(e.c.SpoolDir + "/" + hostName + ".out")
	if err != nil {
		return nil, nil, nil, err
	}
	fpStdErr, err := os.Create(e.c.SpoolDir + "/" + hostName + ".err")
	if err != nil {
		err = fpStdOut.Close()
		if err != nil {
			e.o.Debug("fpStdOut.Close(): %s\n", err)
		}
		return nil, nil, nil, err
	}
	fpRetCode, err := os.Create(e.c.SpoolDir + "/" + hostName + ".ret")
	if err != nil {
		err = fpStdOut.Close()
		if err != nil {
			e.o.Debug("fpStdOut.Close(): %s\n", err)
		}
		err = fpStdErr.Close()
		if err != nil {
			e.o.Debug("fpStdErr.Close(): %s\n", err)
		}
		return nil, nil, nil, err
	}
	return fpStdOut, fpStdErr, fpRetCode, nil
}
