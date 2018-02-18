/*
 * resolve.go
 *
 * This file has the resolve function, and a few other related ones. Resolving
 * is about figuring out how to SSH into a particular host given a chain of
 * hosts that one needs to proxy through to arrive at the final destination.
 *
 * For example, to talk to pc101xydapi-01 you end up needing to proxy through
 * two separate hosts like:
 *
 * `-> bastion-vip.dc1.prod.foobar.com
 *  `-> pci-bastion-vip.dc1.prod.foobar.com
 *    `-> web-01.dc1.prod.foobar.com
 *
 */

package main

import (
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// The resolve function figures out how to get there from here. Based on the
// in chain slice it will proxy through as many hosts as necessary to get to
// the final destination, which is the last element in the slice.
func resolve(chain []string, e Env, isProxy bool, timeout int) error {
	e.o.Debug("resolve() chain: %v, isProxy: %v, timeout: %v\n", chain, isProxy, timeout)
	for idx, link := range chain {
		proxy := idx - 1
		if proxy >= 0 {
			e.o.Debug("resolve(): Using a proxy.\n")
			// This machine has a proxy
			proxyhost := chain[proxy]
			// We're outta here if we've already done this one.
			if e.s.ConnExists(link) {
				continue
			}
			// Let's send the proxy a message asking it to create a con for us.
			respChan := make(chan proxyResponse)
			req := proxyRequest{link, respChan, timeout}
			ci, err := e.s.GetConnInfo(proxyhost)
			if err != nil {
				e.o.Debug("GetConnInfo(): %s\n", err)
				continue
			}
			ci.reqChan <- req
			resp := <-respChan
			if resp.err != nil {
				return resp.err
			}
			e.s.IncProxyCount(proxyhost)
			newreq := make(chan interface{})
			go remoteHost(link, 0, newreq, e, resp.client, isProxy)
			e.s.SetConnInfo(&ConnInfo{
				hostName: link,
				reqChan:  newreq,
				isProxy:  isProxy,
				isDirect: false,
			})
		} else {
			// This is a direct connect.
			e.o.Debug("resolve(): Direct connect to: %s\n", link)
			if e.s.ConnExists(link) {
				e.o.Debug("resolve(): %s connection already exists.\n", link)
				continue
			}
			reqChan := make(chan interface{})
			var localConfig = new(ssh.ClientConfig)
			*localConfig = *(e.s.GetSSHConfig())
			if e.c.Password {
				pwClosure := func() (string, error) {
					host := e.s.GetPTR(link)
					e.s.SetRequiresPw(host)
					return e.s.GetAuthPass(), nil
				}
				localConfig.Auth = append(
					localConfig.Auth,
					ssh.PasswordCallback(pwClosure),
				)
			}
			var wg sync.WaitGroup
			for i := 0; i < e.c.BastionConns; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					timeoutChan := make(chan bool, 1)
					dialChan := make(chan *ssh.Client)
					errChan := make(chan error)

					go directConnect(link, dialChan, errChan, localConfig)
					go sleep(timeoutChan, timeout)

					select {
					case sshClient := <-dialChan:
						go remoteHost(link, id, reqChan, e, sshClient, isProxy)
						e.s.SetConnInfo(&ConnInfo{
							hostName: link,
							reqChan:  reqChan,
							isProxy:  isProxy,
							isDirect: true,
						})
						go func() { <-timeoutChan }()
					case dcErr := <-errChan:
						e.o.Debug("resolve(): Err: %s from directConnect\n", dcErr)
						go func() { <-timeoutChan }()
						return
					case <-timeoutChan:
						go func() {
							select {
							case <-errChan:
							case <-dialChan:
								// Debug("Caught a direct straggler.\n")
								return
							}
						}()
						return
					}
					return
				}(i)
			}
			wg.Wait()
		}
	}
	return nil
}

// The directConnect function will connect you directly to a host, not through
// a proxy. Used when setting up proxies initially.
func directConnect(link string, done chan<- *ssh.Client, echan chan<- error, cfg *ssh.ClientConfig) {
	dest := link + ":" + SSHPort
	client, err := ssh.Dial("tcp", dest, cfg)
	if err != nil {
		echan <- err
		return
	}
	done <- client
	return
}

func sleep(done chan<- bool, timeout int) {
	time.Sleep(time.Duration(timeout) * time.Second)
	done <- true
	return
}

// The resolveProxies function goes through all the targeted hosts and figures
// out which ones are proxies. It then serially makes connections to all the
// proxies before we start using them to connect to other hosts.
//
// If we don't serially connect to the proxies first, we can end up with a
// situation when we connect to all the targeted hosts in parallel where we
// needlessly establish more connections to proxies than we intended. This is
// because initially if the proxy is not connected, a number of go routines will
// all realize they need to connect the proxy at once.
func resolveProxies(e Env) error {
	chainMap := make(map[string]int)
	e.o.Debug("Inside resolveProxies()\n")
	hk := e.s.GetHostKeys()
	for j := range hk {
		hostname := hk[j]
		hi, err := e.s.GetHostInfo(hostname)
		if err != nil {
			return err
		}
		length := len(hi.chain)
		if length == 1 {
			// direct connect
			continue
		}
		e.o.Debug("resolveProxies(): host: %s, chain: %v, length: %d\n", hostname, hi.chain, length)
		chain := strings.Join(hi.chain[:length-1], " ")
		if val, exists := chainMap[chain]; exists {
			chainMap[chain] = val + 1
		} else {
			chainMap[chain] = 1
			err := resolve(strings.Split(chain, " "), e, true, e.c.Timeout)
			if err != nil {
				e.o.ErrExit("Could not resolve proxy: %s\n", err)
			}
		}
	}
	return nil
}
