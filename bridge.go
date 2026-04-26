// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// newSessionID returns a random UUID v4 string used to group all print_history
// rows that belong to the same physical print job.
func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// FilamentBridge manages the connection between PrusaLink and Spoolman
type FilamentBridge struct {
	config           *Config
	spoolman         *SpoolmanClient
	db               *sql.DB
	wasPrinting      map[string]bool
	currentJobFile   map[string]string     // Store current job filename per printer
	processingPrints map[string]bool       // Track prints being processed
	monitoringActive map[string]bool       // Guard against overlapping monitor goroutines per printer
	printErrors      map[string]PrintError // Store print processing errors
	errorMutex       sync.RWMutex
	previousState    map[string]string // Last seen printer state per printer
	mutex            sync.RWMutex
}

// ToolheadMapping represents a mapping between a printer toolhead and a spool
type ToolheadMapping struct {
	PrinterName string    `json:"printer_name"`
	ToolheadID  int       `json:"toolhead_id"`
	SpoolID     int       `json:"spool_id"`
	MappedAt    time.Time `json:"mapped_at"`
	DisplayName string    `json:"display_name,omitempty"` // Custom toolhead name or empty for default
}

// PrintHistory represents a single print job record
type PrintHistory struct {
	ID               int       `json:"id"`
	PrinterName      string    `json:"printer_name"`
	ToolheadID       int       `json:"toolhead_id"`
	SpoolID          int       `json:"spool_id"`
	FilamentUsed     float64   `json:"filament_used"` // grams
	PrintStarted     time.Time `json:"print_started"`
	PrintFinished    time.Time `json:"print_finished"`
	JobName          string    `json:"job_name"`
	Notes            string    `json:"notes"`
	Status           string    `json:"status"` // completed, cancelled, failed
	PrintTimeMinutes float64   `json:"print_time_minutes"`
	ThumbnailBase64  string    `json:"thumbnail_base64"` // JPG, data URI ready
	// Joined from print_costs (may be zero if not calculated)
	TotalCost float64 `json:"total_cost"`
	Currency  string  `json:"currency"`

	// SessionID groups all print_history rows from the same physical print job.
	// Multi-toolhead PrusaLink prints produce N rows; all share one SessionID.
	// Legacy rows (pre-session-id) have an empty string here.
	SessionID string `json:"session_id"`

	// Source and precision metadata (OctoPrint records fill these fully)
	Source            string `json:"source"`             // "prusalink" | "octoprint"
	TotalDurationSec  float64 `json:"total_duration_sec"`
	PrintDurationSec  float64 `json:"print_duration_sec"`
	PauseDurationSec  float64 `json:"pause_duration_sec"`
	PauseCount        int     `json:"pause_count"`
	CancelReason      string  `json:"cancel_reason,omitempty"`
	TimePrecision     string  `json:"time_precision"`     // "exact" | "approximate"
	FilamentPrecision string  `json:"filament_precision"` // "measured" | "estimated"

	// Per-tool filament and pause detail (populated only on single-record fetch)
	FilamentUsages []PrintFilamentUsage `json:"filament_usages,omitempty"`
	Pauses         []PrintPause         `json:"pauses,omitempty"`
}

// PrintFilamentUsage is per-tool filament data stored for a unified print record.
// ChangeNumber distinguishes multiple spool loads on the same tool: 0 = first load,
// 1 = second (first manual change), etc.  Multi-tool prints have distinct ToolIndex
// values; manual filament changes on one tool have the same ToolIndex with
// incrementing ChangeNumber.
type PrintFilamentUsage struct {
	ID             int     `json:"id"`
	PrintID        int     `json:"print_id"`
	ToolIndex      int     `json:"tool_index"`
	ChangeNumber   int     `json:"change_number"`
	SpoolID        int     `json:"spool_id"`
	FilamentUsedMM float64 `json:"filament_used_mm"`
	FilamentUsedG  float64 `json:"filament_used_grams"`
}

// PrintPause records a single pause event within a print job.
type PrintPause struct {
	ID          int       `json:"id"`
	PrintID     int       `json:"print_id"`
	PausedAt    time.Time `json:"paused_at"`
	ResumedAt   time.Time `json:"resumed_at"`
	DurationSec float64   `json:"duration_sec"`
	Reason      string    `json:"reason"` // filament_change | runout | user | unknown
}

// OctoPrintPayload is the request body sent by the OctoPrint plugin.
type OctoPrintPayload struct {
	SessionID         string                    `json:"session_id"` // optional; generated server-side if absent
	Source            string                    `json:"source"`
	PrinterID         string                    `json:"printer_id"`
	FileName          string                    `json:"file_name"`
	Status            string                    `json:"status"`
	StartedAt         time.Time                 `json:"started_at"`
	EndedAt           time.Time                 `json:"ended_at"`
	TotalDurationSec  float64                   `json:"total_duration_sec"`
	PrintDurationSec  float64                   `json:"print_duration_sec"`
	PauseDurationSec  float64                   `json:"pause_duration_sec"`
	PauseCount        int                       `json:"pause_count"`
	Pauses            []OctoPrintPayloadPause   `json:"pauses"`
	CancelReason      *string                   `json:"cancel_reason"`
	Filament          []OctoPrintPayloadFilament `json:"filament"`
	TimePrecision     string                    `json:"time_precision"`
	FilamentPrecision string                    `json:"filament_precision"`
	// SpoolmanManaged: true (or nil/omitted) = the OctoPrint Spoolman/SpoolManager
	// plugin already deducted filament; The Moment must NOT deduct again.
	// false = no Spoolman plugin active; The Moment deducts from Spoolman.
	SpoolmanManaged *bool `json:"spoolman_managed,omitempty"`
}

// OctoPrintPayloadPause is a single pause entry within an OctoPrint payload.
type OctoPrintPayloadPause struct {
	PausedAt    time.Time `json:"paused_at"`
	ResumedAt   time.Time `json:"resumed_at"`
	DurationSec float64   `json:"duration_sec"`
	Reason      string    `json:"reason"`
}

// OctoPrintPayloadFilament is per-tool filament data within an OctoPrint payload.
// ChangeNumber mirrors PrintFilamentUsage.ChangeNumber: 0 for initial load, 1+ for
// each subsequent manual spool swap on that tool index.
type OctoPrintPayloadFilament struct {
	ToolIndex      int     `json:"tool_index"`
	ChangeNumber   int     `json:"change_number"`
	SpoolID        int     `json:"spool_id"`
	FilamentUsedMM float64 `json:"filament_used_mm"`
	FilamentUsedG  float64 `json:"filament_used_grams"`
}

// PrintSession groups all print_history rows sharing a session_id into one logical
// print job. Multi-toolhead PrusaLink prints produce N rows per session; OctoPrint
// produces one row. Legacy rows (empty session_id) each form their own session.
type PrintSession struct {
	SessionID    string         `json:"session_id"`
	JobName      string         `json:"job_name"`
	PrinterName  string         `json:"printer_name"`
	Status       string         `json:"status"`
	Source       string         `json:"source"`
	PrintStarted time.Time      `json:"print_started"`
	PrintFinished time.Time     `json:"print_finished"`
	TotalFilamentG float64      `json:"total_filament_grams"`
	TotalCost    float64        `json:"total_cost"`
	Currency     string         `json:"currency"`
	ToolCount    int            `json:"tool_count"`
	Records      []PrintHistory `json:"records"`
}

// PrintError represents a failed print processing attempt
type PrintError struct {
	ID           string    `json:"id"`
	PrinterName  string    `json:"printer_name"`
	Filename     string    `json:"filename"`
	Error        string    `json:"error"`
	Timestamp    time.Time `json:"timestamp"`
	Acknowledged bool      `json:"acknowledged"`
}

// PrinterStatus represents the current status of all printers
type PrinterStatus struct {
	Printers         map[string]PrinterData             `json:"printers"`
	ToolheadMappings map[string]map[int]ToolheadMapping `json:"toolhead_mappings"`
	Timestamp        time.Time                          `json:"timestamp"`
}

// PrinterData represents data for a single printer
type PrinterData struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// NewFilamentBridge creates a new FilamentBridge instance
func NewFilamentBridge(config *Config) (*FilamentBridge, error) {
	bridge := &FilamentBridge{
		config:           config,
		spoolman:         NewSpoolmanClient(DefaultSpoolmanURL, SpoolmanTimeout, "", ""), // Default URL and timeout, will be updated
		wasPrinting:      make(map[string]bool),
		currentJobFile:   make(map[string]string),
		processingPrints: make(map[string]bool),
		monitoringActive: make(map[string]bool),
		printErrors:      make(map[string]PrintError),
		previousState:    make(map[string]string),
	}

	// Initialize database
	if err := bridge.initDatabase(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	if err := bridge.updatePrintHistoryTable(); err != nil {
		return nil, fmt.Errorf("failed to update for The Moment database additions: %w", err)
	}

	if err := bridge.migrateVirtualPrinterSupport(); err != nil {
		return nil, fmt.Errorf("failed to migrate virtual printer support: %w", err)
	}

	if err := bridge.migrateOctoPrintSupport(); err != nil {
		return nil, fmt.Errorf("failed to migrate octoprint support: %w", err)
	}

	if err := bridge.migrateSessionSupport(); err != nil {
		return nil, fmt.Errorf("failed to migrate session support: %w", err)
	}

	if err := bridge.migratePrinterCostSettings(); err != nil {
		return nil, fmt.Errorf("failed to migrate printer cost settings: %w", err)
	}

	// Update Spoolman URL and timeout if config is provided
	if config != nil && config.SpoolmanURL != "" {
		bridge.spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout, config.SpoolmanUsername, config.SpoolmanPassword)
	}

	return bridge, nil
}

// initDatabase initializes the SQLite database
func (b *FilamentBridge) initDatabase() error {
	dbFile := DefaultDBFileName
	if b.config != nil && b.config.DBFile != "" {
		dbFile = b.config.DBFile
	}
	// Check for environment variable (path only, append filename)
	if envDBPath := os.Getenv("THE_MOMENT_DB_PATH"); envDBPath != "" {
		dbFile = filepath.Join(envDBPath, DefaultDBFileName)
	}

	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	// Required for ON DELETE CASCADE on virtual_printer_files
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		log.Printf("Warning: could not enable SQLite foreign keys: %v", err)
	}

	b.db = db

	// Create tables
	createTables := []string{
		`CREATE TABLE IF NOT EXISTS configuration (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			description TEXT,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS printer_configs (
			printer_id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			model TEXT,
			ip_address TEXT NOT NULL,
			api_key TEXT,
			toolheads INTEGER DEFAULT 1,
			is_virtual INTEGER DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS virtual_printer_files (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_id TEXT NOT NULL,
			filename TEXT NOT NULL,
			display_name TEXT NOT NULL,
			file_size INTEGER DEFAULT 0,
			content BLOB NOT NULL,
			uploaded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (printer_id) REFERENCES printer_configs(printer_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS toolhead_mappings (
			printer_name TEXT,
			toolhead_id INTEGER,
			spool_id INTEGER,
			mapped_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (printer_name, toolhead_id)
		)`,
		`CREATE TABLE IF NOT EXISTS print_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_name TEXT,
			toolhead_id INTEGER,
			spool_id INTEGER,
			filament_used REAL,
			print_started TIMESTAMP,
			print_finished TIMESTAMP,
			job_name TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS nfc_sessions (
			session_id TEXT PRIMARY KEY,
			spool_id INTEGER,
			printer_name TEXT,
			toolhead_id INTEGER,
			location_name TEXT,
			is_printer_location BOOLEAN,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			expires_at TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS toolhead_names (
			printer_id TEXT,
			toolhead_id INTEGER,
			display_name TEXT NOT NULL,
			PRIMARY KEY (printer_id, toolhead_id)
		)`,
		`CREATE TABLE IF NOT EXISTS print_costs (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        print_history_id INTEGER NOT NULL,
        filament_cost REAL NOT NULL DEFAULT 0,
        electricity_cost REAL NOT NULL DEFAULT 0,
        maintenance_cost REAL NOT NULL DEFAULT 0,
        total_cost REAL NOT NULL DEFAULT 0,
        currency TEXT NOT NULL DEFAULT 'USD',
        created_at TIMESTAMP NOT NULL,
        FOREIGN KEY (print_history_id) REFERENCES print_history(id) ON DELETE CASCADE
    )`,
		`CREATE TABLE IF NOT EXISTS pending_spoolman_updates (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_name TEXT NOT NULL,
			toolhead_id INTEGER NOT NULL,
			spool_id INTEGER NOT NULL,
			used_weight REAL NOT NULL,
			job_name TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			last_attempt TIMESTAMP,
			attempts INTEGER DEFAULT 0,
			last_error TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS pending_gcode_downloads (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_name TEXT NOT NULL,
			printer_ip TEXT NOT NULL,
			filename TEXT NOT NULL,
			job_type TEXT NOT NULL DEFAULT 'completed',
			progress_pct REAL NOT NULL DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			last_attempt TIMESTAMP,
			attempts INTEGER DEFAULT 0,
			last_error TEXT
		)`,
	}

	for _, query := range createTables {
		if _, err := b.db.Exec(query); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}

	// Initialize default configuration
	if err := b.initializeDefaultConfig(); err != nil {
		return fmt.Errorf("failed to initialize default configuration: %w", err)
	}

	// Migrate existing The Moment locations to Spoolman
	if err := b.migrateLocationsToSpoolman(); err != nil {
		log.Printf("Warning: Failed to migrate locations to Spoolman: %v", err)
		// Don't fail initialization if migration fails
	}

	// Create Spoolman locations for existing toolhead mappings
	if err := b.migrateToolheadMappingsToSpoolman(); err != nil {
		log.Printf("Warning: Failed to migrate toolhead mappings to Spoolman: %v", err)
		// Don't fail initialization if migration fails
	}

	return nil
}

// Add to initDatabase() method
func (b *FilamentBridge) updatePrintHistoryTable() error {
	// Add new columns to print_history table
	alterQueries := []string{
		`ALTER TABLE print_history ADD COLUMN user_id INTEGER DEFAULT 1`,
		`ALTER TABLE print_history ADD COLUMN print_time_minutes REAL DEFAULT 0`,
		`ALTER TABLE print_history ADD COLUMN layer_height REAL DEFAULT 0`,
		`ALTER TABLE print_history ADD COLUMN infill_density REAL DEFAULT 0`,
		`ALTER TABLE print_history ADD COLUMN support_material INTEGER DEFAULT 0`,
		`ALTER TABLE print_history ADD COLUMN slicer_profile_id INTEGER`,
		`ALTER TABLE print_history ADD COLUMN thumbnail_path TEXT`,
		`ALTER TABLE print_history ADD COLUMN notes TEXT`,
		`ALTER TABLE print_history ADD COLUMN status TEXT DEFAULT 'completed'`, // completed, cancelled, failed
	}

	for _, query := range alterQueries {
		_, err := b.db.Exec(query)
		if err != nil {
			// Column might already exist, continue
			continue
		}
	}

	return nil
}

// migrateOctoPrintSupport adds columns and tables needed for OctoPrint push integration.
func (b *FilamentBridge) migrateOctoPrintSupport() error {
	newColumns := []string{
		`ALTER TABLE printer_configs ADD COLUMN printer_type TEXT DEFAULT 'prusalink'`,
		`ALTER TABLE print_history ADD COLUMN source TEXT DEFAULT 'prusalink'`,
		`ALTER TABLE print_history ADD COLUMN total_duration_sec REAL`,
		`ALTER TABLE print_history ADD COLUMN print_duration_sec REAL`,
		`ALTER TABLE print_history ADD COLUMN pause_duration_sec REAL DEFAULT 0`,
		`ALTER TABLE print_history ADD COLUMN pause_count INTEGER DEFAULT 0`,
		`ALTER TABLE print_history ADD COLUMN cancel_reason TEXT`,
		`ALTER TABLE print_history ADD COLUMN time_precision TEXT DEFAULT 'approximate'`,
		`ALTER TABLE print_history ADD COLUMN filament_precision TEXT DEFAULT 'estimated'`,
	}
	for _, q := range newColumns {
		b.db.Exec(q) // ignore "duplicate column" errors from existing DBs
	}

	// print_costs.print_history_id must be unique for the ON CONFLICT upsert in SavePrintCost.
	b.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_print_costs_print_id ON print_costs(print_history_id)`)

	newTables := []string{
		`CREATE TABLE IF NOT EXISTS print_filament_usage (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			print_id INTEGER NOT NULL,
			tool_index INTEGER NOT NULL DEFAULT 0,
			spool_id INTEGER,
			filament_used_mm REAL NOT NULL DEFAULT 0,
			filament_used_grams REAL NOT NULL DEFAULT 0,
			FOREIGN KEY (print_id) REFERENCES print_history(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS print_pauses (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			print_id INTEGER NOT NULL,
			paused_at TIMESTAMP,
			resumed_at TIMESTAMP,
			duration_sec REAL NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT 'unknown',
			FOREIGN KEY (print_id) REFERENCES print_history(id) ON DELETE CASCADE
		)`,
	}
	for _, q := range newTables {
		if _, err := b.db.Exec(q); err != nil {
			return fmt.Errorf("failed to create octoprint table: %w", err)
		}
	}
	return nil
}

// migratePrinterCostSettings creates the per-printer cost overrides table.
func (b *FilamentBridge) migratePrinterCostSettings() error {
	_, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS printer_cost_settings (
			printer_name         TEXT PRIMARY KEY,
			print_wattage_w      REAL NOT NULL DEFAULT 0,
			preheat_wattage_w    REAL NOT NULL DEFAULT 0,
			preheat_time_min     REAL NOT NULL DEFAULT 0,
			high_temp_extra_w    REAL NOT NULL DEFAULT 0,
			printer_purchase_cost REAL NOT NULL DEFAULT 0,
			estimated_life_hrs   REAL NOT NULL DEFAULT 0,
			depreciation_per_hr  REAL NOT NULL DEFAULT 0,
			updated_at           TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`)
	return err
}

// migrateSessionSupport adds the session_id column to print_history and the
// change_number column to print_filament_usage.
func (b *FilamentBridge) migrateSessionSupport() error {
	b.db.Exec(`ALTER TABLE print_history ADD COLUMN session_id TEXT`)
	b.db.Exec(`CREATE INDEX IF NOT EXISTS idx_print_history_session_id ON print_history(session_id)`)
	b.db.Exec(`ALTER TABLE print_filament_usage ADD COLUMN change_number INTEGER NOT NULL DEFAULT 0`)
	return nil
}

// migrateLocationsToSpoolman migrates existing The Moment locations to Spoolman
func (b *FilamentBridge) migrateLocationsToSpoolman() error {
	// Check if fb_locations table exists by trying to query it
	rows, err := b.db.Query("SELECT name, type, printer_name, toolhead_id FROM fb_locations")
	if err != nil {
		// Table doesn't exist or is empty, nothing to migrate
		return nil
	}
	defer rows.Close()

	migratedCount := 0
	for rows.Next() {
		var name, locationType, printerName sql.NullString
		var toolheadID sql.NullInt64

		if err := rows.Scan(&name, &locationType, &printerName, &toolheadID); err != nil {
			log.Printf("Warning: Failed to scan location row during migration: %v", err)
			continue
		}

		if !name.Valid || name.String == "" {
			continue
		}

		locationName := name.String

		// Skip if this is a virtual printer toolhead location (will be created on-demand)
		if b.isVirtualPrinterToolheadLocation(locationName) {
			log.Printf("Migration: Skipping virtual printer toolhead location '%s'", locationName)
			continue
		}

		// Check if location exists in Spoolman
		// Note: Spoolman API doesn't support creating locations via POST.
		// Locations must be created manually in Spoolman UI or are auto-created when referenced in spools.
		existingLocation, err := b.spoolman.FindLocationByName(locationName)
		if err != nil {
			log.Printf("Warning: Failed to check if location '%s' exists in Spoolman: %v", locationName, err)
			continue
		}

		if existingLocation == nil {
			log.Printf("Migration: Location '%s' does not exist in Spoolman. It will be created when referenced in a spool, or can be created manually in Spoolman UI.", locationName)
		} else {
			migratedCount++
			log.Printf("Migration: Location '%s' already exists in Spoolman", locationName)
		}
	}

	if migratedCount > 0 {
		log.Printf("Migration: Successfully migrated %d location(s) from The Moment to Spoolman", migratedCount)
	}

	return nil
}

// migrateToolheadMappingsToSpoolman creates Spoolman locations for existing toolhead mappings
func (b *FilamentBridge) migrateToolheadMappingsToSpoolman() error {
	// Get all printer configs
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return fmt.Errorf("failed to get printer configs: %w", err)
	}

	// Get all toolhead mappings
	allMappings, err := b.GetAllToolheadMappings()
	if err != nil {
		return fmt.Errorf("failed to get toolhead mappings: %w", err)
	}

	createdCount := 0
	for printerName, printerMappings := range allMappings {
		// Find the printer ID for this printer name
		var printerID string
		for pid, config := range printerConfigs {
			if config.Name == printerName {
				printerID = pid
				break
			}
		}

		if printerID == "" {
			log.Printf("Migration: Could not find printer ID for printer name '%s', skipping", printerName)
			continue
		}

		// Get toolhead names for this printer
		toolheadNames, err := b.GetAllToolheadNames(printerID)
		if err != nil {
			log.Printf("Warning: Failed to get toolhead names for printer %s: %v", printerID, err)
			toolheadNames = make(map[int]string)
		}

		// Create locations for each toolhead mapping
		for toolheadID := range printerMappings {
			// Get display name (custom or default)
			var displayName string
			if name, exists := toolheadNames[toolheadID]; exists {
				displayName = name
			} else {
				displayName = fmt.Sprintf("Toolhead %d", toolheadID)
			}

			locationName := fmt.Sprintf("%s - %s", printerName, displayName)

			// Check if location exists in Spoolman
			// Note: Spoolman API doesn't support creating locations via POST.
			// Locations will be auto-created when spools are assigned to toolheads.
			existingLocation, err := b.spoolman.FindLocationByName(locationName)
			if err != nil {
				log.Printf("Warning: Failed to check if toolhead location '%s' exists in Spoolman: %v", locationName, err)
				continue
			}

			if existingLocation == nil {
				log.Printf("Migration: Toolhead location '%s' does not exist in Spoolman. It will be created when a spool is assigned to this toolhead.", locationName)
			} else {
				createdCount++
				log.Printf("Migration: Toolhead location '%s' already exists in Spoolman", locationName)
			}
		}
	}

	if createdCount > 0 {
		log.Printf("Migration: Successfully created %d toolhead location(s) in Spoolman", createdCount)
	}

	return nil
}

// initializeDefaultConfig sets up default configuration values
func (b *FilamentBridge) initializeDefaultConfig() error {
	defaultConfigs := map[string]string{
		ConfigKeyPrinterIPs:                      "", // Comma-separated list of printer IP addresses
		ConfigKeyAPIKey:                          "", // PrusaLink API key for authentication
		ConfigKeySpoolmanURL:                     DefaultSpoolmanURL,
		ConfigKeySpoolmanUsername:                "", // Spoolman basic auth username (optional)
		ConfigKeySpoolmanPassword:                "", // Spoolman basic auth password (optional)
		ConfigKeyPollInterval:                    fmt.Sprintf("%d", DefaultPollInterval),
		ConfigKeyWebPort:                         DefaultWebPort,
		ConfigKeyPrusaLinkTimeout:                fmt.Sprintf("%d", PrusaLinkTimeout),
		ConfigKeyPrusaLinkFileDownloadTimeout:    fmt.Sprintf("%d", PrusaLinkFileDownloadTimeout),
		ConfigKeySpoolmanTimeout:                 fmt.Sprintf("%d", SpoolmanTimeout),
		ConfigKeyAutoAssignPreviousSpoolEnabled:  "false", // Enable auto-assignment of previous spool to default location
		ConfigKeyAutoAssignPreviousSpoolLocation: "",      // Default location name for auto-assigned previous spools
		ConfigKeyNFCTrashLocation:                "Trash",    // Location for empty/done spools (tag ready to re-program)
		ConfigKeyNFCInventoryLocation:            "Inventory", // Default storage when spool displaced from toolhead
	}

	// Check if this is a fresh installation by checking if any config exists
	var totalCount int
	err := b.db.QueryRow("SELECT COUNT(*) FROM configuration").Scan(&totalCount)
	if err != nil {
		return fmt.Errorf("failed to check config existence: %w", err)
	}

	// Only insert defaults if this is a fresh installation
	if totalCount == 0 {
		for key, value := range defaultConfigs {
			_, err := b.db.Exec(
				"INSERT INTO configuration (key, value, description) VALUES (?, ?, ?)",
				key, value, getConfigDescription(key),
			)
			if err != nil {
				return fmt.Errorf("failed to insert default config %s: %w", key, err)
			}
		}
	}

	return nil
}

// getConfigDescription returns a description for a configuration key
func getConfigDescription(key string) string {
	descriptions := map[string]string{
		ConfigKeyPrinterIPs:                      "Comma-separated list of printer IP addresses for PrusaLink",
		ConfigKeyAPIKey:                          "PrusaLink API key for authentication",
		ConfigKeySpoolmanURL:                     "URL of Spoolman instance",
		ConfigKeySpoolmanUsername:                "Spoolman basic auth username (optional, leave empty if not using basic auth)",
		ConfigKeySpoolmanPassword:                "Spoolman basic auth password (optional, leave empty if not using basic auth)",
		ConfigKeyPollInterval:                    "Polling interval in seconds",
		ConfigKeyWebPort:                         "Port for web interface",
		ConfigKeyPrusaLinkTimeout:                "PrusaLink API timeout in seconds",
		ConfigKeyPrusaLinkFileDownloadTimeout:    "PrusaLink file download timeout in seconds",
		ConfigKeySpoolmanTimeout:                 "Spoolman API timeout in seconds",
		ConfigKeyAutoAssignPreviousSpoolEnabled:  "Enable automatic assignment of previous spool to default location when assigning new spool to toolhead",
		ConfigKeyAutoAssignPreviousSpoolLocation: "Default location name where previous spools will be automatically assigned (must exist as a location)",
		ConfigKeyNFCTrashLocation:                "Spoolman location name for empty/finished spools (NFC tag ready to re-program)",
		ConfigKeyNFCInventoryLocation:            "Spoolman location name used as default storage when a spool is displaced from a toolhead via NFC",
	}
	if desc, exists := descriptions[key]; exists {
		return desc
	}
	return "Configuration value"
}

// GetConfigValue gets a configuration value from the database
func (b *FilamentBridge) GetConfigValue(key string) (string, error) {
	var value string
	err := b.db.QueryRow("SELECT value FROM configuration WHERE key = ?", key).Scan(&value)
	if err != nil {
		return "", fmt.Errorf("failed to get config value for %s: %w", key, err)
	}
	return value, nil
}

// SetConfigValue sets a configuration value in the database
func (b *FilamentBridge) SetConfigValue(key, value string) error {
	_, err := b.db.Exec(
		"INSERT OR REPLACE INTO configuration (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)",
		key, value,
	)
	if err != nil {
		return fmt.Errorf("failed to set config value for %s: %w", key, err)
	}
	return nil
}

// GetAllConfig gets all configuration values
func (b *FilamentBridge) GetAllConfig() (map[string]string, error) {
	rows, err := b.db.Query("SELECT key, value FROM configuration")
	if err != nil {
		return nil, fmt.Errorf("failed to get all config: %w", err)
	}
	defer rows.Close()

	config := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("failed to scan config row: %w", err)
		}
		config[key] = value
	}

	return config, nil
}

// GetAutoAssignPreviousSpoolEnabled gets whether auto-assignment of previous spool is enabled
func (b *FilamentBridge) GetAutoAssignPreviousSpoolEnabled() (bool, error) {
	value, err := b.GetConfigValue(ConfigKeyAutoAssignPreviousSpoolEnabled)
	if err != nil {
		// If key doesn't exist, return false (default)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return value == "true", nil
}

// SetAutoAssignPreviousSpoolEnabled sets whether auto-assignment of previous spool is enabled
func (b *FilamentBridge) SetAutoAssignPreviousSpoolEnabled(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return b.SetConfigValue(ConfigKeyAutoAssignPreviousSpoolEnabled, value)
}

// GetAutoAssignPreviousSpoolLocation gets the default location name for auto-assigned previous spools
func (b *FilamentBridge) GetAutoAssignPreviousSpoolLocation() (string, error) {
	value, err := b.GetConfigValue(ConfigKeyAutoAssignPreviousSpoolLocation)
	if err != nil {
		// If key doesn't exist, return empty string (default)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return value, nil
}

// SetAutoAssignPreviousSpoolLocation sets the default location name for auto-assigned previous spools
func (b *FilamentBridge) SetAutoAssignPreviousSpoolLocation(location string) error {
	return b.SetConfigValue(ConfigKeyAutoAssignPreviousSpoolLocation, location)
}

// GetAllPrinterConfigs gets all printer configurations
func (b *FilamentBridge) GetAllPrinterConfigs() (map[string]PrinterConfig, error) {
	rows, err := b.db.Query("SELECT printer_id, name, model, ip_address, api_key, toolheads, COALESCE(is_virtual, 0), COALESCE(printer_type, 'prusalink') FROM printer_configs")
	if err != nil {
		return nil, fmt.Errorf("failed to get printer configs: %w", err)
	}
	defer rows.Close()

	configs := make(map[string]PrinterConfig)
	for rows.Next() {
		var printerID, name, model, ipAddress, apiKey, printerType string
		var toolheads int
		var isVirtual bool
		if err := rows.Scan(&printerID, &name, &model, &ipAddress, &apiKey, &toolheads, &isVirtual, &printerType); err != nil {
			return nil, fmt.Errorf("failed to scan printer config row: %w", err)
		}
		configs[printerID] = PrinterConfig{
			Name:        name,
			Model:       model,
			IPAddress:   ipAddress,
			APIKey:      apiKey,
			Toolheads:   toolheads,
			IsVirtual:   isVirtual,
			PrinterType: printerType,
		}
	}

	return configs, nil
}

// SavePrinterConfig saves a printer configuration
func (b *FilamentBridge) SavePrinterConfig(printerID string, config PrinterConfig) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	isVirtualInt := 0
	if config.IsVirtual {
		isVirtualInt = 1
	}
	printerType := config.PrinterType
	if printerType == "" {
		printerType = PrinterTypePrusaLink
	}
	_, err := b.db.Exec(`
		INSERT OR REPLACE INTO printer_configs (printer_id, name, model, ip_address, api_key, toolheads, is_virtual, printer_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, printerID, config.Name, config.Model, config.IPAddress, config.APIKey, config.Toolheads, isVirtualInt, printerType)
	if err != nil {
		return fmt.Errorf("failed to save printer config: %w", err)
	}
	return nil
}

// DeletePrinterConfig deletes a printer and all its associated data:
// toolhead_mappings (frees spools for re-assignment) and toolhead_names.
func (b *FilamentBridge) DeletePrinterConfig(printerID string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// Look up the printer name before deleting — mappings are keyed by name
	var printerName string
	err := b.db.QueryRow("SELECT name FROM printer_configs WHERE printer_id = ?", printerID).Scan(&printerName)
	if err != nil {
		return fmt.Errorf("printer %s not found: %w", printerID, err)
	}

	// Remove toolhead spool assignments so those spools become assignable again
	_, _ = b.db.Exec("DELETE FROM toolhead_mappings WHERE printer_name = ?", printerName)

	// Remove toolhead display names
	_, _ = b.db.Exec("DELETE FROM toolhead_names WHERE printer_id = ?", printerID)

	// Delete the printer itself (ON DELETE CASCADE removes virtual_printer_files)
	_, err = b.db.Exec("DELETE FROM printer_configs WHERE printer_id = ?", printerID)
	if err != nil {
		return fmt.Errorf("failed to delete printer config: %w", err)
	}

	log.Printf("🗑️  Deleted printer %s (%s) and freed all toolhead spool assignments", printerName, printerID)
	return nil
}

// GetToolheadName gets the display name for a toolhead, or returns default "Toolhead {ID}"
func (b *FilamentBridge) GetToolheadName(printerID string, toolheadID int) (string, error) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	var displayName string
	err := b.db.QueryRow(
		"SELECT display_name FROM toolhead_names WHERE printer_id = ? AND toolhead_id = ?",
		printerID, toolheadID,
	).Scan(&displayName)

	if err == sql.ErrNoRows {
		// Return default name if not found
		return fmt.Sprintf("Toolhead %d", toolheadID), nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get toolhead name: %w", err)
	}

	return displayName, nil
}

// SetToolheadName sets the display name for a toolhead
func (b *FilamentBridge) SetToolheadName(printerID string, toolheadID int, name string) error {
	// Validate name is not empty
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("toolhead name cannot be empty")
	}

	// Get printer config to find printer name (before acquiring lock)
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return fmt.Errorf("failed to get printer configs: %w", err)
	}

	printerConfig, exists := printerConfigs[printerID]
	if !exists {
		return fmt.Errorf("printer %s not found", printerID)
	}

	printerName := printerConfig.Name

	// Get old toolhead name to calculate old location name (before acquiring lock)
	var oldDisplayName string
	oldName, err := b.GetToolheadName(printerID, toolheadID)
	if err == nil {
		oldDisplayName = oldName
	} else {
		oldDisplayName = fmt.Sprintf("Toolhead %d", toolheadID)
	}

	oldLocationName := fmt.Sprintf("%s - %s", printerName, oldDisplayName)
	newLocationName := fmt.Sprintf("%s - %s", printerName, name)

	// Update toolhead name in database
	b.mutex.Lock()
	_, err = b.db.Exec(
		"INSERT OR REPLACE INTO toolhead_names (printer_id, toolhead_id, display_name) VALUES (?, ?, ?)",
		printerID, toolheadID, name,
	)
	b.mutex.Unlock()

	if err != nil {
		return fmt.Errorf("failed to set toolhead name: %w", err)
	}

	// If location name changed, update Spoolman (outside of lock)
	if oldLocationName != newLocationName {
		// Get all spools from Spoolman
		spools, err := b.spoolman.GetAllSpools()
		if err != nil {
			log.Printf("Warning: Failed to get spools from Spoolman to update location names: %v", err)
		} else {
			// Find spools with the old location name and update them
			updatedCount := 0
			for _, spool := range spools {
				if spool.Location == oldLocationName {
					if err := b.spoolman.UpdateSpoolLocation(spool.ID, newLocationName); err != nil {
						log.Printf("Warning: Failed to update spool %d location from '%s' to '%s': %v", spool.ID, oldLocationName, newLocationName, err)
					} else {
						updatedCount++
					}
				}
			}

			// Ensure the new location exists in Spoolman
			if _, err := b.spoolman.GetOrCreateLocation(newLocationName); err != nil {
				log.Printf("Warning: Failed to create/verify location '%s' in Spoolman: %v", newLocationName, err)
			}

			if updatedCount > 0 {
				log.Printf("Updated %d spool(s) location from '%s' to '%s'", updatedCount, oldLocationName, newLocationName)
			}
		}
	}

	log.Printf("Set toolhead name for printer %s, toolhead %d: %s", printerID, toolheadID, name)
	return nil
}

// GetAllToolheadNames gets all toolhead display names for a printer
func (b *FilamentBridge) GetAllToolheadNames(printerID string) (map[int]string, error) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	rows, err := b.db.Query(
		"SELECT toolhead_id, display_name FROM toolhead_names WHERE printer_id = ? ORDER BY toolhead_id",
		printerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get toolhead names: %w", err)
	}
	defer rows.Close()

	names := make(map[int]string)
	for rows.Next() {
		var toolheadID int
		var displayName string
		if err := rows.Scan(&toolheadID, &displayName); err != nil {
			return nil, fmt.Errorf("failed to scan toolhead name row: %w", err)
		}
		names[toolheadID] = displayName
	}

	return names, nil
}

// GetConfigSnapshot returns a snapshot of the current config for safe iteration
func (b *FilamentBridge) GetConfigSnapshot() *Config {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	// Return a copy of the config to prevent iteration issues during updates
	if b.config == nil {
		return nil
	}

	// Create a shallow copy of the config
	configCopy := &Config{
		SpoolmanURL:                  b.config.SpoolmanURL,
		PollInterval:                 b.config.PollInterval,
		DBFile:                       b.config.DBFile,
		WebPort:                      b.config.WebPort,
		PrusaLinkTimeout:             b.config.PrusaLinkTimeout,
		PrusaLinkFileDownloadTimeout: b.config.PrusaLinkFileDownloadTimeout,
		SpoolmanTimeout:              b.config.SpoolmanTimeout,
		Printers:                     make(map[string]PrinterConfig),
	}

	// Copy printer configs
	for id, printer := range b.config.Printers {
		configCopy.Printers[id] = printer
	}

	return configCopy
}

// ReloadConfig reloads the configuration from the database
func (b *FilamentBridge) ReloadConfig() error {
	// Load config outside the lock to minimize lock time
	config, err := LoadConfig(b)
	if err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}

	// Only lock briefly to swap the config pointer and recreate SpoolmanClient
	b.mutex.Lock()
	b.config = config
	if config.SpoolmanURL != "" {
		b.spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout, config.SpoolmanUsername, config.SpoolmanPassword)
	}
	b.mutex.Unlock()

	return nil
}

// IsFirstRun checks if this is the first time the application is running
func (b *FilamentBridge) IsFirstRun() (bool, error) {
	var count int
	err := b.db.QueryRow("SELECT COUNT(*) FROM printer_configs").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check first run status: %w", err)
	}

	// If no printers are configured, this is a first run
	return count == 0, nil
}

// UpdateConfig updates the bridge configuration
func (b *FilamentBridge) UpdateConfig(config *Config) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.config = config
	b.spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout, config.SpoolmanUsername, config.SpoolmanPassword)

	return nil
}

// GetToolheadMapping gets spool ID mapped to a specific toolhead
func (b *FilamentBridge) GetToolheadMapping(printerName string, toolheadID int) (int, error) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	var spoolID int
	err := b.db.QueryRow(
		"SELECT spool_id FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ?",
		printerName, toolheadID,
	).Scan(&spoolID)

	if err == sql.ErrNoRows {
		return 0, nil // No mapping found
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get toolhead mapping: %w", err)
	}

	return spoolID, nil
}

// SetToolheadMapping maps a spool to a specific toolhead
func (b *FilamentBridge) SetToolheadMapping(printerName string, toolheadID int, spoolID int) error {
	b.mutex.Lock()

	// Get the previous spool ID before replacing it (for auto-assignment feature)
	var previousSpoolID int
	err := b.db.QueryRow(
		"SELECT spool_id FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ?",
		printerName, toolheadID,
	).Scan(&previousSpoolID)
	if err != nil && err != sql.ErrNoRows {
		b.mutex.Unlock()
		return fmt.Errorf("failed to get previous spool mapping: %w", err)
	}
	// If no previous mapping exists, previousSpoolID will be 0

	// Check if this spool is already assigned to a different toolhead
	rows, err := b.db.Query(
		"SELECT printer_name, toolhead_id FROM toolhead_mappings WHERE spool_id = ? AND NOT (printer_name = ? AND toolhead_id = ?)",
		spoolID, printerName, toolheadID,
	)
	if err != nil {
		b.mutex.Unlock()
		return fmt.Errorf("failed to check existing spool assignments: %w", err)
	}
	defer rows.Close()

	// If we find any rows, this spool is already assigned elsewhere
	if rows.Next() {
		var existingPrinterName string
		var existingToolheadID int
		if err := rows.Scan(&existingPrinterName, &existingToolheadID); err != nil {
			b.mutex.Unlock()
			return fmt.Errorf("failed to scan existing assignment: %w", err)
		}
		b.mutex.Unlock()
		return fmt.Errorf("spool %d is already assigned to %s toolhead %d", spoolID, existingPrinterName, existingToolheadID)
	}

	_, err = b.db.Exec(
		"INSERT OR REPLACE INTO toolhead_mappings (printer_name, toolhead_id, spool_id, mapped_at) VALUES (?, ?, ?, ?)",
		printerName, toolheadID, spoolID, time.Now(),
	)
	if err != nil {
		b.mutex.Unlock()
		return fmt.Errorf("failed to set toolhead mapping: %w", err)
	}

	log.Printf("Mapped %s toolhead %d to spool %d", printerName, toolheadID, spoolID)

	// Check if auto-assign feature is enabled and we have a previous spool to assign
	enabled, err := b.GetAutoAssignPreviousSpoolEnabled()
	if err != nil {
		log.Printf("Warning: Failed to check auto-assign previous spool setting: %v", err)
		b.mutex.Unlock()
		return nil // Don't fail the assignment if we can't check the setting
	}

	// Unlock before potentially calling AssignSpoolToLocation (which may need locks)
	b.mutex.Unlock()

	if enabled && previousSpoolID > 0 && previousSpoolID != spoolID {
		// Get the configured default location
		locationName, err := b.GetAutoAssignPreviousSpoolLocation()
		if err != nil {
			log.Printf("Warning: Failed to get auto-assign previous spool location setting: %v", err)
			return nil // Don't fail the assignment
		}

		if locationName != "" {
			// Verify the location exists in Spoolman
			location, err := b.spoolman.FindLocationByName(locationName)
			if err != nil || location == nil {
				log.Printf("Warning: Auto-assign previous spool location '%s' does not exist, skipping auto-assignment of spool %d", locationName, previousSpoolID)
				return nil // Don't fail the assignment
			}

			// Assign the previous spool to the default location
			// Use isPrinterLocation = false since this is a storage location
			if err := b.AssignSpoolToLocation(previousSpoolID, "", 0, locationName, false); err != nil {
				log.Printf("Warning: Failed to auto-assign previous spool %d to location '%s': %v", previousSpoolID, locationName, err)
				// Don't fail the original assignment if auto-assignment fails
			} else {
				log.Printf("Auto-assigned previous spool %d to location '%s'", previousSpoolID, locationName)
			}
		}
	}

	return nil
}

// GetToolheadMappings gets all toolhead mappings for a printer
func (b *FilamentBridge) GetToolheadMappings(printerName string) (map[int]ToolheadMapping, error) {
	rows, err := b.db.Query(
		"SELECT toolhead_id, spool_id, mapped_at FROM toolhead_mappings WHERE printer_name = ?",
		printerName,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mappings := make(map[int]ToolheadMapping)
	for rows.Next() {
		var toolheadID, spoolID int
		var mappedAt time.Time
		if err := rows.Scan(&toolheadID, &spoolID, &mappedAt); err != nil {
			return nil, err
		}
		mappings[toolheadID] = ToolheadMapping{
			PrinterName: printerName,
			ToolheadID:  toolheadID,
			SpoolID:     spoolID,
			MappedAt:    mappedAt,
		}
	}

	return mappings, nil
}

// GetAllToolheadMappings gets all toolhead mappings across all printers
func (b *FilamentBridge) GetAllToolheadMappings() (map[string]map[int]ToolheadMapping, error) {
	rows, err := b.db.Query(
		"SELECT printer_name, toolhead_id, spool_id, mapped_at FROM toolhead_mappings ORDER BY printer_name, toolhead_id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mappings := make(map[string]map[int]ToolheadMapping)
	for rows.Next() {
		var printerName string
		var toolheadID, spoolID int
		var mappedAt time.Time
		if err := rows.Scan(&printerName, &toolheadID, &spoolID, &mappedAt); err != nil {
			return nil, err
		}

		if mappings[printerName] == nil {
			mappings[printerName] = make(map[int]ToolheadMapping)
		}

		mappings[printerName][toolheadID] = ToolheadMapping{
			PrinterName: printerName,
			ToolheadID:  toolheadID,
			SpoolID:     spoolID,
			MappedAt:    mappedAt,
		}
	}

	return mappings, nil
}

// UnmapToolhead removes a spool mapping from a toolhead
func (b *FilamentBridge) UnmapToolhead(printerName string, toolheadID int) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(
		"DELETE FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ?",
		printerName, toolheadID,
	)
	if err != nil {
		return fmt.Errorf("failed to unmap toolhead: %w", err)
	}

	log.Printf("Unmapped %s toolhead %d", printerName, toolheadID)
	return nil
}

// LogPrintUsage logs filament usage for a print job.
// printTimeMinutes and thumbnailBase64 are optional — pass 0 and "" if unavailable.
// sessionID groups rows from the same physical print; pass newSessionID() at the
// call site so all toolheads in one print share the same ID.
func (b *FilamentBridge) LogPrintUsage(printerName string, toolheadID int, spoolID int, filamentUsed float64, jobName string, sessionID string) error {
	return b.LogPrintUsageFull(printerName, toolheadID, spoolID, filamentUsed, jobName, 0, "completed", "", sessionID, "prusalink")
}

// LogPrintUsageFull is the full version with print time, status, thumbnail, session, and source.
// source should be "prusalink", "virtual", or "octoprint".
// Cost is automatically calculated and saved after the insert (outside the mutex).
func (b *FilamentBridge) LogPrintUsageFull(printerName string, toolheadID int, spoolID int,
	filamentUsed float64, jobName string, printTimeMinutes float64, status string, thumbnailBase64 string,
	sessionID string, source string) error {
	b.mutex.Lock()

	if status == "" {
		status = "completed"
	}
	if sessionID == "" {
		sessionID = newSessionID()
	}
	if source == "" {
		source = "prusalink"
	}

	printStarted := time.Now()
	if storedJobFile, exists := b.currentJobFile[printerName]; exists && storedJobFile != "" {
		_ = storedJobFile
		if printTimeMinutes > 0 {
			printStarted = time.Now().Add(-time.Duration(printTimeMinutes) * time.Minute)
		} else {
			printStarted = time.Now().Add(-time.Hour)
		}
	}

	res, err := b.db.Exec(`
		INSERT INTO print_history
			(printer_name, toolhead_id, spool_id, filament_used,
			 print_started, print_finished, job_name,
			 print_time_minutes, status, thumbnail_path, session_id,
			 source, time_precision, filament_precision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'approximate', 'estimated')`,
		printerName, toolheadID, spoolID, filamentUsed,
		printStarted, time.Now(), jobName,
		printTimeMinutes, status, thumbnailBase64, sessionID, source,
	)
	if err != nil {
		b.mutex.Unlock()
		return fmt.Errorf("failed to log print usage: %w", err)
	}
	printID64, _ := res.LastInsertId()
	log.Printf("📋 Logged print history: %s on %s (%.2fg, %.0fmin)",
		jobName, printerName, filamentUsed, printTimeMinutes)
	b.mutex.Unlock() // release before Spoolman network call

	// Auto-calculate and store cost (best-effort — never fails the log).
	if printID64 > 0 && filamentUsed > 0 {
		if bd, calcErr := b.CalculatePrintCostForPrinter(filamentUsed, printTimeMinutes, spoolID, printerName); calcErr == nil {
			if saveErr := b.SavePrintCost(int(printID64), bd); saveErr != nil {
				log.Printf("Warning: cost save failed for print %d: %v", printID64, saveErr)
			}
		}
	}

	return nil
}

// MonitorPrinters monitors all printers for print status changes
func (b *FilamentBridge) MonitorPrinters() {
	log.Printf("Monitoring printers at %s", time.Now().Format(time.RFC3339))

	// Get a safe snapshot of the config to prevent iteration issues
	configSnapshot := b.GetConfigSnapshot()
	if configSnapshot == nil || len(configSnapshot.Printers) == 0 {
		log.Printf("No printers configured - skipping monitoring")
		return
	}

	// Monitor each PrusaLink printer (OctoPrint printers use push — skip them here)
	for printerID, printerConfig := range configSnapshot.Printers {
		if printerID == "no_printers" {
			continue // Skip placeholder
		}
		if printerConfig.PrinterType == PrinterTypeOctoPrint {
			continue // OctoPrint pushes data; no polling needed
		}
		go func(printerID string, config PrinterConfig) {
			b.mutex.Lock()
			if b.monitoringActive[printerID] {
				b.mutex.Unlock()
				log.Printf("Skipping poll for printer %s (%s): previous cycle still running", printerID, config.Name)
				return
			}
			b.monitoringActive[printerID] = true
			b.mutex.Unlock()
			defer func() {
				b.mutex.Lock()
				b.monitoringActive[printerID] = false
				b.mutex.Unlock()
			}()

			if err := b.monitorPrusaLink(printerID, config); err != nil {
				log.Printf("Error monitoring printer %s (%s): %v", config.IPAddress, printerID, err)
			}
		}(printerID, printerConfig)
	}
}

// monitorPrusaLink monitors a single printer using PrusaLink API
// monitorPrusaLink polls a single printer via PrusaLink API and handles all state transitions.
//
// State machine:
//
//	PRINTING          → track wasPrinting=true, store job filename
//	PAUSED            → keep wasPrinting=true (print will resume)
//	ATTENTION         → keep wasPrinting=true (filament runout — user swaps spool, then resumes)
//	IDLE / FINISHED   → if wasPrinting: print completed normally → process full G-code usage
//	STOPPED           → if wasPrinting: job was cancelled → log partial usage from progress %
func (b *FilamentBridge) monitorPrusaLink(printerID string, config PrinterConfig) error {
	log.Printf("Starting monitoring for printer %s (%s) at %s", printerID, config.IPAddress, config.Name)
	client := NewPrusaLinkClient(config.IPAddress, config.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)

	status, err := client.GetStatus()
	if err != nil {
		log.Printf("Warning: Failed to get printer status from %s (%s): %v", config.IPAddress, printerID, err)
		return nil
	}

	jobInfo, err := client.GetJobInfo()
	if err != nil {
		log.Printf("Warning: Failed to get job info from %s (%s): %v", config.IPAddress, printerID, err)
		jobInfo = &PrusaLinkJob{}
	}

	currentState := status.Printer.State
	jobName := "No active job"
	currentJobFilename := ""
	printProgress := jobInfo.Progress // 0..100

	if jobInfo.File.Name != "" {
		jobName = jobInfo.File.DisplayName
		if jobInfo.File.Refs.Download != "" {
			currentJobFilename = strings.TrimPrefix(jobInfo.File.Refs.Download, "/")
		} else {
			storage := strings.TrimPrefix(jobInfo.File.Path, "/")
			currentJobFilename = storage + "/" + jobInfo.File.Name
		}
	}

	// Read current tracking state under read lock
	b.mutex.RLock()
	wasPrinting := b.wasPrinting[printerID]
	storedJobFile := b.currentJobFile[printerID]
	prevState := b.previousState[printerID]
	b.mutex.RUnlock()

	log.Printf("Printer %s (%s): state=%s (prev=%s), wasPrinting=%v, job=%s, file=%s",
		config.IPAddress, printerID, currentState, prevState, wasPrinting, jobName, storedJobFile)

	switch currentState {

	case StatePrinting:
		// Print is active — store the filename on first detection, keep wasPrinting=true
		b.mutex.Lock()
		if currentJobFilename != "" && storedJobFile == "" {
			b.currentJobFile[printerID] = currentJobFilename
			log.Printf("📁 Stored job filename for %s (%s): %s", config.IPAddress, printerID, currentJobFilename)
		}
		b.wasPrinting[printerID] = true
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

	case StatePaused:
		// Print is paused by user — keep wasPrinting=true so we don't lose the transition
		if prevState != StatePaused {
			log.Printf("⏸️  Print paused on %s (%s): %s", config.IPAddress, printerID, jobName)
		}
		b.mutex.Lock()
		b.wasPrinting[printerID] = wasPrinting // preserve existing flag
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

	case StateAttention:
		// Filament runout or change required — keep wasPrinting=true so resume works
		if prevState != StateAttention {
			log.Printf("🔴 ATTENTION on %s (%s): filament runout or change required for job: %s",
				config.IPAddress, printerID, jobName)
		}
		b.mutex.Lock()
		b.wasPrinting[printerID] = wasPrinting // preserve existing flag
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

	case StateFinished, StateIdle:
		if wasPrinting {
			// Normal print completion
			filenameToUse := storedJobFile
			if filenameToUse == "" {
				log.Printf("Warning: No stored filename for %s (%s), using current: %s",
					config.IPAddress, printerID, currentJobFilename)
				filenameToUse = currentJobFilename
			}

			log.Printf("🎉 Print finished on %s (%s): %s (file: %s)",
				config.IPAddress, printerID, jobName, filenameToUse)

			b.mutex.Lock()
			b.wasPrinting[printerID] = false
			b.processingPrints[printerID] = true
			b.previousState[printerID] = currentState
			b.mutex.Unlock()

			err := b.handlePrusaLinkPrintFinished(config, filenameToUse)

			b.mutex.Lock()
			b.processingPrints[printerID] = false
			if err == nil {
				b.currentJobFile[printerID] = ""
			}
			b.mutex.Unlock()

			if err != nil {
				log.Printf("Error handling print finished: %v", err)
			}
		} else {
			// Normal idle — clear any stale tracking
			b.mutex.Lock()
			if !b.processingPrints[printerID] {
				b.currentJobFile[printerID] = ""
			}
			b.previousState[printerID] = currentState
			b.mutex.Unlock()
		}

	case StateStopped:
		if wasPrinting {
			// Print was cancelled — attempt partial usage from progress percentage
			filenameToUse := storedJobFile
			if filenameToUse == "" {
				filenameToUse = currentJobFilename
			}

			log.Printf("🛑 Print CANCELLED on %s (%s): %s (progress: %.1f%%, file: %s)",
				config.IPAddress, printerID, jobName, printProgress, filenameToUse)

			b.mutex.Lock()
			b.wasPrinting[printerID] = false
			b.processingPrints[printerID] = true
			b.previousState[printerID] = currentState
			b.mutex.Unlock()

			err := b.handlePrusaLinkPrintCancelled(config, filenameToUse, printProgress)

			b.mutex.Lock()
			b.processingPrints[printerID] = false
			b.currentJobFile[printerID] = ""
			b.mutex.Unlock()

			if err != nil {
				log.Printf("Error handling cancelled print: %v", err)
			}
		} else {
			b.mutex.Lock()
			b.previousState[printerID] = currentState
			b.mutex.Unlock()
		}

	default:
		// Unknown state — log and do nothing to avoid losing tracking
		log.Printf("Unknown printer state '%s' on %s (%s)", currentState, config.IPAddress, printerID)
		b.mutex.Lock()
		b.previousState[printerID] = currentState
		b.mutex.Unlock()
	}

	return nil
}

// handlePrusaLinkPrintCancelled handles a cancelled print by computing partial filament usage.
// It downloads the G-code, gets the full usage from metadata, then scales by the print progress %.
func (b *FilamentBridge) handlePrusaLinkPrintCancelled(config PrinterConfig, filename string, progressPct float64) error {
	printerName := resolvePrinterName(config)

	if filename == "" {
		msg := "no filename available for cancelled print processing"
		b.addPrintError(printerName, "unknown", msg)
		return fmt.Errorf("%s", msg)
	}

	if progressPct <= 0 {
		log.Printf("⚠️  Cancelled print at 0%% progress on %s — skipping filament deduction", printerName)
		return nil
	}

	// Scale factor: 0..100 → 0.0..1.0
	// Apply a small safety margin (0.95) so we don't over-deduct
	scale := (progressPct / 100.0) * 0.95
	if scale > 1.0 {
		scale = 1.0
	}

	prusaClient := NewPrusaLinkClient(config.IPAddress, config.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)

	gcodeContent, err := prusaClient.GetGcodeFileWithRetry(filename, b.config.PrusaLinkFileDownloadTimeout)
	if err != nil {
		log.Printf("⚠️  G-code download failed for cancelled print %s (%s), queuing for retry: %v", printerName, filename, err)
		if qErr := b.enqueuePendingGcodeDownload(printerName, config.IPAddress, filename, "cancelled", progressPct); qErr != nil {
			msg := fmt.Sprintf("G-code download failed for cancelled print and could not be queued for retry: %v (original error: %v)", qErr, err)
			b.addPrintError(printerName, filename, msg)
			return fmt.Errorf("%s", msg)
		}
		return nil // queued
	}

	fullUsage, err := prusaClient.ParseGcodeFilamentUsage(gcodeContent)
	if err != nil || len(fullUsage) == 0 {
		log.Printf("⚠️  Could not parse G-code for cancelled print on %s — skipping filament deduction", printerName)
		return nil
	}

	// Scale down by progress percentage
	partialUsage := make(map[int]float64)
	for toolheadID, weight := range fullUsage {
		partial := weight * scale
		if partial > 0 {
			partialUsage[toolheadID] = partial
			log.Printf("📉 Cancelled print partial usage: toolhead %d → %.2fg (%.1f%% of %.2fg)",
				toolheadID, partial, progressPct, weight)
		}
	}

	if err := b.processFilamentUsage(printerName, partialUsage, filename+" [CANCELLED]"); err != nil {
		return err
	}

	_, thumbnailB64 := ParseGcodeMetadata(gcodeContent)
	sessionID := newSessionID()
	for toolheadID, usedG := range partialUsage {
		spoolID, _ := b.GetToolheadMapping(printerName, toolheadID)
		_ = b.LogPrintUsageFull(printerName, toolheadID, spoolID, usedG, filename+" [CANCELLED]",
			0, "cancelled", thumbnailB64, sessionID, "prusalink")
	}

	return nil
}

func (b *FilamentBridge) handlePrusaLinkPrintFinished(config PrinterConfig, filename string) error {
	log.Printf("Print finished via PrusaLink (%s): %s", config.IPAddress, filename)

	printerName := resolvePrinterName(config)

	// Create PrusaLink client for this printer
	prusaClient := NewPrusaLinkClient(config.IPAddress, config.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)

	// Use the filename parameter (stored when print started)
	if filename == "" {
		errorMsg := "no filename available for print processing"
		b.addPrintError(printerName, "unknown", errorMsg)
		return fmt.Errorf("%s", errorMsg)
	}

	// Download and parse the G-code file (.gcode or .bgcode) for filament usage
	log.Printf("Analyzing G-code file for filament usage: %s", filename)

	// Download with retry logic; queue for background retry on failure rather than
	// dropping the event — the file usually persists on the printer's USB storage.
	gcodeContent, err := prusaClient.GetGcodeFileWithRetry(filename, b.config.PrusaLinkFileDownloadTimeout)
	if err != nil {
		log.Printf("⚠️  G-code download failed for %s (%s), queuing for retry: %v", printerName, filename, err)
		if qErr := b.enqueuePendingGcodeDownload(printerName, config.IPAddress, filename, "completed", 0); qErr != nil {
			errorMsg := fmt.Sprintf("G-code download failed and could not be queued for retry: %v (original error: %v)", qErr, err)
			b.addPrintError(printerName, filename, errorMsg)
			return fmt.Errorf("%s", errorMsg)
		}
		return nil // queued — caller clears currentJobFile so state machine stays clean
	}

	// Parse the downloaded file
	filamentUsage, err := prusaClient.ParseGcodeFilamentUsage(gcodeContent)
	if err != nil {
		errorMsg := fmt.Sprintf("failed to parse G-code for filament usage: %v", err)
		b.addPrintError(printerName, filename, errorMsg)
		return fmt.Errorf("%s", errorMsg)
	}

	// Check if we got any filament usage data
	if len(filamentUsage) == 0 {
		errorMsg := "no filament usage data found in G-code file"
		b.addPrintError(printerName, filename, errorMsg)
		return fmt.Errorf("%s", errorMsg)
	}

	log.Printf("Successfully parsed G-code file for filament usage: %+v", filamentUsage)

	printTimeSec, thumbnailB64 := ParseGcodeMetadata(gcodeContent)
	printTimeMin := float64(printTimeSec) / 60.0

	if err := b.processFilamentUsage(printerName, filamentUsage, filename); err != nil {
		log.Printf("Error processing filament usage: %v", err)
		return err
	}

	sessionID := newSessionID()
	for toolheadID, usedG := range filamentUsage {
		spoolID, _ := b.GetToolheadMapping(printerName, toolheadID)
		_ = b.LogPrintUsageFull(printerName, toolheadID, spoolID, usedG, filename,
			printTimeMin, "completed", thumbnailB64, sessionID, "prusalink")
	}

	return nil
}

// GetPrintErrors returns all unacknowledged print errors
func (b *FilamentBridge) GetPrintErrors() []PrintError {
	b.errorMutex.RLock()
	defer b.errorMutex.RUnlock()

	var errors []PrintError
	for _, err := range b.printErrors {
		if !err.Acknowledged {
			errors = append(errors, err)
		}
	}
	return errors
}

// AcknowledgePrintError marks a print error as acknowledged
func (b *FilamentBridge) AcknowledgePrintError(errorID string) error {
	b.errorMutex.Lock()
	defer b.errorMutex.Unlock()

	if err, exists := b.printErrors[errorID]; exists {
		err.Acknowledged = true
		b.printErrors[errorID] = err
		return nil
	}
	return fmt.Errorf("print error not found: %s", errorID)
}

// sanitizeErrorID replaces problematic characters in error IDs to make them URL-safe
func sanitizeErrorID(s string) string {
	// Replace forward slashes with underscores
	s = strings.ReplaceAll(s, "/", "_")
	// Replace spaces with underscores
	s = strings.ReplaceAll(s, " ", "_")
	// Replace backslashes with underscores
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

// addPrintError adds a new print error
func (b *FilamentBridge) addPrintError(printerName, filename, errorMsg string) {
	b.errorMutex.Lock()
	defer b.errorMutex.Unlock()

	// Sanitize printer name and filename to ensure URL-safe error IDs
	sanitizedPrinterName := sanitizeErrorID(printerName)
	sanitizedFilename := sanitizeErrorID(filename)
	errorID := fmt.Sprintf("%s_%s_%d", sanitizedPrinterName, sanitizedFilename, time.Now().Unix())
	b.printErrors[errorID] = PrintError{
		ID:           errorID,
		PrinterName:  printerName,
		Filename:     filename,
		Error:        errorMsg,
		Timestamp:    time.Now(),
		Acknowledged: false,
	}

	log.Printf("⚠️  Print processing failed for %s (%s): %s - Manual Spoolman update required",
		printerName, filename, errorMsg)
}

// GetStatus gets current status of all printers and mappings
func (b *FilamentBridge) GetStatus() (*PrinterStatus, error) {
	status := &PrinterStatus{
		Printers:         make(map[string]PrinterData),
		ToolheadMappings: make(map[string]map[int]ToolheadMapping),
		Timestamp:        time.Now(),
	}

	// Get a safe snapshot of the config to prevent iteration issues
	configSnapshot := b.GetConfigSnapshot()
	if configSnapshot == nil {
		// No printers configured
		status.Printers["no_printers"] = PrinterData{
			Name:  "No Printers Configured",
			State: StateNotConfigured,
		}
		return status, nil
	}

	// Get printer statuses from PrusaLink
	if len(configSnapshot.Printers) > 0 {
		for printerID, printerConfig := range configSnapshot.Printers {
			if printerID == "no_printers" {
				continue // Skip placeholder
			}

			// Use the configured printer name, not the hostname from PrusaLink
			printerName := printerConfig.Name

			// Virtual printers have no hardware — show as ready without any API call
			if printerConfig.IsVirtual {
				status.Printers[printerID] = PrinterData{
					Name:  printerName,
					State: StateVirtual,
				}
				continue
			}

			// OctoPrint printers push data; no polling status available
			if printerConfig.PrinterType == PrinterTypeOctoPrint {
				status.Printers[printerID] = PrinterData{
					Name:  printerName,
					State: StateOctoPrint,
				}
				continue
			}

			client := NewPrusaLinkClient(printerConfig.IPAddress, printerConfig.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)

			// Get current status
			printerStatus, err := client.GetStatus()
			if err != nil {
				log.Printf("Warning: Failed to get printer status from %s (%s - %s): %v",
					printerConfig.IPAddress, printerID, printerName, err)
				status.Printers[printerID] = PrinterData{
					Name:  printerName,
					State: StateOffline,
				}
				continue
			}

			status.Printers[printerID] = PrinterData{
				Name:  printerName,
				State: printerStatus.Printer.State,
			}
		}
	} else {
		// No printers configured
		status.Printers["no_printers"] = PrinterData{
			Name:  "No Printers Configured",
			State: StateNotConfigured,
		}
	}

	// Get toolhead mappings for all printers
	for printerID, printerConfig := range configSnapshot.Printers {
		if printerID == "no_printers" {
			continue // Skip placeholder
		}

		printerName := printerConfig.Name
		mappings, err := b.GetToolheadMappings(printerName)
		if err != nil {
			log.Printf("Error getting toolhead mappings for %s: %v", printerName, err)
			mappings = make(map[int]ToolheadMapping)
		}

		// Get toolhead names for this printer
		toolheadNames, err := b.GetAllToolheadNames(printerID)
		if err != nil {
			log.Printf("Warning: Failed to get toolhead names for printer %s: %v", printerID, err)
			toolheadNames = make(map[int]string)
		}

		// Create enhanced mappings for ALL toolheads (including unmapped ones)
		enhancedMappings := make(map[int]ToolheadMapping)
		for toolheadID := 0; toolheadID < printerConfig.Toolheads; toolheadID++ {
			// Get display name (custom or default)
			var displayName string
			if name, exists := toolheadNames[toolheadID]; exists {
				displayName = name
			} else {
				displayName = fmt.Sprintf("Toolhead %d", toolheadID)
			}

			// If this toolhead has a mapping, use it and add display name
			if mapping, exists := mappings[toolheadID]; exists {
				mapping.DisplayName = displayName
				enhancedMappings[toolheadID] = mapping
			} else {
				// Create empty mapping with just display name for unmapped toolheads
				enhancedMappings[toolheadID] = ToolheadMapping{
					PrinterName: printerName,
					ToolheadID:  toolheadID,
					SpoolID:     0, // No spool mapped
					DisplayName: displayName,
				}
			}
		}
		status.ToolheadMappings[printerID] = enhancedMappings
	}

	return status, nil
}

// processFilamentUsage processes filament usage updates for all toolheads.
// Local history is always written first so no print event is silently dropped.
// If Spoolman is unreachable the update is queued in pending_spoolman_updates
// and retried by RetryPendingSpoolmanUpdates on the next ticker tick.
func (b *FilamentBridge) processFilamentUsage(printerName string, filamentUsage map[int]float64, jobName string) error {
	for toolheadID, usedWeight := range filamentUsage {
		if usedWeight <= 0 {
			continue
		}

		spoolID, err := b.GetToolheadMapping(printerName, toolheadID)
		if err != nil {
			log.Printf("Error getting toolhead mapping for %s toolhead %d: %v",
				printerName, toolheadID, err)
			continue
		}
		if spoolID == 0 {
			log.Printf("No spool mapped to %s toolhead %d, skipping filament usage update",
				printerName, toolheadID)
			continue
		}

		// Attempt Spoolman update; on failure queue for background retry.
		if err := b.spoolman.UpdateSpoolUsage(spoolID, usedWeight); err != nil {
			log.Printf("⚠️  Spoolman update failed for spool %d — queuing for retry: %v", spoolID, err)
			if qErr := b.enqueuePendingSpoolmanUpdate(printerName, toolheadID, spoolID, usedWeight, jobName); qErr != nil {
				log.Printf("Error queuing pending Spoolman update: %v", qErr)
				b.addPrintError(printerName, jobName,
					fmt.Sprintf("Spoolman update failed for spool %d and could not be queued for retry: %v", spoolID, err))
			}
			continue
		}

		log.Printf("Updated spool %d: used %.2fg filament on %s toolhead %d",
			spoolID, usedWeight, printerName, toolheadID)
	}

	if len(filamentUsage) > 0 {
		log.Printf("✅ Print completion processing finished for %s: processed %d toolheads", printerName, len(filamentUsage))
	} else {
		log.Printf("⚠️  No filament usage data processed for %s", printerName)
	}

	return nil
}

// enqueuePendingSpoolmanUpdate stores a Spoolman usage update in the local outbox
// for later retry. Called when UpdateSpoolUsage fails (e.g. Spoolman offline).
func (b *FilamentBridge) enqueuePendingSpoolmanUpdate(printerName string, toolheadID, spoolID int, usedWeight float64, jobName string) error {
	_, err := b.db.Exec(`
		INSERT INTO pending_spoolman_updates
			(printer_name, toolhead_id, spool_id, used_weight, job_name)
		VALUES (?, ?, ?, ?, ?)`,
		printerName, toolheadID, spoolID, usedWeight, jobName,
	)
	if err != nil {
		return fmt.Errorf("failed to queue pending Spoolman update: %w", err)
	}
	log.Printf("📋 Queued pending Spoolman update: spool %d, %.2fg (%s toolhead %d)", spoolID, usedWeight, printerName, toolheadID)
	return nil
}

// RetryPendingSpoolmanUpdates drains the outbox: retries every queued Spoolman
// usage update, deleting each record on success. Intended to be called on a
// regular ticker (e.g. every 5 minutes) from the main monitoring loop.
func (b *FilamentBridge) RetryPendingSpoolmanUpdates() error {
	rows, err := b.db.Query(`
		SELECT id, printer_name, toolhead_id, spool_id, used_weight, job_name
		FROM pending_spoolman_updates
		ORDER BY created_at ASC`)
	if err != nil {
		return fmt.Errorf("failed to query pending Spoolman updates: %w", err)
	}

	type pendingUpdate struct {
		id          int
		printerName string
		toolheadID  int
		spoolID     int
		usedWeight  float64
		jobName     string
	}
	var updates []pendingUpdate
	for rows.Next() {
		var u pendingUpdate
		if err := rows.Scan(&u.id, &u.printerName, &u.toolheadID, &u.spoolID, &u.usedWeight, &u.jobName); err != nil {
			log.Printf("Warning: failed to scan pending update row: %v", err)
			continue
		}
		updates = append(updates, u)
	}
	rows.Close()

	if len(updates) == 0 {
		return nil
	}

	log.Printf("Retrying %d pending Spoolman update(s)...", len(updates))
	successCount := 0
	for _, u := range updates {
		if err := b.spoolman.UpdateSpoolUsage(u.spoolID, u.usedWeight); err != nil {
			_, _ = b.db.Exec(`
				UPDATE pending_spoolman_updates
				SET last_attempt = CURRENT_TIMESTAMP,
				    attempts     = attempts + 1,
				    last_error   = ?
				WHERE id = ?`, err.Error(), u.id)
			log.Printf("⚠️  Retry failed for spool %d (%.2fg): %v", u.spoolID, u.usedWeight, err)
			continue
		}
		if _, delErr := b.db.Exec(`DELETE FROM pending_spoolman_updates WHERE id = ?`, u.id); delErr != nil {
			log.Printf("Warning: failed to remove completed pending update %d: %v", u.id, delErr)
		}
		successCount++
		log.Printf("✅ Retried Spoolman update: spool %d, %.2fg (%s toolhead %d)",
			u.spoolID, u.usedWeight, u.printerName, u.toolheadID)
	}

	if successCount > 0 {
		log.Printf("✅ Retry complete: %d/%d pending Spoolman update(s) applied", successCount, len(updates))
	}
	return nil
}

// GetPendingSpoolmanUpdateCount returns how many Spoolman updates are queued.
func (b *FilamentBridge) GetPendingSpoolmanUpdateCount() int {
	var count int
	if err := b.db.QueryRow(`SELECT COUNT(*) FROM pending_spoolman_updates`).Scan(&count); err != nil {
		return 0
	}
	return count
}

// enqueuePendingGcodeDownload stores a failed G-code download in the local retry
// queue. Called when GetGcodeFileWithRetry exhausts all attempts so the event
// is not silently dropped.
func (b *FilamentBridge) enqueuePendingGcodeDownload(printerName, printerIP, filename, jobType string, progressPct float64) error {
	_, err := b.db.Exec(`
		INSERT INTO pending_gcode_downloads
			(printer_name, printer_ip, filename, job_type, progress_pct)
		VALUES (?, ?, ?, ?, ?)`,
		printerName, printerIP, filename, jobType, progressPct,
	)
	if err != nil {
		return fmt.Errorf("failed to queue pending G-code download: %w", err)
	}
	log.Printf("📋 Queued pending G-code download: %s (%s, %s)", filename, printerName, jobType)
	return nil
}

// RetryPendingGcodeDownloads re-attempts every queued G-code download, processes
// filament usage on success, and removes the record. A record is permanently
// removed (with an error surfaced) if the printer is no longer configured or if
// the file parses with no usage data — both are unrecoverable conditions.
func (b *FilamentBridge) RetryPendingGcodeDownloads() error {
	rows, err := b.db.Query(`
		SELECT id, printer_name, printer_ip, filename, job_type, progress_pct
		FROM pending_gcode_downloads
		ORDER BY created_at ASC`)
	if err != nil {
		return fmt.Errorf("failed to query pending G-code downloads: %w", err)
	}

	type pendingDownload struct {
		id          int
		printerName string
		printerIP   string
		filename    string
		jobType     string
		progressPct float64
	}
	var downloads []pendingDownload
	for rows.Next() {
		var d pendingDownload
		if err := rows.Scan(&d.id, &d.printerName, &d.printerIP, &d.filename, &d.jobType, &d.progressPct); err != nil {
			log.Printf("Warning: failed to scan pending G-code download row: %v", err)
			continue
		}
		downloads = append(downloads, d)
	}
	rows.Close()

	if len(downloads) == 0 {
		return nil
	}

	allConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return fmt.Errorf("failed to get printer configs for G-code retry: %w", err)
	}

	log.Printf("Retrying %d pending G-code download(s)...", len(downloads))
	successCount := 0

	for _, d := range downloads {
		// Resolve current config by IP so we pick up any API key rotation.
		var cfg PrinterConfig
		found := false
		for _, c := range allConfigs {
			if c.IPAddress == d.printerIP {
				cfg = c
				found = true
				break
			}
		}
		if !found {
			// Printer removed from config — unrecoverable, surface error and drop.
			msg := fmt.Sprintf("printer at %s no longer configured; manual Spoolman update required for %s", d.printerIP, d.filename)
			log.Printf("⚠️  G-code retry: %s", msg)
			b.addPrintError(d.printerName, d.filename, msg)
			_, _ = b.db.Exec(`DELETE FROM pending_gcode_downloads WHERE id = ?`, d.id)
			continue
		}

		prusaClient := NewPrusaLinkClient(cfg.IPAddress, cfg.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)
		gcodeContent, err := prusaClient.GetGcodeFileWithRetry(d.filename, b.config.PrusaLinkFileDownloadTimeout)
		if err != nil {
			_, _ = b.db.Exec(`
				UPDATE pending_gcode_downloads
				SET last_attempt = CURRENT_TIMESTAMP,
				    attempts     = attempts + 1,
				    last_error   = ?
				WHERE id = ?`, err.Error(), d.id)
			log.Printf("⚠️  G-code retry failed for %s (%s): %v", d.printerName, d.filename, err)
			continue
		}

		filamentUsage, err := prusaClient.ParseGcodeFilamentUsage(gcodeContent)
		if err != nil || len(filamentUsage) == 0 {
			// Parse failure is permanent — remove and alert.
			msg := fmt.Sprintf("G-code retry downloaded %s but found no filament usage data; manual Spoolman update required", d.filename)
			log.Printf("⚠️  %s", msg)
			b.addPrintError(d.printerName, d.filename, msg)
			_, _ = b.db.Exec(`DELETE FROM pending_gcode_downloads WHERE id = ?`, d.id)
			continue
		}

		jobName := d.filename
		if d.jobType == "cancelled" {
			scale := (d.progressPct / 100.0) * 0.95
			if scale > 1.0 {
				scale = 1.0
			}
			partialUsage := make(map[int]float64)
			for toolheadID, weight := range filamentUsage {
				if partial := weight * scale; partial > 0 {
					partialUsage[toolheadID] = partial
				}
			}
			filamentUsage = partialUsage
			jobName = d.filename + " [CANCELLED]"
		}

		if err := b.processFilamentUsage(d.printerName, filamentUsage, jobName); err != nil {
			_, _ = b.db.Exec(`
				UPDATE pending_gcode_downloads
				SET last_attempt = CURRENT_TIMESTAMP,
				    attempts     = attempts + 1,
				    last_error   = ?
				WHERE id = ?`, err.Error(), d.id)
			log.Printf("⚠️  G-code retry: filament processing failed for %s: %v", d.printerName, err)
			continue
		}

		printTimeSec, thumbnailB64 := ParseGcodeMetadata(gcodeContent)
		printTimeMin := float64(printTimeSec) / 60.0
		status := "completed"
		if d.jobType == "cancelled" {
			status = "cancelled"
		}
		sessionID := newSessionID()
		for toolheadID, usedG := range filamentUsage {
			spoolID, _ := b.GetToolheadMapping(d.printerName, toolheadID)
			_ = b.LogPrintUsageFull(d.printerName, toolheadID, spoolID, usedG, jobName,
				printTimeMin, status, thumbnailB64, sessionID, "prusalink")
		}

		_, _ = b.db.Exec(`DELETE FROM pending_gcode_downloads WHERE id = ?`, d.id)
		successCount++
		log.Printf("✅ G-code retry succeeded: %s (%s %s)", d.filename, d.printerName, d.jobType)
	}

	if successCount > 0 {
		log.Printf("✅ G-code retry complete: %d/%d download(s) processed", successCount, len(downloads))
	}
	return nil
}

// GetPendingGcodeDownloadCount returns how many G-code downloads are queued for retry.
func (b *FilamentBridge) GetPendingGcodeDownloadCount() int {
	var count int
	if err := b.db.QueryRow(`SELECT COUNT(*) FROM pending_gcode_downloads`).Scan(&count); err != nil {
		return 0
	}
	return count
}

// isVirtualPrinterToolheadLocation checks if a location name matches the pattern
// of a virtual printer toolhead location (e.g., "PrinterName - Toolhead 0" or "PrinterName - Black")
func (b *FilamentBridge) isVirtualPrinterToolheadLocation(name string) bool {
	// Get all printer configurations
	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		// If we can't get printer configs, assume it's not a virtual location
		log.Printf("Warning: Could not get printer configurations to check virtual location: %v", err)
		return false
	}

	// Check if the name matches any printer's toolhead location pattern
	for printerID, printerConfig := range printerConfigs {
		// Get toolhead names for this printer
		toolheadNames, err := b.GetAllToolheadNames(printerID)
		if err != nil {
			log.Printf("Warning: Could not get toolhead names for printer %s: %v", printerID, err)
			toolheadNames = make(map[int]string)
		}

		for toolheadID := 0; toolheadID < printerConfig.Toolheads; toolheadID++ {
			// Check default pattern
			expectedNameDefault := fmt.Sprintf("%s - Toolhead %d", printerConfig.Name, toolheadID)
			if name == expectedNameDefault {
				return true
			}

			// Check custom name pattern
			if displayName, exists := toolheadNames[toolheadID]; exists {
				expectedNameCustom := fmt.Sprintf("%s - %s", printerConfig.Name, displayName)
				if name == expectedNameCustom {
					return true
				}
			}
		}
	}

	return false
}

// ─── Virtual Printer File Management ─────────────────────────────────────────

// VirtualPrinterFile is the metadata record returned to the UI (no content blob)
type VirtualPrinterFile struct {
	ID          int       `json:"id"`
	PrinterID   string    `json:"printer_id"`
	Filename    string    `json:"filename"`
	DisplayName string    `json:"display_name"`
	FileSize    int64     `json:"file_size"`
	UploadedAt  time.Time `json:"uploaded_at"`
}

// SaveVirtualPrinterFile stores G-code content as a BLOB in SQLite.
// The ON DELETE CASCADE foreign key removes files when the printer row is deleted.
func (b *FilamentBridge) SaveVirtualPrinterFile(printerID, filename, displayName string, content []byte) (int64, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	res, err := b.db.Exec(`
		INSERT INTO virtual_printer_files (printer_id, filename, display_name, file_size, content)
		VALUES (?, ?, ?, ?, ?)
	`, printerID, filename, displayName, len(content), content)
	if err != nil {
		return 0, fmt.Errorf("failed to save virtual file: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get file ID: %w", err)
	}
	log.Printf("💾 Saved G-code '%s' for virtual printer %s (id=%d, %d bytes)", displayName, printerID, id, len(content))
	return id, nil
}

// GetVirtualPrinterFiles returns file metadata for a printer — no content blob.
func (b *FilamentBridge) GetVirtualPrinterFiles(printerID string) ([]VirtualPrinterFile, error) {
	rows, err := b.db.Query(`
		SELECT id, printer_id, filename, display_name, file_size, uploaded_at
		FROM virtual_printer_files WHERE printer_id = ? ORDER BY uploaded_at DESC
	`, printerID)
	if err != nil {
		return nil, fmt.Errorf("failed to query virtual files: %w", err)
	}
	defer rows.Close()

	var files []VirtualPrinterFile
	for rows.Next() {
		var f VirtualPrinterFile
		if err := rows.Scan(&f.ID, &f.PrinterID, &f.Filename, &f.DisplayName, &f.FileSize, &f.UploadedAt); err != nil {
			return nil, fmt.Errorf("failed to scan virtual file: %w", err)
		}
		files = append(files, f)
	}
	if files == nil {
		files = []VirtualPrinterFile{}
	}
	return files, nil
}

// GetVirtualPrinterFileContent returns the raw file bytes and display name.
func (b *FilamentBridge) GetVirtualPrinterFileContent(fileID int) ([]byte, string, error) {
	var content []byte
	var displayName string
	err := b.db.QueryRow(
		"SELECT content, display_name FROM virtual_printer_files WHERE id = ?", fileID,
	).Scan(&content, &displayName)
	if err == sql.ErrNoRows {
		return nil, "", fmt.Errorf("file %d not found", fileID)
	}
	if err != nil {
		return nil, "", fmt.Errorf("failed to load file content: %w", err)
	}
	return content, displayName, nil
}

// DeleteVirtualPrinterFile removes a single uploaded file.
func (b *FilamentBridge) DeleteVirtualPrinterFile(printerID string, fileID int) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	res, err := b.db.Exec(
		"DELETE FROM virtual_printer_files WHERE id = ? AND printer_id = ?", fileID, printerID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete virtual file: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("file %d not found for printer %s", fileID, printerID)
	}
	return nil
}

// ProcessVirtualFile parses the stored G-code, updates Spoolman for every mapped
// toolhead, and returns (usage map, list of toolhead IDs that had usage but no spool).
func (b *FilamentBridge) ProcessVirtualFile(printerID string, fileID int) (usage map[int]float64, skipped []int, printTimeMin float64, err error) {
	content, displayName, err := b.GetVirtualPrinterFileContent(fileID)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("cannot load file: %w", err)
	}

	client := &PrusaLinkClient{}
	usage, err = client.ParseGcodeFilamentUsage(content)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to parse G-code: %w", err)
	}
	if len(usage) == 0 {
		return nil, nil, 0, fmt.Errorf(
			"no filament usage metadata found in '%s' — ensure your slicer writes filament weight comments",
			displayName,
		)
	}

	configs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("cannot load printer config: %w", err)
	}
	config, ok := configs[printerID]
	if !ok {
		return nil, nil, 0, fmt.Errorf("printer %s not found", printerID)
	}
	printerName := resolvePrinterName(config)

	// Identify toolheads that have usage but no spool mapped
	for toolheadID, g := range usage {
		if g <= 0 {
			continue
		}
		spoolID, err2 := b.GetToolheadMapping(printerName, toolheadID)
		if err2 != nil || spoolID == 0 {
			skipped = append(skipped, toolheadID)
		}
	}

	// Extract print time and thumbnail before updating Spoolman
	printTimeSec, thumbnailB64 := ParseGcodeMetadata(content)
	printTimeMin = float64(printTimeSec) / 60.0

	// Update Spoolman for every toolhead with filament usage.
	if err := b.processFilamentUsage(printerName, usage, displayName); err != nil {
		return nil, skipped, 0, fmt.Errorf("failed to update Spoolman: %w", err)
	}

	// All toolheads in this virtual print share one session ID.
	sessionID := newSessionID()
	for toolheadID, usedG := range usage {
		spoolID, _ := b.GetToolheadMapping(printerName, toolheadID)
		_ = b.LogPrintUsageFull(printerName, toolheadID, spoolID, usedG, displayName,
			printTimeMin, "completed", thumbnailB64, sessionID, "virtual")
	}

	log.Printf("✅ Virtual '%s': processed '%s', %d toolhead(s), %d skipped, %.0f min",
		printerName, displayName, len(usage), len(skipped), printTimeMin)
	return usage, skipped, printTimeMin, nil
}

// migrateVirtualPrinterSupport safely adds the is_virtual column to existing databases
// and cleans up any mangled printer_name values in toolhead_mappings caused by the
// h3.textContent bug where the 🧪 VIRTUAL badge span text was captured alongside
// the printer name, producing values with embedded newlines.
func (b *FilamentBridge) migrateVirtualPrinterSupport() error {
	_, _ = b.db.Exec("ALTER TABLE printer_configs ADD COLUMN is_virtual INTEGER DEFAULT 0")

	// Re-enable foreign keys (connection-scoped setting)
	_, err := b.db.Exec("PRAGMA foreign_keys = ON")
	if err != nil {
		log.Printf("Warning: could not enable foreign key enforcement: %v", err)
	}

	// Clean up mangled printer names in toolhead_mappings.
	// The h3.textContent bug stored names like:
	//   "\n                    tets\n                    \n                        🧪 VIRTUAL\n                    "
	// instead of just "tets". We strip everything from the first newline onward,
	// then trim surrounding whitespace to recover the real name.
	rows, err := b.db.Query("SELECT DISTINCT printer_name FROM toolhead_mappings")
	if err == nil {
		defer rows.Close()
		type fix struct{ old, clean string }
		var toFix []fix
		for rows.Next() {
			var name string
			if rows.Scan(&name) != nil {
				continue
			}
			cleaned := strings.TrimSpace(name)
			// Strip everything from the first embedded newline onward
			if idx := strings.Index(cleaned, "\n"); idx >= 0 {
				cleaned = strings.TrimSpace(cleaned[:idx])
			}
			if cleaned != name && cleaned != "" {
				toFix = append(toFix, fix{name, cleaned})
			}
		}
		rows.Close()
		for _, f := range toFix {
			if _, err := b.db.Exec(
				"UPDATE toolhead_mappings SET printer_name = ? WHERE printer_name = ?",
				f.clean, f.old,
			); err == nil {
				log.Printf("🔧 Cleaned mangled printer_name in toolhead_mappings: %q → %q", f.old, f.clean)
			}
		}
	}

	return nil
}

// ─── Print History Queries ───────────────────────────────────────────────────

// GetPrintHistory returns all print history records, newest first.
// Joins print_costs to include total_cost and currency if available.
func (b *FilamentBridge) GetPrintHistory(limit int) ([]PrintHistory, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := b.db.Query(`
		SELECT
			ph.id, ph.printer_name, ph.toolhead_id, ph.spool_id, ph.filament_used,
			ph.print_started, ph.print_finished, ph.job_name,
			COALESCE(ph.notes, ''), COALESCE(ph.status, 'completed'),
			COALESCE(ph.print_time_minutes, 0),
			COALESCE(ph.thumbnail_path, ''),
			COALESCE(pc.total_cost, 0), COALESCE(pc.currency, ''),
			COALESCE(ph.source, 'prusalink'),
			COALESCE(ph.total_duration_sec, ph.print_time_minutes * 60),
			COALESCE(ph.print_duration_sec, ph.print_time_minutes * 60),
			COALESCE(ph.pause_duration_sec, 0),
			COALESCE(ph.pause_count, 0),
			COALESCE(ph.cancel_reason, ''),
			COALESCE(ph.time_precision, 'approximate'),
			COALESCE(ph.filament_precision, 'estimated'),
			COALESCE(ph.session_id, '')
		FROM print_history ph
		LEFT JOIN print_costs pc ON pc.print_history_id = ph.id
		ORDER BY ph.print_finished DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query print history: %w", err)
	}
	defer rows.Close()

	var records []PrintHistory
	for rows.Next() {
		var r PrintHistory
		if err := rows.Scan(
			&r.ID, &r.PrinterName, &r.ToolheadID, &r.SpoolID, &r.FilamentUsed,
			&r.PrintStarted, &r.PrintFinished, &r.JobName,
			&r.Notes, &r.Status, &r.PrintTimeMinutes,
			&r.ThumbnailBase64, &r.TotalCost, &r.Currency,
			&r.Source, &r.TotalDurationSec, &r.PrintDurationSec,
			&r.PauseDurationSec, &r.PauseCount, &r.CancelReason,
			&r.TimePrecision, &r.FilamentPrecision, &r.SessionID,
		); err != nil {
			log.Printf("Warning: failed to scan print history row: %v", err)
			continue
		}
		records = append(records, r)
	}
	if records == nil {
		records = []PrintHistory{}
	}
	return records, nil
}

// GetPrintHistoryEntry returns a single print history record by ID,
// including per-tool filament usage and pause detail for OctoPrint records.
func (b *FilamentBridge) GetPrintHistoryEntry(id int) (*PrintHistory, error) {
	var r PrintHistory
	err := b.db.QueryRow(`
		SELECT
			ph.id, ph.printer_name, ph.toolhead_id, ph.spool_id, ph.filament_used,
			ph.print_started, ph.print_finished, ph.job_name,
			COALESCE(ph.notes, ''), COALESCE(ph.status, 'completed'),
			COALESCE(ph.print_time_minutes, 0),
			COALESCE(ph.thumbnail_path, ''),
			COALESCE(pc.total_cost, 0), COALESCE(pc.currency, ''),
			COALESCE(ph.source, 'prusalink'),
			COALESCE(ph.total_duration_sec, ph.print_time_minutes * 60),
			COALESCE(ph.print_duration_sec, ph.print_time_minutes * 60),
			COALESCE(ph.pause_duration_sec, 0),
			COALESCE(ph.pause_count, 0),
			COALESCE(ph.cancel_reason, ''),
			COALESCE(ph.time_precision, 'approximate'),
			COALESCE(ph.filament_precision, 'estimated'),
			COALESCE(ph.session_id, '')
		FROM print_history ph
		LEFT JOIN print_costs pc ON pc.print_history_id = ph.id
		WHERE ph.id = ?`, id,
	).Scan(
		&r.ID, &r.PrinterName, &r.ToolheadID, &r.SpoolID, &r.FilamentUsed,
		&r.PrintStarted, &r.PrintFinished, &r.JobName,
		&r.Notes, &r.Status, &r.PrintTimeMinutes,
		&r.ThumbnailBase64, &r.TotalCost, &r.Currency,
		&r.Source, &r.TotalDurationSec, &r.PrintDurationSec,
		&r.PauseDurationSec, &r.PauseCount, &r.CancelReason,
		&r.TimePrecision, &r.FilamentPrecision, &r.SessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("print history entry %d not found: %w", id, err)
	}

	// Fetch per-tool filament usage (populated for OctoPrint records).
	fuRows, err := b.db.Query(`
		SELECT id, print_id, tool_index, COALESCE(change_number, 0), COALESCE(spool_id, 0),
		       filament_used_mm, filament_used_grams
		FROM print_filament_usage WHERE print_id = ? ORDER BY tool_index, change_number`, id)
	if err == nil {
		defer fuRows.Close()
		for fuRows.Next() {
			var fu PrintFilamentUsage
			if fuRows.Scan(&fu.ID, &fu.PrintID, &fu.ToolIndex, &fu.ChangeNumber, &fu.SpoolID,
				&fu.FilamentUsedMM, &fu.FilamentUsedG) == nil {
				r.FilamentUsages = append(r.FilamentUsages, fu)
			}
		}
	}

	// Fetch pause events.
	pRows, err := b.db.Query(`
		SELECT id, print_id, paused_at, resumed_at, duration_sec, reason
		FROM print_pauses WHERE print_id = ? ORDER BY paused_at`, id)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var p PrintPause
			if pRows.Scan(&p.ID, &p.PrintID, &p.PausedAt, &p.ResumedAt,
				&p.DurationSec, &p.Reason) == nil {
				r.Pauses = append(r.Pauses, p)
			}
		}
	}

	return &r, nil
}

// GetPrintSessions returns print jobs grouped by session_id, newest first.
// Records with an empty session_id each form their own implicit session.
func (b *FilamentBridge) GetPrintSessions(limit int) ([]PrintSession, error) {
	if limit <= 0 {
		limit = 200
	}
	records, err := b.GetPrintHistory(limit)
	if err != nil {
		return nil, err
	}

	// Group by session_id; records with no session_id get a unique per-row key.
	type sessionKey = string
	order := []sessionKey{}
	groups := map[sessionKey][]PrintHistory{}

	for _, r := range records {
		key := r.SessionID
		if key == "" {
			key = fmt.Sprintf("__solo_%d", r.ID)
		}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], r)
	}

	sessions := make([]PrintSession, 0, len(order))
	for _, key := range order {
		recs := groups[key]
		first := recs[0]

		var totalFilament, totalCost float64
		for _, r := range recs {
			totalFilament += r.FilamentUsed
			totalCost += r.TotalCost
		}
		sessionID := first.SessionID

		sessions = append(sessions, PrintSession{
			SessionID:      sessionID,
			JobName:        first.JobName,
			PrinterName:    first.PrinterName,
			Status:         first.Status,
			Source:         first.Source,
			PrintStarted:   first.PrintStarted,
			PrintFinished:  first.PrintFinished,
			TotalFilamentG: totalFilament,
			TotalCost:      totalCost,
			Currency:       first.Currency,
			ToolCount:      len(recs),
			Records:        recs,
		})
	}
	return sessions, nil
}

// UpdatePrintNote sets the user note on a print history record.
func (b *FilamentBridge) UpdatePrintNote(id int, note string) error {
	_, err := b.db.Exec("UPDATE print_history SET notes = ? WHERE id = ?", note, id)
	if err != nil {
		return fmt.Errorf("failed to update print note: %w", err)
	}
	return nil
}

// DeletePrintHistoryEntry removes a print history record and its associated cost record.
func (b *FilamentBridge) DeletePrintHistoryEntry(id int) error {
	_, err := b.db.Exec("DELETE FROM print_history WHERE id = ?", id)
	return err
}

// ParseGcodeMetadata extracts print time (seconds) and embedded thumbnail from raw gcode bytes.
// Returns printTimeSec=0 and thumbnailBase64="" if not found — both are optional.
func ParseGcodeMetadata(content []byte) (printTimeSec int, thumbnailBase64 string) {
	text := string(content)

	// Print time: ";TIME:20219.44" header at top of file (OrcaSlicer/Cura)
	timeRe := regexp.MustCompile(`;TIME:([0-9]+)`)
	if m := timeRe.FindStringSubmatch(text); len(m) >= 2 {
		fmt.Sscanf(m[1], "%d", &printTimeSec)
	}

	// Thumbnail: OrcaSlicer / PrusaSlicer embed JPG base64 in comment lines:
	//   "; thumbnail_JPG begin 96x96 3656"  ...lines...  "; thumbnail_JPG end"
	thumbStartRe := regexp.MustCompile(`; thumbnail_(?:JPG|PNG) begin [0-9x]+ [0-9]+`)
	thumbEndRe   := regexp.MustCompile(`; thumbnail_(?:JPG|PNG) end`)
	lineRe       := regexp.MustCompile(`(?m)^; ?`)

	startIdx := thumbStartRe.FindStringIndex(text)
	if startIdx != nil {
		afterStart := text[startIdx[1]:]
		endIdx := thumbEndRe.FindStringIndex(afterStart)
		if endIdx != nil {
			block := afterStart[:endIdx[0]]
			clean := lineRe.ReplaceAllString(block, "")
			clean = strings.ReplaceAll(clean, "\n", "")
			clean = strings.ReplaceAll(clean, "\r", "")
			clean = strings.TrimSpace(clean)
			if clean != "" {
				thumbnailBase64 = "data:image/jpeg;base64," + clean
			}
		}
	}
	return
}

// GetOrphanedMappings returns toolhead_mappings rows where the printer_name
// does not match any existing printer in printer_configs.
// These are left over when a printer was deleted before the cleanup fix.
func (b *FilamentBridge) GetOrphanedMappings() ([]map[string]interface{}, error) {
	rows, err := b.db.Query(`
		SELECT tm.printer_name, tm.toolhead_id, tm.spool_id
		FROM toolhead_mappings tm
		WHERE NOT EXISTS (
			SELECT 1 FROM printer_configs pc WHERE pc.name = tm.printer_name
		)
		ORDER BY tm.printer_name, tm.toolhead_id`)
	if err != nil {
		return nil, fmt.Errorf("failed to query orphaned mappings: %w", err)
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var printerName string
		var toolheadID, spoolID int
		if err := rows.Scan(&printerName, &toolheadID, &spoolID); err != nil {
			continue
		}
		result = append(result, map[string]interface{}{
			"printer_name": printerName,
			"toolhead_id":  toolheadID,
			"spool_id":     spoolID,
		})
	}
	if result == nil {
		result = []map[string]interface{}{}
	}
	return result, nil
}

// ClearOrphanedMappings deletes all toolhead_mappings rows that have no
// matching printer in printer_configs — freeing those spools for reassignment.
func (b *FilamentBridge) ClearOrphanedMappings() (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	res, err := b.db.Exec(`
		DELETE FROM toolhead_mappings
		WHERE NOT EXISTS (
			SELECT 1 FROM printer_configs pc WHERE pc.name = toolhead_mappings.printer_name
		)`)
	if err != nil {
		return 0, fmt.Errorf("failed to clear orphaned mappings: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("🧹 Cleared %d orphaned toolhead mapping(s) — spools are now free to reassign", n)
	}
	return int(n), nil
}

// LogOctoPrintRecord persists a complete print record pushed by the OctoPrint plugin.
// It inserts the top-level print_history row, per-tool filament rows, pause rows,
// calculates cost, and queues Spoolman filament-usage updates.
func (b *FilamentBridge) LogOctoPrintRecord(p OctoPrintPayload) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if p.Source == "" {
		p.Source = "octoprint"
	}
	if p.Status == "" {
		p.Status = "completed"
	}
	if p.TimePrecision == "" {
		p.TimePrecision = "exact"
	}
	if p.FilamentPrecision == "" {
		p.FilamentPrecision = "measured"
	}
	if p.SessionID == "" {
		p.SessionID = newSessionID()
	}

	// Sum filament across all tools for the top-level record.
	var totalGrams, totalMM float64
	for _, f := range p.Filament {
		totalGrams += f.FilamentUsedG
		totalMM += f.FilamentUsedMM
	}

	printTimeMin := p.PrintDurationSec / 60.0
	if printTimeMin == 0 {
		printTimeMin = p.TotalDurationSec / 60.0
	}

	var cancelReason sql.NullString
	if p.CancelReason != nil {
		cancelReason = sql.NullString{String: *p.CancelReason, Valid: true}
	}

	res, err := b.db.Exec(`
		INSERT INTO print_history
			(printer_name, toolhead_id, spool_id, filament_used,
			 print_started, print_finished, job_name,
			 print_time_minutes, status, thumbnail_path,
			 source, total_duration_sec, print_duration_sec,
			 pause_duration_sec, pause_count, cancel_reason,
			 time_precision, filament_precision, session_id)
		VALUES (?, 0, 0, ?, ?, ?, ?, ?, ?, '',
		        ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PrinterID, totalGrams,
		p.StartedAt, p.EndedAt, p.FileName,
		printTimeMin, p.Status,
		p.Source, p.TotalDurationSec, p.PrintDurationSec,
		p.PauseDurationSec, p.PauseCount, cancelReason,
		p.TimePrecision, p.FilamentPrecision, p.SessionID,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert octoprint record: %w", err)
	}
	printID64, _ := res.LastInsertId()
	printID := int(printID64)

	// Per-tool filament rows.
	for _, f := range p.Filament {
		if _, err := b.db.Exec(`
			INSERT INTO print_filament_usage
				(print_id, tool_index, change_number, spool_id, filament_used_mm, filament_used_grams)
			VALUES (?, ?, ?, ?, ?, ?)`,
			printID, f.ToolIndex, f.ChangeNumber, f.SpoolID, f.FilamentUsedMM, f.FilamentUsedG,
		); err != nil {
			log.Printf("Warning: failed to insert filament usage for tool %d: %v", f.ToolIndex, err)
		}
	}

	// Pause events.
	for _, pause := range p.Pauses {
		if _, err := b.db.Exec(`
			INSERT INTO print_pauses
				(print_id, paused_at, resumed_at, duration_sec, reason)
			VALUES (?, ?, ?, ?, ?)`,
			printID, pause.PausedAt, pause.ResumedAt, pause.DurationSec, pause.Reason,
		); err != nil {
			log.Printf("Warning: failed to insert pause record: %v", err)
		}
	}

	// Spoolman inventory update — only when OctoPrint is NOT managing Spoolman.
	// SpoolmanManaged nil (field absent) or true → OctoPrint/SpoolManager already
	// deducted; do nothing to avoid double-decrement.
	// SpoolmanManaged false → no Spoolman plugin active; The Moment deducts.
	spoolmanManaged := p.SpoolmanManaged == nil || *p.SpoolmanManaged
	if !spoolmanManaged {
		for _, f := range p.Filament {
			if f.FilamentUsedG <= 0 || f.SpoolID <= 0 {
				continue
			}
			if err := b.spoolman.UpdateSpoolUsage(f.SpoolID, f.FilamentUsedG); err != nil {
				log.Printf("⚠️  Spoolman update failed for spool %d (OctoPrint unmanaged mode) — queuing for retry: %v", f.SpoolID, err)
				if qErr := b.enqueuePendingSpoolmanUpdate(p.PrinterID, f.ToolIndex, f.SpoolID, f.FilamentUsedG, p.FileName); qErr != nil {
					log.Printf("Error queuing pending Spoolman update for spool %d: %v", f.SpoolID, qErr)
				}
			} else {
				log.Printf("✅ Spoolman updated spool %d: %.2fg used (OctoPrint unmanaged)", f.SpoolID, f.FilamentUsedG)
			}
		}
	}

	// CalculatePrintCostMultiSpoolForPrinter prices each filament entry against its own spool
	// and applies per-printer wattage / preheat / depreciation overrides.
	// Neither it nor SavePrintCost acquires b.mutex, so both are safe to call here.
	if bd, err := b.CalculatePrintCostMultiSpoolForPrinter(p.Filament, printTimeMin, p.PrinterID); err == nil {
		if err := b.SavePrintCost(printID, bd); err != nil {
			log.Printf("Warning: failed to save cost for octoprint record %d: %v", printID, err)
		}
	}

	log.Printf("📋 OctoPrint record logged: %s on %s (%.2fg, %.0fmin, %s)",
		p.FileName, p.PrinterID, totalGrams, printTimeMin, p.Status)
	return printID, nil
}

// ReassignFilamentSegment moves the filament usage recorded against segmentID to
// newSpoolID.  It subtracts the grams from the old spool in Spoolman (if any) and
// adds them to the new spool, then updates the local DB row and recalculates cost.
// segmentID is the print_filament_usage.id primary key.
func (b *FilamentBridge) ReassignFilamentSegment(printID, segmentID, newSpoolID int) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// Fetch the existing segment.
	var oldSpoolID int
	var gramsUsed float64
	var mmUsed float64
	var toolIndex, changeNumber int
	err := b.db.QueryRow(
		`SELECT tool_index, change_number, COALESCE(spool_id,0), filament_used_grams, filament_used_mm
		 FROM print_filament_usage WHERE id = ? AND print_id = ?`,
		segmentID, printID,
	).Scan(&toolIndex, &changeNumber, &oldSpoolID, &gramsUsed, &mmUsed)
	if err != nil {
		return fmt.Errorf("segment %d not found for print %d: %w", segmentID, printID, err)
	}
	if gramsUsed <= 0 {
		return fmt.Errorf("segment has no filament usage to reassign")
	}

	// Adjust Spoolman inventory: subtract from old spool, add to new spool.
	if oldSpoolID > 0 && oldSpoolID != newSpoolID {
		if err := b.spoolman.SubtractSpoolUsage(oldSpoolID, gramsUsed); err != nil {
			log.Printf("⚠️  ReassignFilamentSegment: subtract from spool %d failed: %v", oldSpoolID, err)
			// Non-fatal — proceed so the DB stays consistent.
		}
	}
	if newSpoolID > 0 && newSpoolID != oldSpoolID {
		if err := b.spoolman.UpdateSpoolUsage(newSpoolID, gramsUsed); err != nil {
			log.Printf("⚠️  ReassignFilamentSegment: add to spool %d failed: %v", newSpoolID, err)
		}
	}

	// Update the local record.
	if _, err := b.db.Exec(
		`UPDATE print_filament_usage SET spool_id = ? WHERE id = ? AND print_id = ?`,
		newSpoolID, segmentID, printID,
	); err != nil {
		return fmt.Errorf("updating segment DB record: %w", err)
	}

	// Rebuild filament list for cost recalculation.
	rows, err := b.db.Query(
		`SELECT tool_index, COALESCE(change_number,0), COALESCE(spool_id,0), filament_used_grams
		 FROM print_filament_usage WHERE print_id = ? ORDER BY tool_index, change_number`, printID)
	if err != nil {
		return nil // cost recalc is best-effort
	}
	defer rows.Close()

	var filamentForCost []OctoPrintPayloadFilament
	for rows.Next() {
		var f OctoPrintPayloadFilament
		if rows.Scan(&f.ToolIndex, &f.ChangeNumber, &f.SpoolID, &f.FilamentUsedG) == nil {
			filamentForCost = append(filamentForCost, f)
		}
	}
	rows.Close()

	// Fetch print time for cost calc.
	var printTimeMin float64
	var printerName string
	b.db.QueryRow(`SELECT COALESCE(print_time_minutes,0), printer_name FROM print_history WHERE id = ?`, printID).
		Scan(&printTimeMin, &printerName)

	if bd, err := b.CalculatePrintCostMultiSpoolForPrinter(filamentForCost, printTimeMin, printerName); err == nil {
		b.SavePrintCost(printID, bd)
	}

	log.Printf("🔄 Filament segment %d (print %d T%d.%d) reassigned spool %d → %d (%.2fg)",
		segmentID, printID, toolIndex, changeNumber, oldSpoolID, newSpoolID, gramsUsed)
	return nil
}

// Close closes the database connection
func (b *FilamentBridge) Close() error {
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

// All The Moment location management functions have been removed - locations are now managed in Spoolman only
// REMOVED: CreateLocationFromSpoolman
// REMOVED: GetAllThe MomentLocations
// REMOVED: FindLocationByName
// REMOVED: UpdateLocation
// REMOVED: DeleteLocation
// REMOVED: GetLocationStatus
// REMOVED: LocationStatus struct
// REMOVED: AutoSyncSpoolmanLocations
// REMOVED: ImportSpoolmanLocations
// REMOVED: StartLocationSync
