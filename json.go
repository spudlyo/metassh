/*
 * json.go
 *
 * This file contains code that groks the JSON data that is generated
 * by the external 'target' command, and then loads it into the global
 * HostInfo map.
 *
 */

package main

import (
	"encoding/json"
	"io/ioutil"
	"strings"
)

// NewJSON retuns an initialized JSON object.
func NewJSON(e Env) *JSON {
	j := &JSON{}
	j.e = e
	return j
}

// JSON is a singleton object that contains information that
// that we load from a JSON dump of SSH server connection data.
type JSON struct {
	Servers []Server
	e       Env
}

// Server is a type that contains a minimal info about a host. The
// structure tags make the JSON import easier.
type Server struct {
	Name   string `json:"name"`
	IPAddr string `json:"ip_address"`
	Chain  string `json:"chain"`
}

// LoadFile is a method that loads data from the JSON SSH dump file
// which comes from the 'File' command-line option.
func (j *JSON) LoadFile() (int, error) {
	jblob, err := ioutil.ReadFile(j.e.c.File)
	if err != nil {
		return 0, err
	}
	return j.LoadBlob(jblob)
}

// LoadBlob loads a JSON blob from the external 'target' command or
// from LoadFile into the global HostInfo map.
func (j *JSON) LoadBlob(jblob []byte) (int, error) {
	err := json.Unmarshal(jblob, &j.Servers)
	if err != nil {
		return 0, err
	}
	count := 0
	for i := range j.Servers {
		srv := j.Servers[i]
		if j.e.s.HostExists(srv.Name) {
			j.e.o.Debug("Duplicate HostInfo entry for: %s\n", srv.Name)
			continue
		}
		j.e.s.SetHostInfo(HostInfo{
			hostName:  srv.Name,
			ipAddress: srv.IPAddr,
			chain:     strings.Split(srv.Chain, " "),
		})
		count++
	}
	return count, nil
}
