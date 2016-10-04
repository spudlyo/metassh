/*
 * output.go
 *
 * The *Output object has methods which serialize output to to the
 * screen, or to anything that implements an io.Writer interface.
 *
 * We use this because when you're emiting output from a number of
 * independent goroutines, it can get messy unless you serialize things.
 *
 */

package main

import (
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"runtime"
)

type outputString struct {
	fd       io.Writer
	out      string
	respChan chan<- bool
}

type writer struct {
	p        []byte
	respChan chan<- rwResult
}

type rwResult struct {
	n   int
	err error
}

// Output is an object that funnels all terminal ouput through one code path
// which allows us to output to SSH ptys, a log, or STDOUT/STDERR.
type Output struct {
	stdOut  io.Writer
	stdErr  io.Writer
	addCR   bool
	mute    bool
	debug   bool
	nlRegex *regexp.Regexp
	reqChan chan interface{}
}

// NewOutput returns an initialized output object.
func NewOutput(stdOut, stdErr io.Writer, addCR bool, debug bool) *Output {
	o := new(Output)
	o.stdOut = stdOut
	o.stdErr = stdErr
	o.addCR = addCR
	o.debug = debug
	o.nlRegex = regexp.MustCompile("\n")
	o.reqChan = make(chan interface{})
	go o.serializer()
	return o
}

// DupeOuput returns enough private members so you can do something like
// dupe := NewOutput(DupeOutput(e.o)).
func (o *Output) DupeOuput() (io.Writer, io.Writer, bool, bool) {
	return o.stdOut, o.stdErr, o.addCR, o.debug
}

// Write is the serialized Writer wrapper for things that expect to
// use an io.Writer.
func (o *Output) Write(p []byte) (n int, err error) {
	respChan := make(chan rwResult)
	var nMatches = 0

	if o.addCR {
		nMatches = len(o.nlRegex.FindAll(p, -1))
		replaced := o.nlRegex.ReplaceAll(p, []byte("\n\r"))
		o.reqChan <- writer{replaced, respChan}
	} else {
		o.reqChan <- writer{p, respChan}
	}
	resp := <-respChan
	return resp.n - nMatches, resp.err
}

// EnableDebug sets the debug flag to true.
func (o *Output) EnableDebug() {
	o.debug = true
}

// DisableDebug sets the debug flag to false.
func (o *Output) DisableDebug() {
	o.debug = false
}

// Mute mutes all output, handy for when you daemonize for example.
func (o *Output) Mute() {
	o.mute = true
}

// UnMute turns output back on.
func (o *Output) UnMute() {
	o.mute = false
}

// ErrExit outputs an error to STDERR and bail out.
func (o *Output) ErrExit(format string, a ...interface{}) {
	o.doOutput(o.stdErr, fmt.Sprintf(format, a...))
	os.Exit(1)
}

// Err outputs an error message to STDERR.
func (o *Output) Err(format string, a ...interface{}) {
	o.doOutput(o.stdErr, fmt.Sprintf(format, a...))
}

// Out outputs a message to STDOUT.
func (o *Output) Out(format string, a ...interface{}) {
	o.doOutput(o.stdOut, fmt.Sprintf(format, a...))
}

// Debug conditionally outputs some stuff based on the debug flag.
func (o *Output) Debug(format string, a ...interface{}) {
	var newformat string
	_, file, line, ok := runtime.Caller(1)
	if ok {
		newformat = fmt.Sprintf("%s(%d): ", path.Base(file), line)
	}
	newformat = newformat + format
	if o.debug {
		o.doOutput(o.stdErr, fmt.Sprintf(newformat, a...))
	}
}

// SSH clients will freak out and disconnect if you write to them
// from a bunch of goroutines at once. The serializer keeps things
// nice and tidy.
func (o *Output) serializer() {
	for {
		req := <-o.reqChan
		switch req.(type) {
		case outputString:
			osReq := req.(outputString)
			fmt.Fprint(osReq.fd, osReq.out)
			osReq.respChan <- true
		case writer:
			wReq := req.(writer)
			n, err := o.stdOut.Write(wReq.p)
			wReq.respChan <- rwResult{n, err}
		}
	}
}

func (o *Output) doOutput(writer io.Writer, out string) {
	respChan := make(chan bool)
	if !o.mute {
		if o.addCR {
			munged := o.nlRegex.ReplaceAll([]byte(out), []byte("\n\r"))
			o.reqChan <- outputString{writer, string(munged), respChan}
		} else {
			o.reqChan <- outputString{writer, out, respChan}
		}
	}
	<-respChan
}
