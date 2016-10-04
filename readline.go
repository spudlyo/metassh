/*
 * readline.go
 *
 * Golang/C language wrapper for the GNU Readline library. It only implements
 * the handful of functions out of the Readline Library that MetaSSH uses.
 *
 * This should work on OSX if you use 'brew' tool to install readline. If you
 * use some other package manager on OSX you'll need to adjust the paths below.
 */

package main

/*
#cgo darwin CFLAGS: -I/usr/local/opt/readline/include
#cgo darwin LDFLAGS: -L/usr/local/opt/readline/lib
#cgo LDFLAGS: -lreadline -lhistory

#include <stdio.h>
#include <stdlib.h>
#include <readline/readline.h>
#include <readline/history.h>
*/
import "C"

import (
	"errors"
	"io"
	"os"
	"unsafe"
)

func readline(prompt string) (string, error) {
	C.rl_catch_signals = 0
	C.rl_catch_sigwinch = 0

	promptC := C.CString(prompt)
	defer C.free(unsafe.Pointer(promptC))
	lineC := C.readline(promptC)
	defer C.free(unsafe.Pointer(lineC))
	line := C.GoString(lineC)
	if lineC != nil {
		if line != "" {
			C.add_history(lineC)
		}
		return line, nil
	}
	return line, io.EOF
}

func rlInstream(inStream *os.File) error {
	fp := fopen(inStream.Name(), "r")
	if fp == nil {
		return errors.New("fopen returned null")
	}
	C.rl_instream = fp
	return nil
}

func rlOutstream(outStream *os.File) error {
	fp := fopen(outStream.Name(), "w")
	if fp == nil {
		return errors.New("fopen returned null")
	}
	C.rl_outstream = fp
	return nil
}

func fopen(name string, mode string) *C.FILE {
	fNameC := C.CString(name)
	defer C.free(unsafe.Pointer(fNameC))
	modeC := C.CString(mode)
	defer C.free(unsafe.Pointer(modeC))
	return (C.fopen(fNameC, modeC))
}
