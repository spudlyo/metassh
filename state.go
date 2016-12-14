/*
 * state.go
 *
 * This file contains data structures that represent the program's global state
 * and a bunch of functions that manipulate this state in a thread-safe way.
 *
 */

package main

import (
	"errors"
	"time"

	"golang.org/x/crypto/ssh"
)

// HostInfo is a struct that contains all the information you need to make
// an SSH connection to a host (ip address, proxies) as well as some info
// on if we were able to successfully connect and maybe run a command on
// the host, and how long it took.
type HostInfo struct {
	hostName    string
	ipAddress   string
	chain       []string
	requiresPw  bool
	connectedOK bool
	connectTime time.Duration
	runTime     time.Duration
	runOK       bool
	runOnce     bool
	lastError   error
}

// ConnInfo is a struct that contains information about a connection that
// we've successfully made to a host, most importantly the channel we use to
// send messages to the goroutine that services requests for this connection.
type ConnInfo struct {
	hostName   string
	isProxy    bool
	isDirect   bool
	proxyCount int
	reqChan    chan<- interface{}
}

// WaiterInfo is a struct that keeps track of how many connections or remote
// command execxutions are "in flight", and the average amount of time they've
// been that way.
type WaiterInfo struct {
	connWaiters int
	runWaiters  int
	avgConnWait time.Duration
	avgRunWait  time.Duration
	connStates  map[string]int
	runStates   map[string]int
}

type getPTR struct {
	hostName string
	respChan chan<- string
}

// GetPTR is basically a reverse DNS lookup. Give it an IP and it returns
// a hostname. This is needed because we use IP addresses when connecting
// to avoid DNS lookups, but we want human readable names in the various
// maps where we keep track of state.
func (s *State) GetPTR(ip string) string {
	respChan := make(chan string)
	s.reqChan <- getPTR{ip, respChan}
	resp := <-respChan
	return resp
}

type setConnectionStatus struct {
	hostName    string
	connectedOK bool
	connectTime time.Duration
	lastError   error
}

// SetConnectionStatus sets the connection status for a host. Did it connect
// OK, how long did it take? What was the error if not?
func (s *State) SetConnectionStatus(scs setConnectionStatus) {
	s.reqChan <- scs
}

type setRunWaitInfo struct {
	wi waitInfo
}

type setConnWaitState struct {
	hostName string
	state    string
}

type setRunWaitState struct {
	hostName string
	state    string
}

type setRunStatus struct {
	hostName  string
	runOK     bool
	runOnce   bool
	runTime   time.Duration
	lastError error
	cmdOutput string
}

// SetRunStatus is like SetConnectionStatus but for command execution rather
// than connection state.
func (s *State) SetRunStatus(srs setRunStatus) {
	s.reqChan <- srs
}

type incProxyCount struct {
	hostName string
}

// IncProxyCount keeps a record of how many connections have been proxied
// through a specific bastion host.
func (s *State) IncProxyCount(hostName string) {
	s.reqChan <- incProxyCount{hostName}
}

type getAuthPass struct {
	respChan chan<- string
}

// GetAuthPass returns the global password for all hosts, which is used in the
// event pubkey auth fails.
func (s *State) GetAuthPass() string {
	respChan := make(chan string)
	s.reqChan <- getAuthPass{respChan}
	resp := <-respChan
	return resp
}

type setAuthPass struct {
	sshAuthPass string
}

// SetAuthPass lets you set a global password which can be used in the event that
// pubkey auth fails for some reason. This lets us track which hosts fail pubkey
// login but can still be logged into with a password.
// FIXME: This assumes that all hosts can be accessed using the same user/pass.
func (s *State) SetAuthPass(sshAuthPass string) {
	s.reqChan <- setAuthPass{sshAuthPass}
}

type setRequiresPw struct {
	hostName string
}

// SetRequiresPw marks a host as requiring a password, meaning it can't
// be logged into using a public/private keypair.
func (s *State) SetRequiresPw(hostName string) {
	s.reqChan <- setRequiresPw{hostName}
}

type getSSHConfig struct {
	respChan chan<- *ssh.ClientConfig
}

// GetSSHConfig returns the global SSH client config.
// FIXME: This assumes all hosts can be logged in with the same pubkey/username.
func (s *State) GetSSHConfig() *ssh.ClientConfig {
	respChan := make(chan *ssh.ClientConfig)
	s.reqChan <- getSSHConfig{respChan}
	resp := <-respChan
	return resp
}

type setSSHConfig struct {
	sshConfig *ssh.ClientConfig
}

// SetSSHConfig sets the global SSH client configuration used by all hosts to
// connect to remote SSH servers.
func (s *State) SetSSHConfig(sshConfig *ssh.ClientConfig) {
	s.reqChan <- setSSHConfig{sshConfig}
}

type hostExists struct {
	hostName string
	respChan chan<- bool
}

type getHostInfo struct {
	hostName string
	respChan chan<- HostInfo
}

type deleteHostInfo struct {
	hostName string
	respChan chan<- bool
}

type clearHostInfo struct {
	respChan chan<- bool
}

// GetHostInfo returns a HostInfo struct that contains information about a host
// that we may or may not have successfully connected to and run a command on.
// Currently this info comes from the JSON dump we read at startup.
func (s *State) GetHostInfo(hostName string) (HostInfo, error) {
	if !s.HostExists(hostName) {
		err := errors.New("Host '" + hostName + "' does not exit.")
		return HostInfo{}, err
	}
	respChan := make(chan HostInfo)
	ghi := getHostInfo{
		hostName: hostName,
		respChan: respChan,
	}
	s.reqChan <- ghi
	resp := <-respChan
	return resp, nil
}

// ClearHostInfo initializes the HostInfo map. This is useful if you want to
// work on a brand new set of targets.
func (s *State) ClearHostInfo() {
	respChan := make(chan bool)
	chi := clearHostInfo{respChan}
	s.reqChan <- chi
	<-respChan
	return
}

// HostExists will return true if the given host exists in our target host
// data.
func (s *State) HostExists(hostName string) bool {
	respChan := make(chan bool)
	ce := hostExists{
		hostName: hostName,
		respChan: respChan,
	}
	s.reqChan <- ce
	resp := <-respChan
	return resp
}

// SetHostInfo adds a new host to the hash of hosts that we know about.
func (s *State) SetHostInfo(hi HostInfo) {
	s.reqChan <- hi
}

// DeleteHostInfo deletes a target host from the map, this is useful for
// culling hosts that have connection/run problems.
func (s *State) DeleteHostInfo(hostName string) {
	respChan := make(chan bool)
	dhi := deleteHostInfo{hostName, respChan}
	s.reqChan <- dhi
	<-respChan
}

type getHostKeys struct {
	respChan chan<- []string
}

// GetHostKeys returns a list of all the hosts currently "targeted".
func (s *State) GetHostKeys() []string {
	respChan := make(chan []string)
	s.reqChan <- getHostKeys{respChan}
	resp := <-respChan
	return resp
}

type connExists struct {
	hostName string
	respChan chan<- bool
}

type getConnInfo struct {
	hostName string
	respChan chan<- ConnInfo
}

// GetConnInfo returns information about a host we've successfully connected to
// The most important information here is the channel that we can use to send
// commands to. This allows us to run a command on a host, or use it as a proxy.
func (s *State) GetConnInfo(hostName string) (ConnInfo, error) {
	if !s.ConnExists(hostName) {
		err := errors.New("Connection '" + hostName + "' does not exist.")
		return ConnInfo{}, err
	}
	respChan := make(chan ConnInfo)
	gci := getConnInfo{
		hostName: hostName,
		respChan: respChan,
	}
	s.reqChan <- gci
	resp := <-respChan
	return resp, nil
}

type setConnInfo struct {
	ci ConnInfo
}

// SetConnInfo marks a host as having an active, successful connection, and
// stores some metadata about it. Most importantly the channel used to
// communicate with the goroutine that services this connection.
func (s *State) SetConnInfo(ci *ConnInfo) {
	ci.hostName = s.GetPTR(ci.hostName)
	s.reqChan <- setConnInfo{*ci}
}

// ConnExists will return true if the given host has an established SSH
// connection which means there is a ConnInfo struct for it.
func (s *State) ConnExists(hostName string) bool {
	respChan := make(chan bool)
	ce := connExists{
		hostName: hostName,
		respChan: respChan,
	}
	s.reqChan <- ce
	resp := <-respChan
	return resp
}

type deleteConnInfo struct {
	hostName string
	respChan chan<- bool
}

// DeleteConnInfo deletes a connection from the map, you'd want to do this
// if the connection is no longer connected.
func (s *State) DeleteConnInfo(hostName string) {
	respChan := make(chan bool)
	dci := deleteConnInfo{hostName, respChan}
	s.reqChan <- dci
	<-respChan
}

type getConnKeys struct {
	respChan chan<- []string
}

// GetConnKeys returns a list of all the hosts we have connections to.
func (s *State) GetConnKeys() []string {
	respChan := make(chan []string)
	s.reqChan <- getConnKeys{respChan}
	resp := <-respChan
	return resp
}

type getWaiterInfo struct {
	respChan chan<- WaiterInfo
}

// GetWaiterInfo returns you a WaiterInfo struct, which has some stats
// on all connections and command executions that are "in flight".
func (s *State) GetWaiterInfo() WaiterInfo {
	respChan := make(chan WaiterInfo)
	s.reqChan <- getWaiterInfo{respChan}
	resp := <-respChan
	return resp
}

const (
	stateDialing      = "dialing connection"
	stateEstablishing = "establishing new client connection"
	stateNewClient    = "creating new client"
	stateDone         = "done"
	stateNewSession   = "creating new session"
	stateStartSession = "starting session"
	stateRunning      = "running"
)

type waitInfo struct {
	hostName    string
	state       string
	startTime   time.Time
	timeoutChan chan<- bool
}

type setConnWaitInfo struct {
	wi waitInfo
}

// SetConnWaitInfo marks a connection to a host as "in flight" so we can
// keep stats on such things.
func (s *State) SetConnWaitInfo(wi *waitInfo) {
	wi.hostName = s.GetPTR(wi.hostName)
	s.reqChan <- setConnWaitInfo{*wi}
}

// SetConnWaitState sets the connection state for an existing waitInfo struct.
func (s *State) SetConnWaitState(hostName, state string) {
	hostName = s.GetPTR(hostName)
	s.reqChan <- setConnWaitState{hostName, state}
}

// SetRunWaitState sets the run state for an existing witInfo struct.
func (s *State) SetRunWaitState(hostName, state string) {
	hostName = s.GetPTR(hostName)
	s.reqChan <- setRunWaitState{hostName, state}
}

type deleteConnWaitInfo struct {
	hostName string
}

// DeleteConnWaitInfo is called when a connection has either completed or
// failed to remove it from the list of pending connections.
func (s *State) DeleteConnWaitInfo(hostName string) {
	s.reqChan <- deleteConnWaitInfo{s.GetPTR(hostName)}
}

// SetRunWaitInfo marks a remote command execution as "in flight" so we can
// keep stats on such things.
func (s *State) SetRunWaitInfo(wi *waitInfo) {
	wi.hostName = s.GetPTR(wi.hostName)
	s.reqChan <- setRunWaitInfo{*wi}
}

type deleteRunWaitInfo struct {
	hostName string
}

// DeleteRunWaitInfo is called when a command has either completed or
// failed to remove it from the list of pending command executions.
func (s *State) DeleteRunWaitInfo(hostName string) {
	s.reqChan <- deleteRunWaitInfo{s.GetPTR(hostName)}
}

type timeoutWaiters struct {
	respChan chan<- bool
}

// TimeoutWaiters sends a 'fake' timeout message to each of the
// connection or run waiters, so that they finish up immediately.  It's
// away of aborting all connections or commands that haven't completed
// yet without waiting for them to actually timeout.
func (s *State) TimeoutWaiters() {
	respChan := make(chan bool)
	s.reqChan <- timeoutWaiters{respChan}
	<-respChan
}

// State is a singleton object that holds all global program information.
// These would be obnoxious global variables if we didn't need to serialize
// access to them to ensure that reading and writing them is thread-safe.
type State struct {
	targets     map[string]*HostInfo
	conn        map[string]*ConnInfo
	PTR         map[string]string
	connWaiters map[string]*waitInfo
	runWaiters  map[string]*waitInfo
	reqChan     chan interface{}
	sshConfig   *ssh.ClientConfig
	sshAuthPass string
}

// NewState will return you an initialized State object. This also runs the
// serializer goroutine which serializes all access to state variables.
func NewState() *State {
	s := new(State)

	s.targets = make(map[string]*HostInfo)
	s.conn = make(map[string]*ConnInfo)
	s.PTR = make(map[string]string)
	s.connWaiters = make(map[string]*waitInfo)
	s.runWaiters = make(map[string]*waitInfo)
	s.reqChan = make(chan interface{})

	go s.serializer()
	return s
}

// This long and ugly function is what everything that wants to manipulate the
// global program state must go through. Since there is only one of these,
// writes and reads against the global program state are 'serialized' here.
func (s *State) serializer() {
	for {
		req := <-s.reqChan
		switch req.(type) {
		case timeoutWaiters:
			twReq := req.(timeoutWaiters)
			for k := range s.connWaiters {
				s.connWaiters[k].timeoutChan <- false
			}
			for k := range s.runWaiters {
				s.runWaiters[k].timeoutChan <- false
			}
			twReq.respChan <- true
		case getWaiterInfo:
			var totalConnSince, totalRunSince time.Duration
			var connAvgWait, runAvgWait time.Duration

			connStates := make(map[string]int)
			runStates := make(map[string]int)

			for k := range s.connWaiters {
				connStates[s.connWaiters[k].state]++
				totalConnSince += time.Since(s.connWaiters[k].startTime)
			}
			for k := range s.runWaiters {
				runStates[s.runWaiters[k].state]++
				totalRunSince += time.Since(s.runWaiters[k].startTime)
			}
			gwiReq := req.(getWaiterInfo)
			connWaiters := len(s.connWaiters)
			runWaiters := len(s.runWaiters)
			if connWaiters > 0 {
				connAvgWait = totalConnSince / time.Duration(connWaiters)
			}
			if runWaiters > 0 {
				runAvgWait = totalRunSince / time.Duration(runWaiters)
			}
			gwiReq.respChan <- WaiterInfo{
				connWaiters,
				runWaiters,
				connAvgWait,
				runAvgWait,
				connStates,
				runStates,
			}
		case deleteConnWaitInfo:
			dcwiReq := req.(deleteConnWaitInfo)
			delete(s.connWaiters, dcwiReq.hostName)
		case deleteRunWaitInfo:
			drwiReq := req.(deleteRunWaitInfo)
			delete(s.runWaiters, drwiReq.hostName)
		case setConnWaitInfo:
			scwiReq := req.(setConnWaitInfo)
			s.connWaiters[scwiReq.wi.hostName] = &scwiReq.wi
		case setRunWaitInfo:
			srwiReq := req.(setRunWaitInfo)
			s.runWaiters[srwiReq.wi.hostName] = &srwiReq.wi
		case setConnWaitState:
			scwsReq := req.(setConnWaitState)
			if _, exists := s.connWaiters[scwsReq.hostName]; exists {
				s.connWaiters[scwsReq.hostName].state = scwsReq.state
			}
		case setRunWaitState:
			srwsReq := req.(setRunWaitState)
			if _, exists := s.runWaiters[srwsReq.hostName]; exists {
				s.runWaiters[srwsReq.hostName].state = srwsReq.state
			}
		case setRequiresPw:
			srpReq := req.(setRequiresPw)
			s.targets[srpReq.hostName].requiresPw = true
		case getPTR:
			gpReq := req.(getPTR)
			if ptr, exists := s.PTR[gpReq.hostName]; exists {
				gpReq.respChan <- ptr
			} else {
				gpReq.respChan <- gpReq.hostName
			}
		case setConnectionStatus:
			scsReq := req.(setConnectionStatus)
			if _, exists := s.targets[scsReq.hostName]; exists {
				s.targets[scsReq.hostName].connectedOK = scsReq.connectedOK
				s.targets[scsReq.hostName].connectTime = scsReq.connectTime
				s.targets[scsReq.hostName].lastError = scsReq.lastError
			}
		case setRunStatus:
			srsReq := req.(setRunStatus)
			if _, exists := s.targets[srsReq.hostName]; exists {
				s.targets[srsReq.hostName].runOK = srsReq.runOK
				s.targets[srsReq.hostName].runOnce = srsReq.runOnce
				s.targets[srsReq.hostName].runTime = srsReq.runTime
				s.targets[srsReq.hostName].lastError = srsReq.lastError
			}
		case incProxyCount:
			ipcReq := req.(incProxyCount)
			s.conn[ipcReq.hostName].proxyCount++
		case getAuthPass:
			gapReq := req.(getAuthPass)
			gapReq.respChan <- s.sshAuthPass
		case setAuthPass:
			sapReq := req.(setAuthPass)
			s.sshAuthPass = sapReq.sshAuthPass
		case getSSHConfig:
			gscReq := req.(getSSHConfig)
			gscReq.respChan <- s.sshConfig
		case setSSHConfig:
			sscReq := req.(setSSHConfig)
			s.sshConfig = sscReq.sshConfig
		case HostInfo:
			hiReq := req.(HostInfo)
			s.targets[hiReq.hostName] = &hiReq
			s.PTR[hiReq.ipAddress] = hiReq.hostName
		case setConnInfo:
			sciReq := req.(setConnInfo)
			if _, exists := s.conn[sciReq.ci.hostName]; !exists {
				s.conn[sciReq.ci.hostName] = &sciReq.ci
			}
		case getHostInfo:
			ghiReq := req.(getHostInfo)
			ghiReq.respChan <- *s.targets[ghiReq.hostName]
		case deleteHostInfo:
			dhiReq := req.(deleteHostInfo)
			delete(s.targets, dhiReq.hostName)
			dhiReq.respChan <- true
		case clearHostInfo:
			chiReq := req.(clearHostInfo)
			// this shit is garbage collected right?
			s.targets = make(map[string]*HostInfo)
			chiReq.respChan <- true
		case hostExists:
			heReq := req.(hostExists)
			_, exists := s.targets[heReq.hostName]
			heReq.respChan <- exists
		case getConnInfo:
			gciReq := req.(getConnInfo)
			gciReq.respChan <- *s.conn[gciReq.hostName]
		case connExists:
			ceReq := req.(connExists)
			_, exists := s.conn[ceReq.hostName]
			ceReq.respChan <- exists
		case deleteConnInfo:
			dciReq := req.(deleteConnInfo)
			delete(s.conn, dciReq.hostName)
			dciReq.respChan <- true
		case getHostKeys:
			var keys []string
			ghkReq := req.(getHostKeys)
			for k := range s.targets {
				keys = append(keys, k)
			}
			ghkReq.respChan <- keys
		case getConnKeys:
			var keys []string
			gckReq := req.(getConnKeys)
			for k := range s.conn {
				keys = append(keys, k)
			}
			gckReq.respChan <- keys
		}
	}
}
