// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// SpoolmanClient handles communication with Spoolman API for bridge functionality
type SpoolmanClient struct {
	baseURL    string
	httpClient *http.Client
}

// GetBaseURL returns the Spoolman base URL
func (c *SpoolmanClient) GetBaseURL() string {
	return c.baseURL
}

// SpoolmanSpool represents a spool from Spoolman API
type SpoolmanSpool struct {
	ID              int                    `json:"id"`
	Registered      string                 `json:"registered"`
	Filament        *SpoolmanFilament      `json:"filament"`
	RemainingWeight float64                `json:"remaining_weight"`
	InitialWeight   float64                `json:"initial_weight"`
	SpoolWeight     float64                `json:"spool_weight"`
	UsedWeight      float64                `json:"used_weight"`
	RemainingLength float64                `json:"remaining_length"`
	UsedLength      float64                `json:"used_length"`
	FirstUsed       string                 `json:"first_used"`
	LastUsed        string                 `json:"last_used"`
	Archived        bool                   `json:"archived"`
	LocationID      *int                   `json:"location_id"` // Reference to Spoolman Location entity
	Price           *float64               `json:"price"`        // Price per kg (set in Spoolman)
	Extra           map[string]interface{} `json:"extra"`

	// Computed fields for easier access
	Name     string `json:"name"`     // Computed from filament.name
	Brand    string `json:"brand"`    // Computed from filament.vendor.name
	Material string `json:"material"` // Computed from filament.material
	Location string `json:"location"` // Spool location (e.g., "Printer1 - Toolhead 0") - kept for backward compatibility
}

// SpoolmanFilament represents a filament type from Spoolman
type SpoolmanFilament struct {
	ID                   int                    `json:"id"`
	Registered           string                 `json:"registered"`
	Name                 string                 `json:"name"`
	Vendor               *SpoolmanVendor        `json:"vendor"`
	Material             string                 `json:"material"`
	Price                *float64               `json:"price"` // price per kg for this filament type
	Density              float64                `json:"density"`
	Diameter             float64                `json:"diameter"`
	Weight               float64                `json:"weight"`
	SpoolWeight          float64                `json:"spool_weight"`
	SettingsExtruderTemp int                    `json:"settings_extruder_temp"`
	SettingsBedTemp      int                    `json:"settings_bed_temp"`
	ColorHex             string                 `json:"color_hex"`
	MultiColorHexes      string                 `json:"multi_color_hexes"`
	ExternalID           string                 `json:"external_id"`
	Extra                map[string]interface{} `json:"extra"`
	Archived             bool                   `json:"archived"`
}

// PricePerKg returns the effective per-kg price for a spool: spool-level price
// takes precedence over filament-level price (both are stored as price/kg in Spoolman).
func (s *SpoolmanSpool) PricePerKg() float64 {
	if s.Price != nil {
		return *s.Price
	}
	if s.Filament != nil && s.Filament.Price != nil {
		return *s.Filament.Price
	}
	return 0
}

// SpoolmanVendor represents a vendor from Spoolman
type SpoolmanVendor struct {
	ID         int                    `json:"id"`
	Registered string                 `json:"registered"`
	Name       string                 `json:"name"`
	ExternalID string                 `json:"external_id"`
	Extra      map[string]interface{} `json:"extra"`
	Archived   bool                   `json:"archived"`
}

// SpoolmanError represents an error response from Spoolman API
type SpoolmanError struct {
	Detail string `json:"detail"`
	Title  string `json:"title"`
	Type   string `json:"type"`
}

// NewSpoolmanClient creates a new Spoolman client
func NewSpoolmanClient(baseURL string, timeout int) *SpoolmanClient {
	return &SpoolmanClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}
}

// handleAPIError handles API error responses from Spoolman
func (c *SpoolmanClient) handleAPIError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading error response body: %w", err)
	}

	// Try to parse as Spoolman error format
	var spoolmanErr SpoolmanError
	if err := json.Unmarshal(body, &spoolmanErr); err == nil && spoolmanErr.Detail != "" {
		return fmt.Errorf("spoolman API error (HTTP %d): %s - %s", resp.StatusCode, spoolmanErr.Title, spoolmanErr.Detail)
	}

	// Fallback to generic error
	return fmt.Errorf("spoolman API error (HTTP %d): %s", resp.StatusCode, string(body))
}

// normalizeSpoolData normalizes spool data to extract information from nested structures
func (c *SpoolmanClient) normalizeSpoolData(spool SpoolmanSpool) SpoolmanSpool {
	// Extract data from nested filament and vendor structures
	if spool.Filament != nil {
		spool.Name = spool.Filament.Name
		spool.Material = spool.Filament.Material

		if spool.Filament.Vendor != nil {
			spool.Brand = spool.Filament.Vendor.Name
		}
	}

	// If name is still empty, create a default name
	if spool.Name == "" {
		spool.Name = fmt.Sprintf("Spool %d", spool.ID)
	}

	return spool
}

// getSpoolDisplayName returns the display name for sorting purposes
func (spool *SpoolmanSpool) getSpoolDisplayName() string {
	material := "Unknown Material"
	brand := "Unknown Brand"
	name := "Unnamed Spool"

	if spool.Filament != nil {
		if spool.Filament.Material != "" {
			material = spool.Filament.Material
		}
		if spool.Filament.Vendor != nil && spool.Filament.Vendor.Name != "" {
			brand = spool.Filament.Vendor.Name
		}
		if spool.Filament.Name != "" {
			name = spool.Filament.Name
		}
	}

	return fmt.Sprintf("%s - %s - %s", material, brand, name)
}

// GetAllSpools gets all filament spools from Spoolman
func (c *SpoolmanClient) GetAllSpools() ([]SpoolmanSpool, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/spool", nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error getting spools from Spoolman: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleAPIError(resp)
	}

	var spools []SpoolmanSpool
	if err := json.NewDecoder(resp.Body).Decode(&spools); err != nil {
		return nil, fmt.Errorf("error decoding spools from Spoolman: %w", err)
	}

	// Normalize spool data to extract information from nested structures
	for i := range spools {
		spools[i] = c.normalizeSpoolData(spools[i])
	}

	// Sort spools: first alphabetically by display name, then by remaining weight (descending)
	sort.Slice(spools, func(i, j int) bool {
		// First sort by display name (Material - Brand - Name)
		nameI := spools[i].getSpoolDisplayName()
		nameJ := spools[j].getSpoolDisplayName()

		if nameI != nameJ {
			return nameI < nameJ
		}

		// If display names are the same, sort by remaining weight (ascending - use less filament first)
		return spools[i].RemainingWeight < spools[j].RemainingWeight
	})

	return spools, nil
}

// GetAllFilaments gets all filament types from Spoolman
func (c *SpoolmanClient) GetAllFilaments() ([]SpoolmanFilament, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/filament", nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error getting filaments from Spoolman: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleAPIError(resp)
	}

	var filaments []SpoolmanFilament
	if err := json.NewDecoder(resp.Body).Decode(&filaments); err != nil {
		return nil, fmt.Errorf("error decoding filaments from Spoolman: %w", err)
	}

	// Filter out archived filaments
	filteredFilaments := make([]SpoolmanFilament, 0, len(filaments))
	for _, filament := range filaments {
		if !filament.Archived {
			filteredFilaments = append(filteredFilaments, filament)
		}
	}
	filaments = filteredFilaments

	// Sort filaments by ID
	sort.Slice(filaments, func(i, j int) bool {
		return filaments[i].ID < filaments[j].ID
	})

	return filaments, nil
}

// GetAllVendors returns all non-archived vendors from Spoolman, sorted by ID.
func (c *SpoolmanClient) GetAllVendors() ([]SpoolmanVendor, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/vendor", nil)
	if err != nil {
		return nil, fmt.Errorf("error creating vendor request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error getting vendors from Spoolman: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleAPIError(resp)
	}
	var vendors []SpoolmanVendor
	if err := json.NewDecoder(resp.Body).Decode(&vendors); err != nil {
		return nil, fmt.Errorf("error decoding vendors from Spoolman: %w", err)
	}
	filtered := make([]SpoolmanVendor, 0, len(vendors))
	for _, v := range vendors {
		if !v.Archived {
			filtered = append(filtered, v)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	return filtered, nil
}

// UpdateSpool updates spool information (used for filament usage tracking)
func (c *SpoolmanClient) UpdateSpool(spoolID int, data map[string]interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("error marshaling spool update data: %w", err)
	}

	req, err := http.NewRequest("PATCH", fmt.Sprintf("%s/api/v1/spool/%d", c.baseURL, spoolID), bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("error creating PUT request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error updating spool %d in Spoolman: %w", spoolID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.handleAPIError(resp)
	}

	return nil
}

// UpdateSpoolUsage updates spool used weight based on usage (core bridge functionality)
func (c *SpoolmanClient) UpdateSpoolUsage(spoolID int, filamentUsed float64) error {
	// Get current spool data
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/spool/%d", c.baseURL, spoolID), nil)
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error getting spool %d from Spoolman: %w", spoolID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("spool %d not found in Spoolman: %w", spoolID, c.handleAPIError(resp))
	}

	var spool SpoolmanSpool
	if err := json.NewDecoder(resp.Body).Decode(&spool); err != nil {
		return fmt.Errorf("error decoding spool %d from Spoolman: %w", spoolID, err)
	}

	// Calculate new used weight
	newUsedWeight := spool.UsedWeight + filamentUsed
	currentTime := time.Now().UTC().Format(time.RFC3339)

	// Update used_weight and timestamps
	updateData := map[string]interface{}{
		"used_weight": newUsedWeight,
		"last_used":   currentTime,
	}

	// Set first_used if it's not already set
	if spool.FirstUsed == "" {
		updateData["first_used"] = currentTime
	}

	if err := c.UpdateSpool(spoolID, updateData); err != nil {
		return fmt.Errorf("failed to update spool %d: %w", spoolID, err)
	}

	fmt.Printf("Updated spool %d: used_weight %.2fg -> %.2fg (added %.2fg)\n",
		spoolID, spool.UsedWeight, newUsedWeight, filamentUsed)

	return nil
}

// TestConnection tests the connection to Spoolman
func (c *SpoolmanClient) TestConnection() error {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/info", nil)
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error testing connection to Spoolman: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.handleAPIError(resp)
	}

	return nil
}

// SpoolmanLocation represents a location from Spoolman
type SpoolmanLocation struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Comment  string `json:"comment"`
	Archived bool   `json:"archived"`
}

// GetLocations gets all locations from Spoolman
func (c *SpoolmanClient) GetLocations() ([]SpoolmanLocation, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/location", nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error getting locations from Spoolman: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleAPIError(resp)
	}

	// Read full body so we can retry alternative shapes and log on error
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading locations response from Spoolman: %w", err)
	}

	// 1) Try standard array of objects
	var locations []SpoolmanLocation
	if err := json.Unmarshal(bodyBytes, &locations); err == nil {
		return locations, nil
	}

	// 2) Try { data: [...] } wrapper
	var dataWrapper struct {
		Data    []SpoolmanLocation `json:"data"`
		Results []SpoolmanLocation `json:"results"`
	}
	if err := json.Unmarshal(bodyBytes, &dataWrapper); err == nil {
		if len(dataWrapper.Data) > 0 {
			return dataWrapper.Data, nil
		}
		if len(dataWrapper.Results) > 0 {
			return dataWrapper.Results, nil
		}
	}

	// 3) Try simple array of names like ["Testing", ...]
	var names []string
	if err := json.Unmarshal(bodyBytes, &names); err == nil {
		for _, n := range names {
			locations = append(locations, SpoolmanLocation{Name: n})
		}
		return locations, nil
	}

	// Log snippet for diagnostics and return error
	snippet := string(bodyBytes)
	if len(snippet) > 300 {
		snippet = snippet[:300] + "..."
	}
	log.Printf("Spoolman /location unexpected JSON. Snippet: %s", snippet)
	return nil, fmt.Errorf("error decoding locations from Spoolman: unexpected JSON shape")
}

// GetOrCreateLocation gets an existing location by name
// Note: Spoolman API does not support creating locations via POST.
// Locations must be created manually in Spoolman UI or are auto-created when referenced in spools.
func (c *SpoolmanClient) GetOrCreateLocation(name string) (*SpoolmanLocation, error) {
	// Get existing locations
	locations, err := c.GetLocations()
	if err != nil {
		return nil, fmt.Errorf("failed to get locations: %w", err)
	}

	// Look for existing location with this name
	for _, location := range locations {
		if location.Name == name {
			return &location, nil
		}
	}

	// Location doesn't exist in Spoolman
	// Spoolman API doesn't support POST to create locations - they must be created
	// manually in the UI or will be auto-created when referenced in a spool
	// Return a dummy location so the system can continue
	return &SpoolmanLocation{
		ID:   0, // Dummy ID - location doesn't exist yet
		Name: name,
	}, nil
}

// CreateLocation is deprecated - Spoolman API does not support creating locations via POST.
// Locations must be created manually in Spoolman UI or are auto-created when referenced in spools.
// This function is kept for backward compatibility but will always return an error.
func (c *SpoolmanClient) CreateLocation(name string) (*SpoolmanLocation, error) {
	return nil, fmt.Errorf("spoolman API does not support creating locations via POST. Locations must be created manually in Spoolman UI or will be auto-created when referenced in a spool")
}

// FindLocationByName searches for an existing location by name
func (c *SpoolmanClient) FindLocationByName(name string) (*SpoolmanLocation, error) {
	locations, err := c.GetLocations()
	if err != nil {
		return nil, fmt.Errorf("error getting locations: %w", err)
	}

	for _, location := range locations {
		if location.Name == name {
			return &location, nil
		}
	}

	return nil, nil // Location not found
}

// LocationExistsInSpoolman checks if a location exists in Spoolman
func (c *SpoolmanClient) LocationExistsInSpoolman(name string) (bool, error) {
	location, err := c.FindLocationByName(name)
	if err != nil {
		return false, err
	}
	return location != nil, nil
}

// RenameLocation renames a location in Spoolman using the PATCH API
func (c *SpoolmanClient) RenameLocation(oldName, newName string) error {
	updateData := map[string]interface{}{
		"name": newName,
	}

	jsonData, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal location rename data: %w", err)
	}

	req, err := http.NewRequest("PATCH", fmt.Sprintf("%s/api/v1/location/%s", c.baseURL, oldName), bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("error creating PATCH request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error renaming location in Spoolman: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.handleAPIError(resp)
	}

	log.Printf("Successfully renamed Spoolman location from '%s' to '%s'", oldName, newName)
	return nil
}

// UpdateSpoolLocation updates a spool's location in Spoolman using text-based location field
func (c *SpoolmanClient) UpdateSpoolLocation(spoolID int, locationName string) error {
	// Use text-based location assignment - Spoolman will create the location if it doesn't exist
	return c.updateSpoolLocationText(spoolID, locationName)
}

// UpdateLocation updates a location name in Spoolman
func (c *SpoolmanClient) UpdateLocation(locationID int, newName string) error {
	updateData := map[string]interface{}{
		"name": newName,
	}

	jsonData, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal location update data: %w", err)
	}

	req, err := http.NewRequest("PATCH", fmt.Sprintf("%s/api/v1/location/%d", c.baseURL, locationID), bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("error creating PATCH request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error updating location %d in Spoolman: %w", locationID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.handleAPIError(resp)
	}

	log.Printf("Successfully updated Spoolman location %d to '%s'", locationID, newName)
	return nil
}

// ArchiveLocation archives a location in Spoolman
func (c *SpoolmanClient) ArchiveLocation(locationID int) error {
	updateData := map[string]interface{}{
		"archived": true,
	}

	jsonData, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal location archive data: %w", err)
	}

	req, err := http.NewRequest("PATCH", fmt.Sprintf("%s/api/v1/location/%d", c.baseURL, locationID), bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("error creating PATCH request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error archiving location %d in Spoolman: %w", locationID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.handleAPIError(resp)
	}

	log.Printf("Successfully archived Spoolman location %d", locationID)
	return nil
}

// UpdateLocationByName updates a location in Spoolman by name
func (c *SpoolmanClient) UpdateLocationByName(oldName, newName string) error {
	// First, find the location by name
	locations, err := c.GetLocations()
	if err != nil {
		return fmt.Errorf("failed to get locations: %w", err)
	}

	var locationID int
	found := false
	for _, loc := range locations {
		if loc.Name == oldName && !loc.Archived {
			locationID = loc.ID
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("location '%s' not found in Spoolman", oldName)
	}

	// Update the location using its ID
	return c.UpdateLocation(locationID, newName)
}

// updateSpoolLocationText updates a spool's location using the text field
func (c *SpoolmanClient) updateSpoolLocationText(spoolID int, locationName string) error {
	updateData := map[string]interface{}{
		"location": locationName,
	}

	jsonData, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("error marshaling location update data: %w", err)
	}

	req, err := http.NewRequest("PATCH", fmt.Sprintf("%s/api/v1/spool/%d", c.baseURL, spoolID), bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("error creating PATCH request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error updating spool %d location in Spoolman: %w", spoolID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.handleAPIError(resp)
	}

	log.Printf("Successfully updated spool %d to location '%s' (text-based)", spoolID, locationName)
	return nil
}

// UpdateSpoolmanLocationReferences renames the location in Spoolman using the location rename API
func (c *SpoolmanClient) UpdateSpoolmanLocationReferences(oldName, newName string) error {
	log.Printf("UpdateSpoolmanLocationReferences: Renaming location from '%s' to '%s' in Spoolman", oldName, newName)

	// Check if the old location exists in Spoolman
	exists, err := c.LocationExistsInSpoolman(oldName)
	if err != nil {
		log.Printf("UpdateSpoolmanLocationReferences: Failed to check if location exists: %v", err)
		return fmt.Errorf("failed to check if location exists in Spoolman: %w", err)
	}

	if !exists {
		log.Printf("UpdateSpoolmanLocationReferences: Location '%s' does not exist in Spoolman, skipping rename", oldName)
		return nil
	}

	// Use the location rename API to rename the location directly
	if err := c.RenameLocation(oldName, newName); err != nil {
		log.Printf("UpdateSpoolmanLocationReferences: Failed to rename location in Spoolman: %v", err)
		return fmt.Errorf("failed to rename location in Spoolman: %w", err)
	}

	log.Printf("UpdateSpoolmanLocationReferences: Successfully renamed location from '%s' to '%s' in Spoolman", oldName, newName)
	return nil
}

// ─── NFC tag helpers ──────────────────────────────────────────────────────────

// nfcIDKey is the extra-field key used to store an NFC tag UUID on a spool.
const nfcIDKey = "nfc_id"

// GetSpoolByID fetches a single spool from Spoolman by ID.
func (c *SpoolmanClient) GetSpoolByID(spoolID int) (*SpoolmanSpool, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/spool/%d", c.baseURL, spoolID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getting spool %d: %w", spoolID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleAPIError(resp)
	}
	var s SpoolmanSpool
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("decoding spool %d: %w", spoolID, err)
	}
	s = c.normalizeSpoolData(s)
	return &s, nil
}

// SetSpoolNFCTag writes a UUID into the spool's extra["nfc_id"] field.
// This is idempotent — calling again overwrites the previous UUID.
func (c *SpoolmanClient) SetSpoolNFCTag(spoolID int, nfcUUID string) error {
	// Fetch current extra map so we preserve any other extra fields.
	spool, err := c.GetSpoolByID(spoolID)
	if err != nil {
		return fmt.Errorf("fetching spool before NFC tag assignment: %w", err)
	}
	extra := spool.Extra
	if extra == nil {
		extra = make(map[string]interface{})
	}
	extra[nfcIDKey] = nfcUUID
	return c.UpdateSpool(spoolID, map[string]interface{}{"extra": extra})
}

// ClearSpoolNFCTag removes the nfc_id extra field from a spool.
func (c *SpoolmanClient) ClearSpoolNFCTag(spoolID int) error {
	spool, err := c.GetSpoolByID(spoolID)
	if err != nil {
		return fmt.Errorf("fetching spool before NFC tag removal: %w", err)
	}
	extra := spool.Extra
	if extra == nil {
		return nil // nothing to remove
	}
	delete(extra, nfcIDKey)
	return c.UpdateSpool(spoolID, map[string]interface{}{"extra": extra})
}

// GetSpoolByNFCTag searches all spools for one whose extra["nfc_id"] matches uuid.
// Returns nil, nil when no match exists.
func (c *SpoolmanClient) GetSpoolByNFCTag(nfcUUID string) (*SpoolmanSpool, error) {
	// Use the all-spools endpoint; Spoolman has no query-by-extra-field API.
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/spool?allow_archived=true", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching spools for NFC lookup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleAPIError(resp)
	}
	var spools []SpoolmanSpool
	if err := json.NewDecoder(resp.Body).Decode(&spools); err != nil {
		return nil, fmt.Errorf("decoding spools for NFC lookup: %w", err)
	}
	for i := range spools {
		extra := spools[i].Extra
		if extra == nil {
			continue
		}
		if v, ok := extra[nfcIDKey]; ok {
			if id, ok := v.(string); ok && id == nfcUUID {
				s := c.normalizeSpoolData(spools[i])
				return &s, nil
			}
		}
	}
	return nil, nil // not found
}

// GetSpoolExtraField reads a custom field value from a spool's Extra map.
// Returns empty string if the field is absent or not a string.
func GetSpoolExtraField(spool SpoolmanSpool, fieldKey string) string {
	if spool.Extra == nil {
		return ""
	}
	v, ok := spool.Extra[fieldKey]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// SetSpoolExtraField writes a custom field value to a spool via Spoolman PATCH.
// Per Spoolman's API, the value must be JSON-encoded inside the JSON body.
func (c *SpoolmanClient) SetSpoolExtraField(spoolID int, fieldKey string, value string) error {
	extraValue, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshaling extra field value: %w", err)
	}
	body := map[string]interface{}{
		"extra": map[string]string{
			fieldKey: string(extraValue),
		},
	}
	return c.UpdateSpool(spoolID, body)
}

// UpdateFilament sends a PATCH request to update a filament record in Spoolman.
func (c *SpoolmanClient) UpdateFilament(filamentID int, data map[string]interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling filament update: %w", err)
	}
	req, err := http.NewRequest("PATCH", fmt.Sprintf("%s/api/v1/filament/%d", c.baseURL, filamentID), bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("creating filament PATCH request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("updating filament %d: %w", filamentID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c.handleAPIError(resp)
	}
	return nil
}

// SetFilamentExtraField writes a custom field value to a filament via Spoolman PATCH.
// Deprecated: use MergeFilamentExtraField which preserves other extra keys.
func (c *SpoolmanClient) SetFilamentExtraField(filamentID int, fieldKey string, value string) error {
	extraValue, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshaling extra field value: %w", err)
	}
	return c.UpdateFilament(filamentID, map[string]interface{}{
		"extra": map[string]string{
			fieldKey: string(extraValue),
		},
	})
}

// MergeFilamentExtraField updates a single key in a filament's extra map without
// clobbering other keys. Spoolman replaces the entire extra map on each PATCH, so
// this function GETs the current extra, merges in the new key/value, then PATCHes
// the complete merged map.
func (c *SpoolmanClient) MergeFilamentExtraField(filamentID int, fieldKey string, encodedValue string) error {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/api/v1/filament/%d", c.baseURL, filamentID))
	if err != nil {
		return fmt.Errorf("fetching filament %d for extra merge: %w", filamentID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c.handleAPIError(resp)
	}
	var f SpoolmanFilament
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return fmt.Errorf("decoding filament %d: %w", filamentID, err)
	}

	merged := make(map[string]string)
	for k, v := range f.Extra {
		switch s := v.(type) {
		case string:
			merged[k] = s
		default:
			if b, err := json.Marshal(v); err == nil {
				merged[k] = string(b)
			}
		}
	}
	merged[fieldKey] = encodedValue

	return c.UpdateFilament(filamentID, map[string]interface{}{"extra": merged})
}

// SpoolmanFieldCreate is the request body for creating a Spoolman custom field.
type SpoolmanFieldCreate struct {
	Name         string `json:"name"`
	FieldType    string `json:"field_type"`
	DefaultValue string `json:"default_value"`
}

// SpoolmanFieldStatus tracks the result of an EnsureSpoolmanFields check.
type SpoolmanFieldStatus struct {
	Key    string
	Entity string // "spool" or "filament"
}

// requiredSpoolmanFields lists all custom fields the app needs in Spoolman.
var requiredSpoolmanFields = []struct {
	Key          string
	Name         string
	FieldType    string
	DefaultValue string
	Entity       string
}{
	{"nfc_material_class", "NFC Material Class", "text", `""`, "filament"},
	{"nfc_min_print_temp", "NFC Min Print Temp", "integer", "0", "filament"},
	{"nfc_max_print_temp", "NFC Max Print Temp", "integer", "0", "filament"},
	{"nfc_min_bed_temp", "NFC Min Bed Temp", "integer", "0", "filament"},
	{"nfc_max_bed_temp", "NFC Max Bed Temp", "integer", "0", "filament"},
	{"nfc_country_of_origin", "NFC Country of Origin", "text", `""`, "filament"},
	{"nfc_material_properties", "NFC Material Properties", "text", `""`, "filament"},
	{"nfc_transmission_distance", "NFC Transmission Distance", "float", "0.0", "filament"},
	{"nfc_nominal_length", "NFC Nominal Length", "integer", "0", "filament"},
	{"nfc_spool_uuid", "NFC Spool UUID", "text", `""`, "spool"},
	{"nfc_actual_weight", "NFC Actual Weight", "float", "0.0", "spool"},
	{"nfc_manufacturing_date", "NFC Manufacturing Date", "text", `""`, "spool"},
	{"nfc_expiration_date", "NFC Expiration Date", "text", `""`, "spool"},
	// Slicer calibration fields — stored on filament entity
	{"cal_max_flow_rate", "Cal Max Flow Rate", "float", "0.0", "filament"},
	{"cal_pressure_advance", "Cal Pressure Advance", "float", "0.0", "filament"},
	{"cal_flow_ratio", "Cal Flow Ratio", "float", "0.0", "filament"},
	{"cal_retraction_length", "Cal Retraction Length", "float", "0.0", "filament"},
	{"cal_retraction_speed", "Cal Retraction Speed", "float", "0.0", "filament"},
}

// EnsureSpoolmanFields checks and creates all required NFC custom fields in Spoolman.
// Returns created fields, already-existing fields, and failed fields.
func (c *SpoolmanClient) EnsureSpoolmanFields() (created, existed, failed []SpoolmanFieldStatus) {
	for _, f := range requiredSpoolmanFields {
		status := SpoolmanFieldStatus{Key: f.Key, Entity: f.Entity}

		fieldBody := SpoolmanFieldCreate{
			Name:         f.Name,
			FieldType:    f.FieldType,
			DefaultValue: f.DefaultValue,
		}
		bodyBytes, err := json.Marshal(fieldBody)
		if err != nil {
			log.Printf("NFC field create: marshal error for %s/%s: %v", f.Entity, f.Key, err)
			failed = append(failed, status)
			continue
		}

		createURL := fmt.Sprintf("%s/api/v1/field/%s/%s", c.baseURL, f.Entity, f.Key)
		createReq, err := http.NewRequest("POST", createURL, bytes.NewBuffer(bodyBytes))
		if err != nil {
			log.Printf("NFC field create: error building request for %s/%s: %v", f.Entity, f.Key, err)
			failed = append(failed, status)
			continue
		}
		createReq.Header.Set("Content-Type", "application/json")

		createResp, err := c.httpClient.Do(createReq)
		if err != nil {
			log.Printf("NFC field create: error creating %s/%s: %v", f.Entity, f.Key, err)
			failed = append(failed, status)
			continue
		}
		createResp.Body.Close()

		switch createResp.StatusCode {
		case http.StatusCreated:
			created = append(created, status)
		case http.StatusOK, http.StatusConflict, http.StatusUnprocessableEntity:
			// 200 = upsert (field already exists, Spoolman accepted it)
			// 409 = already exists, 422 = duplicate key on some Spoolman versions
			existed = append(existed, status)
		default:
			log.Printf("NFC field create: unexpected status %d for %s/%s", createResp.StatusCode, f.Entity, f.Key)
			failed = append(failed, status)
		}
	}
	return created, existed, failed
}

// GetSpoolmanSetupStatus checks whether all required NFC custom fields exist in Spoolman.
func (c *SpoolmanClient) GetSpoolmanSetupStatus() (ok bool, missing []SpoolmanFieldStatus) {
	// Fetch existing fields per entity type using the list endpoint GET /api/v1/field/{entity}.
	type spoolmanFieldDef struct {
		Key string `json:"key"`
	}
	existingByEntity := map[string]map[string]bool{}
	for _, entityType := range []string{"filament", "spool"} {
		listURL := fmt.Sprintf("%s/api/v1/field/%s", c.baseURL, entityType)
		resp, err := c.httpClient.Get(listURL)
		if err != nil {
			// Can't reach Spoolman — mark all fields for this entity as missing.
			continue
		}
		var fields []spoolmanFieldDef
		json.NewDecoder(resp.Body).Decode(&fields)
		resp.Body.Close()
		existing := map[string]bool{}
		for _, fld := range fields {
			existing[fld.Key] = true
		}
		existingByEntity[entityType] = existing
	}

	for _, f := range requiredSpoolmanFields {
		if !existingByEntity[f.Entity][f.Key] {
			missing = append(missing, SpoolmanFieldStatus{Key: f.Key, Entity: f.Entity})
		}
	}
	return len(missing) == 0, missing
}

// CloneFilament creates a copy of an existing filament in Spoolman.
// The clone inherits all fields except id, registered, and extra.
func (c *SpoolmanClient) CloneFilament(filamentID int) (*SpoolmanFilament, error) {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/api/v1/filament/%d", c.baseURL, filamentID))
	if err != nil {
		return nil, fmt.Errorf("fetching filament %d for clone: %w", filamentID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleAPIError(resp)
	}
	var src SpoolmanFilament
	if err := json.NewDecoder(resp.Body).Decode(&src); err != nil {
		return nil, fmt.Errorf("decoding filament %d: %w", filamentID, err)
	}

	newData := map[string]interface{}{
		"name":                   src.Name + " (copy)",
		"material":               src.Material,
		"density":                src.Density,
		"diameter":               src.Diameter,
		"weight":                 src.Weight,
		"spool_weight":           src.SpoolWeight,
		"settings_extruder_temp": src.SettingsExtruderTemp,
		"settings_bed_temp":      src.SettingsBedTemp,
		"color_hex":              src.ColorHex,
	}
	if src.Vendor != nil {
		newData["vendor_id"] = src.Vendor.ID
	}
	if src.Price != nil {
		newData["price"] = *src.Price
	}

	body, err := json.Marshal(newData)
	if err != nil {
		return nil, fmt.Errorf("marshaling cloned filament: %w", err)
	}
	req, err := http.NewRequest("POST", c.baseURL+"/api/v1/filament", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("creating clone request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	postResp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("posting cloned filament: %w", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusCreated && postResp.StatusCode != http.StatusOK {
		return nil, c.handleAPIError(postResp)
	}
	var cloned SpoolmanFilament
	if err := json.NewDecoder(postResp.Body).Decode(&cloned); err != nil {
		return nil, fmt.Errorf("decoding cloned filament: %w", err)
	}
	return &cloned, nil
}

// CreateFilament creates a new filament record in Spoolman and returns it.
// data holds the Spoolman filament fields (material, color_hex, diameter, density,
// weight, price, name, vendor_id, …). Mirrors the POST half of CloneFilament.
func (c *SpoolmanClient) CreateFilament(data map[string]interface{}) (*SpoolmanFilament, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshaling new filament: %w", err)
	}
	req, err := http.NewRequest("POST", c.baseURL+"/api/v1/filament", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("creating filament request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("posting new filament: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, c.handleAPIError(resp)
	}
	var created SpoolmanFilament
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, fmt.Errorf("decoding new filament: %w", err)
	}
	return &created, nil
}

// CreateSpool creates a new spool in Spoolman and returns it. data holds the Spoolman
// spool fields — at minimum {"filament_id": N}; Spoolman derives the full weight from the
// filament. Mirrors CreateFilament.
func (c *SpoolmanClient) CreateSpool(data map[string]interface{}) (*SpoolmanSpool, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshaling new spool: %w", err)
	}
	req, err := http.NewRequest("POST", c.baseURL+"/api/v1/spool", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("creating spool request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("posting new spool: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, c.handleAPIError(resp)
	}
	var created SpoolmanSpool
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, fmt.Errorf("decoding new spool: %w", err)
	}
	s := c.normalizeSpoolData(created)
	return &s, nil
}

// FindVendorByName returns the non-archived vendor matching name (case-insensitive),
// or (nil, nil) when none matches. Used to map a filament tag's manufacturer field to an
// existing Spoolman vendor without creating one.
func (c *SpoolmanClient) FindVendorByName(name string) (*SpoolmanVendor, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	vendors, err := c.GetAllVendors()
	if err != nil {
		return nil, err
	}
	for i := range vendors {
		if strings.EqualFold(strings.TrimSpace(vendors[i].Name), strings.TrimSpace(name)) {
			return &vendors[i], nil
		}
	}
	return nil, nil
}

// GetFilamentExtraFloat reads a calibration float from a filament's Extra map.
// Returns 0 if the field is absent or cannot be parsed.
func GetFilamentExtraFloat(f SpoolmanFilament, key string) float64 {
	if f.Extra == nil {
		return 0
	}
	v, ok := f.Extra[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f64, _ := n.Float64()
		return f64
	}
	return 0
}

// SubtractSpoolUsage reduces a spool's used_weight by the given amount.
// The used_weight will not go below zero.
func (c *SpoolmanClient) SubtractSpoolUsage(spoolID int, grams float64) error {
	spool, err := c.GetSpoolByID(spoolID)
	if err != nil {
		return fmt.Errorf("fetching spool %d before subtraction: %w", spoolID, err)
	}
	newUsedWeight := spool.UsedWeight - grams
	if newUsedWeight < 0 {
		newUsedWeight = 0
	}
	return c.UpdateSpool(spoolID, map[string]interface{}{"used_weight": newUsedWeight})
}
