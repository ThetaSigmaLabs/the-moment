// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// PrinterConfig represents configuration for a single printer
type PrinterConfig struct {
	Name               string `json:"name"`
	Model              string `json:"model"`
	IPAddress          string `json:"ip_address"`
	APIKey             string `json:"api_key,omitempty"`
	Toolheads          int    `json:"toolheads"`
	IsVirtual          bool   `json:"is_virtual,omitempty"`            // Virtual test printer — no real hardware
	PrinterType        string `json:"printer_type,omitempty"`          // "prusalink" | "octoprint"
	DebugLog           bool   `json:"debug_log,omitempty"`             // Capture per-poll debug log for print history
	CameraSnapshotURL      string                 `json:"camera_snapshot_url,omitempty"`      // HTTP or RTSP URL for print-event snapshots
	SortOrder              int                    `json:"sort_order,omitempty"`               // Dashboard display order (lower = leftmost)
	ProgressSnapshotConfig ProgressSnapshotConfig `json:"progress_snapshot_config,omitempty"` // In-progress snapshot settings
}

// ProgressSnapshotConfig controls automatic camera snapshots during a print.
// Mode "interval": capture every Interval percent (e.g. 10 → 10%, 20%, …, 90%).
// Mode "milestones": capture at the listed percentages.
// Mode "none" (or zero value): no progress snapshots.
type ProgressSnapshotConfig struct {
	Mode       string    `json:"mode,omitempty"`       // "none" | "interval" | "milestones"
	Interval   float64   `json:"interval,omitempty"`   // percent step for interval mode
	Milestones []float64 `json:"milestones,omitempty"` // explicit percentages for milestones mode
}

// FilamentSpool represents a filament spool from Spoolman
type FilamentSpool struct {
	ID              int     `json:"id"`
	Name            string  `json:"name"`
	Brand           string  `json:"brand"`
	Material        string  `json:"material"`
	Color           string  `json:"color"`
	RemainingLength float64 `json:"remaining_length"`
	TotalLength     float64 `json:"total_length"`
	ToolheadMapping *int    `json:"toolhead_mapping,omitempty"`
}

// Config holds all configuration for the application
type Config struct {
	SpoolmanURL                  string
	SpoolmanExternalURL          string
	PollInterval                 time.Duration
	LocationSyncInterval         time.Duration
	DBFile                       string
	GcodePath                    string // root directory for print history file attachments
	UploadsPath                  string // root directory for virtual printer uploaded files
	WebPort                      string
	PrusaLinkTimeout             int
	PrusaLinkFileDownloadTimeout int
	SpoolmanTimeout              int
	Printers                     map[string]PrinterConfig // Key is printer ID, value is printer config
}

// LoadConfig loads configuration from database
func LoadConfig(bridge *FilamentBridge) (*Config, error) {
	// Get configuration from database
	configValues, err := bridge.GetAllConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config from database: %w", err)
	}

	// Parse poll interval
	pollInterval := DefaultPollInterval
	if pollStr, exists := configValues[ConfigKeyPollInterval]; exists {
		if parsed, err := strconv.Atoi(pollStr); err == nil {
			pollInterval = parsed
		}
	}

	// Parse location sync interval
	locationSyncInterval := DefaultLocationSyncInterval
	if syncStr, exists := configValues[ConfigKeyLocationSyncInterval]; exists {
		if parsed, err := strconv.Atoi(syncStr); err == nil {
			locationSyncInterval = parsed
		}
	}

	// Parse timeout values
	prusaLinkTimeout := PrusaLinkTimeout
	if timeoutStr, exists := configValues[ConfigKeyPrusaLinkTimeout]; exists {
		if parsed, err := strconv.Atoi(timeoutStr); err == nil {
			prusaLinkTimeout = parsed
		}
	}

	prusaLinkFileDownloadTimeout := PrusaLinkFileDownloadTimeout
	if timeoutStr, exists := configValues[ConfigKeyPrusaLinkFileDownloadTimeout]; exists {
		if parsed, err := strconv.Atoi(timeoutStr); err == nil {
			prusaLinkFileDownloadTimeout = parsed
		}
	}

	spoolmanTimeout := SpoolmanTimeout
	if timeoutStr, exists := configValues[ConfigKeySpoolmanTimeout]; exists {
		if parsed, err := strconv.Atoi(timeoutStr); err == nil {
			spoolmanTimeout = parsed
		}
	}

	config := &Config{
		SpoolmanURL:                  configValues[ConfigKeySpoolmanURL],
		SpoolmanExternalURL:          configValues[ConfigKeySpoolmanExternalURL],
		PollInterval:                 time.Duration(pollInterval) * time.Second,
		LocationSyncInterval:         time.Duration(locationSyncInterval) * time.Minute,
		DBFile:                       getDBFilePath(),
		GcodePath:                    getGcodePath(),
		UploadsPath:                  getUploadsPath(),
		WebPort:                      configValues[ConfigKeyWebPort],
		PrusaLinkTimeout:             prusaLinkTimeout,
		PrusaLinkFileDownloadTimeout: prusaLinkFileDownloadTimeout,
		SpoolmanTimeout:              spoolmanTimeout,
		Printers:                     make(map[string]PrinterConfig),
	}

	// Load individual printer configurations from database
	printerConfigs, err := bridge.GetAllPrinterConfigs()
	if err != nil {
		fmt.Printf("Error loading printer configs: %v\n", err)
		// Fallback to empty config
		config.Printers["no_printers"] = PrinterConfig{
			Name:      "No Printers Configured",
			Model:     "Unknown",
			IPAddress: "",
			APIKey:    "",
			Toolheads: 0,
		}
		return config, nil
	}

	// Process each printer configuration
	for printerID, printerConfig := range printerConfigs {
		config.Printers[printerID] = printerConfig
	}

	// If no printers configured, add placeholder
	if len(config.Printers) == 0 {
		config.Printers["no_printers"] = PrinterConfig{
			Name:      "No Printers Configured",
			Model:     "Unknown",
			IPAddress: "",
			APIKey:    "",
			Toolheads: 0,
		}
	}

	return config, nil
}

// resolvePrinterName resolves printer name from config, with fallback to IP-based name
func resolvePrinterName(config PrinterConfig) string {
	if config.Name != "" {
		return config.Name
	}
	return fmt.Sprintf("Printer_%s", config.IPAddress)
}

// getDBFilePath returns the database file path from THE_MOMENT_DB_PATH env var.
// THE_MOMENT_DB_PATH is the directory; the DB filename is appended automatically.
func getDBFilePath() string {
	if dbPath := os.Getenv("THE_MOMENT_DB_PATH"); dbPath != "" {
		return filepath.Join(dbPath, DefaultDBFileName)
	}
	return filepath.Join("the-moment-data", "db", DefaultDBFileName)
}

// getGcodePath returns the root directory for print history file attachments (gcode, slicer, etc.)
func getGcodePath() string {
	if d := os.Getenv("THE_MOMENT_GCODE_PATH"); d != "" {
		return d
	}
	return filepath.Join("the-moment-data", "gcode")
}

// getUploadsPath returns the root directory for virtual printer uploaded files.
func getUploadsPath() string {
	if d := os.Getenv("THE_MOMENT_UPLOADS_PATH"); d != "" {
		return d
	}
	return filepath.Join("the-moment-data", "uploads")
}

// getBackupDir returns the directory where backup archives are stored.
func getBackupDir() string {
	if d := os.Getenv("BACKUP_DIR"); d != "" {
		return d
	}
	return "backups"
}
