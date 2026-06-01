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

// PrusaLinkStatus represents the status response from /api/v1/status
// Tool temperatures are stored as a flat map so the struct handles any number
// of toolheads (MINI has 1, XL has 5, INDX will have 8+).
type PrusaLinkStatus struct {
	Printer struct {
		State       string             `json:"state"`
		Temperature map[string]ToolTemp `json:"temperature"` // keys: "bed", "tool0"..."toolN"
		Telemetry   struct {
			PrintTime     int     `json:"print_time"`
			PrintTimeLeft int     `json:"print_time_left"`
			Progress      float64 `json:"progress"`
		} `json:"telemetry"`
	} `json:"printer"`
}

// ToolTemp holds actual and target temperatures for a single tool or bed
type ToolTemp struct {
	Actual float64 `json:"actual"`
	Target float64 `json:"target"`
}

// PrusaLinkJob represents the job response from /api/v1/job
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
		Refs        struct {
			Download string `json:"download"`
		} `json:"refs"`
	} `json:"file"`
	// Filament usage data (available on some firmware versions)
	Filament []struct {
		ToolheadID int     `json:"toolhead_id"`
		Length     float64 `json:"length"`
		Weight     float64 `json:"weight"`
	} `json:"filament,omitempty"`
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

// GetStatus retrieves the current status of the printer
func (c *PrusaLinkClient) GetStatus() (*PrusaLinkStatus, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/status", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create status request: %w", err)
	}
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get status from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	var status PrusaLinkStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode status response: %w", err)
	}

	return &status, nil
}

// GetJobInfo retrieves the current job information
func (c *PrusaLinkClient) GetJobInfo() (*PrusaLinkJob, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/job", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create job request: %w", err)
	}
	c.addAPIKey(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get job info from PrusaLink: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return &PrusaLinkJob{}, nil // No active job
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PrusaLink API error: %d - %s", resp.StatusCode, string(body))
	}

	var job PrusaLinkJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("failed to decode job response: %w", err)
	}

	return &job, nil
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

// ParseGcodeFilamentUsage extracts per-toolhead filament usage (in grams) from .gcode or .bgcode content.
// It handles both PrusaSlicer plain-gcode comment format and the binary .bgcode metadata format.
// Returns a map of toolhead index → grams used.
func (c *PrusaLinkClient) ParseGcodeFilamentUsage(gcodeContent []byte) (map[int]float64, error) {
	content := string(gcodeContent)
	filamentUsage := make(map[int]float64)

	// --- Pattern 1: "; filament used [g] = 1.23, 4.56, ..."  (PrusaSlicer .gcode comment)
	// --- Pattern 2: "filament used [g]=1.23,4.56,..."         (.bgcode embedded metadata)
	gcodeRegex := regexp.MustCompile(`;?\s*filament used \[g\]\s*=\s*([0-9.,\s]+)`)
	if match := gcodeRegex.FindStringSubmatch(content); len(match) >= 2 {
		weights := strings.Split(match[1], ",")
		for i, weightStr := range weights {
			weightStr = strings.TrimSpace(weightStr)
			if weight, err := strconv.ParseFloat(weightStr, 64); err == nil && weight > 0 {
				filamentUsage[i] = weight
			}
		}
		if len(filamentUsage) > 0 {
			log.Printf("🔍 Parsed filament usage via [g] pattern: %v", filamentUsage)
			return filamentUsage, nil
		}
	}

	// --- Pattern 3: Cura-style "; filament_cost = ..." / PrusaSlicer "; filament used [mm] = ..."
	// Convert mm → g using a default density of 1.24 g/cm³ for PLA at 1.75mm diameter.
	// This is a fallback — weight comment above is preferred.
	mmRegex := regexp.MustCompile(`;?\s*filament used \[mm\]\s*=\s*([0-9.,\s]+)`)
	if match := mmRegex.FindStringSubmatch(content); len(match) >= 2 {
		lengths := strings.Split(match[1], ",")
		for i, lenStr := range lengths {
			lenStr = strings.TrimSpace(lenStr)
			if length, err := strconv.ParseFloat(lenStr, 64); err == nil && length > 0 {
				// Volume = π * (d/2)^2 * length  where d=1.75mm
				volumeMM3 := 3.14159265 * (1.75 / 2) * (1.75 / 2) * length
				volumeCM3 := volumeMM3 / 1000.0
				weightG := volumeCM3 * 1.24 // default PLA density
				if weightG > 0 {
					filamentUsage[i] = weightG
				}
			}
		}
		if len(filamentUsage) > 0 {
			log.Printf("🔍 Parsed filament usage via [mm] pattern (estimated from length): %v", filamentUsage)
			return filamentUsage, nil
		}
	}

	// No data found — callers must decide whether to treat this as an error
	return filamentUsage, nil
}

// TestConnection tests the connection to PrusaLink
func (c *PrusaLinkClient) TestConnection() error {
	_, err := c.GetStatus()
	return err
}
