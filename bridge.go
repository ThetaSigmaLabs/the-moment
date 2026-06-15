// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

// prettyJSON returns a human-readable indented JSON string, or the raw bytes
// unchanged if indenting fails (e.g. body was empty or not valid JSON).
func prettyJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
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

	// lastSnapshotPct tracks the highest progress % at which a snapshot was taken for
	// Bambu printers (which lack DB-backed active_print_sessions for snapshot tracking).
	// Protected by b.mutex. Reset to 0 when a Bambu print ends.
	lastSnapshotPct map[string]float64

	// commLogs holds in-memory communication event ring buffers, one per printer.
	commLogs   map[string]*PrinterCommLog
	commLogsMu sync.RWMutex

	// rawResponses holds the most recent raw JSON response bodies from PrusaLink, one per printer.
	rawResponses   map[string]*PrusaLinkRawCapture
	rawResponsesMu sync.RWMutex

	// apiMonitor detects when unknown objects or fields appear in PrusaLink API responses.
	apiMonitor *APIShapeMonitor

	// printerWarnings holds active filament sufficiency warnings, keyed by printerID.
	// Populated by checkFilamentSufficiency at print start; cleared when print ends.
	printerWarnings   map[string][]PrinterWarning
	printerWarningsMu sync.Mutex
}

// PrusaLinkRawCapture holds the last raw response bodies received from a PrusaLink printer.
type PrusaLinkRawCapture struct {
	CapturedAt time.Time `json:"captured_at"`
	Status     []byte    `json:"status,omitempty"`
	Job        []byte    `json:"job,omitempty"`
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

	// Recovered is true when this row is a reconciliation stub (print was in-progress
	// when the service restarted; filament data may be zero). Previously hidden; now
	// surfaced in the UI with an "Incomplete Data" badge.
	Recovered        bool `json:"recovered,omitempty"`
	GcodeUnavailable bool `json:"gcode_unavailable,omitempty"`

	// HasPendingDownload is true when a pending_gcode_downloads entry exists for this
	// record. The G-code file is still being retried; filament data will populate once
	// the retry succeeds.
	HasPendingDownload bool `json:"has_pending_download,omitempty"`
	PendingDownloadID  int  `json:"pending_download_id,omitempty"`

	// SessionRecordIDs holds all print_history IDs that share this session (multi-toolhead).
	// Present only on session-detail fetches; used by the UI to support session-level delete.
	SessionRecordIDs []int `json:"session_record_ids,omitempty"`
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
	// ProgressSnapshots contains in-progress camera snapshots bundled by the plugin.
	// Each entry has a progress percentage and a base64-encoded JPEG (plain or data URI).
	ProgressSnapshots []OctoPrintProgressSnapshot `json:"progress_snapshots,omitempty"`
}

// OctoPrintProgressSnapshot is a single progress camera snapshot bundled in the OctoPrint payload.
type OctoPrintProgressSnapshot struct {
	ProgressPct float64 `json:"progress_pct"`
	JpegBase64  string  `json:"jpeg_base64"` // plain base64 or data URI "data:image/jpeg;base64,..."
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
// PrinterData carries the current state of one printer in the WebSocket broadcast.
// Live fields are populated for PrusaLink printers only; other types leave them zero.
// PrinterWarning represents a predicted filament shortage for one toolhead.
type PrinterWarning struct {
	ToolheadIndex int     `json:"toolhead_index"`
	SpoolID       int     `json:"spool_id"`
	Required      float64 `json:"required_grams"`
	Remaining     float64 `json:"remaining_grams"`
	Message       string  `json:"message"`
}

type PrinterData struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	SortOrder int    `json:"sort_order"`
	DebugLog  bool   `json:"debug_log,omitempty"`
	// Live status — PrusaLink only
	TempNozzle    float64 `json:"temp_nozzle,omitempty"`
	TargetNozzle  float64 `json:"target_nozzle,omitempty"`
	TempBed       float64 `json:"temp_bed,omitempty"`
	TargetBed     float64 `json:"target_bed,omitempty"`
	Progress      float64 `json:"progress,omitempty"`
	TimeRemaining int     `json:"time_remaining,omitempty"`
	TimePrinting  int     `json:"time_printing,omitempty"`
	JobName       string  `json:"job_name,omitempty"`
	AxisZ         float64 `json:"axis_z,omitempty"`
	Flow          int     `json:"flow,omitempty"`
	Speed         int     `json:"speed,omitempty"`
	FanHotend     int     `json:"fan_hotend,omitempty"`
	FanPrint      int     `json:"fan_print,omitempty"`
	// Filament sufficiency warnings — populated when a print starts and a spool may run out
	FilamentWarnings []PrinterWarning `json:"filament_warnings,omitempty"`
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
		lastSnapshotPct:  make(map[string]float64),
		commLogs:         make(map[string]*PrinterCommLog),
		rawResponses:     make(map[string]*PrusaLinkRawCapture),
		apiMonitor:       NewAPIShapeMonitor(),
		printerWarnings:  make(map[string][]PrinterWarning),
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

	if err := bridge.migratePendingRunoutEvents(); err != nil {
		return nil, fmt.Errorf("failed to migrate pending runout events: %w", err)
	}

	if err := bridge.migratePrintAttachments(); err != nil {
		return nil, fmt.Errorf("failed to migrate print attachments: %w", err)
	}

	if err := bridge.migratePendingPrintSnapshots(); err != nil {
		return nil, fmt.Errorf("failed to migrate pending print snapshots: %w", err)
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

	if err := bridge.migrateCameraSnapshotURL(); err != nil {
		return nil, fmt.Errorf("failed to migrate camera snapshot URL: %w", err)
	}

	if err := bridge.migratePrinterSortOrder(); err != nil {
		return nil, fmt.Errorf("failed to migrate printer sort order: %w", err)
	}

	if err := bridge.migrateProgressSnapshotConfig(); err != nil {
		return nil, fmt.Errorf("failed to migrate progress snapshot config: %w", err)
	}

	if err := bridge.migrateProgressSnapshotTracking(); err != nil {
		return nil, fmt.Errorf("failed to migrate progress snapshot tracking: %w", err)
	}

	if err := bridge.migratePrintAttachmentLabel(); err != nil {
		return nil, fmt.Errorf("failed to migrate print attachment label: %w", err)
	}

	if err := bridge.migratePendingSnapshotLabel(); err != nil {
		return nil, fmt.Errorf("failed to migrate pending snapshot label: %w", err)
	}

	if err := bridge.migrateNFCTags(); err != nil {
		return nil, fmt.Errorf("failed to migrate NFC tags: %w", err)
	}

	if err := bridge.migrateOpenPrintTagSources(); err != nil {
		return nil, fmt.Errorf("failed to migrate OpenPrintTag sources: %w", err)
	}

	if err := bridge.deduplicateRecoveryStubs(); err != nil {
		log.Printf("[RECONCILE] dedup migration warning: %v", err)
	}

	bridge.ReconcileActiveSessions()

	// Update Spoolman URL and timeout if config is provided
	if config != nil && config.SpoolmanURL != "" {
		bridge.spoolman = NewSpoolmanClient(config.SpoolmanURL, config.SpoolmanTimeout)
	}

	// These run after all schema migrations (including migrateOctoPrintSupport which adds
	// printer_type) so GetAllPrinterConfigs() finds the column on fresh databases.
	if err := bridge.migrateLocationsToSpoolman(); err != nil {
		log.Printf("Warning: Failed to migrate locations to Spoolman: %v", err)
	}
	if err := bridge.migrateToolheadMappingsToSpoolman(); err != nil {
		log.Printf("Warning: Failed to migrate toolhead mappings to Spoolman: %v", err)
	}

	return bridge, nil
}

// getCommLog returns the PrinterCommLog for printerID, creating it lazily.
func (b *FilamentBridge) getCommLog(printerID string) *PrinterCommLog {
	b.commLogsMu.Lock()
	defer b.commLogsMu.Unlock()
	if _, ok := b.commLogs[printerID]; !ok {
		b.commLogs[printerID] = &PrinterCommLog{}
	}
	return b.commLogs[printerID]
}

// GetRawResponses returns the most recent PrusaLink raw response capture for a printer, or nil.
func (b *FilamentBridge) GetRawResponses(printerID string) *PrusaLinkRawCapture {
	b.rawResponsesMu.RLock()
	defer b.rawResponsesMu.RUnlock()
	return b.rawResponses[printerID]
}

// initDatabase initializes the SQLite database
func (b *FilamentBridge) initDatabase() error {
	dbFile := getDBFilePath()
	if b.config != nil && b.config.DBFile != "" {
		dbFile = b.config.DBFile
	}

	if err := os.MkdirAll(filepath.Dir(dbFile), 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
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

// migratePendingRunoutEvents adds the pending_runout_events table (runouts detected mid-print,
// before print_history rows exist) and the print_progress_pct column to print_spool_events.
func (b *FilamentBridge) migratePendingRunoutEvents() error {
	_, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS pending_runout_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_id TEXT NOT NULL,
			print_start_time DATETIME NOT NULL,
			toolhead_index INTEGER NOT NULL DEFAULT 0,
			progress_pct REAL NOT NULL,
			event_time DATETIME NOT NULL DEFAULT (datetime('now'))
		)`)
	if err != nil {
		return fmt.Errorf("failed to create pending_runout_events table: %w", err)
	}
	// Idempotent: ignore "duplicate column" on existing DBs.
	b.db.Exec(`ALTER TABLE print_spool_events ADD COLUMN print_progress_pct REAL DEFAULT 0.0`)
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

// migratePendingPrintSnapshots creates the staging table for camera snapshots captured
// during a print (e.g. at an ATTENTION/runout event) before the print_history ID exists.
// Rows are moved to print_attachments when the print finishes or is cancelled.
func (b *FilamentBridge) migratePendingPrintSnapshots() error {
	_, err := b.db.Exec(`
		CREATE TABLE IF NOT EXISTS pending_print_snapshots (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_id       TEXT    NOT NULL,
			print_start_time DATETIME NOT NULL,
			event_type       TEXT    NOT NULL,
			file_path        TEXT    NOT NULL,
			captured_at      DATETIME NOT NULL DEFAULT (datetime('now'))
		)`)
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

// deduplicateRecoveryStubs removes duplicate [RECOVERED] stubs left by the
// pre-fix bug where each restart created a new stub for an ongoing print.
// Keeps the oldest stub per (printer_name, base job name) — it has the most
// accurate print start time. Safe to run repeatedly; no-op if no duplicates exist.
func (b *FilamentBridge) deduplicateRecoveryStubs() error {
	_, err := b.db.Exec(`
		DELETE FROM print_history
		WHERE recovered = 1
		  AND id NOT IN (
		    SELECT MIN(id)
		    FROM print_history
		    WHERE recovered = 1
		    GROUP BY printer_name, REPLACE(job_name, ' [RECOVERED]', '')
		  )`)
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

// migrateCameraSnapshotURL adds camera_snapshot_url column to printer_configs.
func (b *FilamentBridge) migrateCameraSnapshotURL() error {
	b.db.Exec(`ALTER TABLE printer_configs ADD COLUMN camera_snapshot_url TEXT DEFAULT ''`)
	return nil
}

// migratePrinterSortOrder adds sort_order column to printer_configs for dashboard display ordering.
func (b *FilamentBridge) migratePrinterSortOrder() error {
	b.db.Exec(`ALTER TABLE printer_configs ADD COLUMN sort_order INTEGER DEFAULT 0`)
	return nil
}

// migrateProgressSnapshotConfig adds per-printer progress snapshot configuration (JSON blob).
func (b *FilamentBridge) migrateProgressSnapshotConfig() error {
	b.db.Exec(`ALTER TABLE printer_configs ADD COLUMN progress_snapshot_config TEXT DEFAULT ''`)
	return nil
}

// migrateProgressSnapshotTracking adds last_snapshot_progress to active_print_sessions
// so the monitor loop knows the highest progress % at which a snapshot was already captured.
func (b *FilamentBridge) migrateProgressSnapshotTracking() error {
	b.db.Exec(`ALTER TABLE active_print_sessions ADD COLUMN last_snapshot_progress REAL DEFAULT 0`)
	return nil
}

// migratePrintAttachmentLabel adds a friendly display label to print_attachments
// (e.g. "25% progress", "Attention @ 42%", "Finished").
func (b *FilamentBridge) migratePrintAttachmentLabel() error {
	b.db.Exec(`ALTER TABLE print_attachments ADD COLUMN label TEXT DEFAULT ''`)
	return nil
}

// migratePendingSnapshotLabel adds a label column to pending_print_snapshots so
// the label is carried through when rows are flushed to print_attachments.
func (b *FilamentBridge) migratePendingSnapshotLabel() error {
	b.db.Exec(`ALTER TABLE pending_print_snapshots ADD COLUMN label TEXT DEFAULT ''`)
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
	LastSnapshotProgress   float64 // highest progress % at which a snapshot was already captured
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
	var lastProgress, lastSnapProgress sql.NullFloat64
	var lastTimePrinting sql.NullInt64
	err := b.db.QueryRow(`
		SELECT printer_id, job_id, started_at, file_path, file_size_bytes,
		       gcode_metadata_json, gcode_local_path, initial_assignments_json,
		       last_seen_progress, last_seen_time_printing, change_count,
		       COALESCE(last_snapshot_progress, 0)
		FROM active_print_sessions WHERE printer_id = ? AND job_id = ?`,
		printerID, jobID).Scan(
		&s.PrinterID, &s.JobID, &startedAt, &filePath, &fileSizeBytes,
		&gcodeMeta, &gcodeLocal, &assignJSON,
		&lastProgress, &lastTimePrinting, &s.ChangeCount, &lastSnapProgress)
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
	if lastSnapProgress.Valid {
		s.LastSnapshotProgress = lastSnapProgress.Float64
	}
	return &s, nil
}

// GetActivePrintSessionForPrinter returns the most-recently-started active session for
// a printer without requiring a job ID. Returns nil, nil if no session exists.
func (b *FilamentBridge) GetActivePrintSessionForPrinter(printerID string) (*ActivePrintSession, error) {
	var s ActivePrintSession
	var startedAt string
	var filePath, gcodeMeta, gcodeLocal, assignJSON sql.NullString
	var fileSizeBytes sql.NullInt64
	var lastProgress, lastSnapProgress sql.NullFloat64
	var lastTimePrinting sql.NullInt64
	err := b.db.QueryRow(`
		SELECT printer_id, job_id, started_at, file_path, file_size_bytes,
		       gcode_metadata_json, gcode_local_path, initial_assignments_json,
		       last_seen_progress, last_seen_time_printing, change_count,
		       COALESCE(last_snapshot_progress, 0)
		FROM active_print_sessions WHERE printer_id = ?
		ORDER BY julianday(started_at) DESC LIMIT 1`,
		printerID).Scan(
		&s.PrinterID, &s.JobID, &startedAt, &filePath, &fileSizeBytes,
		&gcodeMeta, &gcodeLocal, &assignJSON,
		&lastProgress, &lastTimePrinting, &s.ChangeCount, &lastSnapProgress)
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
	if lastSnapProgress.Valid {
		s.LastSnapshotProgress = lastSnapProgress.Float64
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

// UpdateSessionSnapshotProgress records the highest progress % at which a snapshot was
// captured, preventing duplicate captures on subsequent polls.
func (b *FilamentBridge) UpdateSessionSnapshotProgress(printerID string, jobID int, pct float64) error {
	_, err := b.db.Exec(`
		UPDATE active_print_sessions SET last_snapshot_progress = ?
		WHERE printer_id = ? AND job_id = ?`,
		pct, printerID, jobID)
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
			  AND p.outcome != 'recovered'
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

		// Check whether this job was already recovered in a prior restart cycle.
		// If so, skip creating another stub — only restore in-memory state.
		// The session is kept alive (not deleted) so that:
		//   - UpdateSessionProgress continues to record progress across restarts
		//   - started_at from the original session is preserved (not reset to time.Now())
		//   - ReconcileActiveSessions can always restore in-memory state on any restart
		// The session is cleaned up by handlePrusaLinkPrintFinished when the print ends.
		var existingRecovery int
		_ = b.db.QueryRow(
			`SELECT COUNT(*) FROM processed_jobs WHERE printer_id=? AND job_id=? AND outcome='recovered'`,
			o.printerID, o.jobID).Scan(&existingRecovery)
		alreadyRecovered := existingRecovery > 0

		if !alreadyRecovered {
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
							_ = b.savePrintFile(int(capturedID), "gcode", filepath.Base(capturedPath), "", gcodeContent)
						}()
					}
				}
			}

			if mErr := b.MarkJobProcessed(o.printerID, o.jobID, "recovered"); mErr != nil {
				log.Printf("[RECONCILE] mark processed failed: printer=%s job=%d: %v", o.printerID, o.jobID, mErr)
			}
		} else {
			log.Printf("[RECONCILE] printer=%s job=%d already recovered — restoring state only", o.printerID, o.jobID)
		}

		// Always restore in-memory tracking state so the first MonitorPrinters() tick can
		// detect FINISHED/IDLE and write a proper print_history row even if the print
		// completed during startup (between this reconcile and the first poll).
		// The active session is intentionally kept alive (not deleted here) so that
		// started_at and progress remain accurate across multiple restarts.
		b.mutex.Lock()
		b.wasPrinting[o.printerID] = true
		if b.currentJobFile[o.printerID] == "" {
			b.currentJobFile[o.printerID] = o.filePath
		}
		if b.currentJobID[o.printerID] == 0 {
			b.currentJobID[o.printerID] = o.jobID
		}
		if b.printStartTime[o.printerID].IsZero() {
			b.printStartTime[o.printerID] = startTime
		}
		b.mutex.Unlock()
	}
}

// PrintAttachment represents a file attached to a print history record.
type PrintAttachment struct {
	ID             int    `json:"id"`
	PrintHistoryID int    `json:"print_history_id"`
	FileType       string `json:"file_type"` // "gcode", "slicer", "other", "camera"
	Filename       string `json:"filename"`
	FileSize       int64  `json:"file_size"`
	FilePath       string `json:"file_path"` // relative to GcodePath
	StoredAt       string `json:"stored_at"`
	Label          string `json:"label,omitempty"` // friendly display label, e.g. "25% progress", "Attention @ 42%"
}

// SavePrintAttachment inserts a print attachment record into the database.
func (b *FilamentBridge) SavePrintAttachment(printHistoryID int, fileType, filename, filePath, label string, fileSize int64) (int64, error) {
	res, err := b.db.Exec(`
		INSERT INTO print_attachments (print_history_id, file_type, filename, file_size, file_path, label)
		VALUES (?, ?, ?, ?, ?, ?)`,
		printHistoryID, fileType, filename, fileSize, filePath, label,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to save print attachment: %w", err)
	}
	return res.LastInsertId()
}

// GetPrintAttachments returns all attachments for a given print history record.
func (b *FilamentBridge) GetPrintAttachments(printHistoryID int) ([]PrintAttachment, error) {
	rows, err := b.db.Query(`
		SELECT id, print_history_id, file_type, filename, file_size, file_path, stored_at, COALESCE(label, '')
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
		if err := rows.Scan(&a.ID, &a.PrintHistoryID, &a.FileType, &a.Filename, &a.FileSize, &a.FilePath, &a.StoredAt, &a.Label); err != nil {
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
		SELECT id, print_history_id, file_type, filename, file_size, file_path, stored_at, COALESCE(label, '')
		FROM print_attachments WHERE id = ?`, id,
	).Scan(&a.ID, &a.PrintHistoryID, &a.FileType, &a.Filename, &a.FileSize, &a.FilePath, &a.StoredAt, &a.Label)
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
	absPath := filepath.Join(b.gcodePath(), a.FilePath)
	if rmErr := os.Remove(absPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		log.Printf("Warning: could not delete attachment file %s: %v", absPath, rmErr)
	}
	_, err = b.db.Exec(`DELETE FROM print_attachments WHERE id = ?`, id)
	return err
}

// gcodePath returns the root directory for print history file attachments.
func (b *FilamentBridge) gcodePath() string {
	if b.config != nil && b.config.GcodePath != "" {
		return b.config.GcodePath
	}
	return getGcodePath()
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
// fileType should be "gcode", "slicer", "camera", or "other". label is a human-readable
// display label shown in the history UI (empty string for non-camera files).
func (b *FilamentBridge) savePrintFile(printID int, fileType, filename, label string, content []byte) error {
	dir := b.gcodePath()
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
	_, err := b.SavePrintAttachment(printID, fileType, safeName, relPath, label, int64(len(content)))
	return err
}

// snapshotTargets returns the sorted list of progress percentages at which a snapshot
// should be captured, based on the printer's ProgressSnapshotConfig. Returns nil when
// mode is "none" or zero-value. The returned slice never includes 100 (handled by the
// "finished" snapshot) and all values are in (0, 100).
func snapshotTargets(cfg ProgressSnapshotConfig) []float64 {
	switch cfg.Mode {
	case "interval":
		if cfg.Interval <= 0 || cfg.Interval >= 100 {
			return nil
		}
		var targets []float64
		for t := cfg.Interval; t < 100; t += cfg.Interval {
			targets = append(targets, t)
		}
		return targets
	case "milestones":
		if len(cfg.Milestones) == 0 {
			return nil
		}
		seen := map[float64]bool{}
		var targets []float64
		for _, m := range cfg.Milestones {
			if m > 0 && m < 100 && !seen[m] {
				targets = append(targets, m)
				seen[m] = true
			}
		}
		sort.Float64s(targets)
		return targets
	default:
		return nil
	}
}

// crossedTargets returns all targets from the sorted slice that fall strictly in
// (lastPct, currentPct]. All milestones crossed in a single poll tick are returned
// so none are skipped even when progress advances quickly.
func crossedTargets(targets []float64, lastPct, currentPct float64) []float64 {
	if len(targets) == 0 || currentPct <= lastPct {
		return nil
	}
	var crossed []float64
	for _, t := range targets {
		if t > lastPct && t <= currentPct {
			crossed = append(crossed, t)
		}
	}
	return crossed
}

// labelFor returns the human-readable display label for a progress snapshot.
func labelFor(pct float64) string {
	return fmt.Sprintf("%.0f%% progress", pct)
}

// fetchSnapshotFromURL captures a JPEG from an HTTP/HTTPS or RTSP URL.
// RTSP capture uses ffmpeg; HTTP/HTTPS is a plain GET.
func fetchSnapshotFromURL(snapshotURL string) ([]byte, error) {
	if strings.HasPrefix(snapshotURL, "rtsp://") || strings.HasPrefix(snapshotURL, "rtsps://") {
		return fetchRTSPSnapshot(snapshotURL)
	}
	return fetchHTTPSnapshot(snapshotURL)
}

func fetchHTTPSnapshot(snapshotURL string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(snapshotURL)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("camera URL returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func fetchRTSPSnapshot(rtspURL string) ([]byte, error) {
	if !strings.HasPrefix(rtspURL, "rtsp://") && !strings.HasPrefix(rtspURL, "http://") && !strings.HasPrefix(rtspURL, "https://") {
		return nil, fmt.Errorf("invalid camera URL scheme: %s", rtspURL)
	}
	tmp, err := os.CreateTemp("", "snapshot-*.jpg")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-vframes", "1",
		"-f", "image2",
		"-vcodec", "mjpeg",
		"-y",
		tmpPath,
	)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg failed: %w — %s", err, stderr.String())
	}
	return os.ReadFile(tmpPath)
}

// capturePrusaLinkSnapshot saves a JPEG snapshot for the given print event.
// If a CameraSnapshotURL is configured it takes priority; otherwise the PrusaLink
// /api/v1/cameras endpoint is tried as a fallback (requires firmware support).
// Best-effort: logs on failure, never aborts the print record.
func (b *FilamentBridge) capturePrusaLinkSnapshot(config PrinterConfig, printID int, eventType string) {
	var jpegData []byte
	var err error

	if config.CameraSnapshotURL != "" {
		jpegData, err = fetchSnapshotFromURL(config.CameraSnapshotURL)
		if err != nil {
			log.Printf("[SNAPSHOT] configured URL failed for %s (%s): %v", config.Name, config.CameraSnapshotURL, err)
			return
		}
	} else {
		prusaClient := NewPrusaLinkClient(config.IPAddress, config.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkTimeout)
		cameras, err := prusaClient.GetCameras()
		if err != nil {
			log.Printf("[SNAPSHOT] failed to list cameras for %s: %v", config.IPAddress, err)
			return
		}
		if len(cameras) == 0 {
			return
		}
		jpegData, err = prusaClient.GetSnapshot(cameras[0].ID)
		if err != nil {
			log.Printf("[SNAPSHOT] failed to capture snapshot for %s: %v", config.IPAddress, err)
			return
		}
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	filename := fmt.Sprintf("%s_%s.jpg", eventType, ts)
	label := strings.ToUpper(eventType[:1]) + eventType[1:] // "Finished", "Cancelled"
	if err := b.savePrintFile(printID, "camera", filename, label, jpegData); err != nil {
		log.Printf("[SNAPSHOT] failed to save snapshot for print %d: %v", printID, err)
	}
}

// savePendingSnapshot saves a JPEG to disk and records it in pending_print_snapshots
// for association with a print_history record once the print ID is known.
// label is the human-readable display label (e.g. "Attention @ 42%", "25% progress").
func (b *FilamentBridge) savePendingSnapshot(printerID string, startTime time.Time, eventType, label string, jpegData []byte) {
	dir := b.gcodePath()
	now := time.Now()
	subDir := filepath.Join(dir, "print-files", fmt.Sprintf("%d", now.Year()), fmt.Sprintf("%02d", int(now.Month())))
	if err := os.MkdirAll(subDir, 0755); err != nil {
		log.Printf("[SNAPSHOT] failed to create directory for pending snapshot: %v", err)
		return
	}
	ts := now.UTC().Format("20060102T150405Z")
	safeID := strings.ReplaceAll(printerID, "/", "_")
	filename := fmt.Sprintf("pending_%s_%s_%s.jpg", safeID, eventType, ts)
	relPath := filepath.Join("print-files", fmt.Sprintf("%d", now.Year()), fmt.Sprintf("%02d", int(now.Month())), filename)
	absPath := filepath.Join(dir, relPath)
	if err := os.WriteFile(absPath, jpegData, 0644); err != nil {
		log.Printf("[SNAPSHOT] failed to write pending snapshot: %v", err)
		return
	}
	_, err := b.db.Exec(`
		INSERT INTO pending_print_snapshots (printer_id, print_start_time, event_type, file_path, label)
		VALUES (?, ?, ?, ?, ?)`,
		printerID, startTime.UTC().Format(time.RFC3339Nano), eventType, relPath, label,
	)
	if err != nil {
		log.Printf("[SNAPSHOT] failed to record pending snapshot: %v", err)
	}
}

// captureProgressSnapshot captures a JPEG at a configured progress threshold and stores
// it as a pending snapshot (associated with the print when it completes). It tries
// CameraSnapshotURL first; for PrusaLink printers it falls back to the native camera
// API. No-ops silently if no camera source is available (e.g. Bambu without a URL).
func (b *FilamentBridge) captureProgressSnapshot(config PrinterConfig, printerID string, startTime time.Time, targetPct float64) error {
	jpegData, err := b.fetchPrinterSnapshot(config)
	if err != nil {
		return err
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	eventType := fmt.Sprintf("progress_%02d", int(targetPct))
	b.savePendingSnapshot(printerID, startTime, eventType, labelFor(targetPct), jpegData)
	_ = ts
	return nil
}

// captureInterruptSnapshot captures a JPEG at a printer interrupt event (attention or
// resume) and stores it as a pending snapshot with a progress-annotated label.
// eventKind is "attention" or "resume".
func (b *FilamentBridge) captureInterruptSnapshot(config PrinterConfig, printerID string, startTime time.Time, progressPct float64, eventKind string) error {
	jpegData, err := b.fetchPrinterSnapshot(config)
	if err != nil {
		return err
	}
	var label string
	switch eventKind {
	case "attention":
		label = fmt.Sprintf("Attention @ %.0f%%", progressPct)
	case "resume":
		label = fmt.Sprintf("Resumed @ %.0f%%", progressPct)
	default:
		label = eventKind
	}
	b.savePendingSnapshot(printerID, startTime, eventKind, label, jpegData)
	return nil
}

// fetchPrinterSnapshot captures a JPEG from a printer's camera. Tries CameraSnapshotURL
// first; for PrusaLink printers falls back to the native /api/v1/cameras endpoint.
// Returns an error if no camera is available or the capture fails.
func (b *FilamentBridge) fetchPrinterSnapshot(config PrinterConfig) ([]byte, error) {
	if config.CameraSnapshotURL != "" {
		return fetchSnapshotFromURL(config.CameraSnapshotURL)
	}
	if config.PrinterType == PrinterTypePrusaLink || config.PrinterType == "" {
		prusaClient := NewPrusaLinkClient(config.IPAddress, config.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkTimeout)
		cameras, err := prusaClient.GetCameras()
		if err != nil || len(cameras) == 0 {
			return nil, fmt.Errorf("no camera available for %s", config.IPAddress)
		}
		return prusaClient.GetSnapshot(cameras[0].ID)
	}
	return nil, fmt.Errorf("no camera configured for %s", config.Name)
}

// flushPendingSnapshots promotes any pending snapshots for (printerID, startTime) into
// print_attachments rows linked to printID, then deletes the staging records.
func (b *FilamentBridge) flushPendingSnapshots(printerID string, startTime time.Time, printID int) {
	rows, err := b.db.Query(`
		SELECT id, event_type, file_path, COALESCE(label, '') FROM pending_print_snapshots
		WHERE printer_id = ? AND julianday(print_start_time) = julianday(?)`,
		printerID, startTime.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		log.Printf("[SNAPSHOT] failed to query pending snapshots: %v", err)
		return
	}

	type pendingRow struct {
		id        int
		eventType string
		filePath  string
		label     string
	}
	var pending []pendingRow
	for rows.Next() {
		var p pendingRow
		if err := rows.Scan(&p.id, &p.eventType, &p.filePath, &p.label); err != nil {
			continue
		}
		pending = append(pending, p)
	}
	rows.Close()

	for _, p := range pending {
		absPath := filepath.Join(b.gcodePath(), p.filePath)
		info, statErr := os.Stat(absPath)
		if statErr != nil {
			log.Printf("[SNAPSHOT] pending snapshot file missing %s: %v", absPath, statErr)
			_, _ = b.db.Exec(`DELETE FROM pending_print_snapshots WHERE id = ?`, p.id)
			continue
		}
		if _, err := b.SavePrintAttachment(printID, "camera", filepath.Base(p.filePath), p.filePath, p.label, info.Size()); err != nil {
			log.Printf("[SNAPSHOT] failed to attach pending snapshot to print %d: %v", printID, err)
			continue
		}
		_, _ = b.db.Exec(`DELETE FROM pending_print_snapshots WHERE id = ?`, p.id)
	}
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
	PrintProgressPct   float64
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

// CloseAssignmentsBySpool closes all active toolhead assignments for a given spool ID.
func (b *FilamentBridge) CloseAssignmentsBySpool(spoolmanSpoolID int) error {
	_, err := b.db.Exec(`
		UPDATE toolhead_spool_assignments
		SET unassigned_at = ?
		WHERE spoolman_spool_id = ? AND unassigned_at IS NULL
	`, time.Now().UTC(), spoolmanSpoolID)
	if err != nil {
		return fmt.Errorf("CloseAssignmentsBySpool: %w", err)
	}
	return nil
}

// ClearToolheadMappingsBySpool removes all Print Ops toolhead mappings for the given spool.
func (b *FilamentBridge) ClearToolheadMappingsBySpool(spoolID int) error {
	b.pushSpoolToInventory(spoolID)
	_, err := b.db.Exec(`DELETE FROM toolhead_mappings WHERE spool_id = ?`, spoolID)
	if err != nil {
		return fmt.Errorf("ClearToolheadMappingsBySpool: %w", err)
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
		INSERT INTO print_spool_events (print_history_id, toolhead_index, old_spoolman_spool_id, new_spoolman_spool_id, event_type, print_progress_pct)
		VALUES (?, ?, ?, ?, ?, ?)
	`, event.PrintHistoryID, event.ToolheadIndex, event.OldSpoolmanSpoolID, event.NewSpoolmanSpoolID, event.EventType, event.PrintProgressPct)
	if err != nil {
		return fmt.Errorf("AddPrintSpoolEvent: %w", err)
	}
	return nil
}

// PendingRunoutEvent represents a printer attention event (runout or other pause) captured
// mid-print, before print_history rows are created. Persisted in SQLite so multi-day
// prints survive server restarts.
type PendingRunoutEvent struct {
	ID             int
	PrinterID      string
	PrintStartTime time.Time
	ToolheadIndex  int
	ProgressPct    float64
	EventTime      time.Time
}

// AddPendingRunout records an attention-state event for a printer mid-print.
func (b *FilamentBridge) AddPendingRunout(printerID string, startTime time.Time, toolheadIndex int, progressPct float64) error {
	_, err := b.db.Exec(`
		INSERT INTO pending_runout_events (printer_id, print_start_time, toolhead_index, progress_pct, event_time)
		VALUES (?, ?, ?, ?, ?)
	`, printerID, startTime.UTC().Format(time.RFC3339Nano), toolheadIndex, progressPct, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("AddPendingRunout: %w", err)
	}
	return nil
}

// GetPendingRunouts returns all attention events for a printer/start_time, ordered by event_time.
func (b *FilamentBridge) GetPendingRunouts(printerID string, startTime time.Time) ([]PendingRunoutEvent, error) {
	rows, err := b.db.Query(`
		SELECT id, printer_id, print_start_time, toolhead_index, progress_pct, event_time
		FROM pending_runout_events
		WHERE printer_id = ? AND julianday(print_start_time) = julianday(?)
		ORDER BY event_time
	`, printerID, startTime.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("GetPendingRunouts: %w", err)
	}
	defer rows.Close()

	var events []PendingRunoutEvent
	for rows.Next() {
		var e PendingRunoutEvent
		var startStr, eventStr string
		if err := rows.Scan(&e.ID, &e.PrinterID, &startStr, &e.ToolheadIndex, &e.ProgressPct, &eventStr); err != nil {
			return nil, fmt.Errorf("GetPendingRunouts scan: %w", err)
		}
		e.PrintStartTime, _ = time.Parse(time.RFC3339Nano, startStr)
		e.EventTime, _ = time.Parse(time.RFC3339Nano, eventStr)
		events = append(events, e)
	}
	return events, rows.Err()
}

// DeletePendingRunouts removes flushed runout events for a printer/start_time.
func (b *FilamentBridge) DeletePendingRunouts(printerID string, startTime time.Time) error {
	_, err := b.db.Exec(`
		DELETE FROM pending_runout_events
		WHERE printer_id = ? AND julianday(print_start_time) = julianday(?)
	`, printerID, startTime.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("DeletePendingRunouts: %w", err)
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
		ConfigKeyAutoAssignPreviousSpoolEnabled: "false", // Enable auto-assignment of previous spool to default location
		ConfigKeyNFCTrashLocation:                "Trash",     // Location for empty/done spools (tag ready to re-program)
		ConfigKeyNFCInventoryLocation:            "Inventory", // Default storage when spool displaced from toolhead
		ConfigKeySpoolmanLocationSyncEnabled:     "false",     // Bidirectional Spoolman location sync
		ConfigKeyNFCTapTimeoutSeconds:            "15",        // Tap-tap pending window in seconds (Stage 5)
	}

	// INSERT OR IGNORE ensures new keys added in updates are seeded for existing
	// installations. Existing keys are left unchanged; only missing ones are inserted.
	for key, value := range defaultConfigs {
		_, err := b.db.Exec(
			"INSERT OR IGNORE INTO configuration (key, value, description) VALUES (?, ?, ?)",
			key, value, getConfigDescription(key),
		)
		if err != nil {
			return fmt.Errorf("failed to seed default config %s: %w", key, err)
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
		ConfigKeyPrusaLinkFileDownloadTimeout:    "PrusaLink file download header timeout in seconds (body reading is unbounded to support large bgcode files)",
		ConfigKeySpoolmanTimeout:                 "Spoolman API timeout in seconds",
		ConfigKeyAutoAssignPreviousSpoolEnabled: "Enable automatic assignment of previous spool to Inventory location when assigning new spool to toolhead",
		ConfigKeyNFCTrashLocation:                "Spoolman location name for empty/finished spools (NFC tag ready to re-program)",
		ConfigKeyNFCInventoryLocation:            "Spoolman location name used as default storage when a spool is displaced from a toolhead via NFC",
		ConfigKeySpoolmanLocationSyncEnabled:     "When true, The Moment writes spool locations to Spoolman on assign/unassign and polls for Spoolman-initiated moves",
		ConfigKeyNFCTapTimeoutSeconds:            "Seconds a first NFC tap stays pending before a second tap is treated as a fresh first tap (tap-tap engine)",
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


// GetAllPrinterConfigs gets all printer configurations
func (b *FilamentBridge) GetAllPrinterConfigs() (map[string]PrinterConfig, error) {
	rows, err := b.db.Query("SELECT printer_id, name, model, ip_address, api_key, toolheads, COALESCE(is_virtual, 0), COALESCE(printer_type, 'prusalink'), COALESCE(debug_log, 0), COALESCE(camera_snapshot_url, ''), COALESCE(sort_order, 0), COALESCE(progress_snapshot_config, '') FROM printer_configs")
	if err != nil {
		return nil, fmt.Errorf("failed to get printer configs: %w", err)
	}
	defer rows.Close()

	configs := make(map[string]PrinterConfig)
	for rows.Next() {
		var printerID, name, model, ipAddress, apiKey, printerType, cameraSnapshotURL, pscJSON string
		var toolheads, sortOrder int
		var isVirtual, debugLog bool
		if err := rows.Scan(&printerID, &name, &model, &ipAddress, &apiKey, &toolheads, &isVirtual, &printerType, &debugLog, &cameraSnapshotURL, &sortOrder, &pscJSON); err != nil {
			return nil, fmt.Errorf("failed to scan printer config row: %w", err)
		}
		var psc ProgressSnapshotConfig
		if pscJSON != "" {
			_ = json.Unmarshal([]byte(pscJSON), &psc)
		}
		configs[printerID] = PrinterConfig{
			Name:                   name,
			Model:                  model,
			IPAddress:              ipAddress,
			APIKey:                 apiKey,
			Toolheads:              toolheads,
			IsVirtual:              isVirtual,
			PrinterType:            printerType,
			DebugLog:               debugLog,
			CameraSnapshotURL:      cameraSnapshotURL,
			SortOrder:              sortOrder,
			ProgressSnapshotConfig: psc,
		}
	}

	return configs, nil
}

// SavePrinterConfig saves a printer configuration
func (b *FilamentBridge) SavePrinterConfig(printerID string, config PrinterConfig) error {
	b.mutex.Lock()

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

	// Detect toolhead count decrease: collect spools on removed slots for Spoolman sync.
	var removedSpoolIDs []int
	if !config.IsVirtual {
		var oldToolheads int
		_ = b.db.QueryRow("SELECT toolheads FROM printer_configs WHERE printer_id = ?", printerID).Scan(&oldToolheads)
		if config.Toolheads < oldToolheads {
			for t := config.Toolheads; t < oldToolheads; t++ {
				var sid int
				if b.db.QueryRow(
					"SELECT spool_id FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ?",
					config.Name, t,
				).Scan(&sid) == nil && sid > 0 {
					removedSpoolIDs = append(removedSpoolIDs, sid)
					_, _ = b.db.Exec(
						"DELETE FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ?",
						config.Name, t,
					)
				}
			}
		}
	}

	pscJSON, _ := json.Marshal(config.ProgressSnapshotConfig)

	_, err := b.db.Exec(`
		INSERT INTO printer_configs (printer_id, name, model, ip_address, api_key, toolheads, is_virtual, printer_type, debug_log, camera_snapshot_url, sort_order, progress_snapshot_config)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(printer_id) DO UPDATE SET
			name = excluded.name,
			model = excluded.model,
			ip_address = excluded.ip_address,
			api_key = excluded.api_key,
			toolheads = excluded.toolheads,
			is_virtual = excluded.is_virtual,
			printer_type = excluded.printer_type,
			debug_log = excluded.debug_log,
			camera_snapshot_url = excluded.camera_snapshot_url,
			sort_order = excluded.sort_order,
			progress_snapshot_config = excluded.progress_snapshot_config
	`, printerID, config.Name, config.Model, config.IPAddress, config.APIKey, config.Toolheads, isVirtualInt, printerType, debugLogInt, config.CameraSnapshotURL, config.SortOrder, string(pscJSON))

	b.mutex.Unlock()

	if err != nil {
		return fmt.Errorf("failed to save printer config: %w", err)
	}

	// Push spools from removed toolheads to inventory in Spoolman (best effort, after lock released).
	for _, sid := range removedSpoolIDs {
		b.pushSpoolToInventory(sid)
	}

	return nil
}

// DeletePrinterConfig deletes a printer and all its associated data:
// toolhead_mappings (frees spools for re-assignment) and toolhead_names.
func (b *FilamentBridge) DeletePrinterConfig(printerID string) error {
	b.mutex.Lock()

	// Look up the printer name before deleting — mappings are keyed by name
	var printerName string
	err := b.db.QueryRow("SELECT name FROM printer_configs WHERE printer_id = ?", printerID).Scan(&printerName)
	if err != nil {
		b.mutex.Unlock()
		return fmt.Errorf("printer %s not found: %w", printerID, err)
	}

	// Collect spool IDs for Spoolman sync before deleting mappings.
	var spoolIDsForSync []int
	if spoolRows, qErr := b.db.Query("SELECT spool_id FROM toolhead_mappings WHERE printer_name = ?", printerName); qErr == nil {
		for spoolRows.Next() {
			var sid int
			if spoolRows.Scan(&sid) == nil {
				spoolIDsForSync = append(spoolIDsForSync, sid)
			}
		}
		spoolRows.Close()
	}

	// Remove toolhead spool assignments so those spools become assignable again
	_, _ = b.db.Exec("DELETE FROM toolhead_mappings WHERE printer_name = ?", printerName)

	// Remove toolhead display names
	_, _ = b.db.Exec("DELETE FROM toolhead_names WHERE printer_id = ?", printerID)

	// Delete the printer itself (ON DELETE CASCADE removes virtual_printer_files)
	_, err = b.db.Exec("DELETE FROM printer_configs WHERE printer_id = ?", printerID)

	b.mutex.Unlock()

	if err != nil {
		return fmt.Errorf("failed to delete printer config: %w", err)
	}

	log.Printf("🗑️  Deleted printer %s (%s) and freed all toolhead spool assignments", printerName, printerID)

	// Push freed spools to inventory in Spoolman (best effort, after lock released).
	for _, sid := range spoolIDsForSync {
		b.pushSpoolToInventory(sid)
	}

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
	locationName, err := b.GetConfigValue(ConfigKeyNFCInventoryLocation)
	if err != nil || locationName == "" {
		return nil
	}
	if err := b.spoolman.UpdateSpoolLocation(spoolID, locationName); err != nil {
		return fmt.Errorf("syncSpoolLocationForUnassignment spool %d to %q: %w", spoolID, locationName, err)
	}
	log.Printf("Synced unassigned spool %d to inventory location %q", spoolID, locationName)
	return nil
}

// --- Spoolman location sync helpers ---

// GetSpoolmanLocationSyncEnabled returns true when the bidirectional Spoolman location sync is on.
func (b *FilamentBridge) GetSpoolmanLocationSyncEnabled() (bool, error) {
	val, err := b.GetConfigValue(ConfigKeySpoolmanLocationSyncEnabled)
	if err != nil {
		return false, err
	}
	return val == "true", nil
}

// FormatToolheadLocation returns the canonical Spoolman location string for a toolhead.
// Format: "{printerName} - T{toolheadIndex}"  e.g. "Roci - T0"
func FormatToolheadLocation(printerName string, toolheadIndex int) string {
	return fmt.Sprintf("%s - T%d", printerName, toolheadIndex)
}

// ParseToolheadLocation parses a Spoolman location string produced by FormatToolheadLocation.
// Returns ok=false if the string does not match the expected format.
func ParseToolheadLocation(location string) (printerName string, toolheadIndex int, ok bool) {
	// Expected format: "{name} - T{index}"  e.g. "Roci - T0"
	const suffix = " - T"
	idx := strings.LastIndex(location, suffix)
	if idx < 0 {
		return "", 0, false
	}
	name := location[:idx]
	indexStr := location[idx+len(suffix):]
	n, err := strconv.Atoi(indexStr)
	if err != nil || n < 0 {
		return "", 0, false
	}
	return name, n, true
}

// pushSpoolToInventory moves a spool's Spoolman location to the configured inventory location.
// No-op if sync is disabled or inventory location is empty.
func (b *FilamentBridge) pushSpoolToInventory(spoolID int) {
	enabled, err := b.GetSpoolmanLocationSyncEnabled()
	if err != nil || !enabled {
		return
	}
	loc, err := b.GetConfigValue(ConfigKeyNFCInventoryLocation)
	if err != nil || loc == "" {
		return
	}
	if err := b.spoolman.UpdateSpoolLocation(spoolID, loc); err != nil {
		log.Printf("Warning: pushSpoolToInventory spool %d → %q: %v", spoolID, loc, err)
	}
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
		GcodePath:                    b.config.GcodePath,
		UploadsPath:                  b.config.UploadsPath,
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

	// Bidirectional Spoolman location sync: write auto-generated location name to Spoolman.
	if syncEnabled, _ := b.GetSpoolmanLocationSyncEnabled(); syncEnabled {
		locationName := FormatToolheadLocation(printerName, toolheadID)
		if syncErr := b.spoolman.UpdateSpoolLocation(spoolID, locationName); syncErr != nil {
			log.Printf("Warning: Spoolman location sync on assign spool %d → %q: %v", spoolID, locationName, syncErr)
		}
	}

	if enabled && previousSpoolID > 0 && previousSpoolID != spoolID {
		locationName, err := b.GetConfigValue(ConfigKeyNFCInventoryLocation)
		if err != nil {
			log.Printf("Warning: Failed to get inventory location for auto-assign: %v", err)
			return nil
		}
		if locationName != "" {
			if err := b.spoolman.UpdateSpoolLocation(previousSpoolID, locationName); err != nil {
				log.Printf("Warning: Failed to auto-assign previous spool %d to location '%s': %v", previousSpoolID, locationName, err)
			} else {
				log.Printf("Auto-assigned previous spool %d to inventory location '%s'", previousSpoolID, locationName)
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

	// Get current spool before deleting so we can push it to inventory in Spoolman.
	var currentSpoolID int
	_ = b.db.QueryRow(
		"SELECT spool_id FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ?",
		printerName, toolheadID,
	).Scan(&currentSpoolID)

	_, err := b.db.Exec(
		"DELETE FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ?",
		printerName, toolheadID,
	)
	b.mutex.Unlock()
	if err != nil {
		return fmt.Errorf("failed to unmap toolhead: %w", err)
	}

	if currentSpoolID > 0 {
		b.pushSpoolToInventory(currentSpoolID)
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

			// Throttle: skip if the state cache says it's too soon to poll again.
			// Kept here (not inside monitorPrusaLink) so direct calls in tests bypass it.
			if cached, err := b.GetLastKnownState(printerID); err == nil {
				if !cached.NextPollAt.IsZero() && time.Now().Before(cached.NextPollAt) {
					return
				}
			}

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
// checkFilamentSufficiency estimates the filament required for the active job and compares it
// against the remaining weight on each assigned spool. Intended to be called in a goroutine
// at print-start so it never blocks the monitor loop.
//
// Strategy:
//  1. Try the lightweight /api/v1/files metadata endpoint first.
//  2. If that returns no filament data, fall back to a full G-code download and parse.
//  3. Compare per-toolhead required grams against spool remaining_weight from Spoolman.
//  4. Store any warnings in b.printerWarnings[printerID]; cleared when the print ends.
func (b *FilamentBridge) checkFilamentSufficiency(printerID, printerName, filePath string, client *PrusaLinkClient) {
	log.Printf("🔍 [FilamentCheck] Checking filament sufficiency for %s (%s)", printerName, filePath)

	// --- Step 1: try file metadata (lightweight) ---
	requiredByTool := make(map[int]float64) // toolhead index → grams required

	fileInfo, err := client.GetFileInfo(filePath)
	if err != nil {
		log.Printf("⚠️  [FilamentCheck] File metadata unavailable for %s: %v — falling back to G-code download", filePath, err)
	}
	if fileInfo != nil {
		for _, f := range fileInfo.Filament {
			if f.Weight > 0 {
				requiredByTool[f.ToolheadID] = f.Weight
			}
		}
		log.Printf("📋 [FilamentCheck] Got filament data from metadata: %v", requiredByTool)
	}

	// --- Step 2: fallback — download and parse G-code ---
	if len(requiredByTool) == 0 {
		log.Printf("📥 [FilamentCheck] Downloading G-code for filament data: %s", filePath)
		gcodeContent, err := client.GetGcodeFileWithRetry(filePath, b.config.PrusaLinkFileDownloadTimeout)
		if err != nil {
			log.Printf("⚠️  [FilamentCheck] Could not download G-code for %s: %v", filePath, err)
			return
		}
		usage, err := client.ParseGcodeFilamentUsage(gcodeContent)
		if err != nil || len(usage) == 0 {
			log.Printf("⚠️  [FilamentCheck] No filament data in G-code for %s", filePath)
			return
		}
		for toolIdx, u := range usage {
			if u.Grams > 0 {
				requiredByTool[toolIdx] = u.Grams
			}
		}
		log.Printf("📋 [FilamentCheck] Got filament data from G-code: %v", requiredByTool)
	}

	if len(requiredByTool) == 0 {
		return
	}

	// --- Step 3: look up assigned spools ---
	// Primary: NFC/active assignments (toolhead_spool_assignments)
	assignments, err := b.GetAllCurrentAssignments(printerID)
	if err != nil {
		log.Printf("⚠️  [FilamentCheck] Could not get assignments for %s: %v", printerID, err)
	}
	spoolByTool := make(map[int]int) // toolhead index → spool ID
	for _, a := range assignments {
		spoolByTool[a.ToolheadIndex] = a.SpoolmanSpoolID
	}

	// Fallback: Print-Ops toolhead_mappings (for printers not using NFC assignments)
	if len(spoolByTool) == 0 {
		mappings, err := b.GetToolheadMappings(printerName)
		if err != nil {
			log.Printf("⚠️  [FilamentCheck] Could not get toolhead mappings for %s: %v", printerName, err)
		}
		for toolID, m := range mappings {
			if m.SpoolID > 0 {
				spoolByTool[toolID] = m.SpoolID
			}
		}
	}

	// --- Step 4: fetch spool remaining weights from Spoolman ---
	allSpools, err := b.spoolman.GetAllSpools()
	if err != nil {
		log.Printf("⚠️  [FilamentCheck] Could not fetch spools from Spoolman: %v", err)
		return
	}
	remainingBySpoolID := make(map[int]SpoolmanSpool, len(allSpools))
	for _, s := range allSpools {
		remainingBySpoolID[s.ID] = s
	}

	// --- Step 5: compare and build warnings ---
	var warnings []PrinterWarning
	for toolIdx, required := range requiredByTool {
		spoolID, assigned := spoolByTool[toolIdx]
		if !assigned {
			warnings = append(warnings, PrinterWarning{
				ToolheadIndex: toolIdx,
				SpoolID:       0,
				Required:      required,
				Remaining:     0,
				Message:       fmt.Sprintf("T%d: needs %.0fg but no spool assigned", toolIdx, required),
			})
			continue
		}
		spool, found := remainingBySpoolID[spoolID]
		if !found {
			continue
		}
		if spool.RemainingWeight < required {
			spoolName := spool.Name
			if spoolName == "" {
				spoolName = fmt.Sprintf("Spool #%d", spoolID)
			}
			warnings = append(warnings, PrinterWarning{
				ToolheadIndex: toolIdx,
				SpoolID:       spoolID,
				Required:      required,
				Remaining:     spool.RemainingWeight,
				Message: fmt.Sprintf("T%d: needs %.0fg, ~%.0fg remaining (%s)",
					toolIdx, required, spool.RemainingWeight, spoolName),
			})
		}
	}

	b.printerWarningsMu.Lock()
	if len(warnings) > 0 {
		b.printerWarnings[printerID] = warnings
		log.Printf("⚠️  [FilamentCheck] %d filament warning(s) for %s: %v", len(warnings), printerName, warnings)
	} else {
		delete(b.printerWarnings, printerID)
		log.Printf("✅ [FilamentCheck] Sufficient filament for all toolheads on %s", printerName)
	}
	b.printerWarningsMu.Unlock()
}

//	PAUSED            → keep wasPrinting=true (print will resume)
//	ATTENTION         → keep wasPrinting=true (filament runout — user swaps spool, then resumes)
//	IDLE / FINISHED   → if wasPrinting: print completed normally → process full G-code usage
//	STOPPED           → if wasPrinting: job was cancelled → log partial usage from progress %
func (b *FilamentBridge) monitorPrusaLink(printerID string, config PrinterConfig) error {
	client := NewPrusaLinkClient(config.IPAddress, config.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)
	cl := b.getCommLog(printerID)

	cl.Append("TX", "poll_status", "GET /api/v1/status", "")
	status, statusRaw, err := client.GetStatus()
	if err != nil {
		cl.Append("RX", "error", fmt.Sprintf("GET /api/v1/status failed: %v", err), "")
		failures, _ := b.IncrementFailureCount(printerID)
		if failures == 1 {
			log.Printf("[PRUSALINK] printer=%s poll_failure=1 error=%v", printerID, err)
		} else if failures >= 3 {
			log.Printf("[PRUSALINK] printer=%s UNREACHABLE consecutive_failures=%d", printerID, failures)
		}
		return nil
	}
	cl.Append("RX", "poll_status", fmt.Sprintf("state=%s nozzle=%.0f°C bed=%.0f°C", status.Printer.State, status.Printer.TempNozzle, status.Printer.TempBed), prettyJSON(statusRaw))

	cl.Append("TX", "poll_job", "GET /api/v1/job", "")
	jobInfo, jobRaw, err := client.GetJobInfo()
	if err != nil {
		cl.Append("RX", "error", fmt.Sprintf("GET /api/v1/job failed: %v", err), "")
		log.Printf("Warning: Failed to get job info from %s (%s): %v", config.IPAddress, printerID, err)
		jobInfo = &PrusaLinkJob{}
	} else {
		jobSummary := "no active job"
		if jobInfo.File.Name != "" {
			jobSummary = fmt.Sprintf("job_id=%d file=%q progress=%.1f%%", jobInfo.ID, jobInfo.File.DisplayName, jobInfo.Progress)
		}
		cl.Append("RX", "poll_job", jobSummary, prettyJSON(jobRaw))
	}

	b.rawResponsesMu.Lock()
	b.rawResponses[printerID] = &PrusaLinkRawCapture{
		CapturedAt: time.Now(),
		Status:     statusRaw,
		Job:        jobRaw,
	}
	b.rawResponsesMu.Unlock()

	// Detect API shape changes (e.g. unknown objects received from PrusaLink).
	// The "job" sub-object in /api/v1/status is state-dependent: present during printing,
	// absent when idle/finished. Strip it before shape comparison so a print completing
	// doesn't trigger a false-positive "removed fields" alert.
	for _, check := range []struct {
		endpoint string
		body     []byte
	}{
		{"status", normalizePrusaLinkStatusForMonitor(statusRaw)},
		{"job", jobRaw},
	} {
		if len(check.body) == 0 {
			continue
		}
		added, removed, changed := b.apiMonitor.Check(printerID, check.endpoint, check.body)
		if changed {
			diff := FormatDiff("/api/v1/"+check.endpoint, added, removed)
			cl.Append("EV", "api_change", diff, "")
			if b.apiMonitor.ShouldAlert(printerID) {
				b.addPrintError(printerID, "api/v1/"+check.endpoint,
					"PrusaLink API shape changed — "+diff+". Check struct and update if needed.")
				b.apiMonitor.SetAlertPending(printerID)
			}
		}
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
	if prevState != currentState && prevState != "" {
		cl.Append("EV", "state_change", fmt.Sprintf("%s → %s", prevState, currentState), "")
	}

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
			status.Job.TimePrinting,
			status.Job.TimeRemaining,
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
		// Print is active — store the filename on first detection, keep wasPrinting=true.
		// Unmute API-change alerts so unexpected response changes during a print are still detectable.
		b.apiMonitor.UnmuteOnPrint(printerID)
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
			// Check whether any spool has enough filament for this print.
			// Runs in a goroutine so it never delays the monitor loop.
			go b.checkFilamentSufficiency(printerID, config.Name, currentJobFilename, client)
		}
		if jobInfo.ID != 0 {
			_ = b.UpdateSessionProgress(printerID, jobInfo.ID, printProgress, jobInfo.TimePrinting)

			// Progress snapshots: capture at configured interval or milestone thresholds.
			targets := snapshotTargets(config.ProgressSnapshotConfig)
			if len(targets) > 0 {
				if session, _ := b.GetActivePrintSession(printerID, jobInfo.ID); session != nil {
					for _, tgt := range crossedTargets(targets, session.LastSnapshotProgress, printProgress) {
						b.mutex.RLock()
						snapStart := b.printStartTime[printerID]
						b.mutex.RUnlock()
						if err := b.captureProgressSnapshot(config, printerID, snapStart, tgt); err != nil {
							log.Printf("[SNAPSHOT] progress %.0f%% failed for %s: %v", tgt, printerID, err)
						}
						if uErr := b.UpdateSessionSnapshotProgress(printerID, jobInfo.ID, tgt); uErr != nil {
							log.Printf("[SNAPSHOT] failed to update snapshot progress for %s: %v", printerID, uErr)
						}
					}
				}
			}
		}

		// Resume snapshot: printer just recovered from an attention/runout event.
		if prevState == StateAttention {
			log.Printf("▶️  Print RESUMED on %s (%s): %.1f%% for job: %s",
				config.IPAddress, printerID, printProgress, jobName)
			b.mutex.RLock()
			resumeStart := b.printStartTime[printerID]
			b.mutex.RUnlock()
			if !resumeStart.IsZero() {
				if err := b.captureInterruptSnapshot(config, printerID, resumeStart, printProgress, "resume"); err != nil {
					log.Printf("[SNAPSHOT] resume snapshot failed for %s: %v", config.IPAddress, err)
				}
			}
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
		// Printer requires attention (filament runout, door open, etc.).
		// On first transition: record as a pending runout event so print_history can later
		// be split into virtual toolhead segments proportional to progress at this point.
		if prevState != StateAttention {
			log.Printf("🟡 ATTENTION on %s (%s): printer requires attention at %.1f%% for job: %s",
				config.IPAddress, printerID, printProgress, jobName)
			b.mutex.RLock()
			startTime := b.printStartTime[printerID]
			b.mutex.RUnlock()
			if !startTime.IsZero() {
				if rErr := b.AddPendingRunout(printerID, startTime, 0, printProgress); rErr != nil {
					log.Printf("[PRUSALINK] printer=%s failed to record attention event: %v", printerID, rErr)
				}
				// Capture a snapshot at this attention event; store pending until print_history ID is known.
				if err := b.captureInterruptSnapshot(config, printerID, startTime, printProgress, "attention"); err != nil {
					log.Printf("[SNAPSHOT] attention snapshot failed for %s: %v", config.IPAddress, err)
				}
			}
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
				b.printerWarningsMu.Lock()
				delete(b.printerWarnings, printerID)
				b.printerWarningsMu.Unlock()
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
			b.printerWarningsMu.Lock()
			delete(b.printerWarnings, printerID)
			b.printerWarningsMu.Unlock()

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
				b.printerWarningsMu.Lock()
				delete(b.printerWarnings, printerID)
				b.printerWarningsMu.Unlock()
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

			// PrusaLink clears job.progress to 0 when transitioning to STOPPED.
			// Recover the last-known progress from active_print_sessions so filament
			// can be scaled correctly for mid-print cancellations.
			effectiveProgress := printProgress
			if effectiveProgress <= 0 && effectiveJobIDStop != 0 {
				if session, sErr := b.GetActivePrintSession(printerID, effectiveJobIDStop); sErr == nil && session != nil && session.LastSeenProgress > 0 {
					effectiveProgress = session.LastSeenProgress
					log.Printf("[PRUSALINK] printer=%s job=%d: recovered last-known progress %.1f%% (PrusaLink reported 0%%)", printerID, effectiveJobIDStop, effectiveProgress)
				}
			}
			handleErr := b.handlePrusaLinkPrintCancelled(printerID, config, effectiveJobIDStop, filenameToUse, effectiveProgress)

			b.mutex.Lock()
			b.processingPrints[printerID] = false
			b.currentJobFile[printerID] = ""
			b.currentJobDisplayName[printerID] = ""
			b.currentJobID[printerID] = 0
			b.mutex.Unlock()
			b.printerWarningsMu.Lock()
			delete(b.printerWarnings, printerID)
			b.printerWarningsMu.Unlock()

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

	bambuCL := b.getCommLog(printerID)

	status, err := client.GetCurrentStatus()
	if err != nil {
		// No MQTT message received yet — still connecting or printer is off.
		bambuCL.Append("RX", "error", fmt.Sprintf("GetCurrentStatus: %v", err), "")
		log.Printf("[BAMBU] No status yet from %s (%s): %v", printerID, config.Name, err)
		return nil
	}
	bambuCL.Append("RX", "mqtt_recv", fmt.Sprintf("state=%s progress=%d%% file=%q", status.GcodeState, status.McPercent, status.GcodeFile), "")

	currentState := mapBambuState(status.GcodeState)
	progressPct := float64(status.McPercent)

	b.mutex.RLock()
	wasPrinting := b.wasPrinting[printerID]
	storedJobFile := b.currentJobFile[printerID]
	prevState := b.previousState[printerID]
	b.mutex.RUnlock()

	if prevState != currentState && prevState != "" {
		bambuCL.Append("EV", "state_change", fmt.Sprintf("%s → %s", prevState, currentState), "")
	}

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
		startTime := b.printStartTime[printerID]
		b.mutex.Unlock()

		// Resume snapshot: Bambu print resumed after a pause.
		if prevState == StatePaused && !startTime.IsZero() {
			log.Printf("[BAMBU] ▶️  Print RESUMED on %s (%s): %.1f%%", printerID, config.Name, progressPct)
			if err := b.captureInterruptSnapshot(config, printerID, startTime, progressPct, "resume"); err != nil {
				log.Printf("[BAMBU][SNAPSHOT] resume snapshot failed for %s: %v", config.IPAddress, err)
			}
		}

		// Progress snapshots: capture at configured interval or milestone thresholds.
		targets := snapshotTargets(config.ProgressSnapshotConfig)
		if len(targets) > 0 && !startTime.IsZero() {
			b.mutex.RLock()
			lastSnapPct := b.lastSnapshotPct[printerID]
			b.mutex.RUnlock()
			for _, tgt := range crossedTargets(targets, lastSnapPct, progressPct) {
				if err := b.captureProgressSnapshot(config, printerID, startTime, tgt); err != nil {
					log.Printf("[BAMBU][SNAPSHOT] progress %.0f%% failed for %s: %v", tgt, printerID, err)
				}
				b.mutex.Lock()
				b.lastSnapshotPct[printerID] = tgt
				b.mutex.Unlock()
			}
		}

	case StatePaused:
		if prevState != StatePaused {
			log.Printf("[BAMBU] ⏸️  Print paused on %s (%s) at %.1f%%", printerID, config.Name, progressPct)
			b.mutex.RLock()
			startTime := b.printStartTime[printerID]
			b.mutex.RUnlock()
			if !startTime.IsZero() {
				if err := b.captureInterruptSnapshot(config, printerID, startTime, progressPct, "attention"); err != nil {
					log.Printf("[BAMBU][SNAPSHOT] pause snapshot failed for %s: %v", config.IPAddress, err)
				}
			}
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
			b.lastSnapshotPct[printerID] = 0
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
			b.lastSnapshotPct[printerID] = 0
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
		log.Printf("ℹ️  Cancelled print at 0%% progress on %s — recording cancellation with no filament deduction", printerName)
		b.mutex.RLock()
		startTime := b.printStartTime[printerID]
		b.mutex.RUnlock()
		sessionID := newSessionID()
		spoolID, _ := b.GetToolheadMapping(printerName, 0)
		printID, _ := b.LogPrintUsageFull(printerName, 0, spoolID, 0, jobName+" [CANCELLED]",
			0, "cancelled", "", sessionID, "prusalink")
		if printID > 0 {
			if err := b.SnapshotAssignmentsForPrint(printID, printerID, startTime); err != nil {
				log.Printf("Warning: failed to snapshot NFC assignments for 0%% cancelled print %d: %v", printID, err)
			}
			b.flushPendingSnapshots(printerID, startTime, printID)
			b.capturePrusaLinkSnapshot(config, printID, "cancelled")
		}
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

	gcodeUsageCancelled, err := prusaClient.ParseGcodeFilamentUsage(gcodeContent)
	if err != nil || len(gcodeUsageCancelled) == 0 {
		log.Printf("⚠️  Could not parse G-code for cancelled print on %s — skipping filament deduction", printerName)
		return nil
	}

	// Scale down by progress percentage
	partialUsage := make(map[int]float64)
	partialMM := make(map[int]float64)
	for toolheadID, u := range gcodeUsageCancelled {
		partial := u.Grams * scale
		if partial > 0 {
			partialUsage[toolheadID] = partial
			partialMM[toolheadID] = u.MM * scale
			log.Printf("📉 Cancelled print partial usage: toolhead %d → %.2fg (%.1f%% of %.2fg)",
				toolheadID, partial, progressPct, u.Grams)
			if config.DebugLog && jobID != 0 {
				_ = b.AppendPrintDebugLog(printerID, jobID, fmt.Sprintf(
					"[PARTIAL_FILAMENT] T%d: %.2fg (%.1f%% of %.2fg full)", toolheadID, partial, progressPct, u.Grams))
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
			_ = b.AppendFilamentUsage(printID, toolheadID, 0, spoolID, partialMM[toolheadID], usedG)
			if firstPrintID == 0 {
				firstPrintID = printID
			}
		}
	}
	if firstPrintID > 0 {
		if err := b.SnapshotAssignmentsForPrint(firstPrintID, printerID, startTime); err != nil {
			log.Printf("Warning: failed to snapshot NFC assignments for cancelled print %d: %v", firstPrintID, err)
		}
		if err := b.savePrintFile(firstPrintID, "gcode", filepath.Base(filename), "", gcodeContent); err != nil {
			log.Printf("Warning: could not save gcode file for cancelled print %d: %v", firstPrintID, err)
		}
		b.flushPendingSnapshots(printerID, startTime, firstPrintID)
		b.capturePrusaLinkSnapshot(config, firstPrintID, "cancelled")
		if config.DebugLog && jobID != 0 {
			if lErr := b.LinkDebugLogsToPrint(printerID, jobID, firstPrintID); lErr != nil {
				log.Printf("[PRUSALINK] printer=%s job=%d failed to link debug logs: %v", printerID, jobID, lErr)
			}
		}
	}

	return nil
}

// filamentSegment describes one virtual toolhead's share of a print's filament usage.
type filamentSegment struct {
	virtualToolhead  int
	originalToolhead int
	weightG          float64
	progressStart    float64 // 0..100
	progressEnd      float64 // 0..100
}

// splitFilamentByRunouts distributes filament usage across virtual toolhead segments
// based on printer attention events (runouts/pauses). Toolheads without runout events
// pass through unchanged. When runouts are present for a toolhead, its usage is split
// proportionally by progress percentage into N+1 segments numbered sequentially.
func splitFilamentByRunouts(filamentUsage map[int]float64, runouts []PendingRunoutEvent) []filamentSegment {
	// Group runouts by original toolhead.
	runoutsByToolhead := make(map[int][]PendingRunoutEvent)
	for _, r := range runouts {
		runoutsByToolhead[r.ToolheadIndex] = append(runoutsByToolhead[r.ToolheadIndex], r)
	}

	// Sort toolheads for deterministic output.
	toolheads := make([]int, 0, len(filamentUsage))
	for th := range filamentUsage {
		toolheads = append(toolheads, th)
	}
	sort.Ints(toolheads)

	var segs []filamentSegment
	nextVirtual := 0
	for _, th := range toolheads {
		totalG := filamentUsage[th]
		thRunouts := runoutsByToolhead[th]

		if len(thRunouts) == 0 {
			segs = append(segs, filamentSegment{nextVirtual, th, totalG, 0, 100})
			nextVirtual++
			continue
		}

		// Sort by progress ascending.
		sort.Slice(thRunouts, func(i, j int) bool {
			return thRunouts[i].ProgressPct < thRunouts[j].ProgressPct
		})

		prevPct := 0.0
		for _, r := range thRunouts {
			pct := r.ProgressPct
			if pct < 0.1 {
				pct = 0.1
			}
			if pct > 99.9 {
				pct = 99.9
			}
			if pct <= prevPct {
				continue // skip duplicate or out-of-order
			}
			fraction := (pct - prevPct) / 100.0
			segs = append(segs, filamentSegment{nextVirtual, th, totalG * fraction, prevPct, pct})
			nextVirtual++
			prevPct = pct
		}
		// Final segment from last runout to end.
		fraction := (100.0 - prevPct) / 100.0
		segs = append(segs, filamentSegment{nextVirtual, th, totalG * fraction, prevPct, 100})
		nextVirtual++
	}
	return segs
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
	gcodeUsage, err := prusaClient.ParseGcodeFilamentUsage(gcodeContent)
	if err != nil {
		errorMsg := fmt.Sprintf("failed to parse G-code for filament usage: %v", err)
		b.addPrintError(printerName, filename, errorMsg)
		return fmt.Errorf("%s", errorMsg)
	}

	// Check if we got any filament usage data
	if len(gcodeUsage) == 0 {
		errorMsg := "no filament usage data found in G-code file"
		b.addPrintError(printerName, filename, errorMsg)
		return fmt.Errorf("%s", errorMsg)
	}

	// Extract grams and mm into separate maps; grams map drives existing logic unchanged.
	filamentUsage := make(map[int]float64, len(gcodeUsage))
	filamentMM := make(map[int]float64, len(gcodeUsage))
	for t, u := range gcodeUsage {
		filamentUsage[t] = u.Grams
		filamentMM[t] = u.MM
	}

	log.Printf("Successfully parsed G-code file for filament usage: %+v", filamentUsage)

	printTimeSec, thumbnailB64 := ParseGcodeMetadata(gcodeContent)

	// Fallback: use the accumulated TimePrinting from PrusaLink status polls when gcode
	// gives 0 (e.g. binary bgcode without a parseable time comment, or missing metadata).
	if printTimeSec == 0 && jobID != 0 {
		if session, serr := b.GetActivePrintSession(printerID, jobID); serr == nil && session != nil && session.LastSeenTimePrinting > 0 {
			printTimeSec = session.LastSeenTimePrinting
			log.Printf("[PRUSALINK] printer=%s job=%d: gcode gave 0 print time, using PrusaLink TimePrinting=%ds (%.1fmin)",
				printerID, jobID, printTimeSec, float64(printTimeSec)/60.0)
		}
	}

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

	// Check for printer attention events (runouts/pauses) recorded during the print.
	// If any exist, split the single-toolhead usage into virtual segments — one per segment
	// between attention events — so the user can correct spool assignments post-print.
	runouts, _ := b.GetPendingRunouts(printerID, startTime)
	segments := splitFilamentByRunouts(filamentUsage, runouts)

	if len(runouts) > 0 {
		log.Printf("[PRUSALINK] printer=%s splitting %d toolhead(s) into %d segments due to %d attention event(s)",
			printerID, len(filamentUsage), len(segments), len(runouts))
	}

	sessionID := newSessionID()
	var firstPrintID int
	type segResult struct {
		printID         int
		virtualToolhead int
		spoolID         int
		progressStart   float64
	}
	var segResults []segResult

	for _, seg := range segments {
		spoolID, _ := b.GetToolheadMapping(printerName, seg.originalToolhead)
		printID, _ := b.LogPrintUsageFull(printerName, seg.virtualToolhead, spoolID, seg.weightG, jobName,
			printTimeMin, "completed", thumbnailB64, sessionID, "prusalink")
		if printID > 0 {
			// Distribute mm proportionally from the original toolhead's full mm value.
			segMM := 0.0
			if totalG := filamentUsage[seg.originalToolhead]; totalG > 0 {
				segMM = filamentMM[seg.originalToolhead] * (seg.weightG / totalG)
			}
			_ = b.AppendFilamentUsage(printID, seg.virtualToolhead, 0, spoolID, segMM, seg.weightG)
			if firstPrintID == 0 {
				firstPrintID = printID
			}
			segResults = append(segResults, segResult{printID, seg.virtualToolhead, spoolID, seg.progressStart})
		}
	}

	_ = b.DeletePendingRunouts(printerID, startTime)

	if firstPrintID > 0 {
		if err := b.SnapshotAssignmentsForPrint(firstPrintID, printerID, startTime); err != nil {
			log.Printf("Warning: failed to snapshot NFC assignments for print %d: %v", firstPrintID, err)
		}
		// Add start spool events for virtual toolhead segments (attention-event splits).
		// T0's start event is already covered by SnapshotAssignmentsForPrint above.
		for _, sr := range segResults {
			if sr.virtualToolhead > 0 && sr.spoolID > 0 {
				_ = b.AddPrintSpoolEvent(PrintSpoolEvent{
					PrintHistoryID:     firstPrintID,
					ToolheadIndex:      sr.virtualToolhead,
					NewSpoolmanSpoolID: sr.spoolID,
					EventType:          "start",
					PrintProgressPct:   sr.progressStart,
				})
			}
		}
		// Save gcode to disk; best-effort — never aborts the print record.
		if err := b.savePrintFile(firstPrintID, "gcode", filepath.Base(filename), "", gcodeContent); err != nil {
			log.Printf("Warning: could not save gcode file for print %d: %v", firstPrintID, err)
		}
		// Promote any attention snapshots captured mid-print, then grab a completion snapshot.
		b.flushPendingSnapshots(printerID, startTime, firstPrintID)
		b.capturePrusaLinkSnapshot(config, firstPrintID, "finished")
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

// AcknowledgePrintError marks a print error as acknowledged.
// If the error was an API-change alert, the pending flag on the shape monitor
// is also cleared so the next change can fire a new notification.
func (b *FilamentBridge) AcknowledgePrintError(errorID string) error {
	b.errorMutex.Lock()
	pe, exists := b.printErrors[errorID]
	if !exists {
		b.errorMutex.Unlock()
		return fmt.Errorf("print error not found: %s", errorID)
	}
	pe.Acknowledged = true
	b.printErrors[errorID] = pe
	isAPIChange := strings.HasPrefix(pe.Filename, "api/v1/")
	printerID := pe.PrinterName
	b.errorMutex.Unlock()

	if isAPIChange {
		b.apiMonitor.ClearAlert(printerID)
	}
	return nil
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

	b.mutex.RLock()
	jobDisplayNames := make(map[string]string, len(b.currentJobDisplayName))
	maps.Copy(jobDisplayNames, b.currentJobDisplayName)
	b.mutex.RUnlock()

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
					Name:      printerName,
					State:     StateVirtual,
					SortOrder: printerConfig.SortOrder,
				}
				continue
			}

			// OctoPrint printers push data; no polling status available
			if printerConfig.PrinterType == PrinterTypeOctoPrint {
				status.Printers[printerID] = PrinterData{
					Name:      printerName,
					State:     StateOctoPrint,
					SortOrder: printerConfig.SortOrder,
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
					Name:      printerName,
					State:     bambuState,
					SortOrder: printerConfig.SortOrder,
				}
				continue
			}

			client := NewPrusaLinkClient(printerConfig.IPAddress, printerConfig.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)

			// Get current status
			printerStatus, _, err := client.GetStatus()
			if err != nil {
				log.Printf("Warning: Failed to get printer status from %s (%s - %s): %v",
					printerConfig.IPAddress, printerID, printerName, err)
				status.Printers[printerID] = PrinterData{
					Name:      printerName,
					State:     StateOffline,
					SortOrder: printerConfig.SortOrder,
					DebugLog:  printerConfig.DebugLog,
				}
				continue
			}

			b.printerWarningsMu.Lock()
			filamentWarnings := b.printerWarnings[printerID]
			b.printerWarningsMu.Unlock()

			status.Printers[printerID] = PrinterData{
				Name:             printerName,
				State:            printerStatus.Printer.State,
				SortOrder:        printerConfig.SortOrder,
				DebugLog:         printerConfig.DebugLog,
				TempNozzle:       printerStatus.Printer.TempNozzle,
				TargetNozzle:     printerStatus.Printer.TargetNozzle,
				TempBed:          printerStatus.Printer.TempBed,
				TargetBed:        printerStatus.Printer.TargetBed,
				Progress:         printerStatus.Job.Progress,
				TimeRemaining:    printerStatus.Job.TimeRemaining,
				TimePrinting:     printerStatus.Job.TimePrinting,
				JobName:          jobDisplayNames[printerID],
				AxisZ:            printerStatus.Printer.AxisZ,
				Flow:             printerStatus.Printer.Flow,
				Speed:            printerStatus.Printer.Speed,
				FanHotend:        printerStatus.Printer.FanHotend,
				FanPrint:         printerStatus.Printer.FanPrint,
				FilamentWarnings: filamentWarnings,
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
		SELECT id, printer_name, printer_ip, filename, job_type, progress_pct, attempts
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
		attempts    int
	}
	var downloads []pendingDownload
	for rows.Next() {
		var d pendingDownload
		if err := rows.Scan(&d.id, &d.printerName, &d.printerIP, &d.filename, &d.jobType, &d.progressPct, &d.attempts); err != nil {
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
			newAttempts := d.attempts + 1
			_, _ = b.db.Exec(`
				UPDATE pending_gcode_downloads
				SET last_attempt = CURRENT_TIMESTAMP,
				    attempts     = attempts + 1,
				    last_error   = ?
				WHERE id = ?`, err.Error(), d.id)
			log.Printf("⚠️  G-code retry failed for %s (%s): %v", d.printerName, d.filename, err)

			// Drop permanent failures so they don't accumulate forever.
			errStr := err.Error()
			isPermanent := strings.Contains(errStr, "API error: 404") || // file gone
				strings.Contains(errStr, "API error: 403") || // access denied
				newAttempts >= 24 // ~3 days at default retry interval
			if isPermanent {
				msg := fmt.Sprintf("G-code download gave up after %d attempts for %s: %s", newAttempts, d.filename, errStr)
				log.Printf("⚠️  %s", msg)
				b.addPrintError(d.printerName, d.filename, msg)
				_, _ = b.db.Exec(`DELETE FROM pending_gcode_downloads WHERE id = ?`, d.id)
			}
			continue
		}

		retryGcodeUsage, err := prusaClient.ParseGcodeFilamentUsage(gcodeContent)
		if err != nil || len(retryGcodeUsage) == 0 {
			// Parse failure is permanent — remove and alert.
			msg := fmt.Sprintf("G-code retry downloaded %s but found no filament usage data; manual Spoolman update required", d.filename)
			log.Printf("⚠️  %s", msg)
			b.addPrintError(d.printerName, d.filename, msg)
			_, _ = b.db.Exec(`DELETE FROM pending_gcode_downloads WHERE id = ?`, d.id)
			continue
		}

		retryGrams := make(map[int]float64, len(retryGcodeUsage))
		retryMM := make(map[int]float64, len(retryGcodeUsage))
		for t, u := range retryGcodeUsage {
			retryGrams[t] = u.Grams
			retryMM[t] = u.MM
		}

		jobName := filepath.Base(d.filename)
		if d.jobType == "cancelled" {
			scale := (d.progressPct / 100.0) * 0.95
			if scale > 1.0 {
				scale = 1.0
			}
			partialGrams := make(map[int]float64)
			partialMM := make(map[int]float64)
			for toolheadID, g := range retryGrams {
				if partial := g * scale; partial > 0 {
					partialGrams[toolheadID] = partial
					partialMM[toolheadID] = retryMM[toolheadID] * scale
				}
			}
			retryGrams = partialGrams
			retryMM = partialMM
			jobName = d.filename + " [CANCELLED]"
		}

		if err := b.processFilamentUsage(d.printerName, retryGrams, jobName); err != nil {
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
		for toolheadID, usedG := range retryGrams {
			spoolID, _ := b.GetToolheadMapping(d.printerName, toolheadID)
			printID, _ := b.LogPrintUsageFull(d.printerName, toolheadID, spoolID, usedG, jobName,
				printTimeMin, status, thumbnailB64, sessionID, "prusalink")
			if printID > 0 {
				_ = b.AppendFilamentUsage(printID, toolheadID, 0, spoolID, retryMM[toolheadID], usedG)
				if firstPrintID == 0 {
					firstPrintID = printID
				}
			}
		}
		// Back-calculate print_started from the known print duration.
		// LogPrintUsageFull only does this when currentJobFile is set (live prints);
		// in the retry path that map is always empty so we fix it here.
		if printTimeMin > 0 {
			printStarted := time.Now().Add(-time.Duration(printTimeMin) * time.Minute)
			_, _ = b.db.Exec(`UPDATE print_history SET print_started = ? WHERE session_id = ?`,
				printStarted, sessionID)
		}
		if firstPrintID > 0 {
			if err := b.savePrintFile(firstPrintID, "gcode", filepath.Base(d.filename), "", gcodeContent); err != nil {
				log.Printf("Warning: could not save gcode file for retried print %d: %v", firstPrintID, err)
			}
		}

		// Clean up recovery stubs that were created when the app restarted mid-print.
		b.DeleteRecoveryStubs(d.printerName, filepath.Base(d.filename))
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

// PendingGcodeDownload is the API-visible record for a queued G-code retry.
type PendingGcodeDownload struct {
	ID          int    `json:"id"`
	PrinterName string `json:"printer_name"`
	PrinterIP   string `json:"printer_ip"`
	Filename    string `json:"filename"`
	JobType     string `json:"job_type"`
	Attempts    int    `json:"attempts"`
	LastError   string `json:"last_error"`
	CreatedAt   string `json:"created_at"`
	LastAttempt string `json:"last_attempt"`
}

// GetPendingGcodeDownloads returns all queued G-code downloads.
func (b *FilamentBridge) GetPendingGcodeDownloads() ([]PendingGcodeDownload, error) {
	rows, err := b.db.Query(`
		SELECT id, printer_name, printer_ip, filename, job_type,
		       attempts, COALESCE(last_error, ''),
		       COALESCE(created_at, ''), COALESCE(last_attempt, '')
		FROM pending_gcode_downloads
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending G-code downloads: %w", err)
	}
	defer rows.Close()
	var result []PendingGcodeDownload
	for rows.Next() {
		var p PendingGcodeDownload
		if err := rows.Scan(&p.ID, &p.PrinterName, &p.PrinterIP, &p.Filename, &p.JobType,
			&p.Attempts, &p.LastError, &p.CreatedAt, &p.LastAttempt); err != nil {
			continue
		}
		result = append(result, p)
	}
	return result, nil
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
	virtualGcodeUsage, err := client.ParseGcodeFilamentUsage(content)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to parse G-code: %w", err)
	}
	if len(virtualGcodeUsage) == 0 {
		return nil, nil, 0, fmt.Errorf(
			"no filament usage metadata found in '%s' — ensure your slicer writes filament weight comments",
			displayName,
		)
	}

	usage = make(map[int]float64, len(virtualGcodeUsage))
	virtualMM := make(map[int]float64, len(virtualGcodeUsage))
	for t, u := range virtualGcodeUsage {
		usage[t] = u.Grams
		virtualMM[t] = u.MM
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
			_ = b.AppendFilamentUsage(printID, toolheadID, 0, spoolID, virtualMM[toolheadID], usedG)
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
			COALESCE(ph.session_id, ''),
			COALESCE(ph.recovered, 0),
			COALESCE(ph.gcode_unavailable, 0)
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
		var recovered, gcodeUnavailable int
		if err := rows.Scan(
			&r.ID, &r.PrinterName, &r.ToolheadID, &r.SpoolID, &r.FilamentUsed,
			&r.PrintStarted, &r.PrintFinished, &r.JobName,
			&r.Notes, &r.Status, &r.PrintTimeMinutes,
			&r.ThumbnailBase64, &r.TotalCost, &r.Currency,
			&r.Source, &r.TotalDurationSec, &r.PrintDurationSec,
			&r.PauseDurationSec, &r.PauseCount, &r.CancelReason,
			&r.TimePrecision, &r.FilamentPrecision, &r.SessionID,
			&recovered, &gcodeUnavailable,
		); err != nil {
			log.Printf("Warning: failed to scan print history row: %v", err)
			continue
		}
		r.Recovered = recovered != 0
		r.GcodeUnavailable = gcodeUnavailable != 0
		records = append(records, r)
	}
	if records == nil {
		records = []PrintHistory{}
	}

	// Match pending G-code downloads to recovered stubs so the UI can show a retry button.
	pendings, _ := b.GetPendingGcodeDownloads()
	if len(pendings) > 0 {
		// Index by printer_name → list of pending downloads.
		type pending struct {
			id       int
			baseName string // filepath.Base(filename), e.g. "KOGI3D~1.BGC"
		}
		byPrinter := map[string][]pending{}
		for _, p := range pendings {
			byPrinter[p.PrinterName] = append(byPrinter[p.PrinterName], pending{p.ID, filepath.Base(p.Filename)})
		}
		for i, r := range records {
			if !r.Recovered {
				continue
			}
			// Stub job_name = "<base> [RECOVERED]"; strip the suffix to get <base>.
			baseName := strings.TrimSuffix(r.JobName, " [RECOVERED]")
			for _, p := range byPrinter[r.PrinterName] {
				if strings.EqualFold(p.baseName, baseName) {
					records[i].HasPendingDownload = true
					records[i].PendingDownloadID = p.id
					break
				}
			}
		}
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

// GetPrintSessionDetail returns a merged PrintHistory for all toolheads in a session.
// T0 (lowest toolhead_id) is used as the base record; FilamentUsages from every
// toolhead are combined and enriched with Spoolman prices.  SessionRecordIDs lists
// every print_history.id in the session so the UI can offer session-level delete.
func (b *FilamentBridge) GetPrintSessionDetail(sessionID string) (*PrintHistory, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session_id required")
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
		WHERE ph.session_id = ?
		ORDER BY ph.toolhead_id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session %s: %w", sessionID, err)
	}
	defer rows.Close()

	var records []PrintHistory
	var totalCost float64
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
			continue
		}
		totalCost += r.TotalCost
		records = append(records, r)
	}
	rows.Close()

	if len(records) == 0 {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	// Use T0 as the base record; pick the best time and thumbnail across all records.
	base := records[0]
	base.TotalCost = totalCost
	for _, r := range records[1:] {
		if r.ThumbnailBase64 != "" && base.ThumbnailBase64 == "" {
			base.ThumbnailBase64 = r.ThumbnailBase64
		}
		// PrusaLink may record print_time_minutes on any toolhead; take the max.
		if r.PrintTimeMinutes > base.PrintTimeMinutes {
			base.PrintTimeMinutes = r.PrintTimeMinutes
			base.TotalDurationSec = r.TotalDurationSec
			base.PrintDurationSec = r.PrintDurationSec
			base.PauseDurationSec = r.PauseDurationSec
			base.PauseCount = r.PauseCount
		}
	}

	ids := make([]int, len(records))
	for i, r := range records {
		ids[i] = r.ID
	}
	base.SessionRecordIDs = ids

	// Load filament usages for ALL records in the session in one query.
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	fuRows, err := b.db.Query(
		`SELECT id, print_id, tool_index, COALESCE(change_number, 0), COALESCE(spool_id, 0),
		        filament_used_mm, filament_used_grams
		 FROM print_filament_usage
		 WHERE print_id IN (`+placeholders+`)
		 ORDER BY tool_index, change_number`, args...)
	if err == nil {
		defer fuRows.Close()
		for fuRows.Next() {
			var fu PrintFilamentUsage
			if fuRows.Scan(&fu.ID, &fu.PrintID, &fu.ToolIndex, &fu.ChangeNumber, &fu.SpoolID,
				&fu.FilamentUsedMM, &fu.FilamentUsedG) == nil {
				base.FilamentUsages = append(base.FilamentUsages, fu)
			}
		}
	}

	// Enrich filament usages with Spoolman price-per-kg.
	spoolPriceCache := map[int]*float64{}
	for i := range base.FilamentUsages {
		sid := base.FilamentUsages[i].SpoolID
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
		base.FilamentUsages[i].PricePerKg = spoolPriceCache[sid]
	}

	// Pauses, tags, attachments, debug log from the primary (T0) record.
	pRows, err := b.db.Query(`
		SELECT id, print_id, paused_at, resumed_at, duration_sec, reason
		FROM print_pauses WHERE print_id = ? ORDER BY paused_at`, base.ID)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var p PrintPause
			if pRows.Scan(&p.ID, &p.PrintID, &p.PausedAt, &p.ResumedAt,
				&p.DurationSec, &p.Reason) == nil {
				base.Pauses = append(base.Pauses, p)
			}
		}
	}
	base.Tags, _ = b.GetPrintQualityTags(int64(base.ID))
	base.Attachments, _ = b.GetPrintAttachments(base.ID)
	base.HasDebugLog = b.HasPrintDebugLog(base.ID)

	return &base, nil
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

// RenamePrint updates the job_name of a print record and renames the associated gcode
// file on disk. Returns an error if no gcode attachment exists for the record.
func (b *FilamentBridge) RenamePrint(id int, newName string) error {
	// Strip any extension the caller may have included.
	if ext := filepath.Ext(newName); ext != "" {
		newName = newName[:len(newName)-len(ext)]
	}
	if newName == "" {
		return fmt.Errorf("name cannot be empty")
	}

	// Find the gcode attachment.
	rows, err := b.db.Query(
		`SELECT id, file_path, filename FROM print_attachments WHERE print_history_id = ? AND file_type = 'gcode' LIMIT 1`, id)
	if err != nil {
		return fmt.Errorf("failed to query gcode attachment: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return fmt.Errorf("no gcode file attached — attach a gcode file before renaming")
	}
	var attachID int
	var oldRelPath, oldFilename string
	if err := rows.Scan(&attachID, &oldRelPath, &oldFilename); err != nil {
		return fmt.Errorf("failed to read attachment row: %w", err)
	}
	rows.Close()

	ext := filepath.Ext(oldFilename)
	newFilename := newName + ext
	newRelPath := filepath.Join(filepath.Dir(oldRelPath), fmt.Sprintf("%d_%s", id, newFilename))

	oldAbs := filepath.Join(b.gcodePath(), oldRelPath)
	newAbs := filepath.Join(b.gcodePath(), newRelPath)
	if err := os.Rename(oldAbs, newAbs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to rename file: %w", err)
	}

	if _, err := b.db.Exec(
		`UPDATE print_attachments SET filename = ?, file_path = ? WHERE id = ?`,
		newFilename, newRelPath, attachID,
	); err != nil {
		return fmt.Errorf("failed to update attachment record: %w", err)
	}
	if _, err := b.db.Exec(
		`UPDATE print_history SET job_name = ? WHERE id = ?`, newName, id,
	); err != nil {
		return fmt.Errorf("failed to update job_name: %w", err)
	}
	return nil
}

// RenameAttachment renames a file attachment on disk and updates the DB record.
// For gcode attachments it also updates the parent print_history.job_name.
func (b *FilamentBridge) RenameAttachment(attachmentID int, newFilename string) error {
	if newFilename == "" {
		return fmt.Errorf("filename cannot be empty")
	}
	newFilename = filepath.Base(newFilename)

	a, err := b.GetPrintAttachment(attachmentID)
	if err != nil {
		return err
	}

	newRelPath := filepath.Join(filepath.Dir(a.FilePath), fmt.Sprintf("%d_%s", a.PrintHistoryID, newFilename))
	oldAbs := filepath.Join(b.gcodePath(), a.FilePath)
	newAbs := filepath.Join(b.gcodePath(), newRelPath)
	if err := os.Rename(oldAbs, newAbs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to rename file: %w", err)
	}

	if _, err := b.db.Exec(
		`UPDATE print_attachments SET filename = ?, file_path = ? WHERE id = ?`,
		newFilename, newRelPath, attachmentID,
	); err != nil {
		return fmt.Errorf("failed to update attachment record: %w", err)
	}

	if a.FileType == "gcode" {
		newJobName := newFilename
		if ext := filepath.Ext(newJobName); ext != "" {
			newJobName = newJobName[:len(newJobName)-len(ext)]
		}
		if _, err := b.db.Exec(
			`UPDATE print_history SET job_name = ? WHERE id = ?`, newJobName, a.PrintHistoryID,
		); err != nil {
			return fmt.Errorf("failed to update job_name: %w", err)
		}
	}
	return nil
}

// DeletePrintHistoryEntry removes a print history record. Files on disk are cleaned
// up first; the DB cascade handles all child rows (costs, attachments, tags, etc.).
func (b *FilamentBridge) DeletePrintHistoryEntry(id int) error {
	attachments, _ := b.GetPrintAttachments(id)
	for _, a := range attachments {
		absPath := filepath.Join(b.gcodePath(), a.FilePath)
		if err := os.Remove(absPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("Warning: could not delete attachment file %s: %v", absPath, err)
		}
	}
	_, err := b.db.Exec("DELETE FROM print_history WHERE id = ?", id)
	return err
}

// ParseGcodeMetadata extracts print time (seconds) and embedded thumbnail from raw gcode bytes.
// Handles both ASCII gcode (PrusaSlicer, OrcaSlicer, Cura comment format) and binary bgcode
// (PrusaSlicer 2.7+ .bgcode/.BGC format). Returns 0/"" if not found — both are optional.
func ParseGcodeMetadata(content []byte) (printTimeSec int, thumbnailBase64 string) {
	text := string(content)

	// Print time: ";TIME:20219.44" header at top of file (OrcaSlicer/Cura)
	timeRe := regexp.MustCompile(`;TIME:([0-9]+)`)
	if m := timeRe.FindStringSubmatch(text); len(m) >= 2 {
		fmt.Sscanf(m[1], "%d", &printTimeSec)
	}

	// PrusaSlicer ASCII and bgcode: "estimated printing time (normal mode) = 1d 6h 37m 3s"
	// bgcode embeds metadata as plain text (no ';' prefix); days component is present for long prints.
	if printTimeSec == 0 {
		prusaRe := regexp.MustCompile(`estimated printing time.*?=\s*(?:(\d+)d\s*)?(?:(\d+)h\s*)?(?:(\d+)m\s*)?(?:(\d+)s)?`)
		if m := prusaRe.FindStringSubmatch(text); m != nil {
			var days, h, min, sec int
			fmt.Sscanf(m[1], "%d", &days)
			fmt.Sscanf(m[2], "%d", &h)
			fmt.Sscanf(m[3], "%d", &min)
			fmt.Sscanf(m[4], "%d", &sec)
			printTimeSec = days*86400 + h*3600 + min*60 + sec
		}
	}

	// Thumbnail: ASCII gcode (PrusaSlicer / OrcaSlicer) embeds image as base64 comment blocks.
	// Supported formats (in preference order):
	//   "; thumbnail_JPG begin 96x96 3656"  ...lines...  "; thumbnail_JPG end"  (JPEG)
	//   "; thumbnail_PNG begin 96x96 3656"  ...lines...  "; thumbnail_PNG end"  (PNG)
	//   "; thumbnail begin 640x480 21604"   ...lines...  "; thumbnail end"       (PNG, PrusaSlicer)
	// QOI format is skipped — browsers cannot display it natively.
	lineRe := regexp.MustCompile(`(?m)^; ?`)
	type thumbSpec struct {
		startPat string
		endPat   string
		mimeType string
	}
	specs := []thumbSpec{
		{`; thumbnail_JPG begin [0-9x]+ [0-9]+`, `; thumbnail_JPG end`, "image/jpeg"},
		{`; thumbnail_PNG begin [0-9x]+ [0-9]+`, `; thumbnail_PNG end`, "image/png"},
		{`; thumbnail begin [0-9x]+ [0-9]+`, `; thumbnail end`, "image/png"},
	}
	for _, spec := range specs {
		startIdx := regexp.MustCompile(spec.startPat).FindStringIndex(text)
		if startIdx == nil {
			continue
		}
		afterStart := text[startIdx[1]:]
		endIdx := regexp.MustCompile(spec.endPat).FindStringIndex(afterStart)
		if endIdx == nil {
			continue
		}
		block := afterStart[:endIdx[0]]
		clean := lineRe.ReplaceAllString(block, "")
		clean = strings.ReplaceAll(clean, "\n", "")
		clean = strings.ReplaceAll(clean, "\r", "")
		clean = strings.TrimSpace(clean)
		if clean != "" {
			thumbnailBase64 = "data:" + spec.mimeType + ";base64," + clean
			break
		}
	}

	// bgcode (binary gcode, PrusaSlicer 2.7+): thumbnails are raw PNG/JPEG bytes embedded in
	// binary blocks — not base64 comment text. Detect by GCDE magic and scan for image data.
	if thumbnailBase64 == "" && len(content) >= 4 &&
		content[0] == 'G' && content[1] == 'C' && content[2] == 'D' && content[3] == 'E' {
		thumbnailBase64 = parseBgcodeThumbnail(content)
	}

	return
}

// parseBgcodeThumbnail scans the first 500KB of a binary bgcode file for an embedded PNG
// thumbnail. bgcode stores images as raw binary data inside binary blocks. We skip tiny
// thumbnails (< 5KB) used for printer display icons and return the first usable image.
func parseBgcodeThumbnail(content []byte) string {
	const scanLimit = 500 * 1024
	const minSize = 5000
	limit := len(content)
	if limit > scanLimit {
		limit = scanLimit
	}

	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47}
	iend := []byte{0x49, 0x45, 0x4E, 0x44}

	for i := 0; i < limit-8; i++ {
		if !bytes.Equal(content[i:i+4], pngMagic) {
			continue
		}
		endRel := bytes.Index(content[i+8:], iend)
		if endRel < 0 {
			continue
		}
		pngEnd := i + 8 + endRel + 8 // skip "IEND" (4) + CRC32 (4)
		if pngEnd > len(content) {
			continue
		}
		if pngEnd-i < minSize {
			i += 7 // skip past this tiny thumbnail
			continue
		}
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(content[i:pngEnd])
	}
	return ""
}

// ReparseGcodeMetadata re-reads the stored gcode attachment for the given print_history ID,
// extracts print time and thumbnail via ParseGcodeMetadata, and writes any new values back to
// the DB. Only updates fields that are currently zero/empty — does not overwrite existing data.
// Returns the parsed time (seconds) and thumbnail (data URI), which may be 0/"" if the
// attachment has no parseable metadata or no attachment exists.
func (b *FilamentBridge) ReparseGcodeMetadata(printID int) (printTimeSec int, thumbnailB64 string, err error) {
	var relPath string
	if err = b.db.QueryRow(
		`SELECT file_path FROM print_attachments WHERE print_history_id = ? AND file_type = 'gcode' LIMIT 1`,
		printID,
	).Scan(&relPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", nil
		}
		return 0, "", fmt.Errorf("reparse: attachment lookup: %w", err)
	}

	content, err := os.ReadFile(filepath.Join(b.gcodePath(), relPath))
	if err != nil {
		return 0, "", fmt.Errorf("reparse: read file: %w", err)
	}

	printTimeSec, thumbnailB64 = ParseGcodeMetadata(content)

	if printTimeSec > 0 {
		b.db.Exec(
			`UPDATE print_history SET print_time_minutes = ? WHERE id = ? AND (print_time_minutes IS NULL OR print_time_minutes = 0)`,
			float64(printTimeSec)/60.0, printID,
		)
	}
	if thumbnailB64 != "" {
		b.db.Exec(
			`UPDATE print_history SET thumbnail_path = ? WHERE id = ? AND (thumbnail_path IS NULL OR thumbnail_path = '')`,
			thumbnailB64, printID,
		)
	}
	return printTimeSec, thumbnailB64, nil
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
	b.getCommLog(p.PrinterID).Append("RX", "push_recv",
		fmt.Sprintf("OctoPrint push: status=%s file=%q", p.Status, p.FileName), "")

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
		// Idempotency: if the exact same file in this session was already recorded
		// (e.g. the plugin retried a push that actually succeeded), return the existing ID.
		// Match on both session_id and job_name so multi-toolhead sessions (same session_id,
		// different filenames) each get their own row.
		var existingID int
		err := b.db.QueryRow(
			`SELECT id FROM print_history WHERE session_id = ? AND job_name = ? LIMIT 1`,
			p.SessionID, p.FileName,
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

// SyncSpoolmanLocationsToDB reads all Spoolman spools and updates toolhead_mappings when a spool's
// location matches the canonical format "{printer_name} - T{index}". Also clears DB mappings when
// the spool has been moved away from the toolhead location in Spoolman.
// Returns (changed, error) — caller should call ws.BroadcastStatus() when changed is true.
// No-op if spoolman_location_sync_enabled is false.
func (b *FilamentBridge) SyncSpoolmanLocationsToDB() (bool, error) {
	enabled, err := b.GetSpoolmanLocationSyncEnabled()
	if err != nil || !enabled {
		return false, nil
	}

	spools, err := b.spoolman.GetAllSpools()
	if err != nil {
		return false, fmt.Errorf("SyncSpoolmanLocationsToDB: get spools: %w", err)
	}

	printerConfigs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return false, fmt.Errorf("SyncSpoolmanLocationsToDB: get printer configs: %w", err)
	}

	// Build lookup: canonical location name → (printerName, toolheadIndex)
	type slot struct {
		printerName string
		toolheadIdx int
	}
	knownLocations := map[string]slot{}
	for _, cfg := range printerConfigs {
		if cfg.IsVirtual {
			continue
		}
		for t := 0; t < cfg.Toolheads; t++ {
			knownLocations[FormatToolheadLocation(cfg.Name, t)] = slot{cfg.Name, t}
		}
	}

	// Build current DB state: spool_id → (printerName, toolheadIdx)
	b.mutex.RLock()
	rows, err := b.db.Query("SELECT printer_name, toolhead_id, spool_id FROM toolhead_mappings")
	b.mutex.RUnlock()
	if err != nil {
		return false, fmt.Errorf("SyncSpoolmanLocationsToDB: read mappings: %w", err)
	}
	type dbMapping struct {
		printerName string
		toolheadIdx int
	}
	dbBySpoolID := map[int]dbMapping{}
	dbBySlot := map[string]int{} // "printerName|toolheadIdx" → spoolID
	for rows.Next() {
		var pName string
		var tID, sID int
		if rows.Scan(&pName, &tID, &sID) == nil {
			dbBySpoolID[sID] = dbMapping{pName, tID}
			dbBySlot[fmt.Sprintf("%s|%d", pName, tID)] = sID
		}
	}
	rows.Close()

	changed := false

	// Build a quick lookup of Spoolman location by spool ID.
	spoolLocation := map[int]string{}
	for _, spool := range spools {
		spoolLocation[spool.ID] = spool.Location
	}

	// Pass 1 (removals): for each DB mapping, if the spool's Spoolman location no longer matches → remove.
	// Run removals before additions so that a slot reassignment (spool A replaced by spool B in Spoolman)
	// settles in a single sync cycle rather than requiring two.
	for spoolID, m := range dbBySpoolID {
		expectedLoc := FormatToolheadLocation(m.printerName, m.toolheadIdx)
		actualLoc, exists := spoolLocation[spoolID]
		if exists && actualLoc == expectedLoc {
			continue // still correct
		}
		// Location changed or spool not found in Spoolman — clear the DB mapping.
		b.mutex.Lock()
		_, _ = b.db.Exec(
			"DELETE FROM toolhead_mappings WHERE printer_name = ? AND toolhead_id = ? AND spool_id = ?",
			m.printerName, m.toolheadIdx, spoolID,
		)
		b.mutex.Unlock()
		log.Printf("SyncSpoolmanLocationsToDB: DB→cleared spool %d from %s T%d (Spoolman location now %q)", spoolID, m.printerName, m.toolheadIdx, actualLoc)
		// Update in-memory state so Pass 2 doesn't see a stale conflict on this slot.
		slotKey := fmt.Sprintf("%s|%d", m.printerName, m.toolheadIdx)
		delete(dbBySlot, slotKey)
		delete(dbBySpoolID, spoolID)
		changed = true
	}

	// Pass 2 (additions): for each Spoolman spool with a known-format location, ensure DB matches.
	for _, spool := range spools {
		s, ok := knownLocations[spool.Location]
		if !ok {
			continue
		}
		slotKey := fmt.Sprintf("%s|%d", s.printerName, s.toolheadIdx)
		if existing, has := dbBySlot[slotKey]; has && existing == spool.ID {
			continue // already correct
		}
		// Conflict: another spool still mapped to this slot in DB — skip with warning.
		if existing, has := dbBySlot[slotKey]; has && existing != spool.ID {
			log.Printf("SyncSpoolmanLocationsToDB: conflict — spool %d has location %q but DB maps slot %s to spool %d; skipping",
				spool.ID, spool.Location, slotKey, existing)
			continue
		}
		// Insert/replace the mapping.
		b.mutex.Lock()
		_, dbErr := b.db.Exec(
			"INSERT OR REPLACE INTO toolhead_mappings (printer_name, toolhead_id, spool_id, mapped_at) VALUES (?, ?, ?, ?)",
			s.printerName, s.toolheadIdx, spool.ID, time.Now(),
		)
		b.mutex.Unlock()
		if dbErr != nil {
			log.Printf("SyncSpoolmanLocationsToDB: failed to update mapping spool %d → %s T%d: %v", spool.ID, s.printerName, s.toolheadIdx, dbErr)
			continue
		}
		log.Printf("SyncSpoolmanLocationsToDB: Spoolman→DB spool %d → %s T%d", spool.ID, s.printerName, s.toolheadIdx)
		dbBySlot[slotKey] = spool.ID
		dbBySpoolID[spool.ID] = dbMapping{s.printerName, s.toolheadIdx}
		changed = true
	}

	return changed, nil
}

// ─── Dashboard Stats ─────────────────────────────────────────────────────────

// DashboardStats holds aggregated print statistics for the dashboard.
type DashboardStats struct {
	TotalPrintsAllTime int     `json:"total_prints_all_time"`
	Prints30d          int     `json:"prints_30d"`
	FilamentUsed30dG   float64 `json:"filament_used_30d_g"`
	TotalCost30d       float64 `json:"total_cost_30d"`
	AvgPrintTimeMin    float64 `json:"avg_print_time_min"`
	Currency           string  `json:"currency"`
}

// GetDashboardStats returns aggregate print stats for the dashboard view.
// Uses julianday() for date comparisons — direct datetime() TEXT comparison is broken with go-sqlite3 v1.14.x.
// print_history has no created_at; print_finished is when a print completes.
func (b *FilamentBridge) GetDashboardStats() (*DashboardStats, error) {
	s := &DashboardStats{}
	b.db.QueryRow(`SELECT COUNT(*) FROM print_history WHERE status = 'completed'`).Scan(&s.TotalPrintsAllTime)
	b.db.QueryRow(`SELECT COUNT(*) FROM print_history WHERE status = 'completed' AND julianday(print_finished) >= julianday('now', '-30 days')`).Scan(&s.Prints30d)
	b.db.QueryRow(`SELECT COALESCE(SUM(filament_used), 0) FROM print_history WHERE status = 'completed' AND filament_used > 0 AND julianday(print_finished) >= julianday('now', '-30 days')`).Scan(&s.FilamentUsed30dG)
	b.db.QueryRow(`
		SELECT COALESCE(SUM(pc.total_cost), 0), COALESCE(MAX(pc.currency), '')
		FROM print_history ph
		LEFT JOIN print_costs pc ON pc.print_history_id = ph.id
		WHERE ph.status = 'completed' AND julianday(ph.print_finished) >= julianday('now', '-30 days')`).Scan(&s.TotalCost30d, &s.Currency)
	b.db.QueryRow(`SELECT COALESCE(AVG(print_time_minutes), 0) FROM print_history WHERE status = 'completed' AND print_time_minutes > 0 AND julianday(print_finished) >= julianday('now', '-30 days')`).Scan(&s.AvgPrintTimeMin)
	return s, nil
}

// stripJSONKey removes one top-level key from a JSON object body.
// Returns the original body unchanged on any parse error.
func stripJSONKey(body []byte, key string) []byte {
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	delete(m, key)
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// normalizePrusaLinkStatusForMonitor removes fields that are conditionally
// present depending on printer state so the shape monitor only fires on
// genuine structural changes, not idle↔printing transitions.
//
// Stripped fields:
//   - "job": absent when idle, present when printing
//   - "storage": optional/variable across firmware versions
//   - printer.axis_x, printer.axis_y: present when idle, absent during printing
func normalizePrusaLinkStatusForMonitor(body []byte) []byte {
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	delete(m, "job")
	delete(m, "storage")
	delete(m, "transfer")
	if printer, ok := m["printer"].(map[string]interface{}); ok {
		delete(printer, "axis_x")
		delete(printer, "axis_y")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// All The Moment location management functions have been removed - locations are now managed in Spoolman only
// REMOVED: CreateLocationFromSpoolman
// REMOVED: GetAllThe MomentLocations
// REMOVED: FindLocationByName
// REMOVED: UpdateLocation
// REMOVED: DeleteLocation
// REMOVED: GetLocationStatus
// REMOVED: LocationStatus struct
// REMOVED: StartLocationSync
