// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
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

// logStateTransition logs a printer state change in a structured, grep-friendly format.
// Only logs when from != to; no-ops on same-state polls.
func logStateTransition(printerID, from, to string, jobID int, progress float64) {
	if from == to {
		return
	}
	log.Printf("[PRUSALINK] printer=%s transition=%s→%s job=%d progress=%.1f",
		printerID, from, to, jobID, progress)
}

// FilamentBridge manages the connection between PrusaLink and Spoolman
type FilamentBridge struct {
	config           *Config
	spoolman         *SpoolmanClient
	db               *sql.DB
	wasPrinting      map[string]bool
	currentJobFile   map[string]string     // Store current job filename per printer
	currentJobDisplayName map[string]string // Display name (long filename) for current job
	currentJobID     map[string]int        // Job ID from PRINTING state; carried to finish handler
	processingPrints map[string]bool       // Track prints being processed
	monitoringActive map[string]bool       // Guard against overlapping monitor goroutines per printer
	printErrors      map[string]PrintError // Store print processing errors
	errorMutex       sync.RWMutex
	previousState    map[string]string    // Last seen printer state per printer
	printStartTime   map[string]time.Time // When each printer's current job was first detected
	mutex            sync.RWMutex

	// Bambu MQTT clients — long-lived, one per printer, keyed by printerID
	bambuClients      map[string]BambuStatusProvider
	bambuMutex        sync.RWMutex
	// bambuClientFactory creates a new Bambu client; overridable in tests.
	bambuClientFactory func(ip, serial, accessCode string) BambuStatusProvider
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
	Source            string  `json:"source"` // "prusalink" | "octoprint"
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

	// Quality tags (outcome + issue labels)
	Tags []PrintQualityTag `json:"tags,omitempty"`

	// File attachments (gcode, slicer files — populated only on single-record fetch)
	Attachments []PrintAttachment `json:"attachments,omitempty"`

	// HasDebugLog is true when a debug poll transcript exists for this print.
	HasDebugLog bool `json:"has_debug_log,omitempty"`
}

// PrintFilamentUsage is per-tool filament data stored for a unified print record.
// ChangeNumber distinguishes multiple spool loads on the same tool: 0 = first load,
// 1 = second (first manual change), etc.  Multi-tool prints have distinct ToolIndex
// values; manual filament changes on one tool have the same ToolIndex with
// incrementing ChangeNumber.
type PrintFilamentUsage struct {
	ID             int      `json:"id"`
	PrintID        int      `json:"print_id"`
	ToolIndex      int      `json:"tool_index"`
	ChangeNumber   int      `json:"change_number"`
	SpoolID        int      `json:"spool_id"`
	FilamentUsedMM float64  `json:"filament_used_mm"`
	FilamentUsedG  float64  `json:"filament_used_grams"`
	PricePerKg     *float64 `json:"price_per_kg,omitempty"` // from Spoolman, nil if not set
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

// PrintQualityTag is a single outcome or issue label attached to a print.
type PrintQualityTag struct {
	ID         int64  `json:"id"`
	PrintID    int64  `json:"print_id"`
	Tag        string `json:"tag"`
	CustomText string `json:"custom_text,omitempty"`
}

// PrintTagsPayload is the body for POST /api/history/:id/tags.
type PrintTagsPayload struct {
	Outcome    string   `json:"outcome"`    // "success" | "acceptable" | "failed" | ""
	Issues     []string `json:"issues"`     // predefined and/or "custom"
	CustomText string   `json:"custom_text"`
}

// OctoPrintPayload is the request body sent by the OctoPrint plugin.
type OctoPrintPayload struct {
	SessionID         string                     `json:"session_id"` // optional; generated server-side if absent
	Source            string                     `json:"source"`
	PrinterID         string                     `json:"printer_id"`
	FileName          string                     `json:"file_name"`
	Status            string                     `json:"status"`
	StartedAt         time.Time                  `json:"started_at"`
	EndedAt           time.Time                  `json:"ended_at"`
	TotalDurationSec  float64                    `json:"total_duration_sec"`
	PrintDurationSec  float64                    `json:"print_duration_sec"`
	PauseDurationSec  float64                    `json:"pause_duration_sec"`
	PauseCount        int                        `json:"pause_count"`
	Pauses            []OctoPrintPayloadPause    `json:"pauses"`
	CancelReason      *string                    `json:"cancel_reason"`
	Filament          []OctoPrintPayloadFilament `json:"filament"`
	TimePrecision     string                     `json:"time_precision"`
	FilamentPrecision string                     `json:"filament_precision"`
	// SpoolmanManaged: true (or nil/omitted) = the OctoPrint Spoolman/SpoolManager
	// plugin already deducted filament; The Moment must NOT deduct again.
	// false = no Spoolman plugin active; The Moment deducts from Spoolman.
	SpoolmanManaged *bool `json:"spoolman_managed,omitempty"`
	// ThumbnailBase64 is an optional JPEG/PNG thumbnail extracted from the gcode file
	// and sent by the OctoPrint plugin as a data URI (e.g. "data:image/jpeg;base64,...").
	ThumbnailBase64 string `json:"thumbnail_base64,omitempty"`
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
	SessionID      string         `json:"session_id"`
	JobName        string         `json:"job_name"`
	PrinterName    string         `json:"printer_name"`
	Status         string         `json:"status"`
	Source         string         `json:"source"`
	PrintStarted   time.Time      `json:"print_started"`
	PrintFinished  time.Time      `json:"print_finished"`
	TotalFilamentG float64        `json:"total_filament_grams"`
	TotalCost      float64        `json:"total_cost"`
	Currency       string         `json:"currency"`
	ToolCount      int            `json:"tool_count"`
	Records        []PrintHistory `json:"records"`
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
		spoolman:         NewSpoolmanClient(DefaultSpoolmanURL, SpoolmanTimeout),
		wasPrinting:           make(map[string]bool),
		currentJobFile:        make(map[string]string),
		currentJobDisplayName: make(map[string]string),
		currentJobID:          make(map[string]int),
		processingPrints:      make(map[string]bool),
		monitoringActive: make(map[string]bool),
		printErrors:      make(map[string]PrintError),
		previousState:    make(map[string]string),
		printStartTime:   make(map[string]time.Time),
		bambuClients:     make(map[string]BambuStatusProvider),
	}
	bridge.bambuClientFactory = func(ip, serial, accessCode string) BambuStatusProvider {
		return NewBambuMQTTClient(ip, serial, accessCode, newBambuDebugLogger(bridge))
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

	if err := bridge.migrateNFCAssignments(); err != nil {
		return nil, fmt.Errorf("failed to migrate NFC assignments: %w", err)
	}

	if err := bridge.migratePrintAttachments(); err != nil {
		return nil, fmt.Errorf("failed to migrate print attachments: %w", err)
	}

	if err := bridge.migratePrusaLinkStateCache(); err != nil {
		return nil, fmt.Errorf("failed to migrate PrusaLink state cache: %w", err)
	}

	if err := bridge.migratePrintSessions(); err != nil {
		return nil, fmt.Errorf("failed to migrate print sessions: %w", err)
	}

	if err := bridge.migratePrinterDebugLog(); err != nil {
		return nil, fmt.Errorf("failed to migrate printer debug log: %w", err)
	}

	if err := bridge.migrateToolheadLocations(); err != nil {
		return nil, fmt.Errorf("failed to migrate toolhead locations: %w", err)
	}

	bridge.ReconcileActiveSessions()

	// Update Spoolman URL and timeout if config is provided
	if config != nil && config.SpoolmanURL != "" {
		bridge.spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout)
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
		`CREATE TABLE IF NOT EXISTS print_quality_tags (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			print_id INTEGER NOT NULL,
			tag TEXT NOT NULL,
			custom_text TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (print_id) REFERENCES print_history(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_print_quality_tags_print_id ON print_quality_tags(print_id)`,
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

// migrateNFCAssignments creates tables for toolhead spool assignments and print spool events.
func (b *FilamentBridge) migrateNFCAssignments() error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS toolhead_spool_assignments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_id TEXT NOT NULL,
			toolhead_index INTEGER NOT NULL,
			spoolman_spool_id INTEGER NOT NULL,
			assigned_at DATETIME NOT NULL DEFAULT (datetime('now')),
			unassigned_at DATETIME,
			assignment_reason TEXT NOT NULL DEFAULT 'manual'
		)`,
		`CREATE TABLE IF NOT EXISTS print_spool_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			print_history_id INTEGER NOT NULL,
			toolhead_index INTEGER NOT NULL,
			old_spoolman_spool_id INTEGER,
			new_spoolman_spool_id INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			event_time DATETIME NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (print_history_id) REFERENCES print_history(id) ON DELETE CASCADE
		)`,
	}
	for _, q := range tables {
		if _, err := b.db.Exec(q); err != nil {
			return fmt.Errorf("failed to create NFC assignment table: %w", err)
		}
	}
	return nil
}

// migratePrintAttachments creates the table that stores gcode and slicer files attached to print records.
func (b *FilamentBridge) migratePrintAttachments() error {
	_, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS print_attachments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			print_history_id INTEGER NOT NULL,
			file_type TEXT NOT NULL,
			filename TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			file_path TEXT NOT NULL,
			stored_at DATETIME NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (print_history_id) REFERENCES print_history(id) ON DELETE CASCADE
		)`)
	if err != nil {
		return err
	}
	_, err = b.db.Exec(`CREATE INDEX IF NOT EXISTS idx_print_attachments_print_id ON print_attachments(print_history_id)`)
	return err
}

// migratePrusaLinkStateCache creates tables for persisting per-printer poll state
// and deduplicating re-fired job completions (known PrusaLink firmware behaviour).
func (b *FilamentBridge) migratePrusaLinkStateCache() error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS printer_state_cache (
			printer_id TEXT PRIMARY KEY,
			last_state TEXT NOT NULL,
			last_job_id INTEGER,
			last_progress REAL,
			last_time_printing INTEGER,
			last_polled_at DATETIME NOT NULL,
			next_poll_at DATETIME NOT NULL DEFAULT (datetime('now')),
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (printer_id) REFERENCES printer_configs(printer_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS processed_jobs (
			printer_id TEXT NOT NULL,
			job_id INTEGER NOT NULL,
			completed_at DATETIME NOT NULL,
			outcome TEXT NOT NULL,
			PRIMARY KEY (printer_id, job_id),
			FOREIGN KEY (printer_id) REFERENCES printer_configs(printer_id) ON DELETE CASCADE
		)`,
	}
	for _, q := range tables {
		if _, err := b.db.Exec(q); err != nil {
			return fmt.Errorf("migratePrusaLinkStateCache: %w", err)
		}
	}
	return nil
}

// PrinterStateCache holds the last-known poll result for a single printer.
type PrinterStateCache struct {
	PrinterID           string
	LastState           string
	LastJobID           int
	LastProgress        float64
	LastTimePrinting    int
	LastPolledAt        time.Time
	NextPollAt          time.Time
	ConsecutiveFailures int
}

// pollIntervalForState returns the desired poll interval for a given printer state.
func pollIntervalForState(state string) time.Duration {
	switch state {
	case StateFinished:
		return 2 * time.Second
	case StateAttention:
		return 3 * time.Second
	case StatePrinting:
		return 5 * time.Second
	case StatePaused:
		return 10 * time.Second
	case StateIdle:
		return 20 * time.Second
	default:
		return 30 * time.Second
	}
}

// GetLastKnownState returns the cached poll state for a printer, or a zero-value
// struct if no row exists yet.
func (b *FilamentBridge) GetLastKnownState(printerID string) (PrinterStateCache, error) {
	var c PrinterStateCache
	var lastPolled, nextPoll string
	err := b.db.QueryRow(`
		SELECT printer_id, last_state, COALESCE(last_job_id,0),
		       COALESCE(last_progress,0), COALESCE(last_time_printing,0),
		       last_polled_at, next_poll_at, consecutive_failures
		FROM printer_state_cache WHERE printer_id = ?`, printerID).
		Scan(&c.PrinterID, &c.LastState, &c.LastJobID,
			&c.LastProgress, &c.LastTimePrinting,
			&lastPolled, &nextPoll, &c.ConsecutiveFailures)
	if errors.Is(err, sql.ErrNoRows) {
		return PrinterStateCache{}, nil
	}
	if err != nil {
		return PrinterStateCache{}, err
	}
	c.LastPolledAt, _ = time.Parse(time.RFC3339, lastPolled)
	c.NextPollAt, _ = time.Parse(time.RFC3339, nextPoll)
	return c, nil
}

// UpsertLastKnownState writes (or updates) the cached poll state for a printer.
// nextState is used to compute the next desired poll time.
func (b *FilamentBridge) UpsertLastKnownState(printerID, state string, jobID int, progress float64, timePrinting int) error {
	now := time.Now().UTC()
	nextPoll := now.Add(pollIntervalForState(state))
	_, err := b.db.Exec(`
		INSERT INTO printer_state_cache
			(printer_id, last_state, last_job_id, last_progress, last_time_printing,
			 last_polled_at, next_poll_at, consecutive_failures)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(printer_id) DO UPDATE SET
			last_state = excluded.last_state,
			last_job_id = excluded.last_job_id,
			last_progress = excluded.last_progress,
			last_time_printing = excluded.last_time_printing,
			last_polled_at = excluded.last_polled_at,
			next_poll_at = excluded.next_poll_at,
			consecutive_failures = 0`,
		printerID, state, jobID, progress, timePrinting,
		now.Format(time.RFC3339), nextPoll.Format(time.RFC3339))
	return err
}

// IncrementFailureCount increments the consecutive_failures counter and returns
// the new count. Does not update last_polled_at or next_poll_at.
func (b *FilamentBridge) IncrementFailureCount(printerID string) (int, error) {
	_, err := b.db.Exec(`
		INSERT INTO printer_state_cache
			(printer_id, last_state, last_polled_at, next_poll_at, consecutive_failures)
		VALUES (?, '', datetime('now'), datetime('now'), 1)
		ON CONFLICT(printer_id) DO UPDATE SET
			consecutive_failures = consecutive_failures + 1`,
		printerID)
	if err != nil {
		return 0, err
	}
	var count int
	err = b.db.QueryRow(`SELECT consecutive_failures FROM printer_state_cache WHERE printer_id = ?`, printerID).Scan(&count)
	return count, err
}

// IsJobProcessed reports whether a job has been conclusively processed (finished or
// stopped). "recovered" entries do not count — a job recovered at restart can still
// complete normally once the printer resumes.
func (b *FilamentBridge) IsJobProcessed(printerID string, jobID int) (bool, error) {
	if jobID == 0 {
		return false, nil // job ID 0 means unknown; never suppress
	}
	var count int
	err := b.db.QueryRow(`SELECT COUNT(*) FROM processed_jobs WHERE printer_id = ? AND job_id = ? AND outcome != 'recovered'`,
		printerID, jobID).Scan(&count)
	return count > 0, err
}

// MarkJobProcessed records that a job has been fully handled so re-fires are ignored.
// outcome is one of "finished", "stopped", "recovered", "errored".
// Uses INSERT OR REPLACE so a prior "recovered" placeholder gets upgraded to the final outcome.
func (b *FilamentBridge) MarkJobProcessed(printerID string, jobID int, outcome string) error {
	if jobID == 0 {
		return nil
	}
	_, err := b.db.Exec(`
		INSERT OR REPLACE INTO processed_jobs (printer_id, job_id, completed_at, outcome)
		VALUES (?, ?, datetime('now'), ?)`,
		printerID, jobID, outcome)
	return err
}

// migratePrintSessions creates the active_print_sessions table and adds
// recovery-tracking columns to print_history.
func (b *FilamentBridge) migratePrintSessions() error {
	_, err := b.db.Exec(`CREATE TABLE IF NOT EXISTS active_print_sessions (
		printer_id TEXT NOT NULL,
		job_id INTEGER NOT NULL,
		started_at DATETIME NOT NULL,
		file_path TEXT,
		file_size_bytes INTEGER,
		gcode_metadata_json TEXT,
		gcode_local_path TEXT,
		initial_assignments_json TEXT,
		last_seen_progress REAL,
		last_seen_time_printing INTEGER,
		change_count INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (printer_id, job_id),
		FOREIGN KEY (printer_id) REFERENCES printer_configs(printer_id) ON DELETE CASCADE
	)`)
	if err != nil {
		return fmt.Errorf("migratePrintSessions: %w", err)
	}
	for _, q := range []string{
		`ALTER TABLE print_history ADD COLUMN outcome TEXT DEFAULT 'finished'`,
		`ALTER TABLE print_history ADD COLUMN progress_at_stop REAL`,
		`ALTER TABLE print_history ADD COLUMN recovered INTEGER DEFAULT 0`,
		`ALTER TABLE print_history ADD COLUMN gcode_unavailable INTEGER DEFAULT 0`,
	} {
		b.db.Exec(q) // ignore duplicate-column errors on existing DBs
	}
	return nil
}

// migratePrinterDebugLog adds debug_log toggle to printer_configs and creates
// the print_debug_logs table for per-print poll transcripts.
func (b *FilamentBridge) migratePrinterDebugLog() error {
	b.db.Exec(`ALTER TABLE printer_configs ADD COLUMN debug_log INTEGER DEFAULT 0`)
	_, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS print_debug_logs (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_id       TEXT    NOT NULL,
			job_id           INTEGER NOT NULL,
			print_history_id INTEGER,
			logged_at        DATETIME NOT NULL DEFAULT (datetime('now')),
			message          TEXT    NOT NULL
		)`)
	if err != nil {
		return fmt.Errorf("migratePrinterDebugLog: %w", err)
	}
	b.db.Exec(`CREATE INDEX IF NOT EXISTS idx_pdl_hist ON print_debug_logs(print_history_id)`)
	b.db.Exec(`CREATE INDEX IF NOT EXISTS idx_pdl_job  ON print_debug_logs(printer_id, job_id)`)
	return nil
}

// migrateToolheadLocations creates the toolhead_locations table for per-toolhead
// Spoolman location assignments.
func (b *FilamentBridge) migrateToolheadLocations() error {
	_, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS toolhead_locations (
			printer_id    TEXT    NOT NULL,
			toolhead_id   INTEGER NOT NULL,
			location_name TEXT    NOT NULL DEFAULT '',
			updated_at    DATETIME NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (printer_id, toolhead_id)
		)`)
	if err != nil {
		return fmt.Errorf("migrateToolheadLocations: %w", err)
	}
	return nil
}

// AppendPrintDebugLog writes one line to the in-progress debug transcript for
// the given printer+job pair. Called only when DebugLog is enabled on the config.
func (b *FilamentBridge) AppendPrintDebugLog(printerID string, jobID int, message string) error {
	_, err := b.db.Exec(
		`INSERT INTO print_debug_logs (printer_id, job_id, message) VALUES (?, ?, ?)`,
		printerID, jobID, message)
	return err
}

// LinkDebugLogsToPrint sets print_history_id on all unlinked rows for a printer+job.
// Called once the print_history record has been written.
func (b *FilamentBridge) LinkDebugLogsToPrint(printerID string, jobID int, printHistoryID int) error {
	_, err := b.db.Exec(
		`UPDATE print_debug_logs SET print_history_id = ?
		 WHERE printer_id = ? AND job_id = ? AND print_history_id IS NULL`,
		printHistoryID, printerID, jobID)
	return err
}

// GetPrintDebugLog returns the full debug transcript for a print as plain text.
func (b *FilamentBridge) GetPrintDebugLog(printHistoryID int) (string, error) {
	rows, err := b.db.Query(
		`SELECT logged_at, message FROM print_debug_logs
		 WHERE print_history_id = ? ORDER BY id ASC`, printHistoryID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var ts, msg string
		if err := rows.Scan(&ts, &msg); err != nil {
			return "", err
		}
		lines = append(lines, ts+" "+msg)
	}
	return strings.Join(lines, "\n"), nil
}

// HasPrintDebugLog reports whether any debug log rows exist for the given print.
func (b *FilamentBridge) HasPrintDebugLog(printHistoryID int) bool {
	var n int
	b.db.QueryRow(`SELECT COUNT(*) FROM print_debug_logs WHERE print_history_id = ?`, printHistoryID).Scan(&n)
	return n > 0
}

// ActivePrintSession records a print that is currently in progress so a restart
// can detect orphaned sessions and write recovery history rows.
type ActivePrintSession struct {
	PrinterID              string
	JobID                  int
	StartedAt              time.Time
	FilePath               string
	FileSizeBytes          int64
	GcodeMetadataJSON      string
	GcodeLocalPath         string
	InitialAssignmentsJSON string
	LastSeenProgress       float64
	LastSeenTimePrinting   int
	ChangeCount            int
}

// UpsertActivePrintSession inserts a new active session record. ON CONFLICT DO NOTHING
// so that re-entering PRINTING for the same job (e.g. after ATTENTION) is idempotent.
func (b *FilamentBridge) UpsertActivePrintSession(printerID string, jobID int, startedAt time.Time, filePath string, fileSizeBytes int64, assignmentsJSON string) error {
	_, err := b.db.Exec(`
		INSERT INTO active_print_sessions
			(printer_id, job_id, started_at, file_path, file_size_bytes, initial_assignments_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(printer_id, job_id) DO NOTHING`,
		printerID, jobID,
		startedAt.UTC().Format(time.RFC3339Nano),
		filePath, fileSizeBytes, assignmentsJSON)
	return err
}

// GetActivePrintSession returns the active session for a printer/job pair, or nil if none.
func (b *FilamentBridge) GetActivePrintSession(printerID string, jobID int) (*ActivePrintSession, error) {
	var s ActivePrintSession
	var startedAt string
	var filePath, gcodeMeta, gcodeLocal, assignJSON sql.NullString
	var fileSizeBytes sql.NullInt64
	var lastProgress sql.NullFloat64
	var lastTimePrinting sql.NullInt64
	err := b.db.QueryRow(`
		SELECT printer_id, job_id, started_at, file_path, file_size_bytes,
		       gcode_metadata_json, gcode_local_path, initial_assignments_json,
		       last_seen_progress, last_seen_time_printing, change_count
		FROM active_print_sessions WHERE printer_id = ? AND job_id = ?`,
		printerID, jobID).Scan(
		&s.PrinterID, &s.JobID, &startedAt, &filePath, &fileSizeBytes,
		&gcodeMeta, &gcodeLocal, &assignJSON,
		&lastProgress, &lastTimePrinting, &s.ChangeCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	if s.StartedAt.IsZero() {
		s.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
	}
	if filePath.Valid {
		s.FilePath = filePath.String
	}
	if fileSizeBytes.Valid {
		s.FileSizeBytes = fileSizeBytes.Int64
	}
	if gcodeMeta.Valid {
		s.GcodeMetadataJSON = gcodeMeta.String
	}
	if gcodeLocal.Valid {
		s.GcodeLocalPath = gcodeLocal.String
	}
	if assignJSON.Valid {
		s.InitialAssignmentsJSON = assignJSON.String
	}
	if lastProgress.Valid {
		s.LastSeenProgress = lastProgress.Float64
	}
	if lastTimePrinting.Valid {
		s.LastSeenTimePrinting = int(lastTimePrinting.Int64)
	}
	return &s, nil
}

// UpdateSessionProgress persists the latest progress/time into the active session row.
func (b *FilamentBridge) UpdateSessionProgress(printerID string, jobID int, progress float64, timePrinting int) error {
	_, err := b.db.Exec(`
		UPDATE active_print_sessions
		SET last_seen_progress = ?, last_seen_time_printing = ?
		WHERE printer_id = ? AND job_id = ?`,
		progress, timePrinting, printerID, jobID)
	return err
}

// DeleteActivePrintSession removes the active session record once the print has
// been processed (finished, cancelled, or recovered on restart).
func (b *FilamentBridge) DeleteActivePrintSession(printerID string, jobID int) error {
	_, err := b.db.Exec(`DELETE FROM active_print_sessions WHERE printer_id = ? AND job_id = ?`,
		printerID, jobID)
	return err
}

// DeleteRecoveryStubs removes any print_history rows with recovered=1 that were
// created for baseFilename when the session was orphaned at restart. Called after
// a normal completion row has been committed so the stub is no longer needed.
func (b *FilamentBridge) DeleteRecoveryStubs(printerName, baseFilename string) {
	stubName := baseFilename + " [RECOVERED]"
	if _, err := b.db.Exec(`DELETE FROM print_history WHERE recovered=1 AND printer_name=? AND job_name=?`,
		printerName, stubName); err != nil {
		log.Printf("[RECONCILE] failed to delete recovery stub for %s/%s: %v", printerName, baseFilename, err)
	}
}

// ReconcileActiveSessions runs once at startup to salvage prints that were in-progress
// when the service last restarted. For each orphaned session a recovery print_history
// row is written (recovered=1, gcode_unavailable=1) so the print is not silently lost.
func (b *FilamentBridge) ReconcileActiveSessions() {
	type orphan struct {
		printerID        string
		printerName      string
		jobID            int
		startedAt        string
		filePath         string
		lastProgress     float64
		lastTimePrinting int
	}

	rows, err := b.db.Query(`
		SELECT s.printer_id, COALESCE(pc.name, s.printer_id),
		       s.job_id, s.started_at,
		       COALESCE(s.file_path, ''),
		       COALESCE(s.last_seen_progress, 0),
		       COALESCE(s.last_seen_time_printing, 0)
		FROM active_print_sessions s
		LEFT JOIN printer_configs pc ON pc.printer_id = s.printer_id
		WHERE NOT EXISTS (
			SELECT 1 FROM processed_jobs p
			WHERE p.printer_id = s.printer_id AND p.job_id = s.job_id
		)`)
	if err != nil {
		log.Printf("[RECONCILE] query failed: %v", err)
		return
	}
	defer rows.Close()

	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.printerID, &o.printerName, &o.jobID, &o.startedAt,
			&o.filePath, &o.lastProgress, &o.lastTimePrinting); err != nil {
			log.Printf("[RECONCILE] scan error: %v", err)
			continue
		}
		orphans = append(orphans, o)
	}

	if len(orphans) == 0 {
		log.Printf("[RECONCILE] no orphaned sessions")
		return
	}

	for _, o := range orphans {
		log.Printf("[RECONCILE] orphaned session: printer=%s job=%d progress=%.1f%% file=%s",
			o.printerID, o.jobID, o.lastProgress, o.filePath)

		startTime, _ := time.Parse(time.RFC3339Nano, o.startedAt)
		if startTime.IsZero() {
			startTime, _ = time.Parse(time.RFC3339, o.startedAt)
		}
		if startTime.IsZero() {
			startTime = time.Now().Add(-time.Hour)
		}

		jobName := filepath.Base(o.filePath)
		if jobName == "." || jobName == "" {
			jobName = "recovered-job"
		}
		jobName += " [RECOVERED]"
		printTimeMin := float64(o.lastTimePrinting) / 60.0
		sessionID := newSessionID()

		res, insertErr := b.db.Exec(`
			INSERT INTO print_history
				(printer_name, toolhead_id, spool_id, filament_used,
				 print_started, print_finished, job_name,
				 print_time_minutes, status, session_id, source,
				 time_precision, filament_precision,
				 outcome, progress_at_stop, recovered, gcode_unavailable)
			VALUES (?, 0, 0, 0, ?, ?, ?, ?, 'completed', ?, 'prusalink',
			        'approximate', 'estimated',
			        'recovered', ?, 1, 1)`,
			o.printerName,
			startTime.UTC().Format(time.RFC3339),
			time.Now().UTC().Format(time.RFC3339),
			jobName, printTimeMin, sessionID,
			o.lastProgress)
		if insertErr != nil {
			log.Printf("[RECONCILE] failed to write history row for printer=%s job=%d: %v",
				o.printerID, o.jobID, insertErr)
		} else {
			printID64, _ := res.LastInsertId()
			log.Printf("[RECONCILE] wrote recovery row id=%d for printer=%s job=%d",
				printID64, o.printerID, o.jobID)
			if printID64 > 0 {
				if sErr := b.SnapshotAssignmentsForPrint(int(printID64), o.printerID, startTime); sErr != nil {
					log.Printf("[RECONCILE] snapshot failed for print %d: %v", printID64, sErr)
				}
				if o.filePath != "" {
					capturedID := printID64
					capturedPath := o.filePath
					capturedPrinterID := o.printerID
					go func() {
						configs, err := b.GetAllPrinterConfigs()
						if err != nil {
							log.Printf("[RECONCILE] could not load printer configs for thumbnail backfill (print %d): %v", capturedID, err)
							return
						}
						cfg, ok := configs[capturedPrinterID]
						if !ok || cfg.IPAddress == "" || cfg.IsVirtual {
							return
						}
						b.mutex.RLock()
						bConfig := b.config
						b.mutex.RUnlock()
						if bConfig == nil {
							return
						}
						client := NewPrusaLinkClient(cfg.IPAddress, cfg.APIKey, bConfig.PrusaLinkTimeout, bConfig.PrusaLinkFileDownloadTimeout)
						gcodeContent, err := client.GetGcodeFileWithRetry(capturedPath, bConfig.PrusaLinkFileDownloadTimeout)
						if err != nil {
							log.Printf("[RECONCILE] gcode download failed for recovered print %d: %v", capturedID, err)
							return
						}
						_, thumbnailB64 := ParseGcodeMetadata(gcodeContent)
						if thumbnailB64 == "" {
							return
						}
						if _, err := b.db.Exec(`UPDATE print_history SET thumbnail_path=? WHERE id=?`, thumbnailB64, capturedID); err != nil {
							log.Printf("[RECONCILE] failed to update thumbnail for recovered print %d: %v", capturedID, err)
							return
						}
						log.Printf("[RECONCILE] thumbnail backfilled for recovered print %d", capturedID)
						_ = b.savePrintFile(int(capturedID), "gcode", filepath.Base(capturedPath), gcodeContent)
					}()
				}
			}
		}

		if mErr := b.MarkJobProcessed(o.printerID, o.jobID, "recovered"); mErr != nil {
			log.Printf("[RECONCILE] mark processed failed: printer=%s job=%d: %v", o.printerID, o.jobID, mErr)
		}
		if dErr := b.DeleteActivePrintSession(o.printerID, o.jobID); dErr != nil {
			log.Printf("[RECONCILE] delete session failed: printer=%s job=%d: %v", o.printerID, o.jobID, dErr)
		}
	}
}

// PrintAttachment represents a file attached to a print history record.
type PrintAttachment struct {
	ID             int    `json:"id"`
	PrintHistoryID int    `json:"print_history_id"`
	FileType       string `json:"file_type"` // "gcode", "slicer", "other"
	Filename       string `json:"filename"`
	FileSize       int64  `json:"file_size"`
	FilePath       string `json:"file_path"` // relative to DataDir
	StoredAt       string `json:"stored_at"`
}

// SavePrintAttachment inserts a print attachment record into the database.
func (b *FilamentBridge) SavePrintAttachment(printHistoryID int, fileType, filename, filePath string, fileSize int64) (int64, error) {
	res, err := b.db.Exec(`
		INSERT INTO print_attachments (print_history_id, file_type, filename, file_size, file_path)
		VALUES (?, ?, ?, ?, ?)`,
		printHistoryID, fileType, filename, fileSize, filePath,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to save print attachment: %w", err)
	}
	return res.LastInsertId()
}

// GetPrintAttachments returns all attachments for a given print history record.
func (b *FilamentBridge) GetPrintAttachments(printHistoryID int) ([]PrintAttachment, error) {
	rows, err := b.db.Query(`
		SELECT id, print_history_id, file_type, filename, file_size, file_path, stored_at
		FROM print_attachments WHERE print_history_id = ? ORDER BY stored_at ASC`,
		printHistoryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PrintAttachment
	for rows.Next() {
		var a PrintAttachment
		if err := rows.Scan(&a.ID, &a.PrintHistoryID, &a.FileType, &a.Filename, &a.FileSize, &a.FilePath, &a.StoredAt); err != nil {
			continue
		}
		out = append(out, a)
	}
	if out == nil {
		out = []PrintAttachment{}
	}
	return out, nil
}

// GetPrintAttachment returns a single attachment by ID.
func (b *FilamentBridge) GetPrintAttachment(id int) (*PrintAttachment, error) {
	var a PrintAttachment
	err := b.db.QueryRow(`
		SELECT id, print_history_id, file_type, filename, file_size, file_path, stored_at
		FROM print_attachments WHERE id = ?`, id,
	).Scan(&a.ID, &a.PrintHistoryID, &a.FileType, &a.Filename, &a.FileSize, &a.FilePath, &a.StoredAt)
	if err != nil {
		return nil, fmt.Errorf("attachment %d not found: %w", id, err)
	}
	return &a, nil
}

// DeletePrintAttachment removes the DB record and the file from disk.
func (b *FilamentBridge) DeletePrintAttachment(id int) error {
	a, err := b.GetPrintAttachment(id)
	if err != nil {
		return err
	}
	absPath := filepath.Join(b.dataDir(), a.FilePath)
	if rmErr := os.Remove(absPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		log.Printf("Warning: could not delete attachment file %s: %v", absPath, rmErr)
	}
	_, err = b.db.Exec(`DELETE FROM print_attachments WHERE id = ?`, id)
	return err
}

// dataDir returns the resolved data directory for file attachments.
func (b *FilamentBridge) dataDir() string {
	if b.config != nil && b.config.DataDir != "" {
		return b.config.DataDir
	}
	return "the-moment-data"
}

// fileTypeFromName returns the attachment file_type based on filename extension.
func fileTypeFromName(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".gcode", ".bgcode", ".gco", ".g":
		return "gcode"
	case ".3mf", ".orcaslicer", ".prusaslicer", ".fcstd", ".amf", ".step", ".stp":
		return "slicer"
	default:
		return "other"
	}
}

// savePrintFile writes content to disk under DataDir and inserts a print_attachments record.
// fileType should be "gcode", "slicer", or "other". Safe to call even when DataDir is not yet set.
func (b *FilamentBridge) savePrintFile(printID int, fileType, filename string, content []byte) error {
	dir := b.dataDir()
	now := time.Now()
	subDir := filepath.Join(dir, "print-files", fmt.Sprintf("%d", now.Year()), fmt.Sprintf("%02d", int(now.Month())))
	if err := os.MkdirAll(subDir, 0755); err != nil {
		return fmt.Errorf("failed to create attachment directory: %w", err)
	}
	safeName := filepath.Base(filename)
	relPath := filepath.Join("print-files", fmt.Sprintf("%d", now.Year()), fmt.Sprintf("%02d", int(now.Month())),
		fmt.Sprintf("%d_%s", printID, safeName))
	absPath := filepath.Join(dir, relPath)
	if err := os.WriteFile(absPath, content, 0644); err != nil {
		return fmt.Errorf("failed to write attachment file: %w", err)
	}
	_, err := b.SavePrintAttachment(printID, fileType, safeName, relPath, int64(len(content)))
	return err
}

// ToolheadSpoolAssignment represents a spool-to-toolhead assignment record.
type ToolheadSpoolAssignment struct {
	ID               int
	PrinterID        string
	ToolheadIndex    int
	SpoolmanSpoolID  int
	AssignedAt       time.Time
	UnassignedAt     *time.Time
	AssignmentReason string
}

// PrintSpoolEvent represents a spool event tied to a print record.
type PrintSpoolEvent struct {
	ID                 int
	PrintHistoryID     int
	ToolheadIndex      int
	OldSpoolmanSpoolID *int
	NewSpoolmanSpoolID int
	EventType          string
	EventTime          time.Time
}

// GetCurrentAssignment returns the active assignment for a printer/toolhead slot, or nil if none.
func (b *FilamentBridge) GetCurrentAssignment(printerID string, toolheadIndex int) (*ToolheadSpoolAssignment, error) {
	row := b.db.QueryRow(`
		SELECT id, printer_id, toolhead_index, spoolman_spool_id, assigned_at, assignment_reason
		FROM toolhead_spool_assignments
		WHERE printer_id = ? AND toolhead_index = ? AND unassigned_at IS NULL
	`, printerID, toolheadIndex)

	var a ToolheadSpoolAssignment
	err := row.Scan(&a.ID, &a.PrinterID, &a.ToolheadIndex, &a.SpoolmanSpoolID, &a.AssignedAt, &a.AssignmentReason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetCurrentAssignment: %w", err)
	}
	return &a, nil
}

// GetAllCurrentAssignments returns all active assignments for a printer.
func (b *FilamentBridge) GetAllCurrentAssignments(printerID string) ([]ToolheadSpoolAssignment, error) {
	rows, err := b.db.Query(`
		SELECT id, printer_id, toolhead_index, spoolman_spool_id, assigned_at, assignment_reason
		FROM toolhead_spool_assignments
		WHERE printer_id = ? AND unassigned_at IS NULL
		ORDER BY toolhead_index
	`, printerID)
	if err != nil {
		return nil, fmt.Errorf("GetAllCurrentAssignments: %w", err)
	}
	defer rows.Close()

	var assignments []ToolheadSpoolAssignment
	for rows.Next() {
		var a ToolheadSpoolAssignment
		if err := rows.Scan(&a.ID, &a.PrinterID, &a.ToolheadIndex, &a.SpoolmanSpoolID, &a.AssignedAt, &a.AssignmentReason); err != nil {
			return nil, fmt.Errorf("GetAllCurrentAssignments scan: %w", err)
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

// SetAssignment closes any existing open assignment for the slot, then inserts a new one.
// assigned_at is stored as a Go time.Time so sub-second comparisons in
// SnapshotAssignmentsForPrint are reliable (SQLite's datetime('now') has only
// second precision and uses a different string format).
func (b *FilamentBridge) SetAssignment(printerID string, toolheadIndex int, spoolmanSpoolID int, reason string) error {
	if err := b.CloseAssignment(printerID, toolheadIndex); err != nil {
		return err
	}
	_, err := b.db.Exec(`
		INSERT INTO toolhead_spool_assignments
			(printer_id, toolhead_index, spoolman_spool_id, assignment_reason, assigned_at)
		VALUES (?, ?, ?, ?, ?)
	`, printerID, toolheadIndex, spoolmanSpoolID, reason, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("SetAssignment insert: %w", err)
	}
	return nil
}

// CloseAssignment marks the active assignment for a slot as unassigned.
func (b *FilamentBridge) CloseAssignment(printerID string, toolheadIndex int) error {
	_, err := b.db.Exec(`
		UPDATE toolhead_spool_assignments
		SET unassigned_at = ?
		WHERE printer_id = ? AND toolhead_index = ? AND unassigned_at IS NULL
	`, time.Now().UTC(), printerID, toolheadIndex)
	if err != nil {
		return fmt.Errorf("CloseAssignment: %w", err)
	}
	return nil
}

// GetPrintSpoolEvents returns all spool events for a given print history ID.
func (b *FilamentBridge) GetPrintSpoolEvents(printHistoryID int) ([]PrintSpoolEvent, error) {
	rows, err := b.db.Query(`
		SELECT id, print_history_id, toolhead_index, old_spoolman_spool_id, new_spoolman_spool_id, event_type, event_time
		FROM print_spool_events
		WHERE print_history_id = ?
		ORDER BY event_time
	`, printHistoryID)
	if err != nil {
		return nil, fmt.Errorf("GetPrintSpoolEvents: %w", err)
	}
	defer rows.Close()

	var events []PrintSpoolEvent
	for rows.Next() {
		var e PrintSpoolEvent
		var oldID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.PrintHistoryID, &e.ToolheadIndex, &oldID, &e.NewSpoolmanSpoolID, &e.EventType, &e.EventTime); err != nil {
			return nil, fmt.Errorf("GetPrintSpoolEvents scan: %w", err)
		}
		if oldID.Valid {
			v := int(oldID.Int64)
			e.OldSpoolmanSpoolID = &v
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// AddPrintSpoolEvent inserts a spool event for a print.
func (b *FilamentBridge) AddPrintSpoolEvent(event PrintSpoolEvent) error {
	_, err := b.db.Exec(`
		INSERT INTO print_spool_events (print_history_id, toolhead_index, old_spoolman_spool_id, new_spoolman_spool_id, event_type)
		VALUES (?, ?, ?, ?, ?)
	`, event.PrintHistoryID, event.ToolheadIndex, event.OldSpoolmanSpoolID, event.NewSpoolmanSpoolID, event.EventType)
	if err != nil {
		return fmt.Errorf("AddPrintSpoolEvent: %w", err)
	}
	return nil
}

// SnapshotAssignmentsForPrint records spool assignments as 'start' events for a print
// history record. atTime is the moment the print was first detected; assignments active
// at that instant are captured. Pass a zero Time to snapshot current assignments instead
// (used when the original start time is unavailable, e.g. retried G-code downloads).
func (b *FilamentBridge) SnapshotAssignmentsForPrint(printHistoryID int, printerID string, atTime time.Time) error {
	var (
		rows *sql.Rows
		err  error
	)
	if atTime.IsZero() {
		rows, err = b.db.Query(`
			SELECT toolhead_index, spoolman_spool_id
			FROM toolhead_spool_assignments
			WHERE printer_id = ? AND unassigned_at IS NULL
			ORDER BY toolhead_index
		`, printerID)
	} else {
		// Use julianday() so SQLite compares as REAL (floating-point Julian day number)
		// rather than as TEXT. Direct TEXT comparison of two same-format T+Z timestamps
		// produces incorrect results in go-sqlite3 v1.14.x due to implicit type coercion
		// with DATETIME (NUMERIC affinity) columns. julianday() parses the ISO 8601 string
		// and returns a double — comparison is then precise and unambiguous.
		atStr := atTime.UTC().Format(time.RFC3339Nano)
		rows, err = b.db.Query(`
			SELECT toolhead_index, spoolman_spool_id
			FROM toolhead_spool_assignments
			WHERE printer_id = ?
			  AND julianday(assigned_at) <= julianday(?)
			  AND (unassigned_at IS NULL OR julianday(unassigned_at) > julianday(?))
			ORDER BY toolhead_index
		`, printerID, atStr, atStr)
	}
	if err != nil {
		return fmt.Errorf("SnapshotAssignmentsForPrint query: %w", err)
	}

	// Collect all rows before closing the cursor; inserting while a read cursor is
	// open causes "database is locked" with SQLite's default journal mode.
	type slot struct{ toolhead, spool int }
	var slots []slot
	for rows.Next() {
		var s slot
		if err := rows.Scan(&s.toolhead, &s.spool); err != nil {
			rows.Close()
			return fmt.Errorf("SnapshotAssignmentsForPrint scan: %w", err)
		}
		slots = append(slots, s)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("SnapshotAssignmentsForPrint rows.Close: %w", err)
	}

	for _, s := range slots {
		event := PrintSpoolEvent{
			PrintHistoryID:     printHistoryID,
			ToolheadIndex:      s.toolhead,
			OldSpoolmanSpoolID: nil,
			NewSpoolmanSpoolID: s.spool,
			EventType:          "start",
		}
		if err := b.AddPrintSpoolEvent(event); err != nil {
			return err
		}
	}
	return nil
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
		ConfigKeyPollInterval:                    fmt.Sprintf("%d", DefaultPollInterval),
		ConfigKeyWebPort:                         DefaultWebPort,
		ConfigKeyPrusaLinkTimeout:                fmt.Sprintf("%d", PrusaLinkTimeout),
		ConfigKeyPrusaLinkFileDownloadTimeout:    fmt.Sprintf("%d", PrusaLinkFileDownloadTimeout),
		ConfigKeySpoolmanTimeout:                 fmt.Sprintf("%d", SpoolmanTimeout),
		ConfigKeyAutoAssignPreviousSpoolEnabled:  "false",     // Enable auto-assignment of previous spool to default location
		ConfigKeyAutoAssignPreviousSpoolLocation: "",          // Default location name for auto-assigned previous spools
		ConfigKeyNFCTrashLocation:                "Trash",     // Location for empty/done spools (tag ready to re-program)
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
	rows, err := b.db.Query("SELECT printer_id, name, model, ip_address, api_key, toolheads, COALESCE(is_virtual, 0), COALESCE(printer_type, 'prusalink'), COALESCE(debug_log, 0) FROM printer_configs")
	if err != nil {
		return nil, fmt.Errorf("failed to get printer configs: %w", err)
	}
	defer rows.Close()

	configs := make(map[string]PrinterConfig)
	for rows.Next() {
		var printerID, name, model, ipAddress, apiKey, printerType string
		var toolheads int
		var isVirtual, debugLog bool
		if err := rows.Scan(&printerID, &name, &model, &ipAddress, &apiKey, &toolheads, &isVirtual, &printerType, &debugLog); err != nil {
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
			DebugLog:    debugLog,
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
	debugLogInt := 0
	if config.DebugLog {
		debugLogInt = 1
	}
	printerType := config.PrinterType
	if printerType == "" {
		printerType = PrinterTypePrusaLink
	}
	_, err := b.db.Exec(`
		INSERT OR REPLACE INTO printer_configs (printer_id, name, model, ip_address, api_key, toolheads, is_virtual, printer_type, debug_log)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, printerID, config.Name, config.Model, config.IPAddress, config.APIKey, config.Toolheads, isVirtualInt, printerType, debugLogInt)
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

// GetToolheadLocation returns the configured Spoolman location name for a toolhead.
// Returns "" with no error when no location has been configured.
func (b *FilamentBridge) GetToolheadLocation(printerID string, toolheadID int) (string, error) {
	var locationName string
	err := b.db.QueryRow(
		"SELECT location_name FROM toolhead_locations WHERE printer_id = ? AND toolhead_id = ?",
		printerID, toolheadID,
	).Scan(&locationName)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get toolhead location: %w", err)
	}
	return locationName, nil
}

// SetToolheadLocation saves the Spoolman location name for a toolhead.
func (b *FilamentBridge) SetToolheadLocation(printerID string, toolheadID int, locationName string) error {
	_, err := b.db.Exec(
		`INSERT OR REPLACE INTO toolhead_locations (printer_id, toolhead_id, location_name, updated_at)
		 VALUES (?, ?, ?, datetime('now'))`,
		printerID, toolheadID, strings.TrimSpace(locationName),
	)
	if err != nil {
		return fmt.Errorf("failed to set toolhead location: %w", err)
	}
	return nil
}

// GetAllToolheadLocations returns all configured Spoolman location names for a printer,
// keyed by toolhead index.
func (b *FilamentBridge) GetAllToolheadLocations(printerID string) (map[int]string, error) {
	rows, err := b.db.Query(
		"SELECT toolhead_id, location_name FROM toolhead_locations WHERE printer_id = ? ORDER BY toolhead_id",
		printerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get toolhead locations: %w", err)
	}
	defer rows.Close()

	locs := make(map[int]string)
	for rows.Next() {
		var toolheadID int
		var locationName string
		if err := rows.Scan(&toolheadID, &locationName); err != nil {
			return nil, fmt.Errorf("failed to scan toolhead location row: %w", err)
		}
		locs[toolheadID] = locationName
	}
	return locs, nil
}

// syncSpoolLocation moves a newly-assigned spool to the toolhead's configured Spoolman
// location. No-op if no location is configured for the toolhead.
func (b *FilamentBridge) syncSpoolLocation(printerID string, toolheadID int, spoolID int) error {
	locationName, err := b.GetToolheadLocation(printerID, toolheadID)
	if err != nil || locationName == "" {
		return nil
	}
	if err := b.spoolman.UpdateSpoolLocation(spoolID, locationName); err != nil {
		return fmt.Errorf("syncSpoolLocation spool %d to %q: %w", spoolID, locationName, err)
	}
	log.Printf("Synced spool %d location to %q (printer %s toolhead %d)", spoolID, locationName, printerID, toolheadID)
	return nil
}

// syncSpoolLocationForUnassignment moves a spool that was removed from a toolhead to
// the configured default location, if the auto-assign feature is enabled.
func (b *FilamentBridge) syncSpoolLocationForUnassignment(spoolID int) error {
	enabled, err := b.GetAutoAssignPreviousSpoolEnabled()
	if err != nil || !enabled {
		return nil
	}
	locationName, err := b.GetAutoAssignPreviousSpoolLocation()
	if err != nil || locationName == "" {
		return nil
	}
	loc, err := b.spoolman.FindLocationByName(locationName)
	if err != nil || loc == nil {
		log.Printf("syncSpoolLocationForUnassignment: location %q not found in Spoolman, skipping spool %d", locationName, spoolID)
		return nil
	}
	if err := b.spoolman.UpdateSpoolLocation(spoolID, locationName); err != nil {
		return fmt.Errorf("syncSpoolLocationForUnassignment spool %d to %q: %w", spoolID, locationName, err)
	}
	log.Printf("Synced unassigned spool %d to default location %q", spoolID, locationName)
	return nil
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
		b.spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout)
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
	b.spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout)

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

	// Sync the new spool's location to the configured per-toolhead Spoolman location.
	printerConfigs, cfgErr := b.GetAllPrinterConfigs()
	if cfgErr == nil {
		for pid, cfg := range printerConfigs {
			if cfg.Name == printerName {
				if syncErr := b.syncSpoolLocation(pid, toolheadID, spoolID); syncErr != nil {
					log.Printf("Warning: SetToolheadMapping location sync: %v", syncErr)
				}
				break
			}
		}
	}

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

// LogPrintUsageFull is the full version with print time, status, thumbnail, session, and source.
// source should be "prusalink", "virtual", or "octoprint".
// Cost is automatically calculated and saved after the insert (outside the mutex).
// Returns the new print_history row ID so callers can link spool events to it.
func (b *FilamentBridge) LogPrintUsageFull(printerName string, toolheadID int, spoolID int,
	filamentUsed float64, jobName string, printTimeMinutes float64, status string, thumbnailBase64 string,
	sessionID string, source string) (int, error) {
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
		return 0, fmt.Errorf("failed to log print usage: %w", err)
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

	return int(printID64), nil
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

	// Monitor each printer (OctoPrint uses push — skip it here)
	for printerID, printerConfig := range configSnapshot.Printers {
		if printerID == "no_printers" {
			continue // Skip placeholder
		}
		if printerConfig.PrinterType == PrinterTypeOctoPrint {
			continue // OctoPrint pushes data; no polling needed
		}
		if printerConfig.IsVirtual {
			continue // Virtual printers have no hardware to poll
		}

		monitorFn := b.monitorPrusaLink
		if printerConfig.PrinterType == PrinterTypeBambu {
			monitorFn = b.monitorBambu
		}

		go func(printerID string, config PrinterConfig, fn func(string, PrinterConfig) error) {
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

			if err := fn(printerID, config); err != nil {
				log.Printf("Error monitoring printer %s (%s): %v", config.IPAddress, printerID, err)
			}
		}(printerID, printerConfig, monitorFn)
	}
}

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
	// Enforce per-printer variable poll intervals. next_poll_at is written by
	// UpsertLastKnownState based on the last observed state (ATTENTION=3s,
	// PRINTING=5s, IDLE=20s, etc.). With the current 30s global ticker this
	// guard is mostly a no-op, but it activates automatically when the ticker
	// is tightened in future.
	if cached, err := b.GetLastKnownState(printerID); err == nil {
		if !cached.NextPollAt.IsZero() && time.Now().Before(cached.NextPollAt) {
			return nil
		}
	}

	client := NewPrusaLinkClient(config.IPAddress, config.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)

	status, err := client.GetStatus()
	if err != nil {
		failures, _ := b.IncrementFailureCount(printerID)
		if failures == 1 {
			log.Printf("[PRUSALINK] printer=%s poll_failure=1 error=%v", printerID, err)
		} else if failures >= 3 {
			log.Printf("[PRUSALINK] printer=%s UNREACHABLE consecutive_failures=%d", printerID, failures)
		}
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
	currentJobDisplay := ""
	printProgress := jobInfo.Progress // 0..100

	if jobInfo.File.Name != "" {
		currentJobDisplay = jobInfo.File.DisplayName
		if currentJobDisplay == "" {
			currentJobDisplay = jobInfo.File.Name
		}
		jobName = currentJobDisplay
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
	logStateTransition(printerID, prevState, currentState, jobInfo.ID, printProgress)

	// Debug poll log — one line per poll while a job is active.
	if config.DebugLog && jobInfo.ID != 0 {
		filamentStr := "none"
		if len(jobInfo.Filament) > 0 {
			parts := make([]string, len(jobInfo.Filament))
			for i, f := range jobInfo.Filament {
				parts[i] = fmt.Sprintf("T%d:%.0fmm/%.3fg", f.ToolheadID, f.Length, f.Weight)
			}
			filamentStr = strings.Join(parts, " ")
		}
		_ = b.AppendPrintDebugLog(printerID, jobInfo.ID, fmt.Sprintf(
			"[POLL] state=%s progress=%.1f%% time_printing=%ds time_left=%ds job_id=%d file=%q filament=%s",
			currentState, printProgress,
			status.Printer.Telemetry.PrintTime,
			status.Printer.Telemetry.PrintTimeLeft,
			jobInfo.ID, currentJobDisplay, filamentStr))
	}

	// Persist state so restarts can resume tracking correctly.
	defer func() {
		if uErr := b.UpsertLastKnownState(printerID, currentState, jobInfo.ID, printProgress, jobInfo.TimePrinting); uErr != nil {
			log.Printf("[PRUSALINK] printer=%s failed to upsert state cache: %v", printerID, uErr)
		}
	}()

	switch currentState {

	case StatePrinting:
		// Print is active — store the filename on first detection, keep wasPrinting=true
		isNewPrint := false
		b.mutex.Lock()
		if currentJobFilename != "" && storedJobFile == "" {
			b.currentJobFile[printerID] = currentJobFilename
			b.currentJobDisplayName[printerID] = currentJobDisplay
			b.printStartTime[printerID] = time.Now()
			isNewPrint = true
			log.Printf("📁 Stored job filename for %s (%s): %s (display: %s)", config.IPAddress, printerID, currentJobFilename, currentJobDisplay)
		}
		if jobInfo.ID != 0 {
			b.currentJobID[printerID] = jobInfo.ID
		}
		b.wasPrinting[printerID] = true
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

		if isNewPrint && jobInfo.ID != 0 {
			b.mutex.RLock()
			startedAt := b.printStartTime[printerID]
			b.mutex.RUnlock()
			assignments, _ := b.GetAllCurrentAssignments(printerID)
			assignJSON, _ := json.Marshal(assignments)
			if uErr := b.UpsertActivePrintSession(printerID, jobInfo.ID, startedAt, currentJobFilename, 0, string(assignJSON)); uErr != nil {
				log.Printf("[PRUSALINK] printer=%s failed to create active session: %v", printerID, uErr)
			}
		}
		if jobInfo.ID != 0 {
			_ = b.UpdateSessionProgress(printerID, jobInfo.ID, printProgress, jobInfo.TimePrinting)
		}

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
			// PrusaLink returns jobID=0 when transitioning to IDLE/FINISHED (no active job).
			// Resolve effectiveJobID first using the ID captured during PRINTING so dedup,
			// debug log linking, and session cleanup all use the correct job ID.
			b.mutex.RLock()
			storedJobID := b.currentJobID[printerID]
			b.mutex.RUnlock()
			effectiveJobID := jobInfo.ID
			if effectiveJobID == 0 {
				effectiveJobID = storedJobID
			}

			// Check dedup before doing any work — firmware sometimes re-fires the
			// same job_id after the printer is already IDLE.
			if already, err := b.IsJobProcessed(printerID, effectiveJobID); err != nil {
				log.Printf("[PRUSALINK] printer=%s job=%d dedup check error: %v", printerID, effectiveJobID, err)
			} else if already {
				log.Printf("[PRUSALINK] printer=%s job=%d already processed — skipping duplicate FINISHED", printerID, effectiveJobID)
				b.mutex.Lock()
				b.wasPrinting[printerID] = false
				b.currentJobFile[printerID] = ""
				b.currentJobDisplayName[printerID] = ""
				b.previousState[printerID] = currentState
				b.mutex.Unlock()
				break
			}

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

			handleErr := b.handlePrusaLinkPrintFinished(printerID, config, effectiveJobID, filenameToUse)

			b.mutex.Lock()
			b.processingPrints[printerID] = false
			if handleErr == nil {
				b.currentJobFile[printerID] = ""
				b.currentJobDisplayName[printerID] = ""
				b.currentJobID[printerID] = 0
			}
			b.mutex.Unlock()

			if handleErr != nil {
				log.Printf("Error handling print finished: %v", handleErr)
			} else {
				if mErr := b.MarkJobProcessed(printerID, effectiveJobID, "finished"); mErr != nil {
					log.Printf("[PRUSALINK] printer=%s job=%d failed to mark processed: %v", printerID, effectiveJobID, mErr)
				}
				if effectiveJobID != 0 {
					if dErr := b.DeleteActivePrintSession(printerID, effectiveJobID); dErr != nil {
						log.Printf("[PRUSALINK] printer=%s job=%d failed to delete active session: %v", printerID, effectiveJobID, dErr)
					}
				}
			}
		} else {
			// Normal idle — clear any stale tracking
			b.mutex.Lock()
			if !b.processingPrints[printerID] {
				b.currentJobFile[printerID] = ""
				b.currentJobDisplayName[printerID] = ""
			}
			b.previousState[printerID] = currentState
			b.mutex.Unlock()
		}

	case StateStopped:
		if wasPrinting {
			b.mutex.RLock()
			storedJobIDStop := b.currentJobID[printerID]
			b.mutex.RUnlock()
			effectiveJobIDStop := jobInfo.ID
			if effectiveJobIDStop == 0 {
				effectiveJobIDStop = storedJobIDStop
			}

			if already, err := b.IsJobProcessed(printerID, effectiveJobIDStop); err != nil {
				log.Printf("[PRUSALINK] printer=%s job=%d dedup check error: %v", printerID, effectiveJobIDStop, err)
			} else if already {
				log.Printf("[PRUSALINK] printer=%s job=%d already processed — skipping duplicate STOPPED", printerID, effectiveJobIDStop)
				b.mutex.Lock()
				b.wasPrinting[printerID] = false
				b.currentJobFile[printerID] = ""
				b.currentJobDisplayName[printerID] = ""
				b.previousState[printerID] = currentState
				b.mutex.Unlock()
				break
			}

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

			handleErr := b.handlePrusaLinkPrintCancelled(printerID, config, effectiveJobIDStop, filenameToUse, printProgress)

			b.mutex.Lock()
			b.processingPrints[printerID] = false
			b.currentJobFile[printerID] = ""
			b.currentJobDisplayName[printerID] = ""
			b.currentJobID[printerID] = 0
			b.mutex.Unlock()

			if handleErr != nil {
				log.Printf("Error handling cancelled print: %v", handleErr)
			} else {
				if mErr := b.MarkJobProcessed(printerID, effectiveJobIDStop, "stopped"); mErr != nil {
					log.Printf("[PRUSALINK] printer=%s job=%d failed to mark processed: %v", printerID, effectiveJobIDStop, mErr)
				}
				if effectiveJobIDStop != 0 {
					if dErr := b.DeleteActivePrintSession(printerID, effectiveJobIDStop); dErr != nil {
						log.Printf("[PRUSALINK] printer=%s job=%d failed to delete active session: %v", printerID, effectiveJobIDStop, dErr)
					}
				}
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

// monitorBambu polls one Bambu printer per ticker tick.
//
// The MQTT client is long-lived (persistent TLS connection). monitorBambu
// gets-or-creates the client on first call and reads its cached status on
// every subsequent call — no new TCP connection per tick.
//
// State machine mirrors monitorPrusaLink exactly, keyed on the same
// wasPrinting / previousState / currentJobFile maps.
func (b *FilamentBridge) monitorBambu(printerID string, config PrinterConfig) error {
	serial, accessCode, err := parseBambuCredentials(config.APIKey)
	if err != nil {
		log.Printf("[BAMBU] Bad credentials for printer %s (%s): %v", printerID, config.Name, err)
		return nil
	}

	// Get or create the persistent MQTT client for this printer.
	b.bambuMutex.Lock()
	client, exists := b.bambuClients[printerID]
	if !exists {
		client = b.bambuClientFactory(config.IPAddress, serial, accessCode)
		b.bambuClients[printerID] = client
		if err := client.Connect(); err != nil {
			b.bambuMutex.Unlock()
			log.Printf("[BAMBU] Could not connect to printer %s (%s) at %s: %v",
				printerID, config.Name, config.IPAddress, err)
			return nil
		}
	}
	b.bambuMutex.Unlock()

	status, err := client.GetCurrentStatus()
	if err != nil {
		// No MQTT message received yet — still connecting or printer is off.
		log.Printf("[BAMBU] No status yet from %s (%s): %v", printerID, config.Name, err)
		return nil
	}

	currentState := mapBambuState(status.GcodeState)
	progressPct := float64(status.McPercent)

	b.mutex.RLock()
	wasPrinting := b.wasPrinting[printerID]
	storedJobFile := b.currentJobFile[printerID]
	prevState := b.previousState[printerID]
	b.mutex.RUnlock()

	log.Printf("[BAMBU] Printer %s (%s): state=%s (prev=%s), wasPrinting=%v, progress=%.1f%%, file=%s",
		printerID, config.Name, currentState, prevState, wasPrinting, progressPct, storedJobFile)

	switch currentState {

	case StatePrinting:
		b.mutex.Lock()
		if status.GcodeFile != "" && storedJobFile == "" {
			b.currentJobFile[printerID] = status.GcodeFile
			b.printStartTime[printerID] = time.Now()
			log.Printf("[BAMBU] 📁 Stored job filename for %s: %s", printerID, status.GcodeFile)
		}
		b.wasPrinting[printerID] = true
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

	case StatePaused:
		if prevState != StatePaused {
			log.Printf("[BAMBU] ⏸️  Print paused on %s (%s)", printerID, config.Name)
		}
		b.mutex.Lock()
		b.wasPrinting[printerID] = wasPrinting
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

	case StateFinished, StateIdle:
		if wasPrinting {
			log.Printf("[BAMBU] 🎉 Print finished on %s (%s): file=%s weight=%.2fg",
				printerID, config.Name, storedJobFile, status.FilamentWeightTotal)

			b.mutex.Lock()
			b.wasPrinting[printerID] = false
			b.processingPrints[printerID] = true
			b.previousState[printerID] = currentState
			b.mutex.Unlock()

			finishErr := b.handleBambuPrintFinished(printerID, config, storedJobFile, status)

			b.mutex.Lock()
			b.processingPrints[printerID] = false
			if finishErr == nil {
				b.currentJobFile[printerID] = ""
				b.currentJobDisplayName[printerID] = ""
			}
			b.mutex.Unlock()

			if finishErr != nil {
				log.Printf("[BAMBU] Error handling print finished for %s: %v", printerID, finishErr)
			}
		} else {
			b.mutex.Lock()
			if !b.processingPrints[printerID] {
				b.currentJobFile[printerID] = ""
				b.currentJobDisplayName[printerID] = ""
			}
			b.previousState[printerID] = currentState
			b.mutex.Unlock()
		}

	case StateStopped:
		if wasPrinting {
			log.Printf("[BAMBU] 🛑 Print CANCELLED on %s (%s): progress=%.1f%%, file=%s",
				printerID, config.Name, progressPct, storedJobFile)

			b.mutex.Lock()
			b.wasPrinting[printerID] = false
			b.processingPrints[printerID] = true
			b.previousState[printerID] = currentState
			b.mutex.Unlock()

			cancelErr := b.handleBambuPrintCancelled(printerID, config, storedJobFile, status, progressPct)

			b.mutex.Lock()
			b.processingPrints[printerID] = false
			b.currentJobFile[printerID] = ""
			b.currentJobDisplayName[printerID] = ""
			b.mutex.Unlock()

			if cancelErr != nil {
				log.Printf("[BAMBU] Error handling cancelled print for %s: %v", printerID, cancelErr)
			}
		} else {
			b.mutex.Lock()
			b.previousState[printerID] = currentState
			b.mutex.Unlock()
		}

	default:
		log.Printf("[BAMBU] Unknown state %q on %s (%s)", currentState, printerID, config.Name)
		b.mutex.Lock()
		b.previousState[printerID] = currentState
		b.mutex.Unlock()
	}

	return nil
}

// handleBambuPrintFinished records a completed Bambu print.
// Unlike PrusaLink, Bambu provides filament_weight_total directly in the MQTT
// finish payload so no G-code download is needed.
func (b *FilamentBridge) handleBambuPrintFinished(printerID string, config PrinterConfig, filename string, status *BambuStatus) error {
	printerName := resolvePrinterName(config)

	filamentUsage := computeBambuFilamentUsage(status, config.Toolheads)
	if len(filamentUsage) == 0 {
		msg := fmt.Sprintf("no filament usage data from Bambu MQTT payload (weight=%.2fg, ams_slots=%d)",
			status.FilamentWeightTotal, len(status.AMSSlots))
		b.addPrintError(printerName, filename, msg)
		return fmt.Errorf("%s", msg)
	}

	log.Printf("[BAMBU] Filament usage for %s: %v", printerName, filamentUsage)

	if err := b.processFilamentUsage(printerName, filamentUsage, filename); err != nil {
		return err
	}

	b.mutex.RLock()
	startTime := b.printStartTime[printerID]
	b.mutex.RUnlock()

	sessionID := newSessionID()
	var firstPrintID int
	for toolheadID, usedG := range filamentUsage {
		spoolID, _ := b.GetToolheadMapping(printerName, toolheadID)
		printID, _ := b.LogPrintUsageFull(printerName, toolheadID, spoolID, usedG, filename,
			0, "completed", "", sessionID, "bambu")
		if printID > 0 {
			_ = b.AppendFilamentUsage(printID, toolheadID, 0, spoolID, 0, usedG)
			if firstPrintID == 0 {
				firstPrintID = printID
			}
		}
	}
	if firstPrintID > 0 {
		if err := b.SnapshotAssignmentsForPrint(firstPrintID, printerID, startTime); err != nil {
			log.Printf("[BAMBU] Warning: failed to snapshot NFC assignments for print %d: %v", firstPrintID, err)
		}
	}

	b.DeleteRecoveryStubs(printerName, filepath.Base(filename))
	return nil
}

// handleBambuPrintCancelled records a cancelled Bambu print, scaling the
// filament usage by the print progress percentage.
func (b *FilamentBridge) handleBambuPrintCancelled(printerID string, config PrinterConfig, filename string, status *BambuStatus, progressPct float64) error {
	printerName := resolvePrinterName(config)

	if progressPct <= 0 {
		log.Printf("[BAMBU] ⚠️  Cancelled print at 0%% progress on %s — skipping filament deduction", printerName)
		return nil
	}

	scale := (progressPct / 100.0) * 0.95
	if scale > 1.0 {
		scale = 1.0
	}

	fullUsage := computeBambuFilamentUsage(status, config.Toolheads)
	if len(fullUsage) == 0 {
		log.Printf("[BAMBU] No filament data for cancelled print on %s — skipping deduction", printerName)
		return nil
	}

	partialUsage := make(map[int]float64, len(fullUsage))
	for toolheadID, usedG := range fullUsage {
		partialUsage[toolheadID] = usedG * scale
	}

	log.Printf("[BAMBU] Cancelled print on %s: scale=%.3f, usage=%v", printerName, scale, partialUsage)

	if err := b.processFilamentUsage(printerName, partialUsage, filename+" [CANCELLED]"); err != nil {
		return err
	}

	b.mutex.RLock()
	startTime := b.printStartTime[printerID]
	b.mutex.RUnlock()

	sessionID := newSessionID()
	var firstPrintID int
	for toolheadID, usedG := range partialUsage {
		spoolID, _ := b.GetToolheadMapping(printerName, toolheadID)
		printID, _ := b.LogPrintUsageFull(printerName, toolheadID, spoolID, usedG, filename+" [CANCELLED]",
			0, "cancelled", "", sessionID, "bambu")
		if printID > 0 {
			_ = b.AppendFilamentUsage(printID, toolheadID, 0, spoolID, 0, usedG)
			if firstPrintID == 0 {
				firstPrintID = printID
			}
		}
	}
	if firstPrintID > 0 {
		if err := b.SnapshotAssignmentsForPrint(firstPrintID, printerID, startTime); err != nil {
			log.Printf("[BAMBU] Warning: failed to snapshot NFC assignments for cancelled print %d: %v", firstPrintID, err)
		}
	}
	return nil
}

// computeBambuFilamentUsage derives a per-toolhead filament usage map from a
// Bambu MQTT status. AMS slot indices map directly to toolhead indices.
//
// If AMS slot data is present and FilamentWeightTotal > 0, the total is
// distributed evenly across all slots that appear in status.AMSSlots. This is
// a conservative approximation — a future refinement can use pre-print slot
// snapshots for per-slot proportional allocation.
//
// Falls back to assigning the full weight to toolhead 0 when no AMS data is
// present (single-spool printer or AMS not reporting).
func computeBambuFilamentUsage(status *BambuStatus, toolheads int) map[int]float64 {
	if status.FilamentWeightTotal <= 0 {
		return nil
	}

	if len(status.AMSSlots) > 0 && toolheads > 0 {
		activeSlots := 0
		for slotIdx := 0; slotIdx < toolheads; slotIdx++ {
			if _, ok := status.AMSSlots[slotIdx]; ok {
				activeSlots++
			}
		}
		if activeSlots > 0 {
			perSlot := status.FilamentWeightTotal / float64(activeSlots)
			usage := make(map[int]float64, activeSlots)
			for slotIdx := 0; slotIdx < toolheads; slotIdx++ {
				if _, ok := status.AMSSlots[slotIdx]; ok {
					usage[slotIdx] = perSlot
				}
			}
			return usage
		}
	}

	// Fallback: single spool or no AMS data
	return map[int]float64{0: status.FilamentWeightTotal}
}

// handlePrusaLinkPrintCancelled handles a cancelled print by computing partial filament usage.
// It downloads the G-code, gets the full usage from metadata, then scales by the print progress %.
func (b *FilamentBridge) handlePrusaLinkPrintCancelled(printerID string, config PrinterConfig, jobID int, filename string, progressPct float64) error {
	printerName := resolvePrinterName(config)

	b.mutex.RLock()
	displayName := b.currentJobDisplayName[printerID]
	b.mutex.RUnlock()
	jobName := displayName
	if jobName == "" {
		jobName = filepath.Base(filename)
	}

	if config.DebugLog && jobID != 0 {
		_ = b.AppendPrintDebugLog(printerID, jobID, fmt.Sprintf(
			"[CANCELLED] progress=%.1f%% file=%q display_name=%q", progressPct, filename, displayName))
	}

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
		if config.DebugLog && jobID != 0 {
			_ = b.AppendPrintDebugLog(printerID, jobID, fmt.Sprintf("[GCODE_DOWNLOAD_FAILED] %v — queued for retry", err))
		}
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
			if config.DebugLog && jobID != 0 {
				_ = b.AppendPrintDebugLog(printerID, jobID, fmt.Sprintf(
					"[PARTIAL_FILAMENT] T%d: %.2fg (%.1f%% of %.2fg full)", toolheadID, partial, progressPct, weight))
			}
		}
	}

	if err := b.processFilamentUsage(printerName, partialUsage, jobName+" [CANCELLED]"); err != nil {
		return err
	}

	b.mutex.RLock()
	startTime := b.printStartTime[printerID]
	b.mutex.RUnlock()

	_, thumbnailB64 := ParseGcodeMetadata(gcodeContent)
	sessionID := newSessionID()
	var firstPrintID int
	for toolheadID, usedG := range partialUsage {
		spoolID, _ := b.GetToolheadMapping(printerName, toolheadID)
		printID, _ := b.LogPrintUsageFull(printerName, toolheadID, spoolID, usedG, jobName+" [CANCELLED]",
			0, "cancelled", thumbnailB64, sessionID, "prusalink")
		if printID > 0 {
			_ = b.AppendFilamentUsage(printID, toolheadID, 0, spoolID, 0, usedG)
			if firstPrintID == 0 {
				firstPrintID = printID
			}
		}
	}
	if firstPrintID > 0 {
		if err := b.SnapshotAssignmentsForPrint(firstPrintID, printerID, startTime); err != nil {
			log.Printf("Warning: failed to snapshot NFC assignments for cancelled print %d: %v", firstPrintID, err)
		}
		if err := b.savePrintFile(firstPrintID, "gcode", filepath.Base(filename), gcodeContent); err != nil {
			log.Printf("Warning: could not save gcode file for cancelled print %d: %v", firstPrintID, err)
		}
		if config.DebugLog && jobID != 0 {
			if lErr := b.LinkDebugLogsToPrint(printerID, jobID, firstPrintID); lErr != nil {
				log.Printf("[PRUSALINK] printer=%s job=%d failed to link debug logs: %v", printerID, jobID, lErr)
			}
		}
	}

	return nil
}

func (b *FilamentBridge) handlePrusaLinkPrintFinished(printerID string, config PrinterConfig, jobID int, filename string) error {
	log.Printf("Print finished via PrusaLink (%s): %s", config.IPAddress, filename)

	printerName := resolvePrinterName(config)

	// Resolve display name stored when the print started (avoids FAT16 8.3 truncation in job_name).
	b.mutex.RLock()
	displayName := b.currentJobDisplayName[printerID]
	startTime := b.printStartTime[printerID]
	b.mutex.RUnlock()
	jobName := displayName
	if jobName == "" {
		jobName = filepath.Base(filename)
	}

	if config.DebugLog && jobID != 0 {
		_ = b.AppendPrintDebugLog(printerID, jobID, fmt.Sprintf(
			"[FINISH] file=%q display_name=%q", filename, displayName))
	}

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
		if config.DebugLog && jobID != 0 {
			_ = b.AppendPrintDebugLog(printerID, jobID, fmt.Sprintf("[GCODE_DOWNLOAD_FAILED] %v — queued for retry", err))
		}
		if qErr := b.enqueuePendingGcodeDownload(printerName, config.IPAddress, filename, "completed", 0); qErr != nil {
			errorMsg := fmt.Sprintf("G-code download failed and could not be queued for retry: %v (original error: %v)", qErr, err)
			b.addPrintError(printerName, filename, errorMsg)
			return fmt.Errorf("%s", errorMsg)
		}
		return nil // queued — caller clears currentJobFile so state machine stays clean
	}

	if config.DebugLog && jobID != 0 {
		_ = b.AppendPrintDebugLog(printerID, jobID, fmt.Sprintf("[GCODE_DOWNLOAD] %d bytes", len(gcodeContent)))
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

	if config.DebugLog && jobID != 0 {
		for toolheadID, usedG := range filamentUsage {
			_ = b.AppendPrintDebugLog(printerID, jobID, fmt.Sprintf(
				"[PARSED_FILAMENT] T%d: %.3fg  print_time=%ds (%.1fmin)", toolheadID, usedG, printTimeSec, printTimeMin))
		}
	}

	if err := b.processFilamentUsage(printerName, filamentUsage, jobName); err != nil {
		log.Printf("Error processing filament usage: %v", err)
		return err
	}

	sessionID := newSessionID()
	var firstPrintID int
	for toolheadID, usedG := range filamentUsage {
		spoolID, _ := b.GetToolheadMapping(printerName, toolheadID)
		printID, _ := b.LogPrintUsageFull(printerName, toolheadID, spoolID, usedG, jobName,
			printTimeMin, "completed", thumbnailB64, sessionID, "prusalink")
		if printID > 0 {
			_ = b.AppendFilamentUsage(printID, toolheadID, 0, spoolID, 0, usedG)
			if firstPrintID == 0 {
				firstPrintID = printID
			}
		}
	}
	if firstPrintID > 0 {
		if err := b.SnapshotAssignmentsForPrint(firstPrintID, printerID, startTime); err != nil {
			log.Printf("Warning: failed to snapshot NFC assignments for print %d: %v", firstPrintID, err)
		}
		// Save gcode to disk; best-effort — never aborts the print record.
		if err := b.savePrintFile(firstPrintID, "gcode", filepath.Base(filename), gcodeContent); err != nil {
			log.Printf("Warning: could not save gcode file for print %d: %v", firstPrintID, err)
		}
		if config.DebugLog && jobID != 0 {
			if lErr := b.LinkDebugLogsToPrint(printerID, jobID, firstPrintID); lErr != nil {
				log.Printf("[PRUSALINK] printer=%s job=%d failed to link debug logs: %v", printerID, jobID, lErr)
			}
		}
	}

	b.DeleteRecoveryStubs(printerName, filepath.Base(filename))
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

			// Bambu: read cached MQTT state rather than making a new connection
			if printerConfig.PrinterType == PrinterTypeBambu {
				b.bambuMutex.RLock()
				bambuClient, bambuExists := b.bambuClients[printerID]
				b.bambuMutex.RUnlock()
				bambuState := StateOffline
				if bambuExists {
					if s, err := bambuClient.GetCurrentStatus(); err == nil {
						bambuState = mapBambuState(s.GcodeState)
					}
				}
				status.Printers[printerID] = PrinterData{
					Name:  printerName,
					State: bambuState,
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
		var firstPrintID int
		for toolheadID, usedG := range filamentUsage {
			spoolID, _ := b.GetToolheadMapping(d.printerName, toolheadID)
			printID, _ := b.LogPrintUsageFull(d.printerName, toolheadID, spoolID, usedG, jobName,
				printTimeMin, status, thumbnailB64, sessionID, "prusalink")
			if firstPrintID == 0 && printID > 0 {
				firstPrintID = printID
			}
		}
		if firstPrintID > 0 {
			if err := b.savePrintFile(firstPrintID, "gcode", filepath.Base(d.filename), gcodeContent); err != nil {
				log.Printf("Warning: could not save gcode file for retried print %d: %v", firstPrintID, err)
			}
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
		printID, _ := b.LogPrintUsageFull(printerName, toolheadID, spoolID, usedG, displayName,
			printTimeMin, "completed", thumbnailB64, sessionID, "virtual")
		if printID > 0 {
			_ = b.AppendFilamentUsage(printID, toolheadID, 0, spoolID, 0, usedG)
		}
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
		WHERE COALESCE(ph.recovered, 0) = 0
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

	// Bulk-fetch quality tags for all returned records.
	if len(records) > 0 {
		ids := make([]int, len(records))
		for i, r := range records {
			ids[i] = r.ID
		}
		tagMap := b.bulkFetchQualityTags(ids)
		for i, r := range records {
			if tags, ok := tagMap[r.ID]; ok {
				records[i].Tags = tags
			} else {
				records[i].Tags = []PrintQualityTag{}
			}
		}
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

	// Enrich filament usages with Spoolman price-per-kg (best-effort, errors silently skipped).
	spoolPriceCache := map[int]*float64{}
	for i := range r.FilamentUsages {
		sid := r.FilamentUsages[i].SpoolID
		if sid <= 0 {
			continue
		}
		if _, seen := spoolPriceCache[sid]; !seen {
			if spool, serr := b.spoolman.GetSpoolByID(sid); serr == nil && spool != nil {
				p := spool.PricePerKg()
				spoolPriceCache[sid] = &p
			} else {
				spoolPriceCache[sid] = nil
			}
		}
		r.FilamentUsages[i].PricePerKg = spoolPriceCache[sid]
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

	// Fetch quality tags.
	r.Tags, _ = b.GetPrintQualityTags(int64(id))

	// Fetch file attachments.
	r.Attachments, _ = b.GetPrintAttachments(id)

	r.HasDebugLog = b.HasPrintDebugLog(id)

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

// DeletePrintHistoryEntry removes a print history record. Files on disk are cleaned
// up first; the DB cascade handles all child rows (costs, attachments, tags, etc.).
func (b *FilamentBridge) DeletePrintHistoryEntry(id int) error {
	attachments, _ := b.GetPrintAttachments(id)
	for _, a := range attachments {
		absPath := filepath.Join(b.dataDir(), a.FilePath)
		if err := os.Remove(absPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("Warning: could not delete attachment file %s: %v", absPath, err)
		}
	}
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

	// PrusaSlicer: "; estimated printing time (normal mode) = 3h 45m 4s"
	if printTimeSec == 0 {
		prusaRe := regexp.MustCompile(`estimated printing time.*?=\s*(?:(\d+)h\s*)?(?:(\d+)m\s*)?(?:(\d+)s)?`)
		if m := prusaRe.FindStringSubmatch(text); m != nil {
			var h, min, sec int
			fmt.Sscanf(m[1], "%d", &h)
			fmt.Sscanf(m[2], "%d", &min)
			fmt.Sscanf(m[3], "%d", &sec)
			printTimeSec = h*3600 + min*60 + sec
		}
	}

	// Thumbnail: OrcaSlicer / PrusaSlicer embed JPG base64 in comment lines:
	//   "; thumbnail_JPG begin 96x96 3656"  ...lines...  "; thumbnail_JPG end"
	thumbStartRe := regexp.MustCompile(`; thumbnail_(?:JPG|PNG) begin [0-9x]+ [0-9]+`)
	thumbEndRe := regexp.MustCompile(`; thumbnail_(?:JPG|PNG) end`)
	lineRe := regexp.MustCompile(`(?m)^; ?`)

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
	} else {
		// Idempotency: if this session_id was already recorded (e.g. the plugin
		// retried a push that actually succeeded), return the existing ID.
		var existingID int
		err := b.db.QueryRow(
			`SELECT id FROM print_history WHERE session_id = ? LIMIT 1`,
			p.SessionID,
		).Scan(&existingID)
		if err == nil {
			return existingID, nil
		}
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
		VALUES (?, 0, 0, ?, ?, ?, ?, ?, ?, ?,
		        ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PrinterID, totalGrams,
		p.StartedAt, p.EndedAt, p.FileName,
		printTimeMin, p.Status, p.ThumbnailBase64,
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

	// Backfill the legacy spool_id field from the primary tool (T0, initial load)
	// so the history table and cost recalculation can surface the spool.
	for _, f := range p.Filament {
		if f.ToolIndex == 0 && f.ChangeNumber == 0 && f.SpoolID > 0 {
			if _, err := b.db.Exec(
				`UPDATE print_history SET spool_id = ? WHERE id = ?`,
				f.SpoolID, printID,
			); err != nil {
				log.Printf("Warning: failed to backfill spool_id for print %d: %v", printID, err)
			}
			break
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

	if err := b.SnapshotAssignmentsForPrint(printID, p.PrinterID, p.StartedAt); err != nil {
		log.Printf("Warning: failed to snapshot NFC assignments for OctoPrint print %d: %v", printID, err)
	}

	log.Printf("📋 OctoPrint record logged: %s on %s (%.2fg, %.0fmin, %s)",
		p.FileName, p.PrinterID, totalGrams, printTimeMin, p.Status)
	return printID, nil
}

// AppendFilamentUsage adds or updates a filament usage row on an existing print record.
// Called by the /api/prints/:id/filament endpoint when the OctoPrint plugin patches
// filament data that arrived too late to include in the original POST.
//
// Idempotency rules:
//   - Row exists with spool_id > 0 → skip (already fully populated)
//   - Row exists with spool_id = 0 and new spoolID > 0 → UPDATE spool_id and recalc cost
//   - No row → INSERT and recalc cost
func (b *FilamentBridge) AppendFilamentUsage(printID, toolIndex, changeNumber, spoolID int, usedMM, usedG float64) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	var existingSpoolID int
	err := b.db.QueryRow(
		`SELECT COALESCE(spool_id, 0) FROM print_filament_usage WHERE print_id=? AND tool_index=? AND change_number=?`,
		printID, toolIndex, changeNumber,
	).Scan(&existingSpoolID)

	rowExists := err == nil
	if rowExists {
		if existingSpoolID > 0 {
			return nil // already has a valid spool_id, nothing to do
		}
		// Row exists with spool_id=0 — update it if we now have the real ID.
		if spoolID <= 0 {
			return nil
		}
		if _, err := b.db.Exec(
			`UPDATE print_filament_usage SET spool_id=? WHERE print_id=? AND tool_index=? AND change_number=?`,
			spoolID, printID, toolIndex, changeNumber,
		); err != nil {
			return fmt.Errorf("updating spool_id for filament usage: %w", err)
		}
		if toolIndex == 0 && changeNumber == 0 {
			b.db.Exec(`UPDATE print_history SET spool_id=? WHERE id=? AND spool_id=0`, spoolID, printID)
		}
		log.Printf("📎 Filament spool_id patched for print %d: tool=%d spool=%d", printID, toolIndex, spoolID)
	} else {
		if _, err := b.db.Exec(
			`INSERT INTO print_filament_usage (print_id, tool_index, change_number, spool_id, filament_used_mm, filament_used_grams)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			printID, toolIndex, changeNumber, spoolID, usedMM, usedG,
		); err != nil {
			return fmt.Errorf("append filament usage: %w", err)
		}
		b.db.Exec(
			`UPDATE print_history SET filament_used = (
				SELECT COALESCE(SUM(filament_used_grams),0) FROM print_filament_usage WHERE print_id=?
			 ) WHERE id=?`,
			printID, printID,
		)
		if toolIndex == 0 && changeNumber == 0 && spoolID > 0 {
			b.db.Exec(`UPDATE print_history SET spool_id=? WHERE id=? AND spool_id=0`, spoolID, printID)
		}
		log.Printf("📎 Filament appended to print %d: tool=%d spool=%d %.2fmm=%.3fg",
			printID, toolIndex, spoolID, usedMM, usedG)
	}

	// Recalculate cost with the updated filament data. Neither
	// CalculatePrintCostMultiSpoolForPrinter nor SavePrintCost acquires b.mutex.
	costRows, err := b.db.Query(
		`SELECT tool_index, COALESCE(change_number,0), COALESCE(spool_id,0), filament_used_grams
		 FROM print_filament_usage WHERE print_id = ? ORDER BY tool_index, change_number`, printID)
	if err == nil {
		defer costRows.Close()
		var filamentForCost []OctoPrintPayloadFilament
		for costRows.Next() {
			var f OctoPrintPayloadFilament
			if costRows.Scan(&f.ToolIndex, &f.ChangeNumber, &f.SpoolID, &f.FilamentUsedG) == nil {
				filamentForCost = append(filamentForCost, f)
			}
		}
		costRows.Close()
		var printTimeMin float64
		var printerName string
		b.db.QueryRow(`SELECT COALESCE(print_time_minutes,0), printer_name FROM print_history WHERE id = ?`, printID).
			Scan(&printTimeMin, &printerName)
		if bd, calcErr := b.CalculatePrintCostMultiSpoolForPrinter(filamentForCost, printTimeMin, printerName); calcErr == nil {
			b.SavePrintCost(printID, bd)
		}
	}

	return nil
}

// ReassignFilamentSegment moves the filament usage recorded against segmentID to
// newSpoolID and optionally updates the gram amount. Pass newGrams=0 to keep the
// existing weight. Spoolman is adjusted for any spool or gram change, the local DB
// row is updated, print_history.spool_id is backfilled for the primary segment
// (change_number==0), and cost is recalculated.
// segmentID is the print_filament_usage.id primary key.
func (b *FilamentBridge) ReassignFilamentSegment(printID, segmentID, newSpoolID int, newGrams float64) error {
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

	effectiveNewGrams := gramsUsed
	if newGrams > 0 {
		effectiveNewGrams = newGrams
	}

	spoolChanged := newSpoolID != oldSpoolID
	gramsChanged := effectiveNewGrams != gramsUsed

	// Adjust Spoolman: subtract old grams from old spool when spool or grams change.
	if oldSpoolID > 0 && (spoolChanged || gramsChanged) {
		if err := b.spoolman.SubtractSpoolUsage(oldSpoolID, gramsUsed); err != nil {
			log.Printf("⚠️  ReassignFilamentSegment: subtract from spool %d failed: %v", oldSpoolID, err)
			// Non-fatal — proceed so the DB stays consistent.
		}
	}
	// Add effective new grams to new spool when spool or grams change.
	if newSpoolID > 0 && (spoolChanged || gramsChanged) {
		if err := b.spoolman.UpdateSpoolUsage(newSpoolID, effectiveNewGrams); err != nil {
			log.Printf("⚠️  ReassignFilamentSegment: add to spool %d failed: %v", newSpoolID, err)
		}
	}

	// Update the local filament usage record.
	if _, err := b.db.Exec(
		`UPDATE print_filament_usage SET spool_id = ?, filament_used_grams = ? WHERE id = ? AND print_id = ?`,
		newSpoolID, effectiveNewGrams, segmentID, printID,
	); err != nil {
		return fmt.Errorf("updating segment DB record: %w", err)
	}

	// Keep print_history.spool_id in sync for the primary segment (change_number=0)
	// so the Details tab and cost fallback path always reflect the current spool.
	if changeNumber == 0 {
		if _, err := b.db.Exec(
			`UPDATE print_history SET spool_id = ? WHERE id = ?`,
			newSpoolID, printID,
		); err != nil {
			log.Printf("⚠️  ReassignFilamentSegment: update print_history.spool_id failed: %v", err)
		}
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

	log.Printf("🔄 Filament segment %d (print %d T%d.%d) reassigned spool %d → %d (%.2fg → %.2fg)",
		segmentID, printID, toolIndex, changeNumber, oldSpoolID, newSpoolID, gramsUsed, effectiveNewGrams)
	return nil
}

// GetPrintQualityTags returns all quality tags for a single print record.
func (b *FilamentBridge) GetPrintQualityTags(printID int64) ([]PrintQualityTag, error) {
	rows, err := b.db.Query(
		`SELECT id, print_id, tag, COALESCE(custom_text,'') FROM print_quality_tags WHERE print_id = ? ORDER BY id`,
		printID,
	)
	if err != nil {
		return []PrintQualityTag{}, nil
	}
	defer rows.Close()
	var tags []PrintQualityTag
	for rows.Next() {
		var t PrintQualityTag
		if rows.Scan(&t.ID, &t.PrintID, &t.Tag, &t.CustomText) == nil {
			tags = append(tags, t)
		}
	}
	if tags == nil {
		tags = []PrintQualityTag{}
	}
	return tags, nil
}

// SetPrintQualityTags replaces all quality tags for a print with the given payload.
func (b *FilamentBridge) SetPrintQualityTags(printID int64, payload PrintTagsPayload) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM print_quality_tags WHERE print_id = ?`, printID); err != nil {
		return err
	}

	if payload.Outcome != "" {
		if _, err := tx.Exec(`INSERT INTO print_quality_tags (print_id, tag) VALUES (?, ?)`, printID, payload.Outcome); err != nil {
			return err
		}
	}

	for _, issue := range payload.Issues {
		customText := ""
		if issue == "custom" {
			customText = payload.CustomText
		}
		if _, err := tx.Exec(`INSERT INTO print_quality_tags (print_id, tag, custom_text) VALUES (?, ?, ?)`, printID, issue, customText); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// bulkFetchQualityTags fetches quality tags for a set of print IDs in one query.
func (b *FilamentBridge) bulkFetchQualityTags(ids []int) map[int][]PrintQualityTag {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := b.db.Query(
		`SELECT print_id, id, tag, COALESCE(custom_text,'') FROM print_quality_tags WHERE print_id IN (`+placeholders+`) ORDER BY print_id, id`,
		args...,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	tagMap := make(map[int][]PrintQualityTag)
	for rows.Next() {
		var pid int
		var t PrintQualityTag
		if rows.Scan(&pid, &t.ID, &t.PrintID, &t.Tag, &t.CustomText) == nil {
			tagMap[pid] = append(tagMap[pid], t)
		}
	}
	return tagMap
}

// Close closes the database connection
func (b *FilamentBridge) Close() error {
	b.CloseBambuClients()
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

// CloseBambuClients disconnects all Bambu MQTT clients.
func (b *FilamentBridge) CloseBambuClients() {
	b.bambuMutex.Lock()
	defer b.bambuMutex.Unlock()
	for id, client := range b.bambuClients {
		if err := client.Close(); err != nil {
			log.Printf("Warning: error closing Bambu client %s: %v", id, err)
		}
	}
	b.bambuClients = make(map[string]BambuStatusProvider)
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
