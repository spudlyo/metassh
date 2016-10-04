/*
 * summary.go
 *
 * This module has the printSummary function, which outputs some connection and
 * run statistics that it gleans from the metrics we collect in HostInfo.
 *
 */

package main

import (
	"strings"

	"github.com/bmizerany/perks/quantile"
)

func printSummary(e Env, verbose bool) {
	errs := []string{
		"connection refused",
		"too many open files",
		"connection reset by peer",
		"no supported methods remain",
		"administratively prohibited",
		"no route to host",
		"connection timed out",
		"connection aborted",
		"unexpected packet",
		"run timed out",
		"run aborted",
		"eof",
		"no common algorithm",
		"process exited with status",
	}

	var requiresPwHosts []string
	var hk = e.s.GetHostKeys()
	connectErrorCounts := make(map[string]int)
	connectErrorHosts := make(map[string][]string)
	runErrorCounts := make(map[string]int)
	runErrorHosts := make(map[string][]string)
	runTimeToHost := make(map[float64]string)
	conTimeToHost := make(map[float64]string)

	numEntries := len(hk)
	runOK := 0
	runOnce := 0
	connectedOK := 0
	requiresPw := 0

	qConnect := quantile.NewBiased()
	qRun := quantile.NewBiased()

	for i := range hk {
		hostname := hk[i]
		hi, err := e.s.GetHostInfo(hostname)
		if err != nil {
			e.o.Debug("GetHostInfo(): %s\n", err)
			continue
		}
		if hi.runOK {
			runOK++
		}
		if hi.runOnce {
			runOnce++
		}
		if hi.runOK && hi.runOnce {
			qRun.Insert(hi.runTime.Seconds())
			runTimeToHost[hi.runTime.Seconds()] = hostname
		}
		if hi.connectedOK {
			qConnect.Insert(hi.connectTime.Seconds())
			conTimeToHost[hi.connectTime.Seconds()] = hostname
			connectedOK++
		}
		if hi.connectedOK && hi.requiresPw {
			requiresPw++
			requiresPwHosts = append(requiresPwHosts, hostname)
		}
		if hi.lastError != nil {
			found := false
			for e := range errs {
				errstr := strings.ToLower(hi.lastError.Error())
				if strings.Contains(errstr, errs[e]) {
					found = true
					if !hi.connectedOK {
						connectErrorCounts[errs[e]]++
						connectErrorHosts[errs[e]] = append(
							connectErrorHosts[errs[e]],
							hostname,
						)
					} else {
						runErrorCounts[errs[e]]++
						runErrorHosts[errs[e]] = append(
							runErrorHosts[errs[e]],
							hostname,
						)
					}
					break
				}
			}
			if !found {
				if !hi.connectedOK {
					e.o.Debug("Connect UNK: %s\n", hi.lastError.Error())
					connectErrorCounts[UnknownError]++
				} else {
					e.o.Debug("Run UNK: %s\n", hi.lastError.Error())
					runErrorCounts[UnknownError]++
				}
			}
		}
	}

	runFail := runOnce - runOK
	connectFail := numEntries - connectedOK

	e.o.Out("Quantile:    1%%     25%%     50%%     90%%    99%%\n")
	e.o.Out("         +-------+-------+-------+-------+------+\n")
	e.o.Out("Connect:  %05.2fs, %05.2fs, %05.2fs, %05.2fs, %05.2fs (%d samples)\n",
		qConnect.Query(0.1),
		qConnect.Query(0.25),
		qConnect.Query(0.50),
		qConnect.Query(0.90),
		qConnect.Query(0.99),
		qConnect.Count(),
	)
	if qRun.Count() > 0 {
		e.o.Out("Run:      %05.2fs, %05.2fs, %05.2fs, %05.2fs, %05.2fs (%d samples)\n",
			qRun.Query(0.1),
			qRun.Query(0.25),
			qRun.Query(0.50),
			qRun.Query(0.90),
			qRun.Query(0.99),
			qRun.Count(),
		)
	}
	e.o.Out("\n\t%d connection failures\n", connectFail)
	outputErrors(e, connectErrorCounts, connectErrorHosts, verbose)
	if e.c.Password {
		e.o.Out("\trequired a password(%d)\n", requiresPw)
		if verbose {
			for i := range requiresPwHosts {
				e.o.Out("\t\t%s\n", requiresPwHosts[i])
			}
		}
	}
	if qRun.Count() > 0 {
		e.o.Out("\t%d run failures\n", runFail)
	}
	outputErrors(e, runErrorCounts, runErrorHosts, verbose)
	e.o.Out("\n")

	qConSamp := qConnect.Samples()
	qRunSamp := qRun.Samples()

	if len(qConSamp) > 0 {
		fastSlow(e, "Con", qConSamp, conTimeToHost)
	}
	if len(qRunSamp) > 0 {
		fastSlow(e, "Run", qRunSamp, runTimeToHost)
	}
}

func fastSlow(e Env, what string, s quantile.Samples, tm map[float64]string) {
	fast := s[0].Value
	slow := s[len(s)-1].Value

	fastHost := tm[fast]
	slowHost := tm[slow]

	e.o.Out("Fastest %s: %05.5fs - %s\n", what, fast, fastHost)
	e.o.Out("Slowest %s: %05.5fs - %s\n\n", what, slow, slowHost)
}

func outputErrors(e Env, m map[string]int, eh map[string][]string, v bool) {
	errKeys := sortedIntKeys(m)
	for i := range errKeys {
		errstr := errKeys[i]
		count := m[errstr]

		e.o.Out("\t\t%s(%d)\n", errstr, count)
		if v {
			for j := range eh[errstr] {
				e.o.Out("\t\t\t%s\n", eh[errstr][j])
			}
		}
	}
}
