// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

// Printer states — matches PrusaLink /api/v1/status printer.state values exactly
const (
	StateIdle          = "IDLE"
	StatePrinting      = "PRINTING"
	StateFinished      = "FINISHED"
	StatePaused        = "PAUSED"    // User-initiated pause
	StateStopped       = "STOPPED"   // Job was cancelled / stopped
	StateAttention     = "ATTENTION" // Filament runout / change required
	StateOffline       = "offline"   // Cannot reach printer
	StateNotConfigured = "not_configured"
	StateVirtual       = "virtual"    // Virtual test printer — no hardware
	StateOctoPrint     = "octoprint"  // OctoPrint push-only printer
)

// Default configuration values
const (
	DefaultSpoolmanURL          = "http://localhost:7912"
	DefaultSpoolmanExternalURL  = "http://localhost:7912"
	DefaultWebPort              = "5000"
	DefaultPollInterval         = 30
	DefaultLocationSyncInterval = 5 // minutes
	DefaultDBFileName           = "the-moment.db"
)

// Database configuration keys
const (
	ConfigKeyPrinterIPs                      = "printer_ips"
	ConfigKeyAPIKey                          = "prusalink_api_key"
	ConfigKeySpoolmanURL                     = "spoolman_url"
	ConfigKeySpoolmanExternalURL            = "spoolman_external_url"
	ConfigKeyPollInterval                    = "poll_interval"
	ConfigKeyLocationSyncInterval            = "location_sync_interval"
	ConfigKeyWebPort                         = "web_port"
	ConfigKeyPrusaLinkTimeout                = "prusalink_timeout"
	ConfigKeyPrusaLinkFileDownloadTimeout    = "prusalink_file_download_timeout"
	ConfigKeySpoolmanTimeout                 = "spoolman_timeout"
	ConfigKeyAutoAssignPreviousSpoolEnabled = "auto_assign_previous_spool_enabled"
	ConfigKeyTheMomentAPIKey                = "the_moment_api_key"
	ConfigKeyOctoPrintDebug                 = "octoprint_debug"

	// NFC workflow location names stored in Spoolman.
	// nfc_trash_location: where finished/empty spools go so the tag can be re-programmed.
	// nfc_inventory_location: default storage when a spool is displaced from a toolhead.
	ConfigKeyNFCTrashLocation    = "nfc_trash_location"
	ConfigKeyNFCInventoryLocation = "nfc_inventory_location"

	// NFC tap-tap engine (Stage 5): seconds a first tap stays pending before a second
	// tap is treated as a fresh first tap. Default 15. Boundary semantics decided in Stage 5.
	ConfigKeyNFCTapTimeoutSeconds = "nfc_tap_timeout_seconds"

	// Spoolman location sync: keeps Spoolman spool location fields in sync with The Moment toolhead assignments.
	// Format written to Spoolman: "{printer_name} - T{toolhead_index}" (e.g. "Roci - T0").
	ConfigKeySpoolmanLocationSyncEnabled = "spoolman_location_sync_enabled"

	// Cost calculation config keys
	ConfigKeyCostElectricityRate = "cost_electricity_rate" // $/kWh
	ConfigKeyCostPrinterWattage  = "cost_printer_wattage"  // Watts
	ConfigKeyCostMaintenanceRate = "cost_maintenance_rate" // $/hour
	ConfigKeyCostDepreciationRate= "cost_depreciation_rate"// $/hour (printer depreciation)
	ConfigKeyCostMarginPercent   = "cost_margin_percent"   // % markup over cost
	ConfigKeyCostCurrency        = "cost_currency"         // e.g. "USD", "CAD"
)

// HTTP timeouts
const (
	PrusaLinkTimeout             = 10  // seconds
	PrusaLinkFileDownloadTimeout = 600 // seconds — kept for backward compat; no longer used as a hard download deadline
	SpoolmanTimeout              = 10  // seconds
)

// Printer model detection patterns
const (
	ModelCorePattern = "core"
	ModelXLPattern   = "xl"
	ModelMK4Pattern  = "mk4"
	ModelMK3Pattern  = "mk3"
	ModelMiniPattern = "mini"
)

// Printer model names
const (
	ModelCoreOne  = "CORE One"
	ModelCoreOneL = "CORE One L"
	ModelXL       = "XL"
	ModelMK4      = "MK4"
	ModelMK35     = "MK3.5"
	ModelMiniPlus = "MINI+"
	ModelUnknown  = "Unknown"
)

// MaxToolheads is the upper bound for toolhead slots.
// Set to 16 to cover INDX 8-head plus future expansion.
const MaxToolheads = 16

// Printer type identifiers stored in printer_configs.printer_type.
const (
	PrinterTypePrusaLink = "prusalink"
	PrinterTypeOctoPrint = "octoprint"
	PrinterTypeBambu     = "bambu"
)

// ConfigKeyBambuDebug enables verbose Bambu MQTT debug logging when set to "true".
const ConfigKeyBambuDebug = "bambu_debug"

// Backup config keys
const (
	ConfigKeyLastBackupTime = "last_backup_time"
	ConfigKeyRestorePending = "restore_pending"
)

// Backup scope constants
const (
	BackupScopeAll     = "all"
	BackupScopeDB      = "db"
	BackupScopeGcode   = "gcode"
	BackupScopeUploads = "uploads"
)
