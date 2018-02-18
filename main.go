/*
 * main.go
 *
 * This is where execution starts, it has the main() function which sets
 * everything up. It also has the logic for daemonizing.
 *
 */

package main

import (
	"fmt"
	"os"
	"path"
	"syscall"
	"time"

	flag "github.com/ogier/pflag"
	"github.com/vividcortex/godaemon"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/terminal"
)

// Some hardcoded constants used throughout the program.
const (
	ServerPort   = "2222"          // Default port for SSH server feature
	SSHPort      = "22"            // Stock SSH port
	UnknownError = "unknown error" // When we can't string match on an error
	ServerPrompt = "metassh> "     // Prompt you get when you SSH in
	TestCommand  = "exit 0"        // Used to test remote command execution
	TmpDir       = "/.ssh"         // Used for dicey ssh key manpulations
	KeepAlive    = "keepalive@openssh.com"
	SSHKeygen    = "/usr/bin/ssh-keygen"
)

// The Env struct contains some necessary program state that is passed around
// between all the various functions. This is not exactly elegant.
type Env struct {
	s *State
	o *Output
	c *Config
}

func main() {
	var sshClientConfig *ssh.ClientConfig
	var clientPrivateKey, serverPrivateKey *os.File

	c := DefaultConfig()
	f, err := reflectFlags(path.Base(os.Args[0]), c, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not parse flags: %s\n", err)
		os.Exit(1)
	}
	if err = f.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		} else {
			os.Exit(1)
		}
	}
	s := NewState()
	o := NewOutput(os.Stdout, os.Stderr, false, c.Debug)
	e := Env{s, o, c}

	// Daemonize implies server, forbids password.
	if e.c.Daemonize {
		if e.c.Password {
			e.o.ErrExit("Can't use --daemonize with --password.")
		}
		e.c.Server = true
	}
	// If we're the parent, let's do some things that may require user input.
	if godaemon.Stage() == godaemon.StageParent {
		if !e.c.Agent {
			clientPrivateKey, err = getPrivateKeyFile(e.c.Key, e)
			if err != nil {
				e.o.ErrExit("Couldn't open SSH private Key: %s\n", err)
			}
		}
		if e.c.Server {
			serverPrivateKey, err = getPrivateKeyFile(e.c.HostKey, e)
			if err != nil {
				e.o.ErrExit("Couldn't open SSH server Key: %s\n", err)
			}
		}
	}
	if e.c.Daemonize {
		var files []**os.File
		e.o.Debug("Daemonizing.\n")
		e.o.Mute()
		// The Daemonize process using this library actually re-runs your
		// program like three times to set everything up properly. Each time
		// we re-run ourselves, we inherit these two open file descriptors.
		if !e.c.Agent {
			files = append(files, &clientPrivateKey)
		}
		files = append(files, &serverPrivateKey)
		_, _, err = godaemon.MakeDaemon(&godaemon.DaemonAttr{Files: files})
		if err != nil {
			msg := fmt.Sprintf("godaemon.MakeDaemon() failed: %s\n", err)
			panic(msg)
		}
	}
	if e.c.Password {
		var pw []byte
		e.o.Out("Password to use for auth: ")
		pw, err = terminal.ReadPassword(syscall.Stdin)
		e.o.Out("\n")
		if err != nil {
			e.o.ErrExit("Can't read password: %s\n", err)
		}
		e.s.SetAuthPass(string(pw))
	}
	handleSignals(e)
	if e.c.Agent {
		sshClientConfig, err = getSSHConfigAgent(e)
	} else {
		sshClientConfig, err = getSSHConfigFile(clientPrivateKey, e)
	}
	if err != nil {
		e.o.ErrExit("Can't load SSH client config: %s\n", err)
	}
	sshClientConfig.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	e.s.SetSSHConfig(sshClientConfig)
	count := 0
	if e.c.File != "" {
		e.o.Debug("Reading JSON.\n")
		j := NewJSON(e)
		if count, err = j.LoadFile(); err != nil {
			e.o.ErrExit("Can't read the JSON data: %s\n", err)
		}
	}
	if count > 0 {
		e.o.Debug("Connecting to %d hosts.\n", count)
		startTime := time.Now()
		connectEverywhere(e, e.c.Timeout)
		e.o.Debug("Done in %.2fs.\n", time.Since(startTime).Seconds())
	}
	if e.c.Server {
		s, err := NewSSHServer(serverPrivateKey, e)
		if err != nil {
			e.o.ErrExit("NewSSHServer(): %s\n", err)
		}
		s.Start()
	} else {
		printSummary(e, e.c.Verbose)
	}
}
