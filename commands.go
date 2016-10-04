/*
 * commands.go
 *
 * This file contains the commands used by the CLI interface to MetaSSH
 * that you get when you SSH into the metassh server. You can also access
 * these commands without an interactive session via SSH.
 *
 */

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bmizerany/perks/quantile"
	"github.com/ogier/pflag"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/terminal"
	"golang.org/x/sys/unix"
)

type cmdErr struct {
	fatal bool
	msg   string
}

func (e *cmdErr) Error() string {
	return e.msg
}

func (e *cmdErr) Fatal() bool {
	return e.fatal
}

func newCmdErr(fatal bool, text string) error {
	return &cmdErr{fatal, text}
}

type command struct {
	cmd  func(Env, []string) error
	help string
}

var commands map[string]command

// Avoid an initialization loop using init()
func init() {
	commands = map[string]command{
		"state":    {state, "Get the state of in-flight connections/runs."},
		"run":      {run, "Run a command on loaded/connected targets."},
		"target":   {target, "Target some hosts to connect/run commands on."},
		"clear":    {clear, "Clear the list of targets, implies disco."},
		"clean":    {clean, "Removed timed out host from the target list."},
		"exit":     {quit, "I'm outta here."},
		"quit":     {quit, "Cya."},
		"abort":    {abort, "Abort any in-flight connections/runs."},
		"disco":    {disco, "Disconnect all targeted hosts."},
		"connect":  {connect, "Connect all targeted hosts."},
		"summary":  {summary, "Show some stats about connections/runs."},
		"spool":    {spool, "Toggle spool state."},
		"spooldir": {spooldir, "Set or print the spool directory."},
		"help":     {help, "This help screen."},
		"quant":    {quant, "Show some quantiles."},
	}
}

func quant(e Env, args []string) error {
	type config struct {
		Run     bool `short:"r" desc:"Show the specified run quantile"`
		Connect bool `short:"c" desc:"Show the specified connect quantile."`
	}
	cfg := &config{false, false}
	f, err := reflectFlags("quant", cfg, e.o)
	if err != nil {
		return err
	}
	if err = f.Parse(args); err != nil {
		return err
	}
	if !cfg.Run && !cfg.Connect {
		return errors.New("Need to specify either -r or -c.")
	}
	bands := f.Args()

	var start, end float64

	if len(bands) == 1 {
		start, err = strconv.ParseFloat(bands[0], 64)
		if err != nil {
			return err
		}
		switch start {
		case .99:
			end = 1.0
		case .95:
			end = .99
		case .90:
			end = .95
		case .01:
			start = 0.0
			end = 0.1
		default:
			return errors.New("Must specfy a range like, 0.95 0.99.")
		}
	} else if len(bands) == 2 {
		start, err = strconv.ParseFloat(bands[0], 64)
		if err != nil {
			return err
		}
		end, err = strconv.ParseFloat(bands[1], 64)
		if err != nil {
			return err
		}
		if start > end {
			return errors.New("First number must be smaller than second number.")
		}
		if start >= 1 || end > 1 {
			return errors.New("Both start and end values must be <= 1.0")
		}
	} else {
		return errors.New("Must specfy a range like, 0.95 0.99.")
	}

	var hk = e.s.GetHostKeys()
	qConnect := quantile.NewBiased()
	qRun := quantile.NewBiased()

	for i := range hk {
		hostname := hk[i]
		hi, err := e.s.GetHostInfo(hostname)
		if err != nil {
			e.o.Debug("GetHostInfo(): %s\n", err)
			continue
		}
		if hi.runOK && hi.runOnce {
			qRun.Insert(hi.runTime.Seconds())
		}
		if hi.connectedOK {
			qConnect.Insert(hi.connectTime.Seconds())
		}
	}

	conResults := make(map[string]float64)
	runResults := make(map[string]float64)

	for i := range hk {
		hostname := hk[i]
		hi, err := e.s.GetHostInfo(hostname)
		if err != nil {
			e.o.Debug("GetHostInfo(): %s\n", err)
			continue
		}
		runTime := hi.runTime.Seconds()
		conTime := hi.connectTime.Seconds()

		if conTime >= qConnect.Query(start) && conTime <= qConnect.Query(end) {
			conResults[hostname] = conTime
		}
		if runTime >= qRun.Query(start) && runTime <= qRun.Query(end) {
			runResults[hostname] = runTime
		}
	}
	if cfg.Connect {
		outputResults(e, conResults, "Connect")
	}
	if cfg.Run {
		outputResults(e, runResults, "Run")
	}
	return nil
}

func outputResults(e Env, m map[string]float64, what string) {
	hostKeys := sortedRevFloat64Keys(m)
	for i := range hostKeys {
		xTime := m[hostKeys[i]]
		e.o.Out("%s time (%s): %05.5fs\n", what, hostKeys[i], xTime)
	}
}

func spooldir(e Env, args []string) error {
	if len(args) > 0 {
		path := args[0]
		if !writable(path) {
			msg := fmt.Sprintf("ERROR: Directory %s, not writable.", path)
			return errors.New(msg)
		}
		e.c.SpoolDir = path
	}
	e.o.Out("SpoolDir: %s\n", e.c.SpoolDir)
	return nil
}

func spool(e Env, args []string) error {
	if e.c.Spool {
		e.o.Out("Spooling is now OFF.\n")
		e.c.Spool = false
	} else {
		if !writable(e.c.SpoolDir) {
			msg := fmt.Sprintf("%s: not writable. Spooling OFF.", e.c.SpoolDir)
			return errors.New(msg)
		}
		e.o.Out("Spooling is now ON.\n")
		e.c.Spool = true
	}
	return nil
}

func clean(e Env, args []string) error {
	var hk = e.s.GetHostKeys()
	var wg sync.WaitGroup
	i := 0
	startTime := time.Now()
	for j := range hk {
		host := hk[j]
		hi, err := e.s.GetHostInfo(host)
		if err != nil {
			e.o.Debug("GetHostInfo(): %s\n", err)
			continue
		}
		if !hi.connectedOK || (!hi.runOK && hi.runOnce) {
			wg.Add(1)
			i++
			go func(host string, hi HostInfo) {
				defer wg.Done()
				if hi.connectedOK {
					err := disconnectHost(e, host)
					if err != nil {
						e.o.Debug("disconnectHost(): %s\n", err)
					}
				}
				e.s.DeleteHostInfo(host)
			}(host, hi)
		}
	}
	wg.Wait()
	format := "Cleaned out %d targets in %.2fs.\n"
	e.o.Out(format, i, time.Since(startTime).Seconds())
	return nil
}

func max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

func help(e Env, args []string) error {
	sorted := make([]string, len(commands))
	longest := 0
	i := 0
	for k := range commands {
		sorted[i] = k
		i++
		longest = max(longest, len(k))
	}
	sort.Strings(sorted)
	e.o.Out("Available commands:\n\n")
	for i := range sorted {
		cmdName := sorted[i]
		format := fmt.Sprintf("%%-%ds  %%s\n", longest)
		e.o.Out(format, cmdName, commands[cmdName].help)
	}
	e.o.Out("\n")
	return nil
}

func run(e Env, args []string) error {
	newEnv := e
	type config struct {
		Background bool `short:"b" desc:"Run in the background, don't wait."`
		Timeout    int  `short:"t" desc:"Run timeout in seconds."`
		Quiet      bool `short:"q" desc:"No output please."`
	}
	cfg := &config{false, e.c.Timeout, false}
	f, err := reflectFlags("run", cfg, e.o)
	if err != nil {
		return err
	}
	if err := f.Parse(args); err != nil {
		return err
	}
	cmdline := strings.Join(f.Args(), " ")
	if cfg.Quiet {
		o := NewOutput(e.o.DupeOuput())
		o.Mute()
		newEnv.o = o
	}
	if cfg.Background {
		go runEverywhere(cmdline, newEnv, cfg.Timeout)
		return nil
	}
	startTime := time.Now()
	runEverywhere(cmdline, newEnv, cfg.Timeout)
	e.o.Out("Done in %.2fs.\n", time.Since(startTime).Seconds())
	return nil
}

func target(e Env, args []string) error {
	var wg sync.WaitGroup
	outbuf := new(bytes.Buffer)
	errbuf := new(bytes.Buffer)

	cmd := exec.Command(e.c.TargetCmd, args...)
	// We expect a JSON blob on STDOUT.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// We expect some plaintext to be sent to the user on STDERR.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		e.o.Debug("cmd.Start(): %s\n", err)
		return err
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err = io.Copy(outbuf, stdout)
		if err != nil {
			e.o.Debug("io.Copy() failed: %s\n", err)
		}
	}()
	go func() {
		defer wg.Done()
		_, err := io.Copy(errbuf, stderr)
		if err != nil {
			e.o.Debug("io.Copy() failed: %s\n", err)
		}
	}()
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		var exitCode int
		if ee, ok := err.(*exec.ExitError); ok {
			if status, ok := ee.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			} else {
				e.o.Debug("Unknown exit code, faking it.\n")
				exitCode = 255
			}
		} else {
			e.o.Debug("Unknown exit code, faking it.\n")
			exitCode = 255
		}
		e.o.Out(errbuf.String())
		msg := fmt.Sprintf("Command returned non-zero (%d) exit.", exitCode)
		return errors.New(msg)
	}
	e.o.Out(errbuf.String())
	if len(outbuf.Bytes()) > 0 {
		j := NewJSON(e)
		n, err := j.LoadBlob(outbuf.Bytes())
		if err != nil {
			return errors.New("Load JSON from STDOUT: " + err.Error())
		}
		e.o.Out("Targeted %d hosts.\n", n)
	}
	return nil
}

func clear(e Env, args []string) error {
	disconnectEverywhere(e, true)
	e.s.ClearHostInfo()
	e.o.Out("Targets cleared.\n")
	return nil
}

func summary(e Env, args []string) error {
	type config struct {
		Verbose bool `short:"v" desc:"Verbose output."`
	}
	cfg := &config{false}
	f, err := reflectFlags("summary", cfg, e.o)
	if err != nil {
		return err
	}
	if err := f.Parse(args); err != nil {
		return err
	}
	printSummary(e, cfg.Verbose)
	return nil
}

func state(e Env, args []string) error {
	type config struct {
		Run  bool `short:"r" desc:"Show the state of in-flight commands"`
		Conn bool `short:"c" desc:"Show the state of in-flight connections"`
	}
	cfg := &config{false, false}
	f, err := reflectFlags("state", cfg, e.o)
	if err != nil {
		return err
	}
	if err := f.Parse(args); err != nil {
		return err
	}
	wi := e.s.GetWaiterInfo()
	if cfg.Conn || (!cfg.Conn && !cfg.Run) {
		e.o.Out(
			"%d connection threads (%.2fs average wait time)\n",
			wi.connWaiters,
			wi.avgConnWait.Seconds(),
		)
		connStateKeys := sortedIntKeys(wi.connStates)
		for i := range connStateKeys {
			state := connStateKeys[i]
			count := wi.connStates[state]

			e.o.Out("\t%s(%d)\n", state, count)
		}
	}
	if cfg.Run || (!cfg.Conn && !cfg.Run) {
		e.o.Out(
			"%d run threads (%.2fs average wait time)\n",
			wi.runWaiters,
			wi.avgRunWait.Seconds(),
		)
		runStateKeys := sortedIntKeys(wi.runStates)
		for i := range runStateKeys {
			state := runStateKeys[i]
			count := wi.runStates[state]

			e.o.Out("\t%s(%d)\n", state, count)
		}
	}
	return nil
}

func connect(e Env, args []string) error {
	type config struct {
		Background bool `short:"b" desc:"Run in the background, don't wait."`
		Timeout    int  `short:"t" desc:"Connection timeout in seconds."`
	}
	cfg := &config{false, e.c.Timeout}
	f, err := reflectFlags("connect", cfg, e.o)
	if err != nil {
		return err
	}
	if err := f.Parse(args); err != nil {
		return err
	}
	if cfg.Background {
		go connectEverywhere(e, cfg.Timeout)
		e.o.Out("Ok. Use the state command to track connection progress.\n")
		return nil
	}
	totalTime := time.Now()
	count := connectEverywhere(e, cfg.Timeout)
	e.o.Out("%d hosts in %.2fs.\n", count, time.Since(totalTime).Seconds())
	return nil
}

func disco(e Env, args []string) error {
	disconnectEverywhere(e, true)
	return nil
}

func quit(e Env, args []string) error {
	return newCmdErr(true, "byte!\n")
}

func abort(e Env, a []string) error {
	e.s.TimeoutWaiters()
	return nil
}

func runCliCmd(e Env, cmd string, args []string) bool {
	if _, ok := commands[cmd]; ok {
		err := commands[cmd].cmd(e, args)
		if err != nil {
			if err != pflag.ErrHelp {
				e.o.Out("%s: %s\n", cmd, err)
			}
			if cerr, ok := err.(*cmdErr); ok {
				if cerr.Fatal() {
					return false
				}
			}
		}
	} else {
		e.o.Out("Unknown command: '%s'.\n", cmd)
	}
	return true
}

func cli(channel ssh.Channel, e Env, useReadline bool) {
	var t *terminal.Terminal
	defer func() {
		err := channel.Close()
		if err != nil {
			e.o.Debug("channel.Close(): %s\n", err)
		}
	}()

	if !useReadline {
		t = terminal.NewTerminal(channel, ServerPrompt)
	}

	for {
		var line string
		var err error

		if useReadline {
			line, err = readline(ServerPrompt)
		} else {
			line, err = t.ReadLine()
		}
		if err != nil {
			e.o.Debug("readline.Readline(): %s\n", err)
		}
		// This is to allow the goroutine that shuttles data beween
		// the readline pty and the e.o object plenty of time to write
		// the ending newline before the command starts to output.
		time.Sleep(time.Duration(10) * time.Millisecond)
		if line == "" {
			continue
		}
		chunks := strings.Fields(line)
		cmd := strings.ToLower(chunks[0])
		if !runCliCmd(e, cmd, chunks[1:]) {
			return
		}
	}
}

func writable(path string) bool {
	return unix.Access(path, unix.W_OK) == nil
}
