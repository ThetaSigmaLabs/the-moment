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
	if boundID <= 0 {
		if spec == nil {
			return nil, fmt.Errorf("spec required to author a new filament")
		}
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
	entityType := "spoolman_filament"
	if err := b.InsertNFCTag(NFCTag{
		TagID:           tagID,
		TagType:         "filament",
		Label:           label,
		BoundEntityType: &entityType,
		BoundEntityID:   &boundID,
	}); err != nil {
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

// nfcTagsListHandler returns all tags of a given type (default: filament) enriched with
// their current Spoolman binding.
// GET /api/nfc/tags?type=filament
func (ws *WebServer) nfcTagsListHandler(c *gin.Context) {
	tagType := c.DefaultQuery("type", "filament")

	tags, err := ws.bridge.ListNFCTagsByType(tagType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Build a filament lookup for binding enrichment (best effort).
	filamentByID := map[int]SpoolmanFilament{}
	if tagType == "filament" {
		if filaments, fErr := ws.bridge.spoolman.GetAllFilaments(); fErr == nil {
			for _, f := range filaments {
				filamentByID[f.ID] = f
			}
		}
	}

	type filamentSummary struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		Material string `json:"material"`
		ColorHex string `json:"color_hex"`
		Vendor   string `json:"vendor"`
	}
	type tagRow struct {
		TagID    string           `json:"tag_id"`
		Label    *string          `json:"label"`
		Status   string           `json:"status"`
		BoundID  *int             `json:"bound_entity_id"`
		Filament *filamentSummary `json:"filament"`
		TagURL   string           `json:"tag_url"`
	}

	host := c.Request.Host
	rows := make([]tagRow, 0, len(tags))
	for _, t := range tags {
		row := tagRow{TagID: t.TagID, Label: t.Label, Status: t.Status, BoundID: t.BoundEntityID, TagURL: nfcTagURL(host, t.TagID)}
		if t.BoundEntityID != nil {
			if f, ok := filamentByID[*t.BoundEntityID]; ok {
				vendor := ""
				if f.Vendor != nil {
					vendor = f.Vendor.Name
				}
				row.Filament = &filamentSummary{ID: f.ID, Name: f.Name, Material: f.Material, ColorHex: f.ColorHex, Vendor: vendor}
			}
		}
		rows = append(rows, row)
	}
	c.JSON(http.StatusOK, rows)
}

// nfcTagCreateHandler creates a tag. Stage 2 supports filament tags only.
// POST /api/nfc/tags
// Body: {"tag_type":"filament","label":"...","filament_id":7,"spec":{...}}
func (ws *WebServer) nfcTagCreateHandler(c *gin.Context) {
	var body struct {
		TagType    string           `json:"tag_type"`
		Label      string           `json:"label"`
		FilamentID int              `json:"filament_id"`
		Spec       *TagFilamentSpec `json:"spec"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.TagType != "filament" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only filament tags are supported in this stage"})
		return
	}

	var label *string
	if strings.TrimSpace(body.Label) != "" {
		l := strings.TrimSpace(body.Label)
		label = &l
	}

	tag, err := ws.bridge.CreateFilamentTag(label, body.FilamentID, body.Spec)
	if err != nil {
		if isLabelConflict(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "a filament tag with that label already exists"})
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
