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
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PrusaLinkClient handles communication with PrusaLink API
type PrusaLinkClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// PrusaLinkStatus represents the status response from /api/v1/status.
// Field layout verified against real Core One L response.
type PrusaLinkStatus struct {
	Job struct {
		ID            int     `json:"id"`
		Progress      float64 `json:"progress"`
		TimeRemaining int     `json:"time_remaining"`
		TimePrinting  int     `json:"time_printing"`
	} `json:"job"`
	Printer struct {
		State        string  `json:"state"`
		TempNozzle   float64 `json:"temp_nozzle"`
		TargetNozzle float64 `json:"target_nozzle"`
		TempBed      float64 `json:"temp_bed"`
		TargetBed    float64 `json:"target_bed"`
		AxisX        float64 `json:"axis_x"`
		AxisY        float64 `json:"axis_y"`
		AxisZ        float64 `json:"axis_z"`
		Flow         int     `json:"flow"`
		Speed        int     `json:"speed"`
		FanHotend    int     `json:"fan_hotend"`
		FanPrint     int     `json:"fan_print"`
	} `json:"printer"`
	// Transfer is conditionally present during file uploads to the printer.
	Transfer struct {
		ID               int     `json:"id"`
		Progress         float64 `json:"progress"`
		TimeTransferring int     `json:"time_transferring"`
		Transferred      int64   `json:"transferred"`
	} `json:"transfer"`
}

// PrusaLinkJob represents the job response from /api/v1/job.
// Field layout verified against real Core One L response.
type PrusaLinkJob struct {
	ID            int     `json:"id"`
	State         string  `json:"state"`
	Progress      float64 `json:"progress"`
	TimeRemaining int     `json:"time_remaining"`
	TimePrinting  int     `json:"time_printing"`
	File          struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Path        string `json:"path"`
		Size        int    `json:"size"`
		MTimestamp  int64  `json:"m_timestamp"`
		Refs        struct {
			Download  string `json:"download"`
			Icon      string `json:"icon"`
			Thumbnail string `json:"thumbnail"`
		} `json:"refs"`
	} `json:"file"`
	// Filament usage data (available on some firmware versions)
	Filament []struct {
		ToolheadID int     `json:"toolhead_id"`
		Length     float64 `json:"length"`
		Weight     float64 `json:"weight"`
	} `json:"filament,omitempty"`
}

// PrusaLinkFileInfo represents the metadata response from /api/v1/files/{storage}/{path}.
// Filament is populated by PrusaSlicer-sliced files; may be absent on older firmware or
// files sliced without per-tool weight comments.
type PrusaLinkFileInfo struct {
	Name     string `json:"name"`
	Filament []struct {
		ToolheadID int     `json:"toolhead_id"`
		Weight     float64 `json:"weight"` // grams, slicer estimate
		Length     float64 `json:"length"` // mm
	} `json:"filament,omitempty"`
}

// GetFileInfo fetches lightweight file metadata from PrusaLink without downloading the file.
// Returns nil (no error) when the endpoint is unavailable or the file has no filament data,
// so callers can fall back to a full G-code download.
func (c *PrusaLinkClient) GetFileInfo(filePath string) (*PrusaLinkFileInfo, error) {
	// Strip a leading slash if present; PrusaLink expects the path without it.
	trimmed := strings.TrimPrefix(filePath, "/")
	url := c.baseURL + "/api/v1/files/local/" + trimmed

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create file info request: %w", err)
	}
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get file info from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	// 404 means file not found or endpoint not supported on this firmware — treat as unavailable.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink file info API error: %d - %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read file info response: %w", err)
	}

	var info PrusaLinkFileInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("failed to decode file info response: %w", err)
	}

	// Return nil when the response carries no filament data — caller falls back to G-code download.
	if len(info.Filament) == 0 {
		return nil, nil
	}

	log.Printf("📋 [PrusaLink] File metadata for %s: %d toolhead(s) with filament data", filePath, len(info.Filament))
	return &info, nil
}

// PrusaLinkInfo represents the printer info response from /api/v1/info
type PrusaLinkInfo struct {
	Hostname         string  `json:"hostname"`
	Serial           string  `json:"serial"`
	NozzleDiameter   float64 `json:"nozzle_diameter"`
	MMU              bool    `json:"mmu"`
	MinExtrusionTemp int     `json:"min_extrusion_temp"`
}

// NewPrusaLinkClient creates a new PrusaLink client
func NewPrusaLinkClient(ipAddress, apiKey string, timeout, fileDownloadTimeout int) *PrusaLinkClient {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &PrusaLinkClient{
		baseURL: fmt.Sprintf("http://%s", ipAddress),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout:   time.Duration(timeout) * time.Second,
			Transport: transport,
		},
	}
}

// addAPIKey adds X-Api-Key authentication to the request
func (c *PrusaLinkClient) addAPIKey(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)
	}
}

// GetStatus retrieves the current status of the printer.
// Returns the decoded struct, the raw response body, and any error.
func (c *PrusaLinkClient) GetStatus() (*PrusaLinkStatus, []byte, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/status", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create status request: %w", err)
	}
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get status from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read status response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, body, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	var status PrusaLinkStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, body, fmt.Errorf("failed to decode status response: %w", err)
	}

	return &status, body, nil
}

// GetJobInfo retrieves the current job information.
// Returns the decoded struct, the raw response body, and any error.
// body is nil when the response is 204 No Content.
func (c *PrusaLinkClient) GetJobInfo() (*PrusaLinkJob, []byte, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/job", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create job request: %w", err)
	}
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get job info from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return &PrusaLinkJob{}, nil, nil // No active job
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read job response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, body, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	var job PrusaLinkJob
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, body, fmt.Errorf("failed to decode job response: %w", err)
	}

	return &job, body, nil
}

// GetPrinterInfo retrieves the printer information
func (c *PrusaLinkClient) GetPrinterInfo() (*PrusaLinkInfo, error) {
	log.Printf("🔍 [PrusaLink] Getting printer info from %s", c.baseURL)

	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/info", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create printer info request: %w", err)
	}
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("❌ [PrusaLink] API call failed for %s: %v", c.baseURL, err)
		return nil, fmt.Errorf("failed to get printer info from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("❌ [PrusaLink] API error for %s: %d - %s", c.baseURL, resp.StatusCode, string(body))
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read printer info response: %w", err)
	}

	log.Printf("📥 [PrusaLink] Raw API response from %s: %s", c.baseURL, string(body))

	var info PrusaLinkInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("failed to decode printer info response: %w", err)
	}

	log.Printf("✅ [PrusaLink] Parsed printer info: hostname='%s', serial='%s', mmu=%v",
		info.Hostname, info.Serial, info.MMU)

	return &info, nil
}

// PausePrint sends a pause command to PrusaLink
func (c *PrusaLinkClient) PausePrint() error {
	req, err := http.NewRequest("POST", c.baseURL+"/api/v1/job/pause", bytes.NewBufferString("{}"))
	if err != nil {
		return fmt.Errorf("failed to create pause request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send pause command: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PrusaLink pause error: %d - %s", resp.StatusCode, string(body))
	}

	log.Printf("⏸️  [PrusaLink] Pause command sent to %s", c.baseURL)
	return nil
}

// ResumePrint sends a resume command to PrusaLink
func (c *PrusaLinkClient) ResumePrint() error {
	req, err := http.NewRequest("POST", c.baseURL+"/api/v1/job/resume", bytes.NewBufferString("{}"))
	if err != nil {
		return fmt.Errorf("failed to create resume request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send resume command: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PrusaLink resume error: %d - %s", resp.StatusCode, string(body))
	}

	log.Printf("▶️  [PrusaLink] Resume command sent to %s", c.baseURL)
	return nil
}

// StopPrint sends a stop/cancel command to PrusaLink
func (c *PrusaLinkClient) StopPrint() error {
	req, err := http.NewRequest("DELETE", c.baseURL+"/api/v1/job", nil)
	if err != nil {
		return fmt.Errorf("failed to create stop request: %w", err)
	}
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send stop command: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PrusaLink stop error: %d - %s", resp.StatusCode, string(body))
	}

	log.Printf("⏹️  [PrusaLink] Stop command sent to %s", c.baseURL)
	return nil
}

// GetGcodeFile downloads the G-code file for a completed print job
func (c *PrusaLinkClient) GetGcodeFile(filename string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/"+filename, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create G-code request: %w", err)
	}
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get G-code file from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read G-code file: %w", err)
	}

	return body, nil
}

// GetGcodeFileWithRetry downloads the G-code file with exponential-backoff retry
func (c *PrusaLinkClient) GetGcodeFileWithRetry(filename string, fileDownloadTimeout int) ([]byte, error) {
	const maxRetries = 3
	backoffDelays := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		log.Printf("Downloading G-code file attempt %d/%d: %s", attempt+1, maxRetries, filename)

		fileDialer := &net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		// No Client.Timeout — bgcode files can be 100s of MB served over slow USB storage.
		// ResponseHeaderTimeout ensures the server starts responding within 30s.
		// Body reading is unbounded; TCP keepalives detect true dead connections.
		fileClient := &http.Client{
			Transport: &http.Transport{
				DialContext:           fileDialer.DialContext,
				MaxIdleConns:          10,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}

		req, err := http.NewRequest("GET", c.baseURL+"/"+filename, nil)
		if err != nil {
			lastErr = fmt.Errorf("failed to create G-code request: %w", err)
			if attempt < maxRetries-1 {
				time.Sleep(backoffDelays[attempt])
			}
			continue
		}
		c.addAPIKey(req)

		resp, err := fileClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to get G-code file: %w", err)
			if attempt < maxRetries-1 {
				time.Sleep(backoffDelays[attempt])
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
			if attempt < maxRetries-1 {
				time.Sleep(backoffDelays[attempt])
			}
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read G-code file: %w", err)
			if attempt < maxRetries-1 {
				time.Sleep(backoffDelays[attempt])
			}
			continue
		}

		log.Printf("✅ Downloaded G-code on attempt %d: %s (%d bytes)", attempt+1, filename, len(body))
		return body, nil
	}

	return nil, fmt.Errorf("failed to download G-code file after %d attempts: %w", maxRetries, lastErr)
}

// FilamentGcodeUsage holds the per-toolhead filament data extracted from G-code metadata.
// Both fields are populated when PrusaSlicer writes both [g] and [mm] comments (the common case).
// MM is zero when only [g] is present; Grams is estimated from MM when only [mm] is present.
type FilamentGcodeUsage struct {
	Grams float64
	MM    float64
}

// ParseGcodeFilamentUsage extracts per-toolhead filament usage from .gcode or .bgcode content.
// It handles both PrusaSlicer plain-gcode comment format and the binary .bgcode metadata format.
// Returns a map of toolhead index → FilamentGcodeUsage with both grams and mm where available.
func (c *PrusaLinkClient) ParseGcodeFilamentUsage(gcodeContent []byte) (map[int]FilamentGcodeUsage, error) {
	content := string(gcodeContent)

	// --- Pattern 1/2: "; filament used [g] = 1.23, 4.56, ..."  (PrusaSlicer .gcode and .bgcode)
	gramsPerTool := make(map[int]float64)
	gcodeRegex := regexp.MustCompile(`;?\s*filament used \[g\]\s*=\s*([0-9.,\s]+)`)
	if match := gcodeRegex.FindStringSubmatch(content); len(match) >= 2 {
		for i, s := range strings.Split(match[1], ",") {
			if weight, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && weight > 0 {
				gramsPerTool[i] = weight
			}
		}
	}

	// --- Pattern 3: "; filament used [mm] = 2345.67, ..."  (PrusaSlicer always writes both [g] and [mm])
	// Also used as a grams fallback (with PLA density estimate) when [g] is absent.
	mmPerTool := make(map[int]float64)
	mmRegex := regexp.MustCompile(`;?\s*filament used \[mm\]\s*=\s*([0-9.,\s]+)`)
	if match := mmRegex.FindStringSubmatch(content); len(match) >= 2 {
		for i, s := range strings.Split(match[1], ",") {
			if length, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && length > 0 {
				mmPerTool[i] = length
			}
		}
	}

	result := make(map[int]FilamentGcodeUsage)
	seen := make(map[int]bool)
	for i := range gramsPerTool {
		seen[i] = true
	}
	for i := range mmPerTool {
		seen[i] = true
	}

	for i := range seen {
		g := gramsPerTool[i]
		mm := mmPerTool[i]
		if g == 0 && mm > 0 {
			// Convert mm → g using default PLA density as fallback (no [g] comment present)
			volumeMM3 := 3.14159265 * (1.75 / 2) * (1.75 / 2) * mm
			g = (volumeMM3 / 1000.0) * 1.24
		}
		if g > 0 || mm > 0 {
			result[i] = FilamentGcodeUsage{Grams: g, MM: mm}
		}
	}

	if len(result) > 0 {
		if len(gramsPerTool) > 0 {
			log.Printf("🔍 Parsed filament usage via [g] pattern: %v (mm: %v)", gramsPerTool, mmPerTool)
		} else {
			log.Printf("🔍 Parsed filament usage via [mm] pattern (estimated from length): %v", mmPerTool)
		}
	}

	// No data found — callers must decide whether to treat this as an error
	return result, nil
}

// PrusaLinkCamera represents a camera registered with PrusaLink.
type PrusaLinkCamera struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Connected  bool   `json:"connected"`
	Resolution string `json:"resolution,omitempty"`
}

// GetCameras returns all cameras registered with this printer.
// Returns nil (not an error) when the printer has no camera support.
func (c *PrusaLinkClient) GetCameras() ([]PrusaLinkCamera, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/cameras", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cameras request: %w", err)
	}
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get cameras: %w", err)
	}
	defer resp.Body.Close()

	// 404 / 204 = no camera support on this firmware version
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink cameras API error: %d - %s", resp.StatusCode, string(body))
	}

	var cameras []PrusaLinkCamera
	if err := json.NewDecoder(resp.Body).Decode(&cameras); err != nil {
		return nil, fmt.Errorf("failed to decode cameras response: %w", err)
	}
	return cameras, nil
}

// GetSnapshot fetches a JPEG snapshot from the named camera.
func (c *PrusaLinkClient) GetSnapshot(cameraID string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/cameras/"+cameraID+"/snap", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot request: %w", err)
	}
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get snapshot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink snapshot error: %d - %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

// TestConnection tests the connection to PrusaLink
func (c *PrusaLinkClient) TestConnection() error {
	_, _, err := c.GetStatus()
	return err
}
