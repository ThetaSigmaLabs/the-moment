// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

//go:build integration

package main

// =============================================================================
// octoprint_integration_test.go
// =============================================================================
// Integration tests for POST /api/prints — the OctoPrint push endpoint.
//
// Run with:
//   go test ./... -tags integration -v -run TestOctoPrintEndpoint
// =============================================================================

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// getHistoryRecords fetches GET /api/history and returns the records slice.
func getHistoryRecords(t *testing.T, serverURL string) []map[string]interface{} {
	t.Helper()
	_, body := get(t, serverURL+"/api/history")
	var wrapper struct {
		Records []map[string]interface{} `json:"records"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		t.Fatalf("history response not JSON: %v — body: %s", err, body)
	}
	return wrapper.Records
}

// postWithHeaders sends a POST with an arbitrary header map.
func postWithHeaders(t *testing.T, url string, body interface{}, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	return resp, buf
}

// ─── Tests ───────────────────────────────────────────────────────────────────

// TestOctoPrintEndpoint_ValidPayload checks that a well-formed payload returns
// 201 with an id, and the record appears in GET /api/history.
func TestOctoPrintEndpoint_ValidPayload(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	payload := map[string]interface{}{
		"source":             "octoprint",
		"printer_id":         "ender3-v3-se",
		"file_name":          "benchy.gcode",
		"status":             "completed",
		"started_at":         "2026-04-19T10:00:00Z",
		"ended_at":           "2026-04-19T11:23:00Z",
		"total_duration_sec": 4980,
		"print_duration_sec": 4620,
		"pause_duration_sec": 360,
		"pause_count":        1,
		"cancel_reason":      nil,
		"time_precision":     "exact",
		"filament_precision": "measured",
		"pauses": []map[string]interface{}{
			{
				"paused_at":    "2026-04-19T10:45:00Z",
				"resumed_at":   "2026-04-19T10:51:00Z",
				"duration_sec": 360,
				"reason":       "runout",
			},
		},
		"filament": []map[string]interface{}{
			{"tool_index": 0, "spool_id": 0, "filament_used_mm": 4821.3, "filament_used_grams": 14.3},
		},
	}

	resp, body := post(t, serverURL+"/api/prints", payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("response not JSON: %v — body: %s", err, body)
	}
	id, ok := result["id"]
	if !ok || id == nil {
		t.Fatalf("response missing 'id': %s", body)
	}

	history := getHistoryRecords(t, serverURL)
	if len(history) == 0 {
		t.Fatal("expected at least one record in history")
	}
	rec := history[0]
	if rec["source"] != "octoprint" {
		t.Errorf("expected source='octoprint', got %v", rec["source"])
	}
	if rec["job_name"] != "benchy.gcode" {
		t.Errorf("expected job_name='benchy.gcode', got %v", rec["job_name"])
	}
	if rec["time_precision"] != "exact" {
		t.Errorf("expected time_precision='exact', got %v", rec["time_precision"])
	}
	t.Logf("✅ POST /api/prints → id=%v, history confirmed", id)
}

// TestOctoPrintEndpoint_DetailIncludesFilamentAndPauses verifies that
// GET /api/history/:id returns filament_usages and pauses arrays.
func TestOctoPrintEndpoint_DetailIncludesFilamentAndPauses(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	payload := map[string]interface{}{
		"source":             "octoprint",
		"printer_id":         "ender3-v3-se",
		"file_name":          "detail_test.gcode",
		"status":             "completed",
		"started_at":         "2026-04-19T08:00:00Z",
		"ended_at":           "2026-04-19T09:30:00Z",
		"total_duration_sec": 5400,
		"print_duration_sec": 5100,
		"pause_duration_sec": 300,
		"pause_count":        1,
		"time_precision":     "exact",
		"filament_precision": "measured",
		"pauses": []map[string]interface{}{
			{"paused_at": "2026-04-19T09:00:00Z", "resumed_at": "2026-04-19T09:05:00Z",
				"duration_sec": 300, "reason": "user"},
		},
		"filament": []map[string]interface{}{
			{"tool_index": 0, "spool_id": 0, "filament_used_mm": 2000, "filament_used_grams": 5.9},
			{"tool_index": 1, "spool_id": 0, "filament_used_mm": 1500, "filament_used_grams": 4.4},
		},
	}

	resp, body := post(t, serverURL+"/api/prints", payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}
	var created map[string]interface{}
	json.Unmarshal(body, &created)
	id := int(created["id"].(float64))

	_, detailBody := get(t, serverURL+fmt.Sprintf("/api/history/%d", id))
	var detail map[string]interface{}
	if err := json.Unmarshal(detailBody, &detail); err != nil {
		t.Fatalf("detail not JSON: %v", err)
	}

	usages, _ := detail["filament_usages"].([]interface{})
	if len(usages) != 2 {
		t.Errorf("expected 2 filament_usages, got %d — body: %s", len(usages), detailBody)
	}
	pauses, _ := detail["pauses"].([]interface{})
	if len(pauses) != 1 {
		t.Errorf("expected 1 pause, got %d — body: %s", len(pauses), detailBody)
	}
	t.Logf("✅ GET /api/history/%d → %d filament usages, %d pauses", id, len(usages), len(pauses))
}

// TestOctoPrintEndpoint_MultiToolheadTotalFilament verifies that a two-toolhead
// print reports the sum of both tools' filament in the history record.
func TestOctoPrintEndpoint_MultiToolheadTotalFilament(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	payload := map[string]interface{}{
		"source":             "octoprint",
		"printer_id":         "ender3-v3-se",
		"file_name":          "multi_tool.gcode",
		"status":             "completed",
		"started_at":         "2026-04-20T06:00:00Z",
		"ended_at":           "2026-04-20T08:00:00Z",
		"total_duration_sec": 7200,
		"print_duration_sec": 7200,
		"time_precision":     "exact",
		"filament_precision": "measured",
		"filament": []map[string]interface{}{
			{"tool_index": 0, "spool_id": 0, "filament_used_mm": 3000, "filament_used_grams": 8.9},
			{"tool_index": 1, "spool_id": 0, "filament_used_mm": 2100, "filament_used_grams": 6.2},
		},
	}

	resp, body := post(t, serverURL+"/api/prints", payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}

	history := getHistoryRecords(t, serverURL)
	if len(history) == 0 {
		t.Fatal("expected history record")
	}
	totalGrams, _ := history[0]["filament_used"].(float64)
	if totalGrams < 15.0 || totalGrams > 15.2 {
		t.Errorf("expected filament_used ≈ 15.1g, got %.3f", totalGrams)
	}
	t.Logf("✅ Multi-toolhead: total filament=%.3fg", totalGrams)
}

// TestOctoPrintEndpoint_APIKeyRequired verifies that when a key is configured,
// requests without it are rejected 401 and with the correct key are accepted.
func TestOctoPrintEndpoint_APIKeyRequired(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	post(t, serverURL+"/api/config", map[string]string{
		ConfigKeyTheMomentAPIKey: "test-secret-key",
	})

	minimalPayload := map[string]interface{}{
		"printer_id": "ender3-v3-se",
		"file_name":  "auth_test.gcode",
		"status":     "completed",
		"ended_at":   "2026-04-19T12:00:00Z",
		"filament":   []interface{}{},
	}

	// No key → 401
	resp, _ := post(t, serverURL+"/api/prints", minimalPayload)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without key, got %d", resp.StatusCode)
	}

	// Wrong key → 401
	resp, _ = postWithHeaders(t, serverURL+"/api/prints", minimalPayload,
		map[string]string{"X-API-Key": "wrong-key"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong key, got %d", resp.StatusCode)
	}

	// Correct key → 201
	resp, body := postWithHeaders(t, serverURL+"/api/prints", minimalPayload,
		map[string]string{"X-API-Key": "test-secret-key"})
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201 with correct key, got %d: %s", resp.StatusCode, body)
	}
	t.Logf("✅ API key auth: 401 without, 401 wrong, 201 correct")
}

// TestOctoPrintEndpoint_MissingRequiredFields returns 400 for missing printer_id.
func TestOctoPrintEndpoint_MissingRequiredFields(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	resp, body := post(t, serverURL+"/api/prints", map[string]interface{}{
		"file_name": "no_printer.gcode",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing printer_id, got %d: %s", resp.StatusCode, body)
	}
	t.Logf("✅ Missing printer_id → 400")
}

// TestSessionsEndpoint_GroupsBySessionID verifies that two prints sharing a
// session_id appear as one session in GET /api/sessions, and a standalone print
// appears as its own session.
func TestSessionsEndpoint_GroupsBySessionID(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	sharedSession := "test-session-abcd-1234"

	for _, file := range []string{"tool0.gcode", "tool1.gcode"} {
		resp, body := post(t, serverURL+"/api/prints", map[string]interface{}{
			"session_id":         sharedSession,
			"printer_id":         "core-one",
			"file_name":          file,
			"status":             "completed",
			"ended_at":           "2026-04-20T11:00:00Z",
			"total_duration_sec": 3600,
			"filament": []map[string]interface{}{
				{"tool_index": 0, "spool_id": 0, "filament_used_mm": 1000, "filament_used_grams": 3.0},
			},
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 posting %s, got %d: %s", file, resp.StatusCode, body)
		}
	}

	// One standalone print with a different session.
	resp, body := post(t, serverURL+"/api/prints", map[string]interface{}{
		"session_id":         "other-session-xyz",
		"printer_id":         "ender3",
		"file_name":          "solo.gcode",
		"status":             "completed",
		"ended_at":           "2026-04-19T10:00:00Z",
		"total_duration_sec": 1800,
		"filament": []map[string]interface{}{
			{"tool_index": 0, "spool_id": 0, "filament_used_mm": 500, "filament_used_grams": 1.5},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 posting solo.gcode, got %d: %s", resp.StatusCode, body)
	}

	_, sessBody := get(t, serverURL+"/api/sessions")
	var wrapper struct {
		Sessions []map[string]interface{} `json:"sessions"`
		Count    int                      `json:"count"`
	}
	if err := json.Unmarshal(sessBody, &wrapper); err != nil {
		t.Fatalf("sessions response not JSON: %v — body: %s", err, sessBody)
	}
	if wrapper.Count != 2 {
		t.Errorf("expected 2 sessions (1 shared + 1 solo), got %d — body: %s", wrapper.Count, sessBody)
	}

	// Shared session (newest) should have tool_count=2.
	shared := wrapper.Sessions[0]
	if int(shared["tool_count"].(float64)) != 2 {
		t.Errorf("expected tool_count=2 for shared session, got %v", shared["tool_count"])
	}
	if shared["session_id"] != sharedSession {
		t.Errorf("expected session_id=%s, got %v", sharedSession, shared["session_id"])
	}
	t.Logf("✅ GET /api/sessions → %d sessions; shared tool_count=%v", wrapper.Count, shared["tool_count"])
}

// ─── Ping / connectivity tests ────────────────────────────────────────────────

// TestOctoPrintPing_NoAPIKey verifies that GET /api/octoprint/ping returns 200
// when no API key is configured on the server.
func TestOctoPrintPing_NoAPIKey(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	resp, body := get(t, serverURL+"/api/octoprint/ping")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("response not JSON: %v — body: %s", err, body)
	}
	if result["status"] != "ok" {
		t.Errorf("expected status='ok', got %v", result["status"])
	}
	if result["server"] != "The Moment" {
		t.Errorf("expected server='The Moment', got %v", result["server"])
	}
	if result["timestamp"] == nil || result["timestamp"] == "" {
		t.Error("expected non-empty timestamp in ping response")
	}
	t.Logf("✅ GET /api/octoprint/ping → 200, status=%v", result["status"])
}

// TestOctoPrintPing_WithAPIKey verifies that the ping endpoint respects API key auth
// in the same way as POST /api/prints.
func TestOctoPrintPing_WithAPIKey(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	// Configure an API key on the server.
	post(t, serverURL+"/api/config", map[string]string{
		ConfigKeyTheMomentAPIKey: "ping-secret",
	})

	// No key → 401
	resp, body := get(t, serverURL+"/api/octoprint/ping")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without key, got %d: %s", resp.StatusCode, body)
	}

	// Wrong key → 401
	req, _ := http.NewRequest(http.MethodGet, serverURL+"/api/octoprint/ping", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	wrongResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET with wrong key failed: %v", err)
	}
	wrongResp.Body.Close()
	if wrongResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong key, got %d", wrongResp.StatusCode)
	}

	// Correct key → 200
	req2, _ := http.NewRequest(http.MethodGet, serverURL+"/api/octoprint/ping", nil)
	req2.Header.Set("X-API-Key", "ping-secret")
	goodResp, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("GET with correct key failed: %v", err)
	}
	defer goodResp.Body.Close()
	if goodResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with correct key, got %d", goodResp.StatusCode)
	}
	t.Logf("✅ Ping auth: 401 no key, 401 wrong key, 200 correct key")
}

// TestOctoPrintDebugMode verifies that the server logs a debug line when
// ConfigKeyOctoPrintDebug is enabled and accepts a print payload normally.
func TestOctoPrintDebugMode(t *testing.T) {
	serverURL, cleanup := testServer(t)
	defer cleanup()

	// Enable server-side debug logging.
	post(t, serverURL+"/api/config", map[string]string{
		ConfigKeyOctoPrintDebug: "true",
	})

	payload := map[string]interface{}{
		"printer_id": "debug-printer",
		"file_name":  "debug_test.gcode",
		"status":     "completed",
		"ended_at":   "2026-04-24T10:00:00Z",
		"filament":   []interface{}{},
	}
	resp, body := post(t, serverURL+"/api/prints", payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 with debug mode on, got %d: %s", resp.StatusCode, body)
	}
	t.Logf("✅ POST /api/prints succeeds with debug mode enabled")
}
