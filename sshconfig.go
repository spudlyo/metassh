/*
 * sshconfig.go
 *
 * This file contains code that deals with SSH client/server configuration, but
 * more specifically it deals with handling signers and private keys.
 *
 */

package main

import (
	"bytes"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/terminal"
)

// Given a path to an SSH private key, getPrivateKeyFile will return an *os.File
// object. If it's an encrypted key it will prompt you for the passphrase,
// make a temporary copy of the key in the TmpDir directory, and then
// run ssh-keygen on it to strip the encryption. It will then open temporary
// file, delete it, and hand you back an open *os.File to it.
//
// The error handling here is hella ugly. I am truely sorry for your lots.
func getPrivateKeyFile(keyname string, e Env) (*os.File, error) {
	var buf, pw []byte
	var err error
	var fp *os.File
	var closeTheFile = true

	fp, err = os.Open(keyname)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeTheFile {
			myErr := fp.Close()
			if myErr != nil {
				e.o.Debug("fp.Close(): %s\n", myErr)
			}
		}
	}()
	// We read the file so we can tell if it's encrypted or not.
	buf, err = ioutil.ReadAll(fp)
	if err != nil {
		return nil, err
	}
	_, err = fp.Seek(0, 0) // rewind
	if err != nil {
		return nil, err
	}
	if bytes.Contains(buf, []byte("ENCRYPTED")) {
		// Need to reopen fp, so let's close it.
		err = fp.Close()
		closeTheFile = false
		if err != nil {
			e.o.Debug("fp.Close(): %s\n", err)
			return nil, err
		}
		fp, err = ioutil.TempFile(os.Getenv("HOME")+TmpDir, "key")
		if err != nil {
			return nil, err
		}
		closeTheFile = true
		tmpName := fp.Name()
		defer func() {
			// Yes at the end of this function, the caller will have
			// an *os.File object that points to an unlinked file.
			err = os.Remove(tmpName)
			if err != nil {
				e.o.Debug("os.Remove(): %s\n", err)
			}
		}()
		_, err := fp.Write(buf)
		if err != nil {
			return nil, err
		}
		err = fp.Close()
		if err != nil {
			closeTheFile = false
			e.o.Debug("fp.Close(): %s\n", err)
			return nil, err
		}
		closeTheFile = false
		// We now have copied the private key to tmpname.
		e.o.Out("Passphrase for %s: ", keyname)
		pw, err = terminal.ReadPassword(syscall.Stdin)
		e.o.Out("\n")
		if err != nil {
			return nil, err
		}
		// Strip the passphrase from the temporary file.
		// If somebody catches your passphrase in a ps listing before
		// ssh-keygen sanitizes its **argv, I heartily apologize.
		cmd := exec.Command(
			SSHKeygen,
			"-f",
			tmpName,
			"-N",
			"",
			"-P",
			string(pw),
			"-p",
		)
		if err = cmd.Start(); err != nil {
			e.o.Debug("cmd.Start(): %s\n", err)
			return nil, err
		}
		if err = cmd.Wait(); err != nil {
			return nil, err
		}
		// Reopen, no encryption now.
		fp, err = os.Open(tmpName)
		closeTheFile = true
		if err != nil {
			return nil, err
		}
	}
	closeTheFile = false
	return fp, nil
}

func makeSigner(fp *os.File, e Env) (ssh.Signer, error) {
	defer func() {
		err := fp.Close()
		if err != nil {
			e.o.Debug("fp.Close(): %s\n", err)
		}
	}()
	buf, err := ioutil.ReadAll(fp)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(buf)
	if err != nil {
		return nil, err
	}
	return signer, nil
}

// Using a forwarded agent connection can be very slow, especially if that
// agent is running on a different continent from the hosts you are working
// with. Your concurrency is limited to 128, because that's SOMAXCONN pretty
// much  everywhere, but more importantly it's hard coded in ssh-agent as the
// socket backlog. Connecting to any non-trivial amount of machines this
// way can take a long time. Running a local agent really speeds things up.
func getSSHConfigAgent(e Env) (*ssh.ClientConfig, error) {
	agentsock, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User: e.c.User,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeysCallback(agent.NewClient(agentsock).Signers),
		},
	}
	return cfg, nil
}

// Given the name of an encrypted or unencrypted PEM file with a private
// SSH key, getSSHConfig will return a pointer to an ssh.ClientConfig struct.
func getSSHConfigFile(fp *os.File, e Env) (*ssh.ClientConfig, error) {
	signer, err := makeSigner(fp, e)
	if err != nil || signer == nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User: e.c.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
	}
	return cfg, nil
}

func getSSHServerConfig(fp *os.File, e Env) (*ssh.ServerConfig, error) {
	cfg := &ssh.ServerConfig{
		NoClientAuth: true, // FIXME
	}
	signer, err := makeSigner(fp, e)
	if err != nil {
		return nil, err
	}
	cfg.AddHostKey(signer)
	return cfg, nil
}
