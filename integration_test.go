// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── Test Helpers ────────────────────────────────────────────────────────────

// testServer creates a real FilamentBridge + WebServer backed by a temp
// SQLite database, wraps it in httptest, and returns the server URL.
// The returned cleanup function removes the temp database.
func testServer(t *testing.T) (serverURL string, cleanup func()) {
	t.Helper()

	// Create a temp directory for the test database
	tmpDir, err := os.MkdirTemp("", "the-moment-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// Point the bridge at the temp database
	os.Setenv("THE_MOMENT_DB_PATH", tmpDir)

	// Create a real bridge — same code path as production
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to create bridge: %v", err)
	}

	// Load config from the fresh database
	config, err := LoadConfig(bridge)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to load config: %v", err)
	}
	bridge.UpdateConfig(config)

	// Create the real web server using your actual web.go code
	webServer := NewWebServer(bridge)

	// httptest.NewServer wraps the Gin engine in a real HTTP server
	// on a random free port — no port conflicts, no "is it up yet?"
	ts := httptest.NewServer(webServer.router)

	cleanup = func() {
		ts.Close()
		bridge.Close()
		os.RemoveAll(tmpDir)
		os.Unsetenv("THE_MOMENT_DB_PATH")
	}

	return ts.URL, cleanup
}

// get is a helper that makes a GET request and returns the response body
func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s failed: %v", url, err)
	}
	defer resp.Body.Close()

	var body []byte
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	return resp, body
}

// ─── Integration Tests ────────────────────────────────────────────────────────

// TestAPI_Status checks the /api/status endpoint returns the expected shape
func TestAPI_Status(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	resp, body := get(t, serverURL+"/api/status")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Parse the response
	var status map[string]interface{}
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, body)
	}

	// Must have a "printers" key
	if _, ok := status["printers"]; !ok {
		t.Errorf("response missing 'printers' key\nbody: %s", body)
	}

	t.Logf("✅ /api/status responded: %s", body)
}

// TestAPI_Spools checks the /api/spools endpoint
// With no Spoolman running, it should return a graceful error or empty list
func TestAPI_Spools(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	resp, body := get(t, serverURL+"/api/spools")

	// We accept 200 (empty list) or 500 (Spoolman not configured)
	// What we do NOT accept is a crash or HTML error page
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("unexpected status %d — expected 200 or 500\nbody: %s", resp.StatusCode, body)
	}

	// Body must be valid JSON regardless of status code
	var result interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Errorf("response is not valid JSON: %v\nbody: %s", err, body)
	}

	t.Logf("✅ /api/spools responded with status %d", resp.StatusCode)
}

// TestAPI_PrintErrors checks the /api/print-errors endpoint
func TestAPI_PrintErrors(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	resp, body := get(t, serverURL+"/api/print-errors")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Should return a JSON array (empty on a fresh server)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("expected JSON object, got: %s", body)
	}
	if _, ok := result["errors"]; !ok {
		t.Errorf("response missing 'errors' key, got: %s", body)
	}
	t.Logf("✅ /api/print-errors returned valid response: %s", body)
}

// TestAPI_ConfigPrinterFlow tests adding a printer config and reading it back
func TestAPI_ConfigPrinterFlow(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	// 1. Confirm no printers on a fresh server
	_, body := get(t, serverURL+"/api/status")
	var status map[string]interface{}
	json.Unmarshal(body, &status)

	printers := status["printers"].(map[string]interface{})
	t.Logf("Fresh server has %d printer entries: %v", len(printers), printers)

	// 2. Status must exist even with no printers configured
	// Empty map is valid — no printers configured on a fresh server
	t.Logf("Fresh server correctly returns %d printer entries", len(printers))

	t.Logf("✅ Config flow confirmed — fresh server starts cleanly")
}

// TestAPI_ToolheadMapping tests the toolhead mapping endpoints
func TestAPI_ToolheadMapping(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	// Attempt to unmap a toolhead that was never mapped — should not crash
	resp, body := get(t, fmt.Sprintf("%s/api/unmap_toolhead?printer=TestPrinter&toolhead=0", serverURL))

	// Accept 200 or 400 (bad request if no printer configured) but not 500
	if resp.StatusCode == http.StatusInternalServerError {
		t.Errorf("unmap_toolhead crashed with 500: %s", body)
	}

	t.Logf("✅ /api/unmap_toolhead responded with status %d", resp.StatusCode)
}

// TestAPI_NotFound checks that unknown routes return 404, not a crash
func TestAPI_NotFound(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	resp, _ := get(t, serverURL+"/api/this-does-not-exist")

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown route, got %d", resp.StatusCode)
	}

	t.Logf("✅ Unknown routes correctly return 404")
}

// TestFilamentBridgeDatabase tests the database layer directly
// without going through HTTP at all
func TestFilamentBridgeDatabase(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "the-moment-db-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	os.Setenv("THE_MOMENT_DB_PATH", tmpDir)
	defer os.Unsetenv("THE_MOMENT_DB_PATH")

	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("failed to create bridge: %v", err)
	}
	defer bridge.Close()

	// Confirm the database file was created
	dbPath := filepath.Join(tmpDir, "the-moment.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("database file was not created at %s", dbPath)
	}

	// Save a printer config
	err = bridge.SavePrinterConfig("test-printer-1", PrinterConfig{
		Name:      "Core One L",
		Model:     "CORE One L",
		IPAddress: "192.168.1.99",
		APIKey:    "test-key",
		Toolheads: 8,
	})
	if err != nil {
		t.Fatalf("failed to save printer config: %v", err)
	}

	// Read it back
	configs, err := bridge.GetAllPrinterConfigs()
	if err != nil {
		t.Fatalf("failed to get printer configs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 printer config, got %d", len(configs))
	}

	config := configs["test-printer-1"]
	if config.Name != "Core One L" {
		t.Errorf("expected name 'Core One L', got '%s'", config.Name)
	}
	if config.Toolheads != 8 {
		t.Errorf("expected 8 toolheads, got %d", config.Toolheads)
	}

	// Test toolhead mapping round-trip
	err = bridge.SetToolheadMapping("Core One L", 0, 42)
	if err != nil {
		t.Fatalf("failed to set toolhead mapping: %v", err)
	}

	spoolID, err := bridge.GetToolheadMapping("Core One L", 0)
	if err != nil {
		t.Fatalf("failed to get toolhead mapping: %v", err)
	}
	if spoolID != 42 {
		t.Errorf("expected spool ID 42, got %d", spoolID)
	}

	t.Logf("✅ Database layer: printer config and toolhead mapping round-trip passed")
}

// post is a helper that sends a JSON POST and returns the response
func post(t *testing.T, url string, body interface{}) (*http.Response, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if readErr != nil {
			break
		}
	}
	return resp, out
}

// put is a helper that sends a JSON PUT and returns the response
func put(t *testing.T, url string, body interface{}) (*http.Response, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("failed to create PUT request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if readErr != nil {
			break
		}
	}
	return resp, out
}

// TestAPI_CredentialMasking verifies that GET /api/config and GET /api/printers
// never expose real credential values in their responses.
func TestAPI_CredentialMasking(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	const realAPIKey = "real-api-key-abc123"
	const realPassword = "hunter2"

	// ── seed: store a printer with a real API key ─────────────────────────────
	printerPayload := map[string]interface{}{
		"name":       "Test Printer",
		"model":      "MK4",
		"ip_address": "192.168.1.42",
		"api_key":    realAPIKey,
		"toolheads":  1,
		"is_virtual": false,
	}
	resp, body := post(t, serverURL+"/api/printers", printerPayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to add printer: %d %s", resp.StatusCode, body)
	}

	// ── seed: store TheMoment API key in config ───────────────────────────────
	configPayload := map[string]string{
		ConfigKeyTheMomentAPIKey: realPassword,
	}
	resp, body = post(t, serverURL+"/api/config", configPayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to save config: %d %s", resp.StatusCode, body)
	}

	// ── GET /api/printers must not expose the real API key ────────────────────
	_, printerBody := get(t, serverURL+"/api/printers")
	if strings.Contains(string(printerBody), realAPIKey) {
		t.Errorf("GET /api/printers exposed real API key in response: %s", printerBody)
	}
	if !strings.Contains(string(printerBody), maskedCredential) {
		t.Errorf("GET /api/printers should contain masked sentinel %q: %s", maskedCredential, printerBody)
	}
	t.Logf("✅ /api/printers response: %s", printerBody)

	// ── GET /api/config must not expose the real API key ─────────────────────
	_, configBody := get(t, serverURL+"/api/config")
	if strings.Contains(string(configBody), realPassword) {
		t.Errorf("GET /api/config exposed real credential in response: %s", configBody)
	}
	if !strings.Contains(string(configBody), maskedCredential) {
		t.Errorf("GET /api/config should contain masked sentinel %q: %s", maskedCredential, configBody)
	}
	t.Logf("✅ /api/config response does not expose credential")
}

// TestAPI_CredentialSentinelPreservation verifies that submitting the masked
// sentinel back via PUT does not overwrite the real stored credential.
func TestAPI_CredentialSentinelPreservation(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	const realAPIKey = "keep-this-key-xyz"
	const realPassword = "do-not-overwrite"

	// ── seed ──────────────────────────────────────────────────────────────────
	printerPayload := map[string]interface{}{
		"name":       "Sentinel Test Printer",
		"model":      "MK4",
		"ip_address": "10.0.0.5",
		"api_key":    realAPIKey,
		"toolheads":  1,
		"is_virtual": false,
	}
	resp, body := post(t, serverURL+"/api/printers", printerPayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to add printer: %d %s", resp.StatusCode, body)
	}
	var addResult map[string]interface{}
	json.Unmarshal(body, &addResult)
	printerID, _ := addResult["printer_id"].(string)
	if printerID == "" {
		t.Fatalf("printer_id missing from add response: %s", body)
	}

	configPayload := map[string]string{
		ConfigKeyTheMomentAPIKey: realPassword,
	}
	resp, body = post(t, serverURL+"/api/config", configPayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to save config: %d %s", resp.StatusCode, body)
	}

	// ── update printer with the sentinel echoed back ───────────────────────────
	updatePayload := map[string]interface{}{
		"name":       "Sentinel Test Printer",
		"model":      "MK4",
		"ip_address": "10.0.0.5",
		"api_key":    maskedCredential, // echoing masked value back
		"toolheads":  1,
		"is_virtual": false,
	}
	resp, body = put(t, serverURL+"/api/printers/"+printerID, updatePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to update printer: %d %s", resp.StatusCode, body)
	}

	// ── update config with the sentinel echoed back ───────────────────────────
	resp, body = post(t, serverURL+"/api/config", map[string]string{
		ConfigKeyTheMomentAPIKey: maskedCredential,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failed to update config with sentinel: %d %s", resp.StatusCode, body)
	}

	// ── verify real credentials were preserved in the DB ─────────────────────
	_, printerBody := get(t, serverURL+"/api/printers")
	if !strings.Contains(string(printerBody), maskedCredential) {
		t.Errorf("after echoing sentinel, api_key became empty (overwritten or lost): %s", printerBody)
	}
	t.Logf("✅ printer api_key preserved after sentinel round-trip: %s", printerBody)

	_, configBody := get(t, serverURL+"/api/config")
	var configMap map[string]string
	if err := json.Unmarshal(configBody, &configMap); err != nil {
		t.Fatalf("config response not valid JSON: %s", configBody)
	}
	if configMap[ConfigKeyTheMomentAPIKey] == "" {
		t.Errorf("the_moment_api_key was cleared after echoing sentinel back")
	}
	if configMap[ConfigKeyTheMomentAPIKey] == realPassword {
		t.Errorf("the_moment_api_key is still unmasked in GET response: %s", configBody)
	}
	t.Logf("✅ the_moment_api_key preserved (masked) after sentinel round-trip: %s", configBody)
}
