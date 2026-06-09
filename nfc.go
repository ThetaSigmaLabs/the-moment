// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	cbor "github.com/fxamacker/cbor/v2"
)

// NFCSession represents an active NFC scanning session
type NFCSession struct {
	SessionID         string    `json:"session_id"`
	SpoolID           int       `json:"spool_id"`
	PrinterName       string    `json:"printer_name"`
	ToolheadID        int       `json:"toolhead_id"`
	LocationName      string    `json:"location_name"`
	IsPrinterLocation bool      `json:"is_printer_location"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	HasSpool          bool      `json:"has_spool"`
	HasLocation       bool      `json:"has_location"`
}

// parseLocationParam extracts location information from location parameter
// Supports multiple formats:
// 1. "PrinterName - Toolhead N" - printer toolhead locations (numeric ID)
// 2. "PrinterName - CustomName" - printer toolhead locations (custom name)
// 3. "LocationName" - non-printer locations (drybox, storage, etc.)
func (b *FilamentBridge) parseLocationParam(location string) (printerName string, toolheadID int, locationName string, isPrinterLocation bool, err error) {
	// Check if it contains " - " which indicates a printer toolhead location
	if strings.Contains(location, " - ") {
		parts := strings.SplitN(location, " - ", 2)
		if len(parts) == 2 {
			printerName = strings.TrimSpace(parts[0])
			toolheadPart := strings.TrimSpace(parts[1])

			// First, try to find by custom name (prioritize custom names over numeric parsing)
			// This ensures that if a user names toolhead 0 as "Toolhead 1", it will match correctly
			// Get all printer configs to find matching printer and toolhead
			printerConfigs, err := b.GetAllPrinterConfigs()
			if err == nil {
				for printerID, printerConfig := range printerConfigs {
					if printerConfig.Name == printerName {
						// Get toolhead names for this printer
						toolheadNames, err := b.GetAllToolheadNames(printerID)
						if err == nil {
							// Look for matching display name (custom names take precedence)
							for tid, displayName := range toolheadNames {
								if displayName == toolheadPart {
									return printerName, tid, location, true, nil
								}
							}
						}
						// Also check default names
						for tid := 0; tid < printerConfig.Toolheads; tid++ {
							defaultName := fmt.Sprintf("Toolhead %d", tid)
							if defaultName == toolheadPart {
								return printerName, tid, location, true, nil
							}
						}
					}
				}
			}

			// If no custom name match found, try to parse as numeric ID (old format: "Toolhead N")
			// This maintains backward compatibility for numeric-only toolhead IDs
			if strings.HasPrefix(toolheadPart, "Toolhead ") {
				toolheadIDStr := strings.TrimPrefix(toolheadPart, "Toolhead ")
				toolheadID, err = strconv.Atoi(toolheadIDStr)
				if err == nil {
					// Validate that the parsed numeric ID exists for this printer
					// This prevents matching "Toolhead 1" to a non-existent toolhead when it's actually a custom name
					printerConfigs, err := b.GetAllPrinterConfigs()
					if err == nil {
						for _, printerConfig := range printerConfigs {
							if printerConfig.Name == printerName {
								// Verify the numeric ID is within valid range
								if toolheadID >= 0 && toolheadID < printerConfig.Toolheads {
									return printerName, toolheadID, location, true, nil
								}
								// If numeric ID is out of range, don't return it - treat as regular location
								break
							}
						}
					}
				}
			}

			// If we couldn't parse it as a toolhead location, treat as regular location
			// This maintains backward compatibility
		}
	}

	// For all other cases, treat as a location name
	return "", 0, location, false, nil
}

// generateSessionID creates a unique session ID based on client IP only
// This ensures all scans from the same device use the same session
func generateSessionID(clientIP string) string {
	hash := md5.Sum([]byte(clientIP))
	return fmt.Sprintf("%x", hash)[:16] // Use first 16 characters of MD5
}

// getClientIP extracts the real client IP from the request
func getClientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// If SplitHostPort fails, assume the whole string is the IP
		return remoteAddr
	}
	return host
}

// createOrUpdateSession creates a new session or updates an existing one
func (b *FilamentBridge) createOrUpdateSession(sessionID string, spoolID int, printerName string, toolheadID int, locationName string, isPrinterLocation bool) (*NFCSession, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// Check if session already exists
	var existingSession NFCSession
	err := b.db.QueryRow(
		"SELECT session_id, spool_id, printer_name, toolhead_id, location_name, is_printer_location, created_at, expires_at FROM nfc_sessions WHERE session_id = ?",
		sessionID,
	).Scan(&existingSession.SessionID, &existingSession.SpoolID, &existingSession.PrinterName,
		&existingSession.ToolheadID, &existingSession.LocationName, &existingSession.IsPrinterLocation, &existingSession.CreatedAt, &existingSession.ExpiresAt)

	if err == nil {
		// Session exists, update it
		now := time.Now()
		if now.After(existingSession.ExpiresAt) {
			// Session expired, create new one
			return b.createNewSession(sessionID, spoolID, printerName, toolheadID, locationName, isPrinterLocation)
		}

		// Update existing session - only update fields that are actually being set
		// This prevents overwriting existing data when scanning tags in sequence

		// Update spool data only if a new spool is being scanned
		if spoolID > 0 {
			existingSession.SpoolID = spoolID
			existingSession.HasSpool = true

			// Update only spool_id in database, preserve other fields
			_, err = b.db.Exec(
				"UPDATE nfc_sessions SET spool_id = ? WHERE session_id = ?",
				spoolID, sessionID,
			)
			if err != nil {
				return nil, fmt.Errorf("failed to update spool in NFC session: %w", err)
			}
		}

		// Update location data only if a new location is being scanned
		if (isPrinterLocation && printerName != "" && toolheadID >= 0) || (!isPrinterLocation && locationName != "") {
			existingSession.PrinterName = printerName
			existingSession.ToolheadID = toolheadID
			existingSession.LocationName = locationName
			existingSession.IsPrinterLocation = isPrinterLocation
			existingSession.HasLocation = true

			// Update only location fields in database, preserve spool_id
			_, err = b.db.Exec(
				"UPDATE nfc_sessions SET printer_name = ?, toolhead_id = ?, location_name = ?, is_printer_location = ? WHERE session_id = ?",
				printerName, toolheadID, locationName, isPrinterLocation, sessionID,
			)
			if err != nil {
				return nil, fmt.Errorf("failed to update location in NFC session: %w", err)
			}
		}

		// Recalculate flags based on current session data
		existingSession.HasSpool = existingSession.SpoolID > 0
		existingSession.HasLocation = (existingSession.IsPrinterLocation && existingSession.PrinterName != "" && existingSession.ToolheadID >= 0) ||
			(!existingSession.IsPrinterLocation && existingSession.LocationName != "")

		return &existingSession, nil
	}

	// Create new session
	return b.createNewSession(sessionID, spoolID, printerName, toolheadID, locationName, isPrinterLocation)
}

// createNewSession creates a new NFC session
func (b *FilamentBridge) createNewSession(sessionID string, spoolID int, printerName string, toolheadID int, locationName string, isPrinterLocation bool) (*NFCSession, error) {
	now := time.Now()
	expiresAt := now.Add(5 * time.Minute) // 5 minute expiration

	session := &NFCSession{
		SessionID:         sessionID,
		SpoolID:           spoolID,
		PrinterName:       printerName,
		ToolheadID:        toolheadID,
		LocationName:      locationName,
		IsPrinterLocation: isPrinterLocation,
		CreatedAt:         now,
		ExpiresAt:         expiresAt,
		HasSpool:          spoolID > 0,
		HasLocation:       (isPrinterLocation && printerName != "" && toolheadID >= 0) || (!isPrinterLocation && locationName != ""),
	}

	_, err := b.db.Exec(
		"INSERT INTO nfc_sessions (session_id, spool_id, printer_name, toolhead_id, location_name, is_printer_location, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		session.SessionID, session.SpoolID, session.PrinterName, session.ToolheadID, session.LocationName, session.IsPrinterLocation, session.CreatedAt, session.ExpiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create NFC session: %w", err)
	}

	return session, nil
}

// getSession retrieves an existing NFC session
func (b *FilamentBridge) getSession(sessionID string) (*NFCSession, error) {
	var session NFCSession
	err := b.db.QueryRow(
		"SELECT session_id, spool_id, printer_name, toolhead_id, location_name, is_printer_location, created_at, expires_at FROM nfc_sessions WHERE session_id = ?",
		sessionID,
	).Scan(&session.SessionID, &session.SpoolID, &session.PrinterName,
		&session.ToolheadID, &session.LocationName, &session.IsPrinterLocation, &session.CreatedAt, &session.ExpiresAt)

	if err != nil {
		return nil, err
	}

	// Check if session is expired
	if time.Now().After(session.ExpiresAt) {
		// Clean up expired session
		b.deleteSession(sessionID)
		return nil, fmt.Errorf("session expired")
	}

	// Set flags based on data
	session.HasSpool = session.SpoolID > 0
	session.HasLocation = (session.IsPrinterLocation && session.PrinterName != "" && session.ToolheadID >= 0) || (!session.IsPrinterLocation && session.LocationName != "")

	return &session, nil
}

// isSessionComplete checks if both spool and location are set
func (s *NFCSession) isSessionComplete() bool {
	return s.HasSpool && s.HasLocation
}

// deleteSession removes a session from the database
func (b *FilamentBridge) deleteSession(sessionID string) error {
	_, err := b.db.Exec("DELETE FROM nfc_sessions WHERE session_id = ?", sessionID)
	return err
}

// cleanupExpiredSessions removes sessions older than their expiration time
func (b *FilamentBridge) cleanupExpiredSessions() error {
	now := time.Now()
	_, err := b.db.Exec("DELETE FROM nfc_sessions WHERE expires_at < ?", now)
	if err != nil {
		log.Printf("Error cleaning up expired NFC sessions: %v", err)
		return err
	}
	return nil
}

// AssignSpoolToLocation assigns a spool to a location and updates Spoolman
func (b *FilamentBridge) AssignSpoolToLocation(spoolID int, printerName string, toolheadID int, locationName string, isPrinterLocation bool) error {
	if isPrinterLocation {
		// This is a printer toolhead location
		// Update The Moment toolhead mapping
		if err := b.SetToolheadMapping(printerName, toolheadID, spoolID); err != nil {
			return fmt.Errorf("failed to set toolhead mapping: %w", err)
		}

		// Get toolhead display name (custom or default)
		// Find printer ID first
		printerConfigs, err := b.GetAllPrinterConfigs()
		var displayName string
		if err == nil {
			for printerID, printerConfig := range printerConfigs {
				if printerConfig.Name == printerName {
					name, err := b.GetToolheadName(printerID, toolheadID)
					if err == nil {
						displayName = name
					} else {
						displayName = fmt.Sprintf("Toolhead %d", toolheadID)
					}
					break
				}
			}
		}
		if displayName == "" {
			displayName = fmt.Sprintf("Toolhead %d", toolheadID)
		}

		// Update Spoolman location using proper location entities with custom name
		locationName := fmt.Sprintf("%s - %s", printerName, displayName)
		
		// Note: Spoolman API doesn't support creating locations via POST.
		// The location will be auto-created when we update the spool's location field.
		
		if err := b.spoolman.UpdateSpoolLocation(spoolID, locationName); err != nil {
			// If Spoolman update fails, we should still log it but not fail the entire operation
			// since the The Moment mapping is more critical
			log.Printf("Warning: Failed to update Spoolman location for spool %d: %v", spoolID, err)
		}

		log.Printf("Successfully assigned spool %d to %s toolhead %d (%s)", spoolID, printerName, toolheadID, displayName)
	} else {
		// This is a non-printer location (drybox, storage, etc.)
		// First, check if this spool is currently assigned to any toolhead and clear it
		if err := b.clearSpoolFromAllToolheads(spoolID); err != nil {
			log.Printf("Warning: Failed to clear spool %d from toolheads: %v", spoolID, err)
		}

		// Use the location name directly with Spoolman
		if locationName == "" {
			return fmt.Errorf("location name cannot be empty")
		}

		// Ensure the location exists in Spoolman
		if _, err := b.spoolman.GetOrCreateLocation(locationName); err != nil {
			log.Printf("Warning: Failed to create/verify location '%s' in Spoolman: %v", locationName, err)
		}

		// Update Spoolman location
		if err := b.spoolman.UpdateSpoolLocation(spoolID, locationName); err != nil {
			return fmt.Errorf("failed to update Spoolman location for spool %d: %w", spoolID, err)
		}

		log.Printf("Successfully assigned spool %d to location '%s'", spoolID, locationName)
	}

	return nil
}

// ─── NDEF tag generation ──────────────────────────────────────────────────────

var nonAlphanumHyphen = regexp.MustCompile(`[^a-z0-9-]+`)

// PrinterSlug converts a printer name to a URL-safe slug.
// Lowercases, replaces spaces with hyphens, strips non-alphanumeric characters.
func PrinterSlug(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	s = nonAlphanumHyphen.ReplaceAllString(s, "")
	return s
}

// ─── OpenPrintTag CBOR encoding ──────────────────────────────────────────────

// optMaterialClasses maps Spoolman nfc_material_class strings to OpenPrintTag material_class enum.
// CBOR key 8; 0=FFF (filament), 1=SLA (resin). Per OpenPrintTag specification.
var optMaterialClasses = map[string]int{
	"FFF": 0,
	"SLA": 1,
}

// optMaterialProperties maps OpenPrintTag tag property strings to their integer enum values.
// CBOR key 28 ("tags"). Per OpenPrintTag specification.
var optMaterialProperties = map[string]int{
	"filtration_recommended":      0,
	"biocompatible":               1,
	"antibacterial":               2,
	"air_filtering":               3,
	"abrasive":                    4,
	"foaming":                     5,
	"self_extinguishing":          6,
	"paramagnetic":                7,
	"radiation_shielding":         8,
	"high_temperature":            9,
	"esd_safe":                    10,
	"conductive":                  11,
	"blend":                       12,
	"water_soluble":               13,
	"ipa_soluble":                 14,
	"limonene_soluble":            15,
	"matte":                       16,
	"silk":                        17,
	"translucent":                 19,
	"transparent":                 20,
	"iridescent":                  21,
	"pearlescent":                 22,
	"glitter":                     23,
	"glow_in_the_dark":            24,
	"neon":                        25,
	"illuminescent_color_change":  26,
	"temperature_color_change":    27,
	"gradual_color_change":        28,
	"coextruded":                  29,
	"contains_carbon":             30,
	"contains_carbon_fiber":       31,
	"contains_carbon_nano_tubes":  32,
	"contains_glass":              33,
	"contains_glass_fiber":        34,
	"contains_kevlar":             35,
	"contains_stone":              36,
	"contains_magnetite":          37,
	"contains_organic_material":   38,
	"contains_cork":               39,
	"contains_wax":                40,
	"contains_wood":               41,
	"contains_bamboo":             42,
	"contains_pine":               43,
	"contains_ceramic":            44,
	"contains_boron_carbide":      45,
	"contains_metal":              46,
	"contains_bronze":             47,
	"contains_iron":               48,
	"contains_steel":              49,
	"contains_silver":             50,
	"contains_copper":             51,
	"contains_aluminium":          52,
	"contains_brass":              53,
	"contains_tungsten":           54,
	"imitates_wood":               55,
	"imitates_metal":              56,
	"imitates_marble":             57,
	"imitates_stone":              58,
	"lithophane":                  59,
	"recycled":                    60,
	"home_compostable":            61,
	"industrially_compostable":    62,
	"bio_based":                   63,
	"low_outgassing":              64,
	"without_pigments":            65,
	"contains_algae":              66,
	"castable":                    67,
	"contains_ptfe":               68,
	"limited_edition":             69,
	"emi_shielding":               70,
	"high_speed":                  71,
	"contains_graphene":           72,
}

// optMaterialTypes maps Spoolman material strings to OpenPrintTag material_type integer enum.
// Integer values per the OpenPrintTag specification.
var optMaterialTypes = map[string]int{
	"PLA": 0, "PETG": 1, "TPU": 2, "ABS": 3, "ASA": 4, "PC": 5,
	"PCTG": 6, "PP": 7, "PA6": 8, "PA11": 9, "PA12": 10, "PA66": 11,
	"CPE": 12, "TPE": 13, "HIPS": 14, "PHA": 15, "PET": 16, "PEI": 17,
	"PBT": 18, "PVB": 19, "PVA": 20, "PEKK": 21, "PEEK": 22, "BVOH": 23,
	"TPC": 24, "PPS": 25, "PPSU": 26, "PVC": 27, "PEBA": 28, "PVDF": 29,
	"PPA": 30, "PCL": 31, "PES": 32, "PMMA": 33, "POM": 34, "PPE": 35,
	"PS": 36, "PSU": 37, "TPI": 38, "SBS": 39, "OBC": 40, "EVA": 41,
}

func parseHexColor(hexStr string) []byte {
	hexStr = strings.TrimPrefix(hexStr, "#")
	if len(hexStr) != 6 {
		return nil
	}
	b, err := hex.DecodeString(hexStr)
	if err != nil || len(b) != 3 {
		return nil
	}
	return b
}

func parseUUIDBytes(uuidStr string) []byte {
	cleaned := strings.ReplaceAll(uuidStr, "-", "")
	if len(cleaned) != 32 {
		return nil
	}
	b, err := hex.DecodeString(cleaned)
	if err != nil || len(b) != 16 {
		return nil
	}
	return b
}

func parseDateToUnix(dateStr string) int64 {
	if dateStr == "" {
		return 0
	}
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// extraInt reads an integer from a Spoolman extra field map, handling both
// float64 (JSON number decode) and string representations.
func extraInt(extra map[string]interface{}, key string) int {
	if extra == nil {
		return 0
	}
	v, ok := extra[key]
	if !ok {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case string:
		n, _ := strconv.Atoi(val)
		return n
	}
	return 0
}

// extraFloat reads a float64 from a Spoolman extra field map.
func extraFloat(extra map[string]interface{}, key string) float64 {
	if extra == nil {
		return 0
	}
	v, ok := extra[key]
	if !ok {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	}
	return 0
}

// extraStr reads a string from a Spoolman extra field map.
func extraStr(extra map[string]interface{}, key string) string {
	if extra == nil {
		return ""
	}
	v, ok := extra[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// buildOpenPrintTagPayload encodes spool data as an OpenPrintTag CBOR payload.
// Payload layout: [meta CBOR map][main CBOR map][aux CBOR map (optional)].
// Only fields with meaningful values are included to keep the tag compact.
func buildOpenPrintTagPayload(spool SpoolmanSpool) ([]byte, error) {
	em, err := cbor.EncOptions{Sort: cbor.SortCanonical}.EncMode()
	if err != nil {
		return nil, fmt.Errorf("cbor enc mode: %w", err)
	}

	mainMap := make(map[int]interface{})

	if uuidStr := extraStr(spool.Extra, "nfc_spool_uuid"); uuidStr != "" {
		if uuidBytes := parseUUIDBytes(uuidStr); uuidBytes != nil {
			mainMap[0] = uuidBytes // instance_uuid
		}
	}

	if spool.Filament != nil {
		fil := spool.Filament

		if matType, ok := optMaterialTypes[strings.ToUpper(fil.Material)]; ok {
			mainMap[9] = matType // material_type
		}
		if fil.Name != "" {
			mainMap[10] = fil.Name // material_name
		}
		if fil.Vendor != nil && fil.Vendor.Name != "" {
			mainMap[11] = fil.Vendor.Name // brand_name
		}
		if ts := parseDateToUnix(extraStr(spool.Extra, "nfc_manufacturing_date")); ts != 0 {
			mainMap[14] = ts // manufactured_date
		}
		if ts := parseDateToUnix(extraStr(spool.Extra, "nfc_expiration_date")); ts != 0 {
			mainMap[15] = ts // expiration_date
		}
		if fil.Weight > 0 {
			mainMap[16] = fil.Weight // nominal_netto_full_weight (g)
		}
		if aw := extraFloat(spool.Extra, "nfc_actual_weight"); aw > 0 {
			mainMap[17] = aw // actual_netto_full_weight
		} else if spool.InitialWeight > 0 {
			mainMap[17] = spool.InitialWeight
		}
		if fil.SpoolWeight > 0 {
			mainMap[18] = fil.SpoolWeight // empty_container_weight (g)
		}
		if colorBytes := parseHexColor(fil.ColorHex); colorBytes != nil {
			mainMap[19] = colorBytes // primary_color (RGB bytes)
		}
		if td := extraFloat(fil.Extra, "nfc_transmission_distance"); td > 0 {
			mainMap[27] = td // transmission_distance
		}
		if fil.Density > 0 {
			mainMap[29] = fil.Density // density (g/cm³)
		}
		if fil.Diameter > 0 {
			mainMap[30] = fil.Diameter // filament_diameter (mm)
		}
		if v := extraInt(fil.Extra, "nfc_min_print_temp"); v > 0 {
			mainMap[34] = v // min_print_temperature (°C)
		}
		if v := extraInt(fil.Extra, "nfc_max_print_temp"); v > 0 {
			mainMap[35] = v // max_print_temperature (°C)
		}
		if v := extraInt(fil.Extra, "nfc_min_bed_temp"); v > 0 {
			mainMap[37] = v // min_bed_temperature (°C)
		}
		if v := extraInt(fil.Extra, "nfc_max_bed_temp"); v > 0 {
			mainMap[38] = v // max_bed_temperature (°C)
		}
		if fil.Material != "" {
			mainMap[52] = fil.Material // material_abbreviation
		}
		if v := extraInt(fil.Extra, "nfc_nominal_length"); v > 0 {
			mainMap[53] = float64(v) // nominal_full_length (mm)
		}
		if coo := extraStr(fil.Extra, "nfc_country_of_origin"); coo != "" {
			mainMap[55] = coo // country_of_origin
		}
		if mc, ok := optMaterialClasses[strings.ToUpper(extraStr(fil.Extra, "nfc_material_class"))]; ok {
			mainMap[8] = mc // material_class (0=FFF, 1=SLA)
		}
		if propsJSON := extraStr(fil.Extra, "nfc_material_properties"); propsJSON != "" {
			var tags []string
			if json.Unmarshal([]byte(propsJSON), &tags) == nil {
				var encoded []int
				for _, t := range tags {
					if v, ok := optMaterialProperties[strings.ToLower(t)]; ok {
						encoded = append(encoded, v)
					}
				}
				if len(encoded) > 0 {
					mainMap[28] = encoded // tags / material_properties
				}
			}
		}
		for i, h := range strings.SplitN(fil.MultiColorHexes, ",", 6) {
			if i >= 5 {
				break
			}
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			if rgb := parseHexColor(h); rgb != nil {
				mainMap[20+i] = rgb // secondary_color_N (keys 20–24)
			}
		}
	}

	auxMap := make(map[int]interface{})
	if spool.UsedWeight > 0 {
		auxMap[0] = spool.UsedWeight // consumed_weight (g)
	}

	mainBytes, err := em.Marshal(mainMap)
	if err != nil {
		return nil, fmt.Errorf("encoding main section: %w", err)
	}
	var auxBytes []byte
	if len(auxMap) > 0 {
		auxBytes, err = em.Marshal(auxMap)
		if err != nil {
			return nil, fmt.Errorf("encoding aux section: %w", err)
		}
	}

	// Meta section keys: 0=main_offset, 1=main_size, 2=aux_offset, 3=aux_size.
	// Meta size depends on offset values which depend on meta size; resolve by iteration.
	buildMeta := func(metaSize int) map[int]interface{} {
		m := map[int]interface{}{0: metaSize, 1: len(mainBytes)}
		if len(auxBytes) > 0 {
			m[2] = metaSize + len(mainBytes)
			m[3] = len(auxBytes)
		}
		return m
	}
	var metaBytes []byte
	for estimate := 5; estimate <= 25; estimate++ {
		encoded, encErr := em.Marshal(buildMeta(estimate))
		if encErr != nil {
			return nil, fmt.Errorf("encoding meta section: %w", encErr)
		}
		if len(encoded) == estimate {
			metaBytes = encoded
			break
		}
	}
	if metaBytes == nil {
		return nil, fmt.Errorf("could not converge meta section size")
	}

	payload := make([]byte, 0, len(metaBytes)+len(mainBytes)+len(auxBytes))
	payload = append(payload, metaBytes...)
	payload = append(payload, mainBytes...)
	payload = append(payload, auxBytes...)
	return payload, nil
}

// ─── NDEF tag generation ──────────────────────────────────────────────────────

// buildNDEFRecord constructs a single NDEF record with explicit message framing flags.
// tnf: Type Name Format (0x01 = Well Known, 0x02 = MIME Media Type).
// Short Record (SR) format is used when payload fits in one byte (≤255 bytes).
func buildNDEFRecord(msgBegin, msgEnd bool, tnf byte, recordType, payload []byte) []byte {
	shortRecord := len(payload) <= 255
	var flags byte
	if msgBegin {
		flags |= 0x80
	}
	if msgEnd {
		flags |= 0x40
	}
	if shortRecord {
		flags |= 0x10
	}
	flags |= tnf & 0x07

	rec := make([]byte, 0, 4+len(recordType)+len(payload))
	rec = append(rec, flags)
	rec = append(rec, byte(len(recordType)))
	if shortRecord {
		rec = append(rec, byte(len(payload)))
	} else {
		plen := uint32(len(payload))
		rec = append(rec, byte(plen>>24), byte(plen>>16), byte(plen>>8), byte(plen))
	}
	rec = append(rec, recordType...)
	rec = append(rec, payload...)
	return rec
}

// buildNDEFURIRecord builds a Well Known URI record (TNF=0x01, type='U').
// Handles the http:// prefix code (0x03) per the URI record spec.
func buildNDEFURIRecord(msgBegin, msgEnd bool, uri string) ([]byte, error) {
	const httpPrefix = "http://"
	if !strings.HasPrefix(uri, httpPrefix) {
		return nil, fmt.Errorf("URI must start with http://")
	}
	payload := append([]byte{0x03}, []byte(uri[len(httpPrefix):])...)
	return buildNDEFRecord(msgBegin, msgEnd, 0x01, []byte{0x55}, payload), nil
}

// BuildSpoolTagNDEF builds an NDEF message for a spool tag (ICODE SLIX2 / NFC-V).
// Produces a dual-record message: Record 1 = OpenPrintTag CBOR (MIME type),
// Record 2 = URL pointing to /nfc/s/{spoolID} for iPhone fallback.
// Falls back to URL-only on CBOR encoding error.
func BuildSpoolTagNDEF(spoolID int, spool SpoolmanSpool, host string) ([]byte, error) {
	uri := fmt.Sprintf("http://%s/nfc/s/%d", host, spoolID)

	cborPayload, err := buildOpenPrintTagPayload(spool)
	if err != nil {
		log.Printf("OpenPrintTag CBOR failed for spool %d, using URL-only: %v", spoolID, err)
		return buildNDEFURIRecord(true, true, uri)
	}

	mimeType := []byte("application/vnd.openprinttag")
	rec1 := buildNDEFRecord(true, false, 0x02, mimeType, cborPayload)
	rec2, err := buildNDEFURIRecord(false, true, uri)
	if err != nil {
		log.Printf("NDEF URI record failed for spool %d, using URL-only: %v", spoolID, err)
		return buildNDEFURIRecord(true, true, uri)
	}
	return append(rec1, rec2...), nil
}

// BuildLocationTagNDEF builds a URL-only NDEF message for a location tag (NTAG215).
func BuildLocationTagNDEF(printerSlug string, toolheadIndex int, host string) ([]byte, error) {
	uri := fmt.Sprintf("http://%s/nfc/location/%s/%d", host, printerSlug, toolheadIndex)
	return buildNDEFURIRecord(true, true, uri)
}

// clearSpoolFromAllToolheads removes a spool from all toolhead mappings
func (b *FilamentBridge) clearSpoolFromAllToolheads(spoolID int) error {
	// Get all current toolhead mappings
	allMappings, err := b.GetAllToolheadMappings()
	if err != nil {
		return fmt.Errorf("failed to get toolhead mappings: %w", err)
	}

	// Find and clear any mappings for this spool
	for printerName, printerMappings := range allMappings {
		for toolheadID, mapping := range printerMappings {
			if mapping.SpoolID == spoolID {
				// Clear this toolhead mapping
				if err := b.UnmapToolhead(printerName, toolheadID); err != nil {
					log.Printf("Warning: Failed to unmap spool %d from %s toolhead %d: %v", spoolID, printerName, toolheadID, err)
				} else {
					log.Printf("Cleared spool %d from %s toolhead %d", spoolID, printerName, toolheadID)
				}
			}
		}
	}

	return nil
}
