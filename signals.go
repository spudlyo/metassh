/*
 * signals.go
 *
 * This file contains some code that does rudimentary singal handling.
 *
 * SIGTERM: Clean up all our open sockets, exit.
 * SIGINT:  Clean up all our open sockets, exit.
 * SIGQUIT: Show waiters, time them out.
 *
 */

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func handleSignals(e Env) {
	quitChan := make(chan os.Signal, 1)
	signal.Notify(quitChan, syscall.SIGQUIT)
	// The SIQUIT handler is meant to be invoked when you're running in
	// stanalone mode, by hitting ^\.  It's like the abort command from
	// the CLI, but there is no CLI in standalone mode.
	go func() {
		for range quitChan {
			e.o.Out("\n")
			hk := e.s.GetHostKeys()
			e.o.Out("There are currently %d HostKeys loaded.\n", len(hk))
			wi := e.s.GetWaiterInfo()
			e.o.Out(
				"ABORT: %d connection threads (%.2fs average wait time)\n",
				wi.connWaiters,
				wi.avgConnWait.Seconds(),
			)
			e.o.Out(
				"ABORT: %d run threads (%.2fs average wait time)\n",
				wi.runWaiters,
				wi.avgRunWait.Seconds(),
			)
			e.s.TimeoutWaiters()
		}
	}()
	intChan := make(chan os.Signal, 1)
	signal.Notify(intChan, syscall.SIGINT)
	go cleanDeath(intChan, e)
	termChan := make(chan os.Signal, 1)
	signal.Notify(termChan, syscall.SIGTERM)
	go cleanDeath(termChan, e)
}

func cleanDeath(deathChan chan os.Signal, e Env) {
	for range deathChan {
		disconnectEverywhere(e, true)
		e.o.Debug("Cya.\n")
		os.Exit(0)
	}
}
