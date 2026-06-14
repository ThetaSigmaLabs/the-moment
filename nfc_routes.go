// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── API handlers ─────────────────────────────────────────────────────────────

// nfcSpoolTagHandler generates and returns a URL-only NDEF .bin file for a spool tag.
func (ws *WebServer) nfcSpoolTagHandler(c *gin.Context) {
	idStr := c.Param("spoolman_id")
	spoolID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid spool ID"})
		return
	}

	spool, err := ws.bridge.spoolman.GetSpoolByID(spoolID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Auto-generate nfc_spool_uuid if not set
	if GetSpoolExtraField(*spool, "nfc_spool_uuid") == "" {
		newUUID := uuid.New().String()
		if err := ws.bridge.spoolman.SetSpoolExtraField(spoolID, "nfc_spool_uuid", newUUID); err != nil {
			// Log but don't fail — tag generation can still proceed
			_ = err
		}
	}

	host := c.Request.Host
	data, err := BuildSpoolTagNDEF(spoolID, *spool, host)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	filename := fmt.Sprintf("spool-%d.bin", spoolID)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Data(http.StatusOK, "application/octet-stream", data)
}

// nfcLocationTagHandler generates and returns a URL-only NDEF .bin file for a location tag.
func (ws *WebServer) nfcLocationTagHandler(c *gin.Context) {
	slug := c.Param("printer_slug")
	indexStr := c.Param("toolhead_index")
	toolheadIndex, err := strconv.Atoi(indexStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid toolhead index"})
		return
	}

	host := c.Request.Host
	data, err := BuildLocationTagNDEF(slug, toolheadIndex, host)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	filename := fmt.Sprintf("location-%s-t%d.bin", slug, toolheadIndex)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Data(http.StatusOK, "application/octet-stream", data)
}

// nfcSpoolsHandler proxies the Spoolman spool list with basic fields.
func (ws *WebServer) nfcSpoolsHandler(c *gin.Context) {
	spools, err := ws.bridge.spoolman.GetAllSpools()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	type spoolSummary struct {
		ID              int     `json:"id"`
		Name            string  `json:"name"`
		Material        string  `json:"material"`
		Vendor          string  `json:"vendor"`
		ColorHex        string  `json:"color_hex"`
		RemainingWeight float64 `json:"remaining_weight"`
		NFCSpoolUUID    string  `json:"nfc_spool_uuid"`
	}

	result := make([]spoolSummary, 0, len(spools))
	for _, s := range spools {
		vendor := ""
		colorHex := ""
		material := ""
		if s.Filament != nil {
			material = s.Filament.Material
			colorHex = s.Filament.ColorHex
			if s.Filament.Vendor != nil {
				vendor = s.Filament.Vendor.Name
			}
		}
		result = append(result, spoolSummary{
			ID:              s.ID,
			Name:            s.Name,
			Material:        material,
			Vendor:          vendor,
			ColorHex:        colorHex,
			RemainingWeight: s.RemainingWeight,
			NFCSpoolUUID:    GetSpoolExtraField(s, "nfc_spool_uuid"),
		})
	}
	c.JSON(http.StatusOK, result)
}

// nfcSpoolDetailHandler proxies a single spool from Spoolman.
func (ws *WebServer) nfcSpoolDetailHandler(c *gin.Context) {
	idStr := c.Param("spoolman_id")
	spoolID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid spool ID"})
		return
	}
	spool, err := ws.bridge.spoolman.GetSpoolByID(spoolID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, spool)
}

// nfcSpoolUseHandler deducts filament weight from a spool via Spoolman.
func (ws *WebServer) nfcSpoolUseHandler(c *gin.Context) {
	idStr := c.Param("spoolman_id")
	spoolID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid spool ID"})
		return
	}

	var body struct {
		UseWeight float64 `json:"use_weight"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := ws.bridge.spoolman.UpdateSpoolUsage(spoolID, body.UseWeight); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nfcAssignmentsHandler returns current toolhead-to-spool assignments.
func (ws *WebServer) nfcAssignmentsHandler(c *gin.Context) {
	printerID := c.Query("printer_id")

	type assignmentResult struct {
		ID               int     `json:"id"`
		PrinterID        string  `json:"printer_id"`
		ToolheadIndex    int     `json:"toolhead_index"`
		SpoolmanSpoolID  int     `json:"spoolman_spool_id"`
		AssignedAt       string  `json:"assigned_at"`
		AssignmentReason string  `json:"assignment_reason"`
		SpoolName        string  `json:"spool_name"`
		SpoolColor       string  `json:"spool_color"`
	}

	var assignments []ToolheadSpoolAssignment
	var err error

	if printerID != "" {
		assignments, err = ws.bridge.GetAllCurrentAssignments(printerID)
	} else {
		// Return assignments for all non-virtual printers
		configs, cfgErr := ws.bridge.GetAllPrinterConfigs()
		if cfgErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": cfgErr.Error()})
			return
		}
		for pid, cfg := range configs {
			if cfg.IsVirtual {
				continue
			}
			pAssignments, pErr := ws.bridge.GetAllCurrentAssignments(pid)
			if pErr != nil {
				err = pErr
				break
			}
			assignments = append(assignments, pAssignments...)
		}
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	results := make([]assignmentResult, 0, len(assignments))
	for _, a := range assignments {
		ar := assignmentResult{
			ID:               a.ID,
			PrinterID:        a.PrinterID,
			ToolheadIndex:    a.ToolheadIndex,
			SpoolmanSpoolID:  a.SpoolmanSpoolID,
			AssignedAt:       a.AssignedAt.Format("2006-01-02T15:04:05Z"),
			AssignmentReason: a.AssignmentReason,
		}
		spool, spoolErr := ws.bridge.spoolman.GetSpoolByID(a.SpoolmanSpoolID)
		if spoolErr == nil && spool != nil {
			ar.SpoolName = spool.Name
			if spool.Filament != nil {
				ar.SpoolColor = spool.Filament.ColorHex
			}
		}
		results = append(results, ar)
	}
	c.JSON(http.StatusOK, results)
}

// nfcCreateAssignmentHandler assigns a spool to a toolhead slot.
func (ws *WebServer) nfcCreateAssignmentHandler(c *gin.Context) {
	var body struct {
		PrinterID       string `json:"printer_id"`
		ToolheadIndex   int    `json:"toolhead_index"`
		SpoolmanSpoolID int    `json:"spoolman_spool_id"`
		Reason          string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.PrinterID == "" || body.SpoolmanSpoolID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "printer_id and spoolman_spool_id are required"})
		return
	}
	if body.Reason == "" {
		body.Reason = "manual"
	}

	if err := ws.bridge.SetAssignment(body.PrinterID, body.ToolheadIndex, body.SpoolmanSpoolID, body.Reason); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := ws.bridge.syncSpoolLocation(body.PrinterID, body.ToolheadIndex, body.SpoolmanSpoolID); err != nil {
		log.Printf("nfcCreateAssignment: location sync warning: %v", err)
	}

	ws.BroadcastStatus()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nfcDeleteAssignmentHandler unassigns the spool from a toolhead slot.
func (ws *WebServer) nfcDeleteAssignmentHandler(c *gin.Context) {
	printerID := c.Param("printer_id")
	indexStr := c.Param("toolhead_index")
	toolheadIndex, err := strconv.Atoi(indexStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid toolhead index"})
		return
	}

	prevAssignment, _ := ws.bridge.GetCurrentAssignment(printerID, toolheadIndex)

	if err := ws.bridge.CloseAssignment(printerID, toolheadIndex); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if prevAssignment != nil && prevAssignment.SpoolmanSpoolID > 0 {
		if err := ws.bridge.syncSpoolLocationForUnassignment(prevAssignment.SpoolmanSpoolID); err != nil {
			log.Printf("nfcDeleteAssignment: location sync warning: %v", err)
		}
	}

	ws.BroadcastStatus()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nfcSpoolEventsHandler returns all spool events for a print.
func (ws *WebServer) nfcSpoolEventsHandler(c *gin.Context) {
	idStr := c.Param("print_history_id")
	printID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid print history ID"})
		return
	}
	events, err := ws.bridge.GetPrintSpoolEvents(printID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, events)
}

// nfcSpoolSwapHandler records a spool swap event mid-print.
func (ws *WebServer) nfcSpoolSwapHandler(c *gin.Context) {
	idStr := c.Param("print_history_id")
	printID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid print history ID"})
		return
	}

	var body struct {
		ToolheadIndex int    `json:"toolhead_index"`
		OldSpoolID    *int   `json:"old_spool_id"`
		NewSpoolID    int    `json:"new_spool_id"`
		Reason        string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.NewSpoolID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "new_spool_id is required"})
		return
	}

	eventType := "swap_manual"
	switch body.Reason {
	case "runout":
		eventType = "swap_runout"
	case "multicolor":
		eventType = "swap_multicolor"
	}

	event := PrintSpoolEvent{
		PrintHistoryID:     printID,
		ToolheadIndex:      body.ToolheadIndex,
		OldSpoolmanSpoolID: body.OldSpoolID,
		NewSpoolmanSpoolID: body.NewSpoolID,
		EventType:          eventType,
	}
	if err := ws.bridge.AddPrintSpoolEvent(event); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nfcSetupStatusHandler checks whether all required Spoolman custom fields exist.
func (ws *WebServer) nfcSetupStatusHandler(c *gin.Context) {
	ok, missing := ws.bridge.spoolman.GetSpoolmanSetupStatus()
	c.JSON(http.StatusOK, gin.H{
		"ok":      ok,
		"missing": missing,
	})
}

// nfcSetupHandler creates any missing Spoolman custom fields.
func (ws *WebServer) nfcSetupHandler(c *gin.Context) {
	created, existed, failed := ws.bridge.spoolman.EnsureSpoolmanFields()
	c.JSON(http.StatusOK, gin.H{
		"created":        created,
		"already_existed": existed,
		"errors":         failed,
	})
}

// ─── Mobile web pages ─────────────────────────────────────────────────────────

// nfcLocationPageHandler renders the mobile page for assigning a spool to a toolhead.
func (ws *WebServer) nfcLocationPageHandler(c *gin.Context) {
	slug := c.Param("printer_slug")
	indexStr := c.Param("toolhead_index")
	toolheadIndex, err := strconv.Atoi(indexStr)
	if err != nil {
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte("<p>Invalid toolhead index.</p>"))
		return
	}

	// Find the printer matching this slug
	configs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte("<p>Error loading printers.</p>"))
		return
	}

	printerName := ""
	printerID := ""
	for pid, cfg := range configs {
		if cfg.IsVirtual {
			continue
		}
		if PrinterSlug(cfg.Name) == slug {
			printerName = cfg.Name
			printerID = pid
			break
		}
	}

	if printerName == "" {
		c.Data(http.StatusNotFound, "text/html; charset=utf-8", []byte("<p>Printer not found.</p>"))
		return
	}

	spools, err := ws.bridge.spoolman.GetAllSpools()
	if err != nil {
		spools = nil
	}

	type spoolItem struct {
		ID              int
		Name            string
		ColorHex        string
		Material        string
		RemainingWeight float64
	}

	var spoolItems []spoolItem
	for _, s := range spools {
		colorHex := "#888888"
		material := ""
		if s.Filament != nil {
			if s.Filament.ColorHex != "" {
				colorHex = "#" + s.Filament.ColorHex
			}
			material = s.Filament.Material
		}
		spoolItems = append(spoolItems, spoolItem{
			ID:              s.ID,
			Name:            s.Name,
			ColorHex:        colorHex,
			Material:        material,
			RemainingWeight: s.RemainingWeight,
		})
	}

	data := struct {
		PrinterName   string
		PrinterID     string
		ToolheadIndex int
		Spools        []spoolItem
	}{
		PrinterName:   printerName,
		PrinterID:     printerID,
		ToolheadIndex: toolheadIndex,
		Spools:        spoolItems,
	}

	c.HTML(http.StatusOK, "nfc_location.html", data)
}

// nfcSpoolIDPageHandler renders the mobile page for a spool identified by Spoolman ID.
func (ws *WebServer) nfcSpoolIDPageHandler(c *gin.Context) {
	idStr := c.Param("spoolman_id")
	spoolID, err := strconv.Atoi(idStr)
	if err != nil {
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte("<p>Invalid spool ID.</p>"))
		return
	}

	spool, err := ws.bridge.spoolman.GetSpoolByID(spoolID)
	if err != nil {
		c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte("<p>Error loading spool.</p>"))
		return
	}

	colorHex := "#888888"
	material := ""
	vendor := ""
	if spool.Filament != nil {
		if spool.Filament.ColorHex != "" {
			colorHex = "#" + spool.Filament.ColorHex
		}
		material = spool.Filament.Material
		if spool.Filament.Vendor != nil {
			vendor = spool.Filament.Vendor.Name
		}
	}

	// Find current assignment for this spool
	configs, err := ws.bridge.GetAllPrinterConfigs()
	if err != nil {
		configs = nil
	}

	type printerToolhead struct {
		PrinterID   string
		PrinterName string
		Index       int
		Label       string
	}

	var toolheads []printerToolhead
	currentAssignment := ""

	for pid, cfg := range configs {
		if cfg.IsVirtual {
			continue
		}
		assignments, aErr := ws.bridge.GetAllCurrentAssignments(pid)
		if aErr != nil {
			continue
		}
		for _, a := range assignments {
			if a.SpoolmanSpoolID == spoolID {
				currentAssignment = fmt.Sprintf("%s — Toolhead %d", cfg.Name, a.ToolheadIndex)
			}
		}
		for i := 0; i < cfg.Toolheads; i++ {
			toolheads = append(toolheads, printerToolhead{
				PrinterID:   pid,
				PrinterName: cfg.Name,
				Index:       i,
				Label:       fmt.Sprintf("%s — Toolhead %d", cfg.Name, i),
			})
		}
	}

	// Build JSON for inline script use
	toolheadsJSON, _ := json.Marshal(toolheads)

	data := struct {
		SpoolID           int
		SpoolName         string
		ColorHex          string
		Material          string
		Vendor            string
		RemainingWeight   float64
		CurrentAssignment string
		Toolheads         []printerToolhead
		ToolheadsJSON     string
	}{
		SpoolID:           spoolID,
		SpoolName:         spool.Name,
		ColorHex:          colorHex,
		Material:          material,
		Vendor:            vendor,
		RemainingWeight:   spool.RemainingWeight,
		CurrentAssignment: currentAssignment,
		Toolheads:         toolheads,
		ToolheadsJSON:     string(toolheadsJSON),
	}

	c.HTML(http.StatusOK, "nfc_spool_id.html", data)
}

// ─── OpenPrintTag field editor ────────────────────────────────────────────────

// nfcSpoolFieldsGetHandler returns current OpenPrintTag field values for a spool,
// pre-filling from Spoolman standard fields where nfc_* extra fields are not yet set.
func (ws *WebServer) nfcSpoolFieldsGetHandler(c *gin.Context) {
	idStr := c.Param("spoolman_id")
	spoolID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid spool ID"})
		return
	}
	spool, err := ws.bridge.spoolman.GetSpoolByID(spoolID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	filamentID := 0
	extruderTemp := 0
	bedTemp := 0
	var filExtra map[string]interface{}
	if spool.Filament != nil {
		filamentID = spool.Filament.ID
		extruderTemp = spool.Filament.SettingsExtruderTemp
		bedTemp = spool.Filament.SettingsBedTemp
		filExtra = spool.Filament.Extra
	}

	intFieldOrDefault := func(extra map[string]interface{}, key string, dflt int) int {
		if v := extraInt(extra, key); v != 0 {
			return v
		}
		return dflt
	}
	strFieldOrDefault := func(extra map[string]interface{}, key, dflt string) string {
		if v := extraStr(extra, key); v != "" {
			return v
		}
		return dflt
	}

	actualWeight := extraFloat(spool.Extra, "nfc_actual_weight")
	if actualWeight == 0 && spool.InitialWeight > 0 {
		actualWeight = spool.InitialWeight
	}
	if actualWeight == 0 {
		actualWeight = spool.RemainingWeight + spool.UsedWeight
	}

	c.JSON(http.StatusOK, gin.H{
		"spool_id":                  spoolID,
		"filament_id":               filamentID,
		"nfc_spool_uuid":            GetSpoolExtraField(*spool, "nfc_spool_uuid"),
		"nfc_actual_weight":         actualWeight,
		"nfc_manufacturing_date":    GetSpoolExtraField(*spool, "nfc_manufacturing_date"),
		"nfc_expiration_date":       GetSpoolExtraField(*spool, "nfc_expiration_date"),
		"nfc_material_class":        strFieldOrDefault(filExtra, "nfc_material_class", "FFF"),
		"nfc_min_print_temp":        intFieldOrDefault(filExtra, "nfc_min_print_temp", extruderTemp),
		"nfc_max_print_temp":        intFieldOrDefault(filExtra, "nfc_max_print_temp", extruderTemp),
		"nfc_min_bed_temp":          intFieldOrDefault(filExtra, "nfc_min_bed_temp", bedTemp),
		"nfc_max_bed_temp":          intFieldOrDefault(filExtra, "nfc_max_bed_temp", bedTemp),
		"nfc_country_of_origin":     extraStr(filExtra, "nfc_country_of_origin"),
		"nfc_material_properties":   extraStr(filExtra, "nfc_material_properties"),
		"nfc_transmission_distance": extraFloat(filExtra, "nfc_transmission_distance"),
		"nfc_nominal_length":        extraInt(filExtra, "nfc_nominal_length"),
	})
}

// nfcSpoolFieldsPostHandler saves OpenPrintTag field values to Spoolman for a spool.
// Spool-level fields are written to the spool record; filament-level fields to the filament.
// All fields are written regardless of whether they are blank.
func (ws *WebServer) nfcSpoolFieldsPostHandler(c *gin.Context) {
	idStr := c.Param("spoolman_id")
	spoolID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid spool ID"})
		return
	}

	var body struct {
		NfcActualWeight        float64 `json:"nfc_actual_weight"`
		NfcManufacturingDate   string  `json:"nfc_manufacturing_date"`
		NfcExpirationDate      string  `json:"nfc_expiration_date"`
		NfcMaterialClass       string  `json:"nfc_material_class"`
		NfcMinPrintTemp        int     `json:"nfc_min_print_temp"`
		NfcMaxPrintTemp        int     `json:"nfc_max_print_temp"`
		NfcMinBedTemp          int     `json:"nfc_min_bed_temp"`
		NfcMaxBedTemp          int     `json:"nfc_max_bed_temp"`
		NfcCountryOfOrigin     string  `json:"nfc_country_of_origin"`
		NfcMaterialProperties  string  `json:"nfc_material_properties"`
		NfcTransmissionDist    float64 `json:"nfc_transmission_distance"`
		NfcNominalLength       int     `json:"nfc_nominal_length"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	spool, err := ws.bridge.spoolman.GetSpoolByID(spoolID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if spool.Filament == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "spool has no filament"})
		return
	}
	filamentID := spool.Filament.ID

	spoolFields := map[string]string{
		"nfc_actual_weight":      fmt.Sprintf("%g", body.NfcActualWeight),
		"nfc_manufacturing_date": body.NfcManufacturingDate,
		"nfc_expiration_date":    body.NfcExpirationDate,
	}
	for key, val := range spoolFields {
		if err := ws.bridge.spoolman.SetSpoolExtraField(spoolID, key, val); err != nil {
			log.Printf("Warning: failed to set spool extra field %s: %v", key, err)
		}
	}

	filamentFields := map[string]string{
		"nfc_material_class":        body.NfcMaterialClass,
		"nfc_min_print_temp":        strconv.Itoa(body.NfcMinPrintTemp),
		"nfc_max_print_temp":        strconv.Itoa(body.NfcMaxPrintTemp),
		"nfc_min_bed_temp":          strconv.Itoa(body.NfcMinBedTemp),
		"nfc_max_bed_temp":          strconv.Itoa(body.NfcMaxBedTemp),
		"nfc_country_of_origin":     body.NfcCountryOfOrigin,
		"nfc_material_properties":   body.NfcMaterialProperties,
		"nfc_transmission_distance": fmt.Sprintf("%g", body.NfcTransmissionDist),
		"nfc_nominal_length":        strconv.Itoa(body.NfcNominalLength),
	}
	for key, val := range filamentFields {
		if err := ws.bridge.spoolman.SetFilamentExtraField(filamentID, key, val); err != nil {
			log.Printf("Warning: failed to set filament extra field %s: %v", key, err)
		}
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nfcSpoolTrashHandler zeros out remaining weight and moves spool to the configured Trash location.
func (ws *WebServer) nfcSpoolTrashHandler(c *gin.Context) {
	idStr := c.Param("spoolman_id")
	spoolID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid spool ID"})
		return
	}

	spool, err := ws.bridge.spoolman.GetSpoolByID(spoolID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if spool.RemainingWeight > 0 {
		if err := ws.bridge.spoolman.UpdateSpoolUsage(spoolID, spool.RemainingWeight); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to zero remaining weight: " + err.Error()})
			return
		}
	}

	trashLoc, _ := ws.bridge.GetConfigValue(ConfigKeyNFCTrashLocation)
	if trashLoc == "" {
		trashLoc = "Trash"
	}
	if err := ws.bridge.spoolman.UpdateSpoolLocation(spoolID, trashLoc); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to move spool to trash: " + err.Error()})
		return
	}

	if err := ws.bridge.spoolman.UpdateSpool(spoolID, map[string]interface{}{"archived": true}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to archive spool in Spoolman: " + err.Error()})
		return
	}

	if err := ws.bridge.CloseAssignmentsBySpool(spoolID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear toolhead assignments: " + err.Error()})
		return
	}

	if err := ws.bridge.ClearToolheadMappingsBySpool(spoolID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear toolhead mappings: " + err.Error()})
		return
	}

	// Free any NFC tag bound to this spool so it enters the available pool for reuse.
	if err := ws.bridge.ArchiveSpoolNFCForSpool(spoolID); err != nil {
		log.Printf("nfcSpoolTrash: ArchiveSpoolNFCForSpool %d: %v", spoolID, err)
	}

	ws.BroadcastStatus()
	c.JSON(http.StatusOK, gin.H{"archived": true, "location": trashLoc})
}
