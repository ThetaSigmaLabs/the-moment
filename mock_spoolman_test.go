//go:build integration

package main

// =============================================================================
// mock_spoolman_test.go
// =============================================================================
// A fake Spoolman server that:
//   - Pre-loads test spools in memory
//   - Records every PATCH (usage update) call The Moment makes
//   - Lets tests assert the correct filament amounts were deducted
// =============================================================================

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// SpoolRecord represents a spool stored in the mock.
type SpoolRecord struct {
	ID            int     `json:"id"`
	UsedWeight    float64 `json:"used_weight"`
	InitialWeight float64 `json:"initial_weight"`
	Location      string  `json:"location"`
	// FilamentID is the filament bound to this spool. 0 means no filament (Filament=null in JSON).
	FilamentID int `json:"filament_id"`
}

// FilamentIDUpdate records a PATCH call that set a spool's filament_id.
type FilamentIDUpdate struct {
	SpoolID    int
	FilamentID int
}

// UsageUpdate records a single call The Moment made to update a spool.
type UsageUpdate struct {
	SpoolID    int
	UsedWeight float64 // The used_weight value sent in the PATCH body
}

// LocationUpdate records a PATCH call that set a spool's location.
type LocationUpdate struct {
	SpoolID      int
	LocationName string
}

// MockSpoolman is a fake Spoolman server.
type MockSpoolman struct {
	Server *httptest.Server

	mu               sync.RWMutex
	spools           map[int]*SpoolRecord
	nextSpoolID      int              // auto-increment for POST /api/v1/spool
	updates          []UsageUpdate    // PATCH calls with used_weight
	locationUpdates  []LocationUpdate // PATCH calls with location
	filamentUpdates  []FilamentIDUpdate // PATCH calls with filament_id
	offline          bool             // When true, all requests receive 503
}

// NewMockSpoolman creates and starts a fake Spoolman server pre-loaded with
// the given spools. spoolInitialWeights maps spool ID → initial weight in grams.
func NewMockSpoolman(t *testing.T, spoolInitialWeights map[int]float64) *MockSpoolman {
	t.Helper()

	mock := &MockSpoolman{
		spools:      make(map[int]*SpoolRecord),
		nextSpoolID: 1000,
	}

	// Pre-load spools (default: filament id == spool id, matching existing mock behaviour).
	for id, weight := range spoolInitialWeights {
		mock.spools[id] = &SpoolRecord{
			ID:            id,
			UsedWeight:    0,
			InitialWeight: weight,
			FilamentID:    id, // existing tests expect filament to be present
		}
		if id >= mock.nextSpoolID {
			mock.nextSpoolID = id + 1
		}
	}

	mux := http.NewServeMux()

	// GET /api/v1/info — Spoolman version info
	mux.HandleFunc("/api/v1/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version": "0.20.0"}`)
	})

	// GET + POST /api/v1/spool — list all spools (GET) or create a new spool (POST).
	mux.HandleFunc("/api/v1/spool", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/spool" {
			http.NotFound(w, r)
			return
		}

		// POST /api/v1/spool — create spool (used by Stage 5 create_new conflict choice).
		if r.Method == http.MethodPost {
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			mock.mu.Lock()
			newID := mock.nextSpoolID
			mock.nextSpoolID++
			filamentID := 0
			if fid, ok := body["filament_id"].(float64); ok {
				filamentID = int(fid)
			}
			mock.spools[newID] = &SpoolRecord{
				ID:            newID,
				InitialWeight: 1000,
				FilamentID:    filamentID,
			}
			mock.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":               newID,
				"used_weight":      0,
				"remaining_weight": 1000,
				"initial_weight":   1000,
			})
			return
		}

		mock.mu.RLock()
		defer mock.mu.RUnlock()

		var list []map[string]interface{}
		for _, spool := range mock.spools {
			remaining := spool.InitialWeight - spool.UsedWeight
			entry := map[string]interface{}{
				"id":               spool.ID,
				"used_weight":      spool.UsedWeight,
				"initial_weight":   spool.InitialWeight,
				"remaining_weight": remaining,
				"location":         spool.Location,
			}
			if spool.FilamentID > 0 {
				entry["filament"] = map[string]interface{}{
					"id":       spool.FilamentID,
					"name":     fmt.Sprintf("Test PLA %d", spool.FilamentID),
					"material": "PLA",
					"vendor":   map[string]interface{}{"id": 1, "name": "TestBrand"},
				}
			}
			list = append(list, entry)
		}
		if list == nil {
			list = []map[string]interface{}{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	// Handle /api/v1/spool/:id for GET and PATCH
	mux.HandleFunc("/api/v1/spool/", func(w http.ResponseWriter, r *http.Request) {
		// Extract ID from path: /api/v1/spool/42
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/spool/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}

		spoolID, err := strconv.Atoi(parts[0])
		if err != nil {
			http.Error(w, "invalid spool ID", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodGet:
			mock.mu.RLock()
			spool, ok := mock.spools[spoolID]
			mock.mu.RUnlock()

			if !ok {
				http.Error(w, fmt.Sprintf(`{"detail":"Spool %d not found"}`, spoolID), http.StatusNotFound)
				return
			}

			remaining := spool.InitialWeight - spool.UsedWeight
			resp := map[string]interface{}{
				"id":               spool.ID,
				"used_weight":      spool.UsedWeight,
				"initial_weight":   spool.InitialWeight,
				"remaining_weight": remaining,
				"location":         spool.Location,
			}
			if spool.FilamentID > 0 {
				resp["filament"] = map[string]interface{}{
					"id":       spool.FilamentID,
					"name":     fmt.Sprintf("Test PLA %d", spool.FilamentID),
					"material": "PLA",
					"vendor":   map[string]interface{}{"id": 1, "name": "TestBrand"},
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case http.MethodPatch:
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}

			mock.mu.Lock()
			spool, ok := mock.spools[spoolID]
			if !ok {
				mock.mu.Unlock()
				http.Error(w, fmt.Sprintf(`{"detail":"Spool %d not found"}`, spoolID), http.StatusNotFound)
				return
			}

			// Record weight update.
			if usedWeight, ok := body["used_weight"].(float64); ok {
				spool.UsedWeight = usedWeight
				mock.updates = append(mock.updates, UsageUpdate{
					SpoolID:    spoolID,
					UsedWeight: usedWeight,
				})
			}

			// Record and persist location update.
			if loc, ok := body["location"].(string); ok {
				spool.Location = loc
				mock.locationUpdates = append(mock.locationUpdates, LocationUpdate{
					SpoolID:      spoolID,
					LocationName: loc,
				})
			}

			// Record and persist filament_id update.
			if fid, ok := body["filament_id"].(float64); ok {
				spool.FilamentID = int(fid)
				mock.filamentUpdates = append(mock.filamentUpdates, FilamentIDUpdate{
					SpoolID:    spoolID,
					FilamentID: int(fid),
				})
			}

			remaining := spool.InitialWeight - spool.UsedWeight
			mock.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":               spoolID,
				"used_weight":      spool.UsedWeight,
				"remaining_weight": remaining,
			})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// GET /api/v1/location — return empty locations list
	mux.HandleFunc("/api/v1/location", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	})

	// GET /api/v1/filament
	mux.HandleFunc("/api/v1/filament", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	})

	mock.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mock.mu.RLock()
		offline := mock.offline
		mock.mu.RUnlock()
		if offline {
			http.Error(w, `{"detail":"Service unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { mock.Server.Close() })

	return mock
}

// URL returns the base URL of the mock server.
func (m *MockSpoolman) URL() string {
	return m.Server.URL
}

// Updates returns a copy of all usage updates received so far.
func (m *MockSpoolman) Updates() []UsageUpdate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]UsageUpdate, len(m.updates))
	copy(result, m.updates)
	return result
}

// UpdatesForSpool returns all usage updates for a specific spool ID.
func (m *MockSpoolman) UpdatesForSpool(spoolID int) []UsageUpdate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []UsageUpdate
	for _, u := range m.updates {
		if u.SpoolID == spoolID {
			result = append(result, u)
		}
	}
	return result
}

// RemainingWeight returns the current remaining weight for a spool.
func (m *MockSpoolman) RemainingWeight(spoolID int) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	spool, ok := m.spools[spoolID]
	if !ok {
		return -1
	}
	return spool.InitialWeight - spool.UsedWeight
}

// ResetUpdates clears the recorded updates (useful between sub-tests).
func (m *MockSpoolman) ResetUpdates() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates = nil
	m.locationUpdates = nil
}

// LocationUpdates returns a copy of all location updates received so far.
func (m *MockSpoolman) LocationUpdates() []LocationUpdate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]LocationUpdate, len(m.locationUpdates))
	copy(result, m.locationUpdates)
	return result
}

// LocationUpdatesForSpool returns all location updates for a specific spool ID.
func (m *MockSpoolman) LocationUpdatesForSpool(spoolID int) []LocationUpdate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []LocationUpdate
	for _, u := range m.locationUpdates {
		if u.SpoolID == spoolID {
			result = append(result, u)
		}
	}
	return result
}

// SetOffline makes the mock return HTTP 503 for all requests when true,
// simulating Spoolman being unreachable.
func (m *MockSpoolman) SetOffline(offline bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.offline = offline
}

// SetUsedWeight directly sets the used_weight on a mock spool, so that
// remaining_weight = initial_weight - used_weight reflects a partially-consumed spool.
func (m *MockSpoolman) SetUsedWeight(spoolID int, usedWeight float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if spool, ok := m.spools[spoolID]; ok {
		spool.UsedWeight = usedWeight
	}
}

// SetSpoolLocation directly sets the location field on a mock spool, simulating
// a user editing the spool location in Spoolman's own UI.
func (m *MockSpoolman) SetSpoolLocation(spoolID int, location string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if spool, ok := m.spools[spoolID]; ok {
		spool.Location = location
	}
}

// FilamentIDUpdates returns a copy of all filament_id updates received so far.
func (m *MockSpoolman) FilamentIDUpdates() []FilamentIDUpdate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]FilamentIDUpdate, len(m.filamentUpdates))
	copy(result, m.filamentUpdates)
	return result
}

// AddSpool adds a custom SpoolRecord to the mock, letting tests configure a spool
// with no filament (FilamentID=0) or a specific filament ID.
func (m *MockSpoolman) AddSpool(s SpoolRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spools[s.ID] = &s
	if s.ID >= m.nextSpoolID {
		m.nextSpoolID = s.ID + 1
	}
}
