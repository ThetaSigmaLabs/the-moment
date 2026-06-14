// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
)

// ─── NFC Management tab — tag CRUD (nfc_tags registry, single source of truth) ──
//
// These handlers back the NFCs tab. The binding lives in nfc_tags; Spoolman remains the
// source of truth for filament/spool/location data. Stage 2 implements the Filament
// sub-tab; spool and location sub-tabs activate in Stages 3 and 4.

// CreateFilamentTag creates a filament-type nfc_tags row. When filamentID > 0 it binds to
// that existing Spoolman filament. Otherwise it authors a new Spoolman filament from spec
// (mapping manufacturer to an existing vendor when found), then binds the tag to it. The
// authored spec is stored in tag_filament_spec. Spoolman HTTP happens outside any held mutex.
func (b *FilamentBridge) CreateFilamentTag(label *string, filamentID int, spec *TagFilamentSpec) (*NFCTag, error) {
	boundID := filamentID
	// filamentID<=0 with no spec creates an unbound filament tag — a filament type that may
	// not exist in Spoolman yet. With a spec, author a new Spoolman filament and bind to it.
	if boundID <= 0 && spec != nil {
		if strings.TrimSpace(spec.Material) == "" && strings.TrimSpace(spec.ColorName) == "" {
			return nil, fmt.Errorf("material or color is required to author a new filament")
		}
		data := map[string]interface{}{}
		if spec.Material != "" {
			data["material"] = spec.Material
		}
		if hex := strings.TrimPrefix(strings.TrimSpace(spec.ColorHex), "#"); hex != "" {
			data["color_hex"] = hex
		}
		if spec.Density > 0 {
			data["density"] = spec.Density
		}
		diameter := spec.DiameterMM
		if diameter <= 0 {
			diameter = 1.75
		}
		data["diameter"] = diameter
		if spec.DefaultWeightG > 0 {
			data["weight"] = spec.DefaultWeightG
		}
		if spec.DefaultPrice > 0 {
			data["price"] = spec.DefaultPrice
		}
		name := strings.TrimSpace(spec.ColorName)
		if name == "" {
			name = strings.TrimSpace(spec.Material)
		}
		if name == "" {
			name = "NFC Filament"
		}
		data["name"] = name
		if strings.TrimSpace(spec.Manufacturer) != "" {
			if v, err := b.spoolman.FindVendorByName(spec.Manufacturer); err == nil && v != nil {
				data["vendor_id"] = v.ID
			}
		}
		created, err := b.spoolman.CreateFilament(data)
		if err != nil {
			return nil, fmt.Errorf("creating Spoolman filament: %w", err)
		}
		boundID = created.ID
	}

	tagID := uuid.New().String()
	tag := NFCTag{TagID: tagID, TagType: "filament", Label: label}
	if boundID > 0 {
		entityType := "spoolman_filament"
		tag.BoundEntityType = &entityType
		tag.BoundEntityID = &boundID
	}
	if err := b.InsertNFCTag(tag); err != nil {
		return nil, err
	}

	if spec != nil {
		s := *spec
		s.TagID = tagID
		if s.OpenPrintTagJSON == "" {
			if blob, err := json.Marshal(s); err == nil {
				s.OpenPrintTagJSON = string(blob)
			}
		}
		if err := b.SetTagFilamentSpec(s); err != nil {
			log.Printf("warning: failed to store filament spec for tag %s: %v", tagID, err)
		}
	}

	return b.GetNFCTag(tagID)
}

// nfcTagWriteNote returns the recommended physical tag type for a tag type. The app never
// writes tags; this is a reminder shown in the "Write to NFC" display.
func nfcTagWriteNote(tagType string) string {
	if tagType == "location" {
		return "Write this URL to an NTAG215 tag."
	}
	return "Write this URL to an NFC-V tag (ICODE SLIX2 recommended for future Prusa-native compatibility)."
}

// nfcTagURL builds the unified /tag/{tag_id} URL written to the physical sticker.
func nfcTagURL(host, tagID string) string {
	return fmt.Sprintf("http://%s/tag/%s", host, tagID)
}

type nfcFilamentSummary struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Material string `json:"material"`
	ColorHex string `json:"color_hex"`
	Vendor   string `json:"vendor"`
}

type nfcSpoolSummary struct {
	ID              int     `json:"id"`
	Name            string  `json:"name"`
	Material        string  `json:"material"`
	ColorHex        string  `json:"color_hex"`
	Vendor          string  `json:"vendor"`
	Location        string  `json:"location"`
	RemainingWeight float64 `json:"remaining_weight"`
	Archived        bool    `json:"archived"`
}

type nfcLocationSummary struct {
	Kind      string `json:"kind"`                // toolhead|inventory|archive|trash
	SpoolID   int    `json:"spool_id,omitempty"`
	SpoolName string `json:"spool_name,omitempty"`
	ColorHex  string `json:"color_hex,omitempty"`
	Material  string `json:"material,omitempty"`
}

type nfcTagRow struct {
	TagID    string               `json:"tag_id"`
	Label    *string              `json:"label"`
	Status   string               `json:"status"`
	BoundID  *int                 `json:"bound_entity_id"`
	Filament *nfcFilamentSummary  `json:"filament,omitempty"`
	Spool    *nfcSpoolSummary     `json:"spool,omitempty"`
	Location *nfcLocationSummary  `json:"location,omitempty"`
	TagURL   string               `json:"tag_url"`
}

// nfcTagsListHandler returns all tags of a given type (default: filament) enriched with
// their current Spoolman binding.
// GET /api/nfc/tags?type=filament|spool|location
func (ws *WebServer) nfcTagsListHandler(c *gin.Context) {
	tagType := c.DefaultQuery("type", "filament")

	tags, err := ws.bridge.ListNFCTagsByType(tagType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Build Spoolman lookups for binding enrichment (best effort).
	filamentByID := map[int]SpoolmanFilament{}
	spoolByID := map[int]SpoolmanSpool{}
	switch tagType {
	case "filament":
		if filaments, fErr := ws.bridge.spoolman.GetAllFilaments(); fErr == nil {
			for _, f := range filaments {
				filamentByID[f.ID] = f
			}
		}
	case "spool", "location":
		if spools, sErr := ws.bridge.spoolman.GetAllSpools(); sErr == nil {
			for _, s := range spools {
				spoolByID[s.ID] = s
			}
		}
	}

	host := c.Request.Host
	rows := make([]nfcTagRow, 0, len(tags))
	for _, t := range tags {
		row := nfcTagRow{TagID: t.TagID, Label: t.Label, Status: t.Status, BoundID: t.BoundEntityID, TagURL: nfcTagURL(host, t.TagID)}
		if t.BoundEntityID != nil {
			if f, ok := filamentByID[*t.BoundEntityID]; ok {
				vendor := ""
				if f.Vendor != nil {
					vendor = f.Vendor.Name
				}
				row.Filament = &nfcFilamentSummary{ID: f.ID, Name: f.Name, Material: f.Material, ColorHex: f.ColorHex, Vendor: vendor}
			}
			if s, ok := spoolByID[*t.BoundEntityID]; ok {
				vendor := ""
				colorHex := ""
				if s.Filament != nil {
					colorHex = s.Filament.ColorHex
					if s.Filament.Vendor != nil {
						vendor = s.Filament.Vendor.Name
					}
				}
				row.Spool = &nfcSpoolSummary{
					ID: s.ID, Name: s.Name, Material: s.Material, ColorHex: colorHex, Vendor: vendor,
					Location: s.Location, RemainingWeight: s.RemainingWeight, Archived: s.Archived,
				}
			}
		}
		if t.TagType == "location" {
			kind := ""
			if t.LocationKind != nil {
				kind = *t.LocationKind
			}
			loc := &nfcLocationSummary{Kind: kind}
			// For toolhead locations, look up the currently assigned spool.
			if kind == "toolhead" && t.Label != nil {
				if printerName, toolheadIdx, ok := ParseToolheadLocation(*t.Label); ok {
					if spoolID, mErr := ws.bridge.GetToolheadMapping(printerName, toolheadIdx); mErr == nil && spoolID > 0 {
						if s, sOK := spoolByID[spoolID]; sOK {
							loc.SpoolID = s.ID
							loc.SpoolName = s.Name
							loc.Material = s.Material
							if s.Filament != nil {
								loc.ColorHex = s.Filament.ColorHex
							}
						}
					}
				}
			}
			row.Location = loc
		}
		rows = append(rows, row)
	}
	c.JSON(http.StatusOK, rows)
}

// CreateLocationTag creates a location-type nfc_tags row with the given kind.
func (b *FilamentBridge) CreateLocationTag(label *string, locationKind string) (*NFCTag, error) {
	tag := NFCTag{TagID: uuid.New().String(), TagType: "location", Label: label}
	if locationKind != "" {
		tag.LocationKind = &locationKind
	}
	if err := b.InsertNFCTag(tag); err != nil {
		return nil, err
	}
	return b.GetNFCTag(tag.TagID)
}

// nfcTagCreateHandler creates a tag.
// POST /api/nfc/tags
// Body: {"tag_type":"filament|spool|location","label":"...","filament_id":7,"spool_id":3,"location_kind":"toolhead","spec":{...}}
func (ws *WebServer) nfcTagCreateHandler(c *gin.Context) {
	var body struct {
		TagType      string           `json:"tag_type"`
		Label        string           `json:"label"`
		FilamentID   int              `json:"filament_id"`
		SpoolID      int              `json:"spool_id"`
		LocationKind string           `json:"location_kind"`
		Spec         *TagFilamentSpec `json:"spec"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var label *string
	if strings.TrimSpace(body.Label) != "" {
		l := strings.TrimSpace(body.Label)
		label = &l
	}

	var tag *NFCTag
	var err error
	switch body.TagType {
	case "filament":
		tag, err = ws.bridge.CreateFilamentTag(label, body.FilamentID, body.Spec)
	case "spool":
		tag, err = ws.bridge.CreateSpoolTag(label, body.SpoolID)
	case "location":
		tag, err = ws.bridge.CreateLocationTag(label, body.LocationKind)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported tag_type (expected filament, spool, or location)"})
		return
	}
	if err != nil {
		if isLabelConflict(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "a tag with that label already exists for this type"})
			return
		}
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

// nfcTagLocationKindHandler updates a location tag's location_kind field.
// Empty kind clears it.
// PATCH /api/nfc/tags/:tag_id/location-kind
func (ws *WebServer) nfcTagLocationKindHandler(c *gin.Context) {
	tagID := c.Param("tag_id")
	var body struct {
		LocationKind string `json:"location_kind"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var kind *string
	if strings.TrimSpace(body.LocationKind) != "" {
		k := strings.TrimSpace(body.LocationKind)
		kind = &k
	}
	if err := ws.bridge.SetNFCTagLocationKind(tagID, kind); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nfcTagRebindHandler changes or clears a tag's Spoolman binding.
// entity_id=0 or absent → unbind (clears bound_entity_type and bound_entity_id).
// The previously bound Spoolman record is not modified.
// PATCH /api/nfc/tags/:tag_id/rebind
func (ws *WebServer) nfcTagRebindHandler(c *gin.Context) {
	tagID := c.Param("tag_id")
	var body struct {
		EntityID int `json:"entity_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tag, err := ws.bridge.GetNFCTag(tagID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if tag == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tag not found"})
		return
	}

	var entityType *string
	var entityID *int
	if body.EntityID > 0 {
		var et string
		switch tag.TagType {
		case "spool":
			et = "spoolman_spool"
		case "filament":
			et = "spoolman_filament"
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "rebind not supported for tag type " + tag.TagType})
			return
		}
		entityType = &et
		entityID = &body.EntityID
	}

	if err := ws.bridge.SetNFCTagBinding(tagID, entityType, entityID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nfcTagLabelHandler updates a tag's display nickname. An empty label clears it.
// PATCH /api/nfc/tags/:tag_id/label
func (ws *WebServer) nfcTagLabelHandler(c *gin.Context) {
	tagID := c.Param("tag_id")
	var body struct {
		Label string `json:"label"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var label *string
	if strings.TrimSpace(body.Label) != "" {
		l := strings.TrimSpace(body.Label)
		label = &l
	}
	if err := ws.bridge.SetNFCTagLabel(tagID, label); err != nil {
		if isLabelConflict(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "another tag of this type already uses that label"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nfcTagDeleteHandler removes a tag from the registry. The bound Spoolman entity is untouched.
// DELETE /api/nfc/tags/:tag_id
func (ws *WebServer) nfcTagDeleteHandler(c *gin.Context) {
	tagID := c.Param("tag_id")
	if err := ws.bridge.DeleteNFCTag(tagID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nfcTagPayloadHandler returns the "Write to NFC" display payload for a tag: the URL to
// write externally, a QR rendering, and the recommended tag type. Display-only — the app
// never writes tags. Redo/replace simply calls this again (same tag_id, refreshed display).
// GET /api/nfc/tags/:tag_id/payload
func (ws *WebServer) nfcTagPayloadHandler(c *gin.Context) {
	tagID := c.Param("tag_id")
	tag, err := ws.bridge.GetNFCTag(tagID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if tag == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tag not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"tag_id":         tag.TagID,
		"tag_type":       tag.TagType,
		"tag_url":        nfcTagURL(c.Request.Host, tag.TagID),
		"qr_code_base64": encodeTagQR(c.Request.Host, tag.TagID),
		"note":           nfcTagWriteNote(tag.TagType),
	})
}

// ─── Spool tags (Stage 3) ──────────────────────────────────────────────────────

// CreateSpoolTag creates a spool-type nfc_tags row. spoolID>0 binds to that Spoolman
// spool; spoolID<=0 leaves the tag unbound (added to the available pool for later binding).
func (b *FilamentBridge) CreateSpoolTag(label *string, spoolID int) (*NFCTag, error) {
	tag := NFCTag{TagID: uuid.New().String(), TagType: "spool", Label: label}
	if spoolID > 0 {
		et := "spoolman_spool"
		tag.BoundEntityType = &et
		tag.BoundEntityID = &spoolID
	}
	if err := b.InsertNFCTag(tag); err != nil {
		return nil, err
	}
	return b.GetNFCTag(tag.TagID)
}

// CreateSpoolFromFilament creates a new Spoolman spool of the given filament via the
// Spoolman create-spool API. The new spool then appears everywhere existing spools do.
func (b *FilamentBridge) CreateSpoolFromFilament(filamentID int) (*SpoolmanSpool, error) {
	if filamentID <= 0 {
		return nil, fmt.Errorf("filament_id is required")
	}
	return b.spoolman.CreateSpool(map[string]interface{}{"filament_id": filamentID})
}

// ListUnboundSpoolTags returns active spool tags with no Spoolman binding — the pool of
// "available" Spool NFCs that can be linked to a newly created spool.
func (b *FilamentBridge) ListUnboundSpoolTags() ([]NFCTag, error) {
	all, err := b.ListNFCTagsByType("spool")
	if err != nil {
		return nil, err
	}
	out := make([]NFCTag, 0)
	for _, t := range all {
		if t.Status == "active" && t.BoundEntityID == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// BindSpoolTag binds an existing spool tag to a Spoolman spool (link dropdown / tap-to-bind).
func (b *FilamentBridge) BindSpoolTag(tagID string, spoolID int) error {
	if spoolID <= 0 {
		return fmt.Errorf("spool_id is required")
	}
	tag, err := b.GetNFCTag(tagID)
	if err != nil {
		return err
	}
	if tag == nil || tag.TagType != "spool" {
		return fmt.Errorf("spool tag %q not found", tagID)
	}
	entityType := "spoolman_spool"
	return b.SetNFCTagBinding(tagID, &entityType, &spoolID)
}

// nfcSpoolTagsUnboundHandler returns the available (active, unbound) spool tags.
// GET /api/nfc/unbound-spool-tags
func (ws *WebServer) nfcSpoolTagsUnboundHandler(c *gin.Context) {
	tags, err := ws.bridge.ListUnboundSpoolTags()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, nfcTagStubList(tags))
}

// nfcTagBindHandler binds a spool tag to a Spoolman spool.
// POST /api/nfc/tags/:tag_id/bind  Body: {"spool_id": N}
func (ws *WebServer) nfcTagBindHandler(c *gin.Context) {
	tagID := c.Param("tag_id")
	var body struct {
		SpoolID int `json:"spool_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := ws.bridge.BindSpoolTag(tagID, body.SpoolID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nfcCreateSpoolFromFilamentHandler creates a new Spoolman spool of a filament and returns
// it with the current pool of unbound spool tags (for the link dropdown).
// POST /api/nfc/create-spool-from-filament  Body: {"filament_id": N}
func (ws *WebServer) nfcCreateSpoolFromFilamentHandler(c *gin.Context) {
	var body struct {
		FilamentID int `json:"filament_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	spool, err := ws.bridge.CreateSpoolFromFilament(body.FilamentID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	unbound, _ := ws.bridge.ListUnboundSpoolTags()
	c.JSON(http.StatusCreated, gin.H{"spool": spool, "unbound_spool_tags": nfcTagStubList(unbound)})
}

// nfcTagStub is the minimal tag shape used in dropdowns (tag id + nickname).
type nfcTagStub struct {
	TagID string  `json:"tag_id"`
	Label *string `json:"label"`
}

func nfcTagStubList(tags []NFCTag) []nfcTagStub {
	out := make([]nfcTagStub, 0, len(tags))
	for _, t := range tags {
		out = append(out, nfcTagStub{TagID: t.TagID, Label: t.Label})
	}
	return out
}

// nfcTagResolveHandler is the tap-tap engine entry point for every NFC scan.
// Every physical tag scan opens GET /tag/:tag_id in iPhone Safari (plain URL, no Web NFC).
// ProcessTap handles the stateful first-tap / second-tap / expiry logic.
// GET /tag/:tag_id
func (ws *WebServer) nfcTagResolveHandler(c *gin.Context) {
	tagID := c.Param("tag_id")

	result, err := ws.bridge.ProcessTap(tagID)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "nfc_error.html", gin.H{"Error": "Failed to process NFC tap."})
		return
	}

	spoolmanURL := ws.bridge.config.SpoolmanURL

	switch result.Action {
	case TapUnknown:
		c.HTML(http.StatusNotFound, "nfc_error.html", gin.H{"Error": "This NFC tag is not registered. Add it in The Moment's NFCs tab."})

	case TapStored, TapReplaced:
		tag, _ := ws.bridge.GetNFCTag(tagID)
		data := ws.buildPendingPageData(tag, result, c.Request.Host)
		c.HTML(http.StatusOK, "tag_pending.html", data)

	case TapAssigned:
		kind := "location_spool"
		if result.ToolheadIdx < 0 {
			kind = "location_storage"
		}
		c.HTML(http.StatusOK, "tag_assigned.html", gin.H{
			"Kind":          kind,
			"PrinterName":   result.PrinterName,
			"ToolheadIdx":   result.ToolheadIdx,
			"SpoolID":       result.SpoolID,
			"SpoolName":     result.SpoolName,
			"SpoolmanURL":   spoolmanURL,
		})

	case TapBound:
		c.HTML(http.StatusOK, "tag_assigned.html", gin.H{
			"Kind":         "spool_filament",
			"SpoolID":      result.SpoolID,
			"SpoolName":    result.SpoolName,
			"FilamentID":   result.FilamentID,
			"FilamentName": result.FilamentName,
			"Message":      result.Message,
			"SpoolmanURL":  spoolmanURL,
		})

	case TapConflict:
		c.HTML(http.StatusOK, "tag_conflict.html", gin.H{
			"SpoolTagID":      result.SpoolTagID,
			"FilamentTagID":   result.FilamentTagID,
			"SpoolID":         result.SpoolID,
			"SpoolName":       result.SpoolName,
			"OldFilamentID":   result.OldFilamentID,
			"OldFilamentName": result.OldFilamentName,
			"NewFilamentID":   result.NewFilamentID,
			"NewFilamentName": result.NewFilamentName,
		})

	default: // TapError or unexpected
		msg := result.Message
		if msg == "" {
			msg = "Unexpected tap result."
		}
		c.HTML(http.StatusOK, "nfc_error.html", gin.H{"Error": msg})
	}
}

// buildPendingPageData enriches the tag with Spoolman display data for tag_pending.html.
func (ws *WebServer) buildPendingPageData(tag *NFCTag, result TapResult, host string) gin.H {
	data := gin.H{
		"Action":      result.Action,
		"TagType":     tag.TagType,
		"TagURL":      nfcTagURL(host, tag.TagID),
		"SpoolmanURL": ws.bridge.config.SpoolmanURL,
	}
	if tag.Label != nil {
		data["Label"] = *tag.Label
	}
	switch tag.TagType {
	case "filament":
		if tag.BoundEntityID != nil {
			data["FilamentID"] = *tag.BoundEntityID
			if fils, err := ws.bridge.spoolman.GetAllFilaments(); err == nil {
				for _, f := range fils {
					if f.ID == *tag.BoundEntityID {
						data["FilamentName"] = f.Name
						data["Material"] = f.Material
						if f.ColorHex != "" {
							data["ColorHex"] = "#" + f.ColorHex
						}
						if f.Vendor != nil {
							data["Vendor"] = f.Vendor.Name
						}
						break
					}
				}
			}
		}
	case "spool":
		if tag.BoundEntityID != nil {
			data["SpoolID"] = *tag.BoundEntityID
			if s, err := ws.bridge.spoolman.GetSpoolByID(*tag.BoundEntityID); err == nil && s != nil {
				data["SpoolName"] = s.Name
				data["Material"] = s.Material
				data["Location"] = s.Location
				data["RemainingWeight"] = fmt.Sprintf("%.0f", s.RemainingWeight)
				if s.Filament != nil && s.Filament.ColorHex != "" {
					data["ColorHex"] = "#" + s.Filament.ColorHex
				}
			}
		}
	case "location":
		if tag.LocationKind != nil {
			data["LocationKind"] = *tag.LocationKind
		}
	}
	return data
}

// nfcTagTapPostHandler handles POST /tag/tap — conflict resolution for spool↔filament taps.
// The tag_conflict.html page submits here with the user's choice.
func (ws *WebServer) nfcTagTapPostHandler(c *gin.Context) {
	spoolTagID := c.PostForm("spool_tag_id")
	filamentTagID := c.PostForm("filament_tag_id")
	choice := c.PostForm("choice")

	if spoolTagID == "" || filamentTagID == "" || choice == "" {
		c.HTML(http.StatusBadRequest, "nfc_error.html", gin.H{"Error": "Missing required fields."})
		return
	}

	result, err := ws.bridge.ResolveTapConflict(spoolTagID, filamentTagID, choice)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "nfc_error.html", gin.H{"Error": "Failed to resolve conflict."})
		return
	}

	if result.Action == TapError {
		c.HTML(http.StatusOK, "nfc_error.html", gin.H{"Error": result.Message})
		return
	}

	c.HTML(http.StatusOK, "tag_assigned.html", gin.H{
		"Kind":         "spool_filament",
		"SpoolID":      result.SpoolID,
		"SpoolName":    result.SpoolName,
		"FilamentID":   result.FilamentID,
		"FilamentName": result.FilamentName,
		"Message":      result.Message,
		"SpoolmanURL":  ws.bridge.config.SpoolmanURL,
	})
}

// encodeTagQR renders the tag URL as a base64 PNG QR code, or "" on error.
func encodeTagQR(host, tagID string) string {
	png, err := qrcode.Encode(nfcTagURL(host, tagID), qrcode.Medium, 256)
	if err != nil {
		log.Printf("warning: failed to encode QR for tag %s: %v", tagID, err)
		return ""
	}
	return base64.StdEncoding.EncodeToString(png)
}

// isLabelConflict reports whether err is a unique-constraint violation on the per-type
// label index (SQLite surfaces this as "UNIQUE constraint failed").
func isLabelConflict(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
