// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// FilamentBridge manages the connection between PrusaLink and Spoolman
type FilamentBridge struct {
	config           *Config
	spoolman         *SpoolmanClient
	db               *sql.DB
	wasPrinting      map[string]bool
	currentJobFile   map[string]string     // Store current job filename per printer
	processingPrints map[string]bool       // Track prints being processed
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

// PrintHistory represents a record of filament usage
type PrintHistory struct {
	ID            int       `json:"id"`
	PrinterName   string    `json:"printer_name"`
	ToolheadID    int       `json:"toolhead_id"`
	SpoolID       int       `json:"spool_id"`
	FilamentUsed  float64   `json:"filament_used"`
	PrintStarted  time.Time `json:"print_started"`
	PrintFinished time.Time `json:"print_finished"`
	JobName       string    `json:"job_name"`
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

	// Enable foreign key constraint enforcement — required for ON DELETE CASCADE
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
	rows, err := b.db.Query("SELECT printer_id, name, model, ip_address, api_key, toolheads, COALESCE(is_virtual, 0) FROM printer_configs")
	if err != nil {
		return nil, fmt.Errorf("failed to get printer configs: %w", err)
	}
	defer rows.Close()

	configs := make(map[string]PrinterConfig)
	for rows.Next() {
		var printerID, name, model, ipAddress, apiKey string
		var toolheads int
		var isVirtual bool
		if err := rows.Scan(&printerID, &name, &model, &ipAddress, &apiKey, &toolheads, &isVirtual); err != nil {
			return nil, fmt.Errorf("failed to scan printer config row: %w", err)
		}
		configs[printerID] = PrinterConfig{
			Name:      name,
			Model:     model,
			IPAddress: ipAddress,
			APIKey:    apiKey,
			Toolheads: toolheads,
			IsVirtual: isVirtual,
		}
	}

	return configs, nil
}

// SavePrinterConfig saves a printer configuration
func (b *FilamentBridge) SavePrinterConfig(printerID string, config PrinterConfig) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	isVirtual := 0
	if config.IsVirtual {
		isVirtual = 1
	}
	_, err := b.db.Exec(`
		INSERT OR REPLACE INTO printer_configs (printer_id, name, model, ip_address, api_key, toolheads, is_virtual)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, printerID, config.Name, config.Model, config.IPAddress, config.APIKey, config.Toolheads, isVirtual)
	if err != nil {
		return fmt.Errorf("failed to save printer config: %w", err)
	}
	return nil
}

// DeletePrinterConfig deletes a printer configuration
func (b *FilamentBridge) DeletePrinterConfig(printerID string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec("DELETE FROM printer_configs WHERE printer_id = ?", printerID)
	if err != nil {
		return fmt.Errorf("failed to delete printer config: %w", err)
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

// LogPrintUsage logs filament usage for a print job
func (b *FilamentBridge) LogPrintUsage(printerName string, toolheadID int, spoolID int, filamentUsed float64, jobName string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// Get print start time from current job file tracking
	printStarted := time.Now() // Default to now if we can't determine start time
	if storedJobFile, exists := b.currentJobFile[printerName]; exists && storedJobFile != "" {
		// If we have a stored job file, the print likely started when we first stored it
		// This is a rough approximation - ideally we'd track this more precisely
		printStarted = time.Now().Add(-time.Hour) // Assume 1 hour ago as rough estimate
	}

	_, err := b.db.Exec(
		"INSERT INTO print_history (printer_name, toolhead_id, spool_id, filament_used, print_started, print_finished, job_name) VALUES (?, ?, ?, ?, ?, ?, ?)",
		printerName, toolheadID, spoolID, filamentUsed, printStarted, time.Now(), jobName,
	)
	if err != nil {
		return fmt.Errorf("failed to log print usage: %w", err)
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

	// Monitor each printer using PrusaLink
	for printerID, printerConfig := range configSnapshot.Printers {
		if printerID == "no_printers" {
			continue // Skip placeholder
		}
		if printerConfig.IsVirtual {
			continue // Skip virtual test printers — they are driven manually
		}
		go func(printerID string, config PrinterConfig) {
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
		// Cannot download G-code — log error and skip rather than deducting wrong amount
		msg := fmt.Sprintf("failed to download G-code for cancelled print: %v", err)
		b.addPrintError(printerName, filename, msg)
		return fmt.Errorf("%s", msg)
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

	// Download with retry logic
	gcodeContent, err := prusaClient.GetGcodeFileWithRetry(filename, b.config.PrusaLinkFileDownloadTimeout)
	if err != nil {
		errorMsg := fmt.Sprintf("failed to download G-code file after retries: %v", err)
		b.addPrintError(printerName, filename, errorMsg)
		return fmt.Errorf("%s", errorMsg)
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

	// Process filament usage using helper function
	if err := b.processFilamentUsage(printerName, filamentUsage, filename); err != nil {
		log.Printf("Error processing filament usage: %v", err)
		return err
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

			client := NewPrusaLinkClient(printerConfig.IPAddress, printerConfig.APIKey, b.config.PrusaLinkTimeout, b.config.PrusaLinkFileDownloadTimeout)

			// Use the configured printer name, not the hostname from PrusaLink
			printerName := printerConfig.Name

			// Get current status
			printerStatus, err := client.GetStatus()
			if err != nil {
				// Enhanced error logging to help diagnose connection issues
				// This is especially useful for DNS resolution problems with hostnames
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

// processFilamentUsage processes filament usage updates for all toolheads
func (b *FilamentBridge) processFilamentUsage(printerName string, filamentUsage map[int]float64, jobName string) error {
	// Update Spoolman with filament usage for each toolhead
	for toolheadID, usedWeight := range filamentUsage {
		if usedWeight <= 0 {
			continue
		}

		// Get the mapped spool for this toolhead
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

		// Update Spoolman
		if err := b.spoolman.UpdateSpoolUsage(spoolID, usedWeight); err != nil {
			log.Printf("Error updating spool %d usage: %v", spoolID, err)
			continue
		}

		// Log the usage in our database
		if err := b.LogPrintUsage(printerName, toolheadID, spoolID, usedWeight, jobName); err != nil {
			log.Printf("Error logging print usage: %v", err)
		}

		log.Printf("Updated spool %d: used %.2fg filament on %s toolhead %d",
			spoolID, usedWeight, printerName, toolheadID)
	}

	// Summary log
	if len(filamentUsage) > 0 {
		log.Printf("✅ Print completion processing finished for %s: processed %d toolheads", printerName, len(filamentUsage))
	} else {
		log.Printf("⚠️  No filament usage data processed for %s", printerName)
	}

	return nil
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

// ─── Virtual Printer File Management ────────────────────────────────────────

// VirtualPrinterFile represents a G-code file stored for a virtual test printer
type VirtualPrinterFile struct {
	ID          int       `json:"id"`
	PrinterID   string    `json:"printer_id"`
	Filename    string    `json:"filename"`
	DisplayName string    `json:"display_name"`
	FileSize    int64     `json:"file_size"`
	UploadedAt  time.Time `json:"uploaded_at"`
}

// SaveVirtualPrinterFile stores a G-code file for a virtual printer.
// The file content is stored as a BLOB in SQLite.
// SQLite ON DELETE CASCADE removes all files automatically when the printer is deleted.
func (b *FilamentBridge) SaveVirtualPrinterFile(printerID, filename, displayName string, content []byte) (int64, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	result, err := b.db.Exec(`
		INSERT INTO virtual_printer_files (printer_id, filename, display_name, file_size, content)
		VALUES (?, ?, ?, ?, ?)
	`, printerID, filename, displayName, len(content), content)
	if err != nil {
		return 0, fmt.Errorf("failed to save virtual printer file: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get inserted file ID: %w", err)
	}

	log.Printf("💾 Saved G-code file '%s' for virtual printer %s (id=%d, %d bytes)", displayName, printerID, id, len(content))
	return id, nil
}

// GetVirtualPrinterFiles returns all uploaded files for a virtual printer (metadata only, no content).
func (b *FilamentBridge) GetVirtualPrinterFiles(printerID string) ([]VirtualPrinterFile, error) {
	rows, err := b.db.Query(`
		SELECT id, printer_id, filename, display_name, file_size, uploaded_at
		FROM virtual_printer_files
		WHERE printer_id = ?
		ORDER BY uploaded_at DESC
	`, printerID)
	if err != nil {
		return nil, fmt.Errorf("failed to query virtual printer files: %w", err)
	}
	defer rows.Close()

	var files []VirtualPrinterFile
	for rows.Next() {
		var f VirtualPrinterFile
		if err := rows.Scan(&f.ID, &f.PrinterID, &f.Filename, &f.DisplayName, &f.FileSize, &f.UploadedAt); err != nil {
			return nil, fmt.Errorf("failed to scan virtual file row: %w", err)
		}
		files = append(files, f)
	}

	if files == nil {
		files = []VirtualPrinterFile{}
	}
	return files, nil
}

// GetVirtualPrinterFileContent retrieves the full content of a single file.
func (b *FilamentBridge) GetVirtualPrinterFileContent(fileID int) ([]byte, string, error) {
	var content []byte
	var displayName string
	err := b.db.QueryRow(`
		SELECT content, display_name FROM virtual_printer_files WHERE id = ?
	`, fileID).Scan(&content, &displayName)
	if err == sql.ErrNoRows {
		return nil, "", fmt.Errorf("file %d not found", fileID)
	}
	if err != nil {
		return nil, "", fmt.Errorf("failed to get file content: %w", err)
	}
	return content, displayName, nil
}

// DeleteVirtualPrinterFile deletes a single uploaded file.
func (b *FilamentBridge) DeleteVirtualPrinterFile(printerID string, fileID int) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	result, err := b.db.Exec(`
		DELETE FROM virtual_printer_files WHERE id = ? AND printer_id = ?
	`, fileID, printerID)
	if err != nil {
		return fmt.Errorf("failed to delete virtual printer file: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("file %d not found for printer %s", fileID, printerID)
	}
	log.Printf("🗑️  Deleted virtual file id=%d from printer %s", fileID, printerID)
	return nil
}

// ProcessVirtualFile parses a stored G-code file and updates Spoolman.
// This is the core action the user triggers from the UI.
func (b *FilamentBridge) ProcessVirtualFile(printerID string, fileID int) (map[int]float64, error) {
	// Fetch content
	content, displayName, err := b.GetVirtualPrinterFileContent(fileID)
	if err != nil {
		return nil, fmt.Errorf("cannot load file: %w", err)
	}

	// Parse filament usage
	client := &PrusaLinkClient{}
	usage, err := client.ParseGcodeFilamentUsage(content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse G-code: %w", err)
	}
	if len(usage) == 0 {
		return nil, fmt.Errorf("no filament usage metadata found in '%s' — ensure your slicer writes filament weight comments", displayName)
	}

	// Look up printer name for toolhead mapping
	configs, err := b.GetAllPrinterConfigs()
	if err != nil {
		return nil, fmt.Errorf("cannot load printer config: %w", err)
	}
	config, ok := configs[printerID]
	if !ok {
		return nil, fmt.Errorf("printer %s not found", printerID)
	}

	printerName := resolvePrinterName(config)

	// Deduct from Spoolman
	if err := b.processFilamentUsage(printerName, usage, displayName); err != nil {
		return nil, fmt.Errorf("failed to update Spoolman: %w", err)
	}

	log.Printf("✅ Virtual printer '%s': processed '%s', %d toolhead(s)", printerName, displayName, len(usage))
	return usage, nil
}

// migrateVirtualPrinterSupport adds is_virtual column to existing databases.
// Safe to call on fresh databases — ALTER TABLE errors are ignored.
func (b *FilamentBridge) migrateVirtualPrinterSupport() error {
	queries := []string{
		`ALTER TABLE printer_configs ADD COLUMN is_virtual INTEGER DEFAULT 0`,
	}
	for _, q := range queries {
		_, err := b.db.Exec(q)
		if err != nil {
			// Column likely already exists — not an error
			continue
		}
	}

	// Enable foreign key enforcement (needed for ON DELETE CASCADE)
	_, err := b.db.Exec("PRAGMA foreign_keys = ON")
	if err != nil {
		log.Printf("Warning: could not enable foreign key enforcement: %v", err)
	}

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
