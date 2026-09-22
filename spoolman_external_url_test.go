// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestGetSpoolmanExternalURL_FallsBackToInternal verifies the helper returns the
// internal URL when the external one is empty (single-URL deployments).
func TestGetSpoolmanExternalURL_FallsBackToInternal(t *testing.T) {
	b := &FilamentBridge{
		config: &Config{
			SpoolmanURL:         "http://spoolman:8000",
			SpoolmanExternalURL: "",
		},
	}
	if got := b.GetSpoolmanExternalURL(); got != "http://spoolman:8000" {
		t.Errorf("expected fallback to internal URL, got %q", got)
	}
}

// TestGetSpoolmanExternalURL_PrefersExternal verifies the helper returns the
// external URL when both are set (Docker/k8s deploy with separate external host).
func TestGetSpoolmanExternalURL_PrefersExternal(t *testing.T) {
	b := &FilamentBridge{
		config: &Config{
			SpoolmanURL:         "http://spoolman:8000",
			SpoolmanExternalURL: "http://nas.local:7912",
		},
	}
	if got := b.GetSpoolmanExternalURL(); got != "http://nas.local:7912" {
		t.Errorf("expected external URL, got %q", got)
	}
}

// TestSpoolmanLinkURL covers the precedence used for every Spoolman link handed to a
// browser: explicit external URL, then the request host plus the published Spoolman
// port, then the internal URL.
func TestSpoolmanLinkURL(t *testing.T) {
	tests := []struct {
		name       string
		internal   string
		external   string
		publicPort string
		host       string
		tls        bool
		forwarded  string
		want       string
	}{
		{
			name:       "explicit external URL wins over derivation",
			internal:   "http://spoolman:8000",
			external:   "https://spoolman.example.com",
			publicPort: "7912",
			host:       "moment.local:5000",
			want:       "https://spoolman.example.com",
		},
		{
			name:     "trailing slash on the external URL is trimmed",
			internal: "http://spoolman:8000",
			external: "https://spoolman.example.com/",
			host:     "moment.local:5000",
			want:     "https://spoolman.example.com",
		},
		{
			name:       "host with port swaps in the Spoolman port",
			internal:   "http://spoolman:8000",
			publicPort: "7912",
			host:       "192.168.1.50:5000",
			want:       "http://192.168.1.50:7912",
		},
		{
			name:       "host without a port still derives",
			internal:   "http://spoolman:8000",
			publicPort: "7912",
			host:       "moment.local",
			want:       "http://moment.local:7912",
		},
		{
			name:       "IPv6 literal keeps its brackets",
			internal:   "http://spoolman:8000",
			publicPort: "7912",
			host:       "[fd00::1]:5000",
			want:       "http://[fd00::1]:7912",
		},
		{
			name:       "TLS request derives an https link",
			internal:   "http://spoolman:8000",
			publicPort: "7912",
			host:       "moment.local:5000",
			tls:        true,
			want:       "https://moment.local:7912",
		},
		{
			name:       "X-Forwarded-Proto derives an https link",
			internal:   "http://spoolman:8000",
			publicPort: "7912",
			host:       "moment.local",
			forwarded:  "https",
			want:       "https://moment.local:7912",
		},
		{
			name:     "no public port falls through to the internal URL",
			internal: "http://localhost:7912",
			host:     "localhost:5000",
			want:     "http://localhost:7912",
		},
	}

	gin.SetMode(gin.TestMode)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := &WebServer{bridge: &FilamentBridge{config: &Config{
				SpoolmanURL:         tc.internal,
				SpoolmanExternalURL: tc.external,
				SpoolmanPublicPort:  tc.publicPort,
			}}}

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			c.Request.Host = tc.host
			if tc.tls {
				c.Request.TLS = &tls.ConnectionState{}
			}
			if tc.forwarded != "" {
				c.Request.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}

			if got := ws.spoolmanLinkURL(c); got != tc.want {
				t.Errorf("spoolmanLinkURL = %q, want %q", got, tc.want)
			}
		})
	}
}
