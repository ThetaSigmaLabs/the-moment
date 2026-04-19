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
)

// Default configuration values
const (
	DefaultSpoolmanURL          = "http://localhost:7912"
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
	ConfigKeyPollInterval                    = "poll_interval"
	ConfigKeyLocationSyncInterval            = "location_sync_interval"
	ConfigKeyWebPort                         = "web_port"
	ConfigKeyPrusaLinkTimeout                = "prusalink_timeout"
	ConfigKeyPrusaLinkFileDownloadTimeout    = "prusalink_file_download_timeout"
	ConfigKeySpoolmanTimeout                 = "spoolman_timeout"
	ConfigKeySpoolmanUsername                = "spoolman_username"
	ConfigKeySpoolmanPassword                = "spoolman_password"
	ConfigKeyAutoAssignPreviousSpoolEnabled  = "auto_assign_previous_spool_enabled"
	ConfigKeyAutoAssignPreviousSpoolLocation = "auto_assign_previous_spool_location"
)

// HTTP timeouts
const (
	PrusaLinkTimeout             = 10  // seconds
	PrusaLinkFileDownloadTimeout = 300 // seconds — USB storage can be slow
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
