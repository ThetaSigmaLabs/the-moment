// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// stripControlChars removes HTML angle brackets and null bytes from external
// filament data to prevent XSS/injection when the value is reflected in responses.
func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || r == 0 {
			return -1
		}
		return r
	}, s)
}

// ─── Source registry handlers ─────────────────────────────────────────────────

// GET /api/openprinttag/sources
func (ws *WebServer) optSourcesListHandler(c *gin.Context) {
	sources, err := ws.bridge.ListOPTSources()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if sources == nil {
		sources = []OpenPrintTagSource{}
	}
	c.JSON(http.StatusOK, sources)
}

// POST /api/openprinttag/sources
func (ws *WebServer) optSourcesCreateHandler(c *gin.Context) {
	var body struct {
		Name       string `json:"name"`
		URL        string `json:"url"`
		SourceType string `json:"source_type"`
		Enabled    bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.URL = strings.TrimSpace(body.URL)
	body.SourceType = strings.TrimSpace(body.SourceType)
	if body.Name == "" || body.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and url are required"})
		return
	}
	if !validOPTSourceType(body.SourceType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown source_type: " + body.SourceType})
		return
	}
	if err := validateSourceURL(body.URL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, err := ws.bridge.InsertOPTSource(OpenPrintTagSource{
		Name:       body.Name,
		URL:        body.URL,
		SourceType: body.SourceType,
		Enabled:    body.Enabled,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	sources, _ := ws.bridge.ListOPTSources()
	for _, s := range sources {
		if s.ID == id {
			c.JSON(http.StatusCreated, s)
			return
		}
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// PUT /api/openprinttag/sources/:id
func (ws *WebServer) optSourcesUpdateHandler(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body struct {
		Name       string `json:"name"`
		URL        string `json:"url"`
		SourceType string `json:"source_type"`
		Enabled    bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validOPTSourceType(body.SourceType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown source_type: " + body.SourceType})
		return
	}
	if err := validateSourceURL(strings.TrimSpace(body.URL)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := ws.bridge.UpdateOPTSource(OpenPrintTagSource{
		ID:         id,
		Name:       strings.TrimSpace(body.Name),
		URL:        strings.TrimSpace(body.URL),
		SourceType: body.SourceType,
		Enabled:    body.Enabled,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DELETE /api/openprinttag/sources/:id
func (ws *WebServer) optSourcesDeleteHandler(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := ws.bridge.DeleteOPTSource(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// POST /api/openprinttag/sources/reset-defaults
func (ws *WebServer) optSourcesResetHandler(c *gin.Context) {
	if err := ws.bridge.ResetOPTSourcesToDefaults(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	sources, err := ws.bridge.ListOPTSources()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sources)
}

// POST /api/openprinttag/sources/:id/test
// Returns {"ok":true,"latency_ms":42} or {"ok":false,"error":"...","latency_ms":0}
func (ws *WebServer) optSourcesTestHandler(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	sources, err := ws.bridge.ListOPTSources()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var source *OpenPrintTagSource
	for i := range sources {
		if sources[i].ID == id {
			source = &sources[i]
			break
		}
	}
	if source == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		return
	}
	latency, testErr := TestOPTSource(*source)
	if testErr != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": testErr.Error(), "latency_ms": latency})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "latency_ms": latency})
}

// GET /api/openprinttag/variants?source_id=1&ref=brands/polymaker/materials/PLA/filaments/polylite_pla
// Returns colour variants and print parameters for a specific OFD filament.
// Only supported for ofd_api source type.
func (ws *WebServer) optVariantsHandler(c *gin.Context) {
	sourceIDStr := c.Query("source_id")
	ref := strings.TrimSpace(c.Query("ref"))
	if sourceIDStr == "" || ref == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_id and ref are required"})
		return
	}
	sourceID, err := strconv.Atoi(sourceIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source_id"})
		return
	}
	sources, err := ws.bridge.ListOPTSources()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var source *OpenPrintTagSource
	for i := range sources {
		if sources[i].ID == sourceID {
			source = &sources[i]
			break
		}
	}
	if source == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		return
	}
	result, err := fetchOPTVariants(*source, ref)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if result == nil {
		c.JSON(http.StatusOK, gin.H{"variants": []OPTFilamentVariant{}, "min_print_temp": 0, "max_print_temp": 0, "min_bed_temp": 0, "max_bed_temp": 0, "density": 0.0, "diameter_mm": 0.0})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"variants":       result.Variants,
		"min_print_temp": result.MinPrintTemp,
		"max_print_temp": result.MaxPrintTemp,
		"min_bed_temp":   result.MinBedTemp,
		"max_bed_temp":   result.MaxBedTemp,
		"density":        result.Density,
		"diameter_mm":    result.DiameterMM,
	})
}

// GET /api/openprinttag/search?source_id=1&q=polymaker+pla
func (ws *WebServer) optSearchHandler(c *gin.Context) {
	sourceIDStr := c.Query("source_id")
	query := strings.TrimSpace(c.Query("q"))
	if sourceIDStr == "" || query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_id and q are required"})
		return
	}
	sourceID, err := strconv.Atoi(sourceIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source_id"})
		return
	}
	sources, err := ws.bridge.ListOPTSources()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var source *OpenPrintTagSource
	for i := range sources {
		if sources[i].ID == sourceID {
			source = &sources[i]
			break
		}
	}
	if source == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		return
	}
	if !source.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source is disabled"})
		return
	}
	results, err := SearchOPTSource(*source, query)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if results == nil {
		results = []OPTSearchResult{}
	}
	c.JSON(http.StatusOK, results)
}

// ─── NFC tag creation from OPT source ────────────────────────────────────────

// POST /api/nfc/openprinttag-tag
// Creates a filament NFC tag from an OpenPrintTag external source result.
// If action="update_existing" and filament_id>0, updates nfc_* fields on that filament.
// If action="create_new" (or filament_id=0), creates a new Spoolman filament from the result.
func (ws *WebServer) nfcOPTTagCreateHandler(c *gin.Context) {
	var body struct {
		Label      string `json:"label"`
		SourceID   int    `json:"source_id"`
		SourceRef  string `json:"source_ref"`
		Action     string `json:"action"`      // "create_new" | "update_existing"
		FilamentID int    `json:"filament_id"` // required when action=update_existing
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.SourceID == 0 || strings.TrimSpace(body.SourceRef) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_id and source_ref are required"})
		return
	}
	if body.Action == "" {
		body.Action = "create_new"
	}

	// Re-fetch the filament record from the external source using the source_ref.
	sources, err := ws.bridge.ListOPTSources()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var source *OpenPrintTagSource
	for i := range sources {
		if sources[i].ID == body.SourceID {
			source = &sources[i]
			break
		}
	}
	if source == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		return
	}

	result, err := fetchOPTByRef(*source, body.SourceRef)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not fetch filament from source: " + err.Error()})
		return
	}

	// Sanitize free-text fields from external source to prevent XSS/injection.
	result.FilamentName = stripControlChars(result.FilamentName)
	result.Brand = stripControlChars(result.Brand)
	result.Material = stripControlChars(result.Material)
	result.ColorName = stripControlChars(result.ColorName)

	var filamentID int
	switch body.Action {
	case "update_existing":
		if body.FilamentID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "filament_id required for update_existing"})
			return
		}
		if err := ws.bridge.UpdateSpoolmanFilamentNFCFields(body.FilamentID, *result); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		filamentID = body.FilamentID
	default: // "create_new"
		fid, err := ws.bridge.CreateSpoolmanFilamentFromOPT(*result)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		filamentID = fid
	}

	var label *string
	if l := strings.TrimSpace(body.Label); l != "" {
		label = &l
	}
	tag, err := ws.bridge.CreateFilamentTag(label, filamentID, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"tag":            tag,
		"tag_url":        nfcTagURL(c.Request.Host, tag.TagID),
		"qr_code_base64": encodeTagQR(c.Request.Host, tag.TagID),
		"note":           nfcTagWriteNote(tag.TagType),
	})
}
