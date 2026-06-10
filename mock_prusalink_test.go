//go:build integration

package main

// =============================================================================
// mock_prusalink_test.go
// =============================================================================
// A controllable fake PrusaLink server. Tests set the state they want, then
// call bridge.monitorPrusaLink() to trigger one polling cycle. No real printer
// is needed at any point.
// =============================================================================

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// MockPrinterState holds the current simulated state of the fake printer.
type MockPrinterState struct {
	mu sync.RWMutex

	// State returned by GET /api/v1/status
	PrinterState string  // "IDLE", "PRINTING", "PAUSED", "ATTENTION", "FINISHED", "STOPPED"
	Progress     float64 // 0..100
	JobID        int     // job ID returned in status/job responses; default 1

	// Job info returned by GET /api/v1/job
	JobFileName    string // e.g. "usb/testprint.gcode"
	JobDisplayName string

	// G-code content returned when the file is downloaded
	GcodeContent string

	// When true, G-code download requests return 503 (simulates USB busy / file gone)
	GcodeUnavailable bool

	// FileInfoFilament is the per-toolhead filament data returned by GET /api/v1/files/local/...
	// When nil, the endpoint returns 404 (metadata unavailable — triggers G-code fallback).
	FileInfoFilament []struct {
		ToolheadID int     `json:"toolhead_id"`
		Weight     float64 `json:"weight"`
		Length     float64 `json:"length"`
	}

	// Camera snapshot returned by GET /api/v1/cameras/{id}/snap
	// nil means no camera (GET /api/v1/cameras returns 404)
	CameraSnapshot []byte

	// Track how many times each endpoint was called
	StatusCalls    int
	JobCalls       int
	FileCalls      int
	FileInfoCalls  int
	SnapshotCalls  int
}

// MockPrusaLink is a fake PrusaLink-compatible printer server.
type MockPrusaLink struct {
	Server *httptest.Server
	State  *MockPrinterState
}

// NewMockPrusaLink creates and starts a fake PrusaLink server.
func NewMockPrusaLink(t *testing.T) *MockPrusaLink {
	t.Helper()

	state := &MockPrinterState{
		PrinterState:   StateIdle,
		JobID:          1,
		JobFileName:    "usb/testprint.gcode",
		JobDisplayName: "Test Print",
		GcodeContent:   gcodeWithUsage(10.0), // default: single toolhead, 10g
	}

	mux := http.NewServeMux()
	mock := &MockPrusaLink{State: state}

	// GET /api/v1/status — returns printer state
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.StatusCalls++
		currentState := state.PrinterState
		progress := state.Progress
		jobID := state.JobID
		state.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"job": {
				"id": %d,
				"progress": %f,
				"time_remaining": 1800,
				"time_printing": 3600
			},
			"printer": {
				"state": %q,
				"temp_nozzle": 215.0,
				"target_nozzle": 215.0,
				"temp_bed": 60.0,
				"target_bed": 60.0,
				"axis_z": 10.0,
				"flow": 100,
				"speed": 100,
				"fan_hotend": 5000,
				"fan_print": 4500
			}
		}`, jobID, progress, currentState)
	})

	// GET /api/v1/job — returns current job info
	mux.HandleFunc("/api/v1/job", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.JobCalls++
		currentState := state.PrinterState
		filename := state.JobFileName
		displayName := state.JobDisplayName
		progress := state.Progress
		jobID := state.JobID
		state.mu.Unlock()

		// Return 204 No Content when idle — matches real PrusaLink behaviour
		if currentState == StateIdle || currentState == StateFinished {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"id": %d,
			"state": %q,
			"progress": %f,
			"time_remaining": 1800,
			"time_printing": 3600,
			"file": {
				"name": %q,
				"display_name": %q,
				"path": "/usb",
				"size": 12345,
				"refs": {
					"download": "/%s"
				}
			}
		}`, jobID, currentState, progress, filename, displayName, filename)
	})

	// GET /api/v1/info — printer info
	mux.HandleFunc("/api/v1/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"hostname": "mock-printer",
			"serial": "TEST123",
			"nozzle_diameter": 0.4,
			"mmu": false,
			"min_extrusion_temp": 170
		}`)
	})

	// GET /api/v1/files/local/... — file metadata (404 when FileInfoFilament is nil)
	mux.HandleFunc("/api/v1/files/local/", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		filament := state.FileInfoFilament
		state.FileInfoCalls++
		state.mu.Unlock()

		if filament == nil {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"name":     state.JobFileName,
			"filament": filament,
		}
		json.NewEncoder(w).Encode(resp)
	})

	// GET /api/v1/cameras — list cameras (404 when CameraSnapshot is nil)
	mux.HandleFunc("/api/v1/cameras", func(w http.ResponseWriter, r *http.Request) {
		state.mu.RLock()
		snap := state.CameraSnapshot
		state.mu.RUnlock()
		if snap == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":"buddy","name":"Buddy Camera","connected":true}]`)
	})

	// GET /api/v1/cameras/buddy/snap — return JPEG snapshot
	mux.HandleFunc("/api/v1/cameras/buddy/snap", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		snap := state.CameraSnapshot
		state.SnapshotCalls++
		state.mu.Unlock()
		if snap == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(snap) //nolint:errcheck
	})

	// GET /{filename} — G-code file download
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}

		state.mu.Lock()
		content := state.GcodeContent
		expectedFile := state.JobFileName
		unavailable := state.GcodeUnavailable
		state.FileCalls++
		state.mu.Unlock()

		if unavailable {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		// Serve the file if the path matches
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == expectedFile {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, content)
			return
		}

		http.NotFound(w, r)
	})

	mock.Server = httptest.NewServer(mux)
	t.Cleanup(func() { mock.Server.Close() })

	return mock
}

// SetState transitions the printer to a new state.
func (m *MockPrusaLink) SetState(state string) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.PrinterState = state
}

// SetProgress sets the print progress percentage (0..100).
func (m *MockPrusaLink) SetProgress(pct float64) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.Progress = pct
}

// SetGcodeUnavailable makes G-code download requests return HTTP 503 when true,
// simulating USB storage being busy or the file having been removed.
func (m *MockPrusaLink) SetGcodeUnavailable(unavailable bool) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.GcodeUnavailable = unavailable
}

// SetCameraSnapshot configures a JPEG payload to return from the camera endpoint.
// Pass nil to simulate a printer with no camera (GET /api/v1/cameras returns 404).
func (m *MockPrusaLink) SetCameraSnapshot(jpeg []byte) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.CameraSnapshot = jpeg
}

// SetJobID sets the job ID returned by the /api/v1/status and /api/v1/job endpoints.
// Use unique IDs per test to prevent GetActivePrintSession cross-test interference.
func (m *MockPrusaLink) SetJobID(id int) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.JobID = id
}

// SnapshotCallCount returns how many times the snapshot endpoint was called.
func (m *MockPrusaLink) SnapshotCallCount() int {
	m.State.mu.RLock()
	defer m.State.mu.RUnlock()
	return m.State.SnapshotCalls
}

// SetFileInfoFilament sets the per-toolhead filament data returned by the /api/v1/files/local/ endpoint.
// Pass nil to simulate firmware that does not support file metadata (triggers G-code fallback).
func (m *MockPrusaLink) SetFileInfoFilament(weights map[int]float64) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	if weights == nil {
		m.State.FileInfoFilament = nil
		return
	}
	type entry struct {
		ToolheadID int     `json:"toolhead_id"`
		Weight     float64 `json:"weight"`
		Length     float64 `json:"length"`
	}
	var filament []struct {
		ToolheadID int     `json:"toolhead_id"`
		Weight     float64 `json:"weight"`
		Length     float64 `json:"length"`
	}
	for idx, w := range weights {
		filament = append(filament, entry{ToolheadID: idx, Weight: w})
	}
	m.State.FileInfoFilament = filament
}

// SetGcodeUsage sets the filament usage metadata in the fake G-code.
// toolheadWeights maps toolhead index → grams.
func (m *MockPrusaLink) SetGcodeUsage(toolheadWeights map[int]float64) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.GcodeContent = gcodeWithMultiUsage(toolheadWeights)
}

// HostPort returns just the host:port portion needed for PrinterConfig.IPAddress.
func (m *MockPrusaLink) HostPort() string {
	// httptest URLs look like "http://127.0.0.1:PORT"
	return strings.TrimPrefix(m.Server.URL, "http://")
}

// printerConfig returns a PrinterConfig pointing at this mock server.
func (m *MockPrusaLink) PrinterConfig(name string, toolheads int) PrinterConfig {
	return PrinterConfig{
		Name:      name,
		Model:     ModelCoreOneL,
		IPAddress: m.HostPort(),
		APIKey:    "", // Mock does not require auth
		Toolheads: toolheads,
	}
}

// ─── G-code generation helpers ────────────────────────────────────────────────

// gcodeWithUsage generates a minimal G-code string with a single toolhead weight.
func gcodeWithUsage(weightG float64) string {
	return fmt.Sprintf(`; generated by PrusaSlicer
; filament used [g] = %.2f
G28
G1 X0 Y0 Z0.2
G1 X100 Y0 E10
`, weightG)
}

// gcodeWithMultiUsage generates G-code with per-toolhead weights.
func gcodeWithMultiUsage(weights map[int]float64) string {
	// Find the max toolhead index to build the comma-separated list
	maxIdx := 0
	for idx := range weights {
		if idx > maxIdx {
			maxIdx = idx
		}
	}

	parts := make([]string, maxIdx+1)
	for i := 0; i <= maxIdx; i++ {
		if w, ok := weights[i]; ok {
			parts[i] = fmt.Sprintf("%.2f", w)
		} else {
			parts[i] = "0.00"
		}
	}

	return fmt.Sprintf(`; generated by PrusaSlicer
; filament used [g] = %s
G28
G1 X0 Y0 Z0.2
`, strings.Join(parts, ", "))
}

// ─── JSON helper ──────────────────────────────────────────────────────────────

// mustMarshal marshals v to JSON or panics (test helper only).
func mustMarshal(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustMarshal: %v", err))
	}
	return b
}
