// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"database/sql"
	"fmt"
	"time"
)

// NFCTag is one row in the nfc_tags binding registry — the single source of truth
// for which physical sticker (tag_id) maps to which Spoolman entity. Spoolman remains
// the source of truth for the entity DATA; this table holds only the binding.
type NFCTag struct {
	TagID           string    `json:"tag_id"`
	TagType         string    `json:"tag_type"`           // spool | location | filament
	Label           *string   `json:"label"`              // nullable display nickname
	BoundEntityType *string   `json:"bound_entity_type"`  // nullable: spoolman_spool|spoolman_location|spoolman_filament
	BoundEntityID   *int      `json:"bound_entity_id"`    // nullable Spoolman id
	LocationKind    *string   `json:"location_kind"`      // nullable: toolhead|inventory|archive|trash (location tags, Stage 4)
	Status          string    `json:"status"`             // active | archived
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// PendingTap is the single ephemeral tap-tap state row used by the tap-tap engine
// (Stage 5). The table holds zero or one row; an empty table means "no pending tap".
type PendingTap struct {
	TagID     string    `json:"pending_tag_id"`
	TagType   string    `json:"pending_tag_type"`
	EntityID  *int      `json:"pending_entity_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// migrateNFCTags creates the NFC binding registry, the filament-tag spec sidecar table,
// and the single-row pending tap-tap state table. Additive only — follows the same
// pattern as migrateNFCAssignments.
func (b *FilamentBridge) migrateNFCTags() error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS nfc_tags (
			tag_id            TEXT PRIMARY KEY,
			tag_type          TEXT NOT NULL CHECK (tag_type IN ('spool','location','filament')),
			label             TEXT,
			bound_entity_type TEXT CHECK (bound_entity_type IN ('spoolman_spool','spoolman_location','spoolman_filament')),
			bound_entity_id   INTEGER,
			location_kind     TEXT,
			status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
			created_at        DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at        DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS tag_filament_spec (
			tag_id            TEXT PRIMARY KEY,
			manufacturer      TEXT,
			material          TEXT,
			color_name        TEXT,
			color_hex         TEXT,
			diameter_mm       REAL,
			density           REAL,
			default_weight_g  REAL,
			default_price     REAL,
			openprinttag_json TEXT,
			FOREIGN KEY (tag_id) REFERENCES nfc_tags(tag_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS nfc_pending_tap (
			id                INTEGER PRIMARY KEY CHECK (id = 1),
			pending_tag_id    TEXT NOT NULL,
			pending_tag_type  TEXT NOT NULL,
			pending_entity_id INTEGER,
			expires_at        DATETIME NOT NULL
		)`,
	}
	for _, q := range tables {
		if _, err := b.db.Exec(q); err != nil {
			return fmt.Errorf("failed to create NFC tags table: %w", err)
		}
	}

	// "label unique within tag_type when non-null". The partial index (WHERE label IS NOT NULL)
	// leaves duplicate null labels allowed.
	if _, err := b.db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_nfc_tags_label_per_type ON nfc_tags(tag_type, label) WHERE label IS NOT NULL`,
	); err != nil {
		return fmt.Errorf("failed to create NFC tag label index: %w", err)
	}
	return nil
}

// ─── nfc_tags CRUD ────────────────────────────────────────────────────────────

// InsertNFCTag inserts a new tag row. The tag_id primary key makes a duplicate insert
// fail rather than silently overwrite. created_at/updated_at default in the DB.
func (b *FilamentBridge) InsertNFCTag(tag NFCTag) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if tag.Status == "" {
		tag.Status = "active"
	}
	_, err := b.db.Exec(
		`INSERT INTO nfc_tags (tag_id, tag_type, label, bound_entity_type, bound_entity_id, location_kind, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tag.TagID, tag.TagType, tag.Label, tag.BoundEntityType, tag.BoundEntityID, tag.LocationKind, tag.Status,
	)
	if err != nil {
		return fmt.Errorf("failed to insert NFC tag %q: %w", tag.TagID, err)
	}
	return nil
}

// scanNFCTag scans one nfc_tags row, translating SQL nulls to nil pointers.
func scanNFCTag(s interface {
	Scan(dest ...interface{}) error
}) (*NFCTag, error) {
	var (
		t               NFCTag
		label           sql.NullString
		boundEntityType sql.NullString
		boundEntityID   sql.NullInt64
		locationKind    sql.NullString
	)
	if err := s.Scan(
		&t.TagID, &t.TagType, &label, &boundEntityType, &boundEntityID, &locationKind,
		&t.Status, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if label.Valid {
		t.Label = &label.String
	}
	if boundEntityType.Valid {
		t.BoundEntityType = &boundEntityType.String
	}
	if boundEntityID.Valid {
		v := int(boundEntityID.Int64)
		t.BoundEntityID = &v
	}
	if locationKind.Valid {
		t.LocationKind = &locationKind.String
	}
	return &t, nil
}

const nfcTagColumns = `tag_id, tag_type, label, bound_entity_type, bound_entity_id, location_kind, status, created_at, updated_at`

// GetNFCTag returns the tag with the given id, or (nil, nil) when no row exists.
func (b *FilamentBridge) GetNFCTag(tagID string) (*NFCTag, error) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	row := b.db.QueryRow(`SELECT `+nfcTagColumns+` FROM nfc_tags WHERE tag_id = ?`, tagID)
	tag, err := scanNFCTag(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get NFC tag %q: %w", tagID, err)
	}
	return tag, nil
}

// ListNFCTagsByType returns all tags of one type, newest first.
func (b *FilamentBridge) ListNFCTagsByType(tagType string) ([]NFCTag, error) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	rows, err := b.db.Query(
		`SELECT `+nfcTagColumns+` FROM nfc_tags WHERE tag_type = ? ORDER BY created_at DESC, tag_id`,
		tagType,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list NFC tags of type %q: %w", tagType, err)
	}
	defer rows.Close()

	var tags []NFCTag
	for rows.Next() {
		tag, err := scanNFCTag(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan NFC tag: %w", err)
		}
		tags = append(tags, *tag)
	}
	return tags, rows.Err()
}

// SetNFCTagLabel updates the display nickname (nil clears it). Uniqueness within the
// tag type is enforced by idx_nfc_tags_label_per_type.
func (b *FilamentBridge) SetNFCTagLabel(tagID string, label *string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(
		`UPDATE nfc_tags SET label = ?, updated_at = datetime('now') WHERE tag_id = ?`,
		label, tagID,
	)
	if err != nil {
		return fmt.Errorf("failed to set label on NFC tag %q: %w", tagID, err)
	}
	return nil
}

// SetNFCTagBinding sets or clears the Spoolman binding (pass nil, nil to unbind).
func (b *FilamentBridge) SetNFCTagBinding(tagID string, entityType *string, entityID *int) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(
		`UPDATE nfc_tags SET bound_entity_type = ?, bound_entity_id = ?, updated_at = datetime('now') WHERE tag_id = ?`,
		entityType, entityID, tagID,
	)
	if err != nil {
		return fmt.Errorf("failed to set binding on NFC tag %q: %w", tagID, err)
	}
	return nil
}

// SetNFCTagStatus sets the tag status (active|archived). The CHECK constraint rejects
// any other value.
func (b *FilamentBridge) SetNFCTagStatus(tagID, status string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(
		`UPDATE nfc_tags SET status = ?, updated_at = datetime('now') WHERE tag_id = ?`,
		status, tagID,
	)
	if err != nil {
		return fmt.Errorf("failed to set status on NFC tag %q: %w", tagID, err)
	}
	return nil
}

// SetNFCTagLocationKind sets or clears the location_kind for a location tag. Pass nil to clear.
func (b *FilamentBridge) SetNFCTagLocationKind(tagID string, kind *string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(
		`UPDATE nfc_tags SET location_kind = ?, updated_at = datetime('now') WHERE tag_id = ?`,
		kind, tagID,
	)
	if err != nil {
		return fmt.Errorf("failed to set location_kind on NFC tag %q: %w", tagID, err)
	}
	return nil
}

// DeleteNFCTag removes a tag row. The Spoolman entity it was bound to is untouched.
func (b *FilamentBridge) DeleteNFCTag(tagID string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(`DELETE FROM nfc_tags WHERE tag_id = ?`, tagID)
	if err != nil {
		return fmt.Errorf("failed to delete NFC tag %q: %w", tagID, err)
	}
	return nil
}

// ─── tag_filament_spec (filament tags) ────────────────────────────────────────

// TagFilamentSpec is the OpenPrintTag authoring spec stored alongside a filament tag.
// Only populated for filament tags authored from typed fields; bound tags read their
// data from the Spoolman filament instead.
type TagFilamentSpec struct {
	TagID            string  `json:"tag_id"`
	Manufacturer     string  `json:"manufacturer"`
	Material         string  `json:"material"`
	ColorName        string  `json:"color_name"`
	ColorHex         string  `json:"color_hex"`
	DiameterMM       float64 `json:"diameter_mm"`
	Density          float64 `json:"density"`
	DefaultWeightG   float64 `json:"default_weight_g"`
	DefaultPrice     float64 `json:"default_price"`
	OpenPrintTagJSON string  `json:"openprinttag_json"`
}

// SetTagFilamentSpec inserts or replaces the spec row for a filament tag.
func (b *FilamentBridge) SetTagFilamentSpec(spec TagFilamentSpec) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(
		`INSERT OR REPLACE INTO tag_filament_spec
		 (tag_id, manufacturer, material, color_name, color_hex, diameter_mm, density, default_weight_g, default_price, openprinttag_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		spec.TagID, spec.Manufacturer, spec.Material, spec.ColorName, spec.ColorHex,
		spec.DiameterMM, spec.Density, spec.DefaultWeightG, spec.DefaultPrice, spec.OpenPrintTagJSON,
	)
	if err != nil {
		return fmt.Errorf("failed to set filament spec for tag %q: %w", spec.TagID, err)
	}
	return nil
}

// GetTagFilamentSpec returns the spec for a filament tag, or (nil, nil) when none exists.
func (b *FilamentBridge) GetTagFilamentSpec(tagID string) (*TagFilamentSpec, error) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	var s TagFilamentSpec
	err := b.db.QueryRow(
		`SELECT tag_id, manufacturer, material, color_name, color_hex, diameter_mm, density, default_weight_g, default_price, openprinttag_json
		 FROM tag_filament_spec WHERE tag_id = ?`, tagID,
	).Scan(&s.TagID, &s.Manufacturer, &s.Material, &s.ColorName, &s.ColorHex,
		&s.DiameterMM, &s.Density, &s.DefaultWeightG, &s.DefaultPrice, &s.OpenPrintTagJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get filament spec for tag %q: %w", tagID, err)
	}
	return &s, nil
}

// ─── pending tap-tap state ────────────────────────────────────────────────────

// GetPendingTap returns the pending tap, or (nil, nil) when none is set — a clean
// "none", not an error.
func (b *FilamentBridge) GetPendingTap() (*PendingTap, error) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	var (
		p        PendingTap
		entityID sql.NullInt64
	)
	err := b.db.QueryRow(
		`SELECT pending_tag_id, pending_tag_type, pending_entity_id, expires_at FROM nfc_pending_tap WHERE id = 1`,
	).Scan(&p.TagID, &p.TagType, &entityID, &p.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get pending tap: %w", err)
	}
	if entityID.Valid {
		v := int(entityID.Int64)
		p.EntityID = &v
	}
	return &p, nil
}

// SetPendingTap stores the single pending tap row, replacing any existing one.
func (b *FilamentBridge) SetPendingTap(p PendingTap) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(
		`INSERT OR REPLACE INTO nfc_pending_tap (id, pending_tag_id, pending_tag_type, pending_entity_id, expires_at)
		 VALUES (1, ?, ?, ?, ?)`,
		p.TagID, p.TagType, p.EntityID, p.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("failed to set pending tap: %w", err)
	}
	return nil
}

// ClearPendingTap removes the pending tap row. Clearing when already empty is a no-op.
func (b *FilamentBridge) ClearPendingTap() error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	_, err := b.db.Exec(`DELETE FROM nfc_pending_tap WHERE id = 1`)
	if err != nil {
		return fmt.Errorf("failed to clear pending tap: %w", err)
	}
	return nil
}
