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

	// Job info returned by GET /api/v1/job
	JobFileName    string // e.g. "usb/testprint.gcode"
	JobDisplayName string

	// G-code content returned when the file is downloaded
	GcodeContent string

	// When true, G-code download requests return 503 (simulates USB busy / file gone)
	GcodeUnavailable bool

	// Track how many times each endpoint was called
	StatusCalls int
	JobCalls    int
	FileCalls   int
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
		state.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"printer": {
				"state": %q,
				"temperature": {
					"bed":   {"actual": 60.0, "target": 60.0},
					"tool0": {"actual": 215.0, "target": 215.0}
				},
				"telemetry": {
					"print_time": 3600,
					"print_time_left": 1800,
					"progress": %f
				}
			}
		}`, currentState, progress)
	})

	// GET /api/v1/job — returns current job info
	mux.HandleFunc("/api/v1/job", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.JobCalls++
		currentState := state.PrinterState
		filename := state.JobFileName
		displayName := state.JobDisplayName
		progress := state.Progress
		state.mu.Unlock()

		// Return 204 No Content when idle — matches real PrusaLink behaviour
		if currentState == StateIdle || currentState == StateFinished {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"id": 1,
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
		}`, currentState, progress, filename, displayName, filename)
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
