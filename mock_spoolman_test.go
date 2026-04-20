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
	ID          int     `json:"id"`
	UsedWeight  float64 `json:"used_weight"`
	InitialWeight float64 `json:"initial_weight"`
}

// UsageUpdate records a single call The Moment made to update a spool.
type UsageUpdate struct {
	SpoolID    int
	UsedWeight float64 // The used_weight value sent in the PATCH body
}

// MockSpoolman is a fake Spoolman server.
type MockSpoolman struct {
	Server *httptest.Server

	mu      sync.RWMutex
	spools  map[int]*SpoolRecord
	updates []UsageUpdate // All PATCH /api/v1/spool/:id calls received
	offline bool          // When true, all requests receive 503
}

// NewMockSpoolman creates and starts a fake Spoolman server pre-loaded with
// the given spools. spoolInitialWeights maps spool ID → initial weight in grams.
func NewMockSpoolman(t *testing.T, spoolInitialWeights map[int]float64) *MockSpoolman {
	t.Helper()

	mock := &MockSpoolman{
		spools:  make(map[int]*SpoolRecord),
		updates: nil,
	}

	// Pre-load spools
	for id, weight := range spoolInitialWeights {
		mock.spools[id] = &SpoolRecord{
			ID:            id,
			UsedWeight:    0,
			InitialWeight: weight,
		}
	}

	mux := http.NewServeMux()

	// GET /api/v1/info — Spoolman version info
	mux.HandleFunc("/api/v1/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version": "0.20.0"}`)
	})

	// GET /api/v1/spool — list all spools
	mux.HandleFunc("/api/v1/spool", func(w http.ResponseWriter, r *http.Request) {
		// Route PATCH /api/v1/spool/:id here since Go's mux is prefix-based
		// Only handle the exact /api/v1/spool path for GET
		if r.URL.Path != "/api/v1/spool" {
			http.NotFound(w, r)
			return
		}

		mock.mu.RLock()
		defer mock.mu.RUnlock()

		var list []map[string]interface{}
		for _, spool := range mock.spools {
			remaining := spool.InitialWeight - spool.UsedWeight
			list = append(list, map[string]interface{}{
				"id":               spool.ID,
				"used_weight":      spool.UsedWeight,
				"initial_weight":   spool.InitialWeight,
				"remaining_weight": remaining,
				"filament": map[string]interface{}{
					"id":       spool.ID,
					"name":     fmt.Sprintf("Test PLA %d", spool.ID),
					"material": "PLA",
					"vendor": map[string]interface{}{
						"id":   1,
						"name": "TestBrand",
					},
				},
			})
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
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":               spool.ID,
				"used_weight":      spool.UsedWeight,
				"initial_weight":   spool.InitialWeight,
				"remaining_weight": remaining,
				"filament": map[string]interface{}{
					"id":       spool.ID,
					"name":     fmt.Sprintf("Test PLA %d", spool.ID),
					"material": "PLA",
					"vendor": map[string]interface{}{
						"id":   1,
						"name": "TestBrand",
					},
				},
			})

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

			// Record the update
			if usedWeight, ok := body["used_weight"].(float64); ok {
				spool.UsedWeight = usedWeight
				mock.updates = append(mock.updates, UsageUpdate{
					SpoolID:    spoolID,
					UsedWeight: usedWeight,
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
}

// SetOffline makes the mock return HTTP 503 for all requests when true,
// simulating Spoolman being unreachable.
func (m *MockSpoolman) SetOffline(offline bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.offline = offline
}
