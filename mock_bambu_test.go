//go:build integration

package main

// =============================================================================
// mock_bambu_test.go
// =============================================================================
// A controllable fake Bambu printer client. Tests set the state they want,
// then call bridge.monitorBambu() to trigger one polling cycle. No real
// printer, MQTT broker, or network connection is needed.
//
// Usage:
//
//	bridge, mock, spoolman := setupBridgeWithBambuMock(t, map[int]float64{1: 1000})
//	mock.SetState("RUNNING")
//	mock.SetFilamentTotal(25.0)
//	pollBambu(t, bridge, "test-printer-id", bambuTestConfig("My X1C", 1))
// =============================================================================

import (
	"fmt"
	"sync"
	"testing"
)

// MockBambuState holds the simulated state returned by MockBambuClient.
type MockBambuState struct {
	mu sync.RWMutex

	GcodeState          string              // "IDLE","RUNNING","PAUSE","FINISH","FAILED","CANCEL"
	McPercent           int                 // 0-100
	GcodeFile           string
	FilamentWeightTotal float64
	AMSSlots            map[int]AMSSlotInfo

	// ConnectError simulates a TLS/MQTT connection failure.
	ConnectError error
	// StatusUnavailable simulates the state before the first MQTT message arrives.
	StatusUnavailable bool
}

// MockBambuClient implements BambuStatusProvider for tests.
type MockBambuClient struct {
	State         *MockBambuState
	connectCalled int
	closeCalled   int
}

// NewMockBambuClient creates a mock client in the IDLE state.
func NewMockBambuClient() *MockBambuClient {
	return &MockBambuClient{
		State: &MockBambuState{
			GcodeState: StateIdle,
			AMSSlots:   make(map[int]AMSSlotInfo),
		},
	}
}

// Connect records the call and returns any configured error.
func (m *MockBambuClient) Connect() error {
	m.State.mu.Lock()
	m.connectCalled++
	err := m.State.ConnectError
	m.State.mu.Unlock()
	return err
}

// GetCurrentStatus returns a copy of the current simulated state.
// Returns an error when StatusUnavailable is true.
func (m *MockBambuClient) GetCurrentStatus() (*BambuStatus, error) {
	m.State.mu.RLock()
	defer m.State.mu.RUnlock()

	if m.State.StatusUnavailable {
		return nil, fmt.Errorf("no status received yet (simulated)")
	}

	slots := make(map[int]AMSSlotInfo, len(m.State.AMSSlots))
	for k, v := range m.State.AMSSlots {
		slots[k] = v
	}
	return &BambuStatus{
		GcodeState:          m.State.GcodeState,
		McPercent:           m.State.McPercent,
		GcodeFile:           m.State.GcodeFile,
		FilamentWeightTotal: m.State.FilamentWeightTotal,
		AMSSlots:            slots,
	}, nil
}

// Close records the call.
func (m *MockBambuClient) Close() error {
	m.State.mu.Lock()
	m.closeCalled++
	m.State.mu.Unlock()
	return nil
}

// SetState transitions to a new Bambu gcode_state value.
func (m *MockBambuClient) SetState(state string) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.GcodeState = state
}

// SetProgress sets the McPercent field (0–100).
func (m *MockBambuClient) SetProgress(pct int) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.McPercent = pct
}

// SetFilamentTotal sets the FilamentWeightTotal field in grams.
func (m *MockBambuClient) SetFilamentTotal(grams float64) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.FilamentWeightTotal = grams
}

// SetGcodeFile sets the GcodeFile field.
func (m *MockBambuClient) SetGcodeFile(filename string) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.GcodeFile = filename
}

// SetAMSSlot adds or updates a single AMS slot.
func (m *MockBambuClient) SetAMSSlot(slotIdx int, info AMSSlotInfo) {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.AMSSlots[slotIdx] = info
}

// ClearAMSSlots removes all AMS slot data.
func (m *MockBambuClient) ClearAMSSlots() {
	m.State.mu.Lock()
	defer m.State.mu.Unlock()
	m.State.AMSSlots = make(map[int]AMSSlotInfo)
}

// ─── Test scaffolding ─────────────────────────────────────────────────────────

// setupBridgeWithBambuMock creates a FilamentBridge wired to a MockBambuClient
// and a MockSpoolman. The mock factory replaces the real MQTT factory so no
// network connection is ever attempted.
func setupBridgeWithBambuMock(t *testing.T, spoolMap map[int]float64) (*FilamentBridge, *MockBambuClient, *MockSpoolman) {
	t.Helper()

	mockClient := NewMockBambuClient()
	spoolman := NewMockSpoolman(t, spoolMap)

	t.Setenv("THE_MOMENT_DB_PATH", t.TempDir())
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })

	config, err := LoadConfig(bridge)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	config.SpoolmanURL = spoolman.URL()
	bridge.UpdateConfig(config)

	// Override the factory so monitorBambu uses the mock instead of real MQTT.
	bridge.bambuClientFactory = func(_, _, _ string) BambuStatusProvider {
		return mockClient
	}

	return bridge, mockClient, spoolman
}

// pollBambu triggers a single monitorBambu cycle — equivalent to one ticker tick.
func pollBambu(t *testing.T, bridge *FilamentBridge, printerID string, config PrinterConfig) {
	t.Helper()
	if err := bridge.monitorBambu(printerID, config); err != nil {
		t.Fatalf("monitorBambu: %v", err)
	}
}

// bambuTestConfig returns a PrinterConfig for Bambu tests.
// No real IP is contacted — the mock factory intercepts it.
func bambuTestConfig(name string, toolheads int) PrinterConfig {
	return PrinterConfig{
		Name:        name,
		Model:       "X1C",
		IPAddress:   "192.168.1.100",
		APIKey:      "00M09C000001:testaccesscode",
		Toolheads:   toolheads,
		PrinterType: PrinterTypeBambu,
	}
}
