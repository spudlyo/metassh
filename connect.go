/*
 * connect.go
 *
 * This file has functions that will connect/disconnect a set of targets.
 *
 */

package main

import (
	"sync"
	"time"
)

func connectEverywhere(e Env, timeout int) int {
	var wg sync.WaitGroup

	if err := resolveProxies(e); err != nil {
		e.o.ErrExit("proxy resolve failed: %s\n", err)
	}
	concurrency := e.c.Concurrency
	if e.c.Agent {
		concurrency = 128 // Agent can only handle 128 concurrent requests.
	}
	limiter := make(chan struct{}, concurrency)
	j := 0
	var hk = e.s.GetHostKeys()
	for j = range hk {
		hostname := hk[j]
		if e.s.ConnExists(hostname) {
			e.o.Debug("Already connected to: %s\n", hostname)
			continue
		}
		wg.Add(1)
		limiter <- struct{}{}
		go func(me string) {
			defer func() { wg.Done(); <-limiter }()
			hi, err := e.s.GetHostInfo(me)
			if err != nil {
				e.o.Debug("GetHostInfo(): %s\n", err)
				return
			}
			startTime := time.Now()
			err = resolve(hi.chain, e, false, timeout)
			e.s.SetConnectionStatus(setConnectionStatus{
				hostName:    me,
				connectedOK: err == nil,
				connectTime: time.Since(startTime),
				lastError:   err,
			})
			if err == nil && e.c.Execute {
				runOnce(me, e.c.TestCmd, e, timeout)
			}
		}(hostname)
	}
	wg.Wait()
	return j + 1
}

func disconnectHost(e Env, host string) error {
	respChan := make(chan cleanupResponse)
	ci, err := e.s.GetConnInfo(host)
	if err != nil {
		return err
	}
	ci.reqChan <- cleanupRequest{respChan}
	<-respChan
	e.s.DeleteConnInfo(host)
	return nil
}

func disconnectEverywhere(e Env, Proxies bool) {
	respChan := make(chan cleanupResponse)

	// Disconnect any 'in-flight' connections or runs.
	e.s.TimeoutWaiters()
	// Cleanup all the non-proxies first.
	ck := e.s.GetConnKeys()
	for i := range ck {
		ci, err := e.s.GetConnInfo(ck[i])
		if err != nil {
			e.o.Debug("GetConnInfo(): %s\n", err)
			continue
		}
		if ci.isProxy {
			continue
		}
		ci.reqChan <- cleanupRequest{respChan}
		<-respChan
		e.s.DeleteConnInfo(ck[i])
	}
	if !Proxies {
		return
	}
	// Now get the indirect proxies.
	ck = e.s.GetConnKeys()
	for i := range ck {
		ci, err := e.s.GetConnInfo(ck[i])
		if err != nil {
			e.o.Debug("GetConnInfo(): %s\n", err)
			continue
		}
		if !ci.isProxy {
			e.o.Debug("Whaa, how did %s not get killed!?\n", ck[i])
			continue
		}
		if ci.isDirect {
			continue
		}
		ci.reqChan <- cleanupRequest{respChan}
		<-respChan
		e.s.DeleteConnInfo(ck[i])
	}
	// Finally get the direct proxies.
	ck = e.s.GetConnKeys()
	for i := range ck {
		ci, err := e.s.GetConnInfo(ck[i])
		if err != nil {
			e.o.Debug("GetConnInfo(): %s\n", err)
			continue
		}
		if !ci.isProxy || !ci.isDirect {
			e.o.Debug("Whaa, how did %s not get killed!?\n", ck[i])
			continue
		}
		for i := 0; i < e.c.BastionConns; i++ {
			ci.reqChan <- cleanupRequest{respChan}
			<-respChan
		}
		e.s.DeleteConnInfo(ck[i])
	}
}
