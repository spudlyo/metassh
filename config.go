/*
 * config.go
 *
 * Deals with MetaSSH's various command line options.
 *
 */

package main

import (
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"unsafe"

	flag "github.com/ogier/pflag"
)

// Default option values.
const (
	BastionConnects   = 2               // Split the proxy load up a bit
	Concurrency       = 65536           // SSH to this many boxes per iteration
	ControlPath       = "/.ssh/control" // Location of the control master sockets
	DefaultSSHKey     = "/.ssh/id_rsa"  // Default private key location
	SSHHostKey        = "/.ssh/id_host" // Server's SSH host key to prevent MITM
	SSHTimeout        = 60              // How long to wait for SSH to return
	SpoolDir          = "/.ssh/spool"   // Where remote program output gets saved
	DefaultTarget     = "/bin/target"   // External target program
	KeepAliveInterval = 0               // Send keep-alives this often
)

// Config is a structure that defines the various command line switches and
// options accepted by the program. The reflectFlags function uses reflection
// to get names, types, and the tags in this structure in order to call the
// appropriate pflag functions to set things up.
type Config struct {
	Agent        bool   `short:"a" desc:"Use ssh-agent auth. Limits concurrency to 128"`
	BastionConns int    `short:"b" desc:"Number of connections to maintain to each bastion"`
	Concurrency  int    `short:"c" desc:"Maximum number of concurrent SSH connections"`
	ControlPath  string `desc:"Specify where to create the control master UNIX domain sockets"`
	Debug        bool   `short:"d" desc:"Turn on debugging output"`
	Daemonize    bool   `desc:"Daemonize the program; run in the background"`
	Execute      bool   `short:"e" desc:"Execute a test command on the server after connecting"`
	File         string `short:"f" desc:"JSON file describing our SSH targets"`
	HostKey      string `desc:"Path of the SSH server's private host key"`
	KeepAlive    int    `desc:"Send server keep alive messages every 'n' seconds"`
	Key          string `short:"k" desc:"Private SSH key to use for client authentication"`
	Password     bool   `short:"p" desc:"Prompt for a password for password auth fallback"`
	Server       bool   `short:"s" desc:"Run in SSH server mode"`
	Spool        bool   `desc:"Save remote execution output to the SpoolDir"`
	SpoolDir     string `desc:"Specify path to save program execution output"`
	TargetCmd    string `desc:"Specify external program to implement target functionality"`
	Tee          bool   `desc:"Tee spooled output to stdout/stderr."`
	TestCmd      string `desc:"Specify a test command to execute"`
	Timeout      int    `short:"t" desc:"Number of seconds to wait for SSH connections to finish"`
	User         string `short:"u" desc:"Specify the user to SSH in as"`
	Verbose      bool   `short:"v" desc:"Enable verbose reporting"`
}

// DefaultConfig returns you back a pointer to a Config structure that has
// some reasonable program defaults.
func DefaultConfig() *Config {
	return &Config{
		Agent:        false,
		BastionConns: BastionConnects,
		Concurrency:  Concurrency,
		ControlPath:  os.Getenv("HOME") + ControlPath,
		Debug:        false,
		Daemonize:    false,
		Execute:      false,
		File:         "",
		HostKey:      os.Getenv("HOME") + SSHHostKey,
		KeepAlive:    KeepAliveInterval,
		Key:          os.Getenv("HOME") + DefaultSSHKey,
		Password:     false,
		Server:       false,
		Spool:        false,
		SpoolDir:     os.Getenv("HOME") + SpoolDir,
		TargetCmd:    os.Getenv("HOME") + DefaultTarget,
		Tee:          false,
		TestCmd:      TestCommand,
		Timeout:      SSHTimeout,
		User:         os.Getenv("USER"),
		Verbose:      false,
	}
}

// reflectFlags takes as args a command name, a pointer to a structure and
// an io.Writer.
//
// The flags argument is supposed to be a pointer to a structure containing
// elements of type int, bool, or string, along with a tag for each element
// in the struct that encodes the option's short name (like -h) and a
// description that appears in the help for the command.
//
// This code uses reflection to figure out the type, value, and address of
// each element in the passed in 'flags' stucture and then calls the
// appropriate flag.<Type>VarP function for each structure element.
// We hand back to you an initialized, but not parsed *flag.FlagSet, with
// the output (where the help message goes) set to the passed in io.Writer
func reflectFlags(name string, flags interface{}, o io.Writer) (*flag.FlagSet, error) {
	val := reflect.ValueOf(flags)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	} else {
		return nil, errors.New("Expected pointer type for flags.")
	}
	if val.Kind() != reflect.Struct {
		return nil, errors.New("Expected pointer to struct for flags.")
	}
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(o)
	for i := 0; i < val.NumField(); i++ {
		valueField := val.Field(i)
		typeField := val.Type().Field(i)

		name := strings.ToLower(typeField.Name)
		value := valueField.Interface()
		short := typeField.Tag.Get("short")
		desc := typeField.Tag.Get("desc")
		ptr := unsafe.Pointer(valueField.Addr().Pointer())

		switch valueField.Kind() {
		case reflect.Float64:
			f.Float64VarP((*float64)(ptr), name, short, value.(float64), desc)
		case reflect.Bool:
			f.BoolVarP((*bool)(ptr), name, short, value.(bool), desc)
		case reflect.Int:
			f.IntVarP((*int)(ptr), name, short, value.(int), desc)
		case reflect.String:
			f.StringVarP((*string)(ptr), name, short, value.(string), desc)
		}
	}
	return f, nil
}
