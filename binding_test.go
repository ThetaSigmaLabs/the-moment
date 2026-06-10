// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

//go:build integration

package main

// =============================================================================
// binding_test.go
// =============================================================================
// Verifies the web server binds to 0.0.0.0 and is reachable on localhost
// and the machine's LAN IP.
//
// Run with:
//   go test ./... -tags integration -v -run TestServerBinding
//
// macOS note: the `go test` binary is compiled to a temp path and is blocked
// by the Application Firewall on the LAN interface (loopback is exempt).
// The compiled binary (go build -o the-moment .) is firewall-permitted and
// works fine on the LAN. The LAN portion of this test is skipped when the
// firewall is the cause of the failure.
// =============================================================================

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// detectLANIP returns the outbound LAN IP without sending any packets.
func detectLANIP(t *testing.T) string {
	t.Helper()
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		t.Logf("could not detect LAN IP: %v", err)
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// macOSFirewallEnabled returns true when running on macOS with the
// Application Firewall active.
func macOSFirewallEnabled() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	out, err := exec.Command(
		"/usr/libexec/ApplicationFirewall/socketfilterfw",
		"--getglobalstate",
	).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Firewall is enabled")
}

// testServerOnListener creates a real FilamentBridge + WebServer, starts it
// on the provided listener, and returns a cleanup function.
func testServerOnListener(t *testing.T, l net.Listener) (cleanup func()) {
	t.Helper()

	t.Setenv("THE_MOMENT_DB_PATH", t.TempDir())

	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}

	config, err := LoadConfig(bridge)
	if err != nil {
		bridge.Close()
		t.Fatalf("LoadConfig: %v", err)
	}
	bridge.UpdateConfig(config)

	ws := NewWebServer(bridge)
	go ws.StartListener(l) //nolint:errcheck

	time.Sleep(50 * time.Millisecond)

	return func() {
		l.Close()
		bridge.Close()
	}
}

// checkStatus performs GET /api/status and returns (ok, error description).
func checkStatus(addr string) (bool, string) {
	url := fmt.Sprintf("http://%s/api/status", addr)
	resp, err := http.Get(url)
	if err != nil {
		return false, err.Error()
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return true, ""
}

// TestServerBinding verifies that binding to 0.0.0.0 makes the server
// reachable on both localhost and the machine's LAN IP.
func TestServerBinding(t *testing.T) {
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	portStr := fmt.Sprintf("%d", port)
	t.Logf("server bound on 0.0.0.0:%s", portStr)

	cleanup := testServerOnListener(t, l)
	defer cleanup()

	// Loopback addresses — always expected to pass.
	for _, addr := range []string{"localhost:" + portStr, "127.0.0.1:" + portStr} {
		ok, errStr := checkStatus(addr)
		if ok {
			t.Logf("OK    http://%s/api/status", addr)
		} else {
			t.Errorf("FAIL  http://%s/api/status — %s", addr, errStr)
		}
	}

	// LAN IP — may be blocked by macOS Application Firewall for the temp
	// go-test binary; the compiled binary does not have this restriction.
	lanIP := detectLANIP(t)
	if lanIP == "" {
		t.Log("SKIP  LAN IP check — could not detect outbound IP")
		return
	}
	lanAddr := lanIP + ":" + portStr
	ok, errStr := checkStatus(lanAddr)
	if ok {
		t.Logf("OK    http://%s/api/status", lanAddr)
		return
	}

	if macOSFirewallEnabled() {
		t.Logf("SKIP  http://%s/api/status — macOS Application Firewall blocks the", lanAddr)
		t.Logf("      temporary go-test binary on the LAN interface (loopback is exempt).")
		t.Logf("      The compiled binary is firewall-permitted; run `./the-moment` to verify LAN access.")
	} else {
		t.Errorf("FAIL  http://%s/api/status — %s", lanAddr, errStr)
	}
}
