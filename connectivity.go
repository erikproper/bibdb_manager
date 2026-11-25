/*
 *
 * Module:    bibdb_manager
 * Component:
 * - connectivity
 *
 * Internet connectivity detection. Online is set once at startup by an HTTP
 * probe. All network-dependent code should gate on Online.
 *
 * Creator: Henderik A. Proper (erikproper@gmail.com)
 *
 * Version of: 22.11.2026
 *
 */

package main

import (
	"net/http"
	"time"
)

const (
	onlineProbeHTTP = "http://connectivitycheck.gstatic.com/generate_204" // returns 204 on real internet; captive portals return 200/302
	dialTimeout     = 3 * time.Second
)

// Online is set once at startup: true when general internet connectivity is available.
var Online bool

func init() {
	Online = probeConnectivity()
}

// probeConnectivity reports whether an HTTP GET of the probe URL answers 204: real
// internet, not a captive portal (which answers 200 or a redirect). There used to be
// a TCP pre-check to 8.8.8.8:53 first, but networks that block outside DNS refuse
// it while the internet works, so every run there was "offline" (2026-11-22).
func probeConnectivity() bool {
	client := &http.Client{
		Timeout: dialTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // don't follow captive-portal redirects
		},
	}
	resp, err := client.Get(onlineProbeHTTP)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}
