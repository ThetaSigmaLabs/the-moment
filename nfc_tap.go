// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"fmt"
	"log"
	"strconv"
	"time"
)

// Tap action constants returned by ProcessTap.
const (
	TapStored   = "stored"   // first tap; pending stored; user should scan second tag
	TapReplaced = "replaced" // same-type double-tap; pending replaced; scan second tag
	TapAssigned = "assigned" // location↔spool: SetToolheadMapping completed
	TapBound    = "bound"    // spool↔filament: direct bind (no prior filament conflict)
	TapConflict = "conflict" // spool↔filament: spool already has a different filament; needs user choice
	TapUnknown  = "unknown"  // tag_id not found in nfc_tags
	TapError    = "error"    // assignment failed; Message has details
)

// TapResult is the outcome of ProcessTap. Not all fields are set for every action.
type TapResult struct {
	Action  string
	Message string
	// assigned — location↔spool
	PrinterName string
	ToolheadIdx int
	SpoolID     int
	SpoolName   string
	// bound — spool↔filament direct bind
	FilamentID   int
	FilamentName string
	// conflict — spool↔filament with different existing filament; needs user choice
	SpoolTagID      string
	FilamentTagID   string
	OldFilamentID   int
	OldFilamentName string
	NewFilamentID   int
	NewFilamentName string
}

// ProcessTap is the tap-tap engine entry point. Every physical NFC scan opens
// GET /tag/:tag_id which calls this. On first scan: stores pending. On second
// scan with a valid combo: executes the assignment and clears pending. Same-type
// double-tap replaces pending without triggering an assignment.
//
// Boundary is inclusive: a second tap at exactly expires_at is still valid
// because time.Now().After(expiresAt) is false at exact equality.
func (b *FilamentBridge) ProcessTap(tagID string) (TapResult, error) {
	tag, err := b.GetNFCTag(tagID)
	if err != nil {
		return TapResult{}, err
	}
	if tag == nil {
		return TapResult{Action: TapUnknown}, nil
	}

	pending, err := b.GetPendingTap()
	if err != nil {
		return TapResult{}, err
	}

	// Expire stale pending first.
	if pending != nil && time.Now().After(pending.ExpiresAt) {
		if clearErr := b.ClearPendingTap(); clearErr != nil {
			log.Printf("ProcessTap: ClearPendingTap (expired): %v", clearErr)
		}
		pending = nil
	}

	// First tap: store as pending and return.
	if pending == nil {
		if err := b.storePendingForTag(tag); err != nil {
			return TapResult{}, err
		}
		return TapResult{Action: TapStored}, nil
	}

	// Second tap: determine the combo.
	firstType, secondType := pending.TagType, tag.TagType

	// Same type: replace pending (no assignment, no error).
	if firstType == secondType {
		if err := b.storePendingForTag(tag); err != nil {
			return TapResult{}, err
		}
		return TapResult{Action: TapReplaced}, nil
	}

	// Clear pending before executing — assignment either succeeds or fails; either way, done.
	if err := b.ClearPendingTap(); err != nil {
		log.Printf("ProcessTap: ClearPendingTap before assignment: %v", err)
	}

	switch {
	case isLocationSpool(firstType, secondType):
		var locTagID string
		var spoolEntityID *int
		if firstType == "location" {
			locTagID = pending.TagID
			spoolEntityID = tag.BoundEntityID
		} else {
			locTagID = tagID
			spoolEntityID = pending.EntityID
		}
		return b.completeLocationSpoolTap(locTagID, spoolEntityID)

	case isSpoolFilament(firstType, secondType):
		var spoolTagID, filamentTagID string
		var spoolEntityID, filamentEntityID *int
		if firstType == "spool" {
			spoolTagID, spoolEntityID = pending.TagID, pending.EntityID
			filamentTagID, filamentEntityID = tagID, tag.BoundEntityID
		} else {
			filamentTagID, filamentEntityID = pending.TagID, pending.EntityID
			spoolTagID, spoolEntityID = tagID, tag.BoundEntityID
		}
		return b.completeSpoolFilamentTap(spoolTagID, filamentTagID, spoolEntityID, filamentEntityID)

	default:
		// Unrecognized combo (e.g. location↔filament): replace pending.
		if err := b.storePendingForTag(tag); err != nil {
			return TapResult{}, err
		}
		return TapResult{Action: TapReplaced}, nil
	}
}

// completeLocationSpoolTap assigns or moves a spool based on the location tag's kind.
// For toolhead kind: calls SetToolheadMapping (existing path).
// For inventory/archive/trash kind: unassigns from any toolhead and sets the Spoolman
// location to the tag's label so the spool moves to the named storage location.
func (b *FilamentBridge) completeLocationSpoolTap(locTagID string, spoolEntityID *int) (TapResult, error) {
	locTag, err := b.GetNFCTag(locTagID)
	if err != nil {
		return TapResult{Action: TapError, Message: "failed to read location tag"}, nil
	}
	if locTag == nil || locTag.Label == nil {
		return TapResult{Action: TapError, Message: "location tag has no label — set one in the NFCs tab"}, nil
	}
	if spoolEntityID == nil {
		return TapResult{Action: TapError, Message: "spool tag is not linked to a spool — bind it in the NFCs tab first"}, nil
	}
	spoolID := *spoolEntityID

	kind := ""
	if locTag.LocationKind != nil {
		kind = *locTag.LocationKind
	}

	// ── Toolhead location ────────────────────────────────────────────────────────
	if kind == "" || kind == "toolhead" {
		printerName, toolheadIdx, ok := ParseToolheadLocation(*locTag.Label)
		if !ok {
			return TapResult{Action: TapError, Message: fmt.Sprintf("location tag label %q is not in printer format (e.g. \"Ender3 - T0\")", *locTag.Label)}, nil
		}
		// SetToolheadMapping may call Spoolman (if sync enabled); must not hold b.mutex.
		if err := b.SetToolheadMapping(printerName, toolheadIdx, spoolID); err != nil {
			return TapResult{Action: TapError, Message: "assignment failed: " + err.Error()}, nil
		}
		spoolName := ""
		if spool, sErr := b.spoolman.GetSpoolByID(spoolID); sErr == nil && spool != nil {
			spoolName = spool.Name
		}
		return TapResult{
			Action:      TapAssigned,
			PrinterName: printerName,
			ToolheadIdx: toolheadIdx,
			SpoolID:     spoolID,
			SpoolName:   spoolName,
		}, nil
	}

	// ── Storage location (inventory / archive / trash) ───────────────────────────
	// Unassign from any active toolhead assignments, then set the Spoolman location
	// to this tag's label so the spool appears in the correct storage area.
	if err := b.CloseAssignmentsBySpool(spoolID); err != nil {
		log.Printf("completeLocationSpoolTap: CloseAssignmentsBySpool %d: %v", spoolID, err)
	}
	if err := b.ClearToolheadMappingsBySpool(spoolID); err != nil {
		log.Printf("completeLocationSpoolTap: ClearToolheadMappingsBySpool %d: %v", spoolID, err)
	}
	locationLabel := *locTag.Label
	if err := b.spoolman.UpdateSpoolLocation(spoolID, locationLabel); err != nil {
		return TapResult{Action: TapError, Message: "failed to set spool location: " + err.Error()}, nil
	}
	spoolName := ""
	if spool, sErr := b.spoolman.GetSpoolByID(spoolID); sErr == nil && spool != nil {
		spoolName = spool.Name
	}
	// ToolheadIdx = -1 signals "storage location, not a toolhead" to the handler.
	return TapResult{
		Action:      TapAssigned,
		PrinterName: locationLabel,
		ToolheadIdx: -1,
		SpoolID:     spoolID,
		SpoolName:   spoolName,
	}, nil
}

// completeSpoolFilamentTap binds a spool to a filament via Spoolman. If the spool
// already has a different filament the result is TapConflict — the caller must
// render a choice page and the user POSTs to /tag/tap to resolve.
func (b *FilamentBridge) completeSpoolFilamentTap(spoolTagID, filamentTagID string, spoolEntityID, filamentEntityID *int) (TapResult, error) {
	if spoolEntityID == nil {
		return TapResult{Action: TapError, Message: "spool tag is not linked to a spool — bind it in the NFCs tab first"}, nil
	}
	if filamentEntityID == nil {
		return TapResult{Action: TapError, Message: "filament tag is not linked to a filament — bind it in the NFCs tab first"}, nil
	}
	spoolID := *spoolEntityID
	filamentID := *filamentEntityID

	spool, err := b.spoolman.GetSpoolByID(spoolID)
	if err != nil {
		return TapResult{Action: TapError, Message: "failed to look up spool in Spoolman: " + err.Error()}, nil
	}

	// No conflict: spool has no filament, or already has exactly this filament.
	if spool.Filament == nil || spool.Filament.ID == filamentID {
		if err := b.spoolman.UpdateSpool(spoolID, map[string]interface{}{"filament_id": filamentID}); err != nil {
			return TapResult{Action: TapError, Message: "failed to set filament on spool: " + err.Error()}, nil
		}
		filamentName := filamentNameByID(b, filamentID)
		return TapResult{
			Action:       TapBound,
			SpoolID:      spoolID,
			SpoolName:    spool.Name,
			FilamentID:   filamentID,
			FilamentName: filamentName,
		}, nil
	}

	// Conflict: spool already has a different filament — return details for the choice page.
	newFilamentName := filamentNameByID(b, filamentID)
	return TapResult{
		Action:          TapConflict,
		SpoolTagID:      spoolTagID,
		FilamentTagID:   filamentTagID,
		SpoolID:         spoolID,
		SpoolName:       spool.Name,
		OldFilamentID:   spool.Filament.ID,
		OldFilamentName: spool.Filament.Name,
		NewFilamentID:   filamentID,
		NewFilamentName: newFilamentName,
	}, nil
}

// ResolveTapConflict handles POST /tag/tap after a spool↔filament conflict.
// choice "edit_existing": update the spool's filament_id to the new filament.
// choice "create_new":    create a new Spoolman spool and rebind the NFC tag to it.
func (b *FilamentBridge) ResolveTapConflict(spoolTagID, filamentTagID, choice string) (TapResult, error) {
	spoolTag, err := b.GetNFCTag(spoolTagID)
	if err != nil || spoolTag == nil || spoolTag.BoundEntityID == nil {
		return TapResult{Action: TapError, Message: "spool tag not found or unbound"}, nil
	}
	filamentTag, err := b.GetNFCTag(filamentTagID)
	if err != nil || filamentTag == nil || filamentTag.BoundEntityID == nil {
		return TapResult{Action: TapError, Message: "filament tag not found or unbound"}, nil
	}
	spoolID := *spoolTag.BoundEntityID
	filamentID := *filamentTag.BoundEntityID

	switch choice {
	case "edit_existing":
		if err := b.spoolman.UpdateSpool(spoolID, map[string]interface{}{"filament_id": filamentID}); err != nil {
			return TapResult{Action: TapError, Message: "failed to update spool filament: " + err.Error()}, nil
		}
		return TapResult{
			Action:     TapBound,
			SpoolID:    spoolID,
			FilamentID: filamentID,
			Message:    "Spool filament updated.",
		}, nil

	case "create_new":
		newSpool, err := b.spoolman.CreateSpool(map[string]interface{}{"filament_id": filamentID})
		if err != nil {
			return TapResult{Action: TapError, Message: "failed to create new spool: " + err.Error()}, nil
		}
		entityType := "spoolman_spool"
		if err := b.SetNFCTagBinding(spoolTagID, &entityType, &newSpool.ID); err != nil {
			return TapResult{Action: TapError, Message: "new spool created but failed to rebind NFC tag: " + err.Error()}, nil
		}
		return TapResult{
			Action:     TapBound,
			SpoolID:    newSpool.ID,
			SpoolName:  newSpool.Name,
			FilamentID: filamentID,
			Message:    "New spool created and NFC tag rebound.",
		}, nil

	default:
		return TapResult{Action: TapError, Message: "unknown choice: " + choice}, nil
	}
}

// ArchiveSpoolNFCForSpool finds every active spool NFC bound to spoolID, clears the
// Spoolman binding, and sets status to "archived" so the tag enters the available pool
// for reuse. The Spoolman spool record is NOT modified — history is preserved.
func (b *FilamentBridge) ArchiveSpoolNFCForSpool(spoolID int) error {
	tags, err := b.ListNFCTagsByType("spool")
	if err != nil {
		return fmt.Errorf("ArchiveSpoolNFCForSpool: list tags: %w", err)
	}
	for _, t := range tags {
		if t.BoundEntityID == nil || *t.BoundEntityID != spoolID {
			continue
		}
		if err := b.SetNFCTagBinding(t.TagID, nil, nil); err != nil {
			log.Printf("ArchiveSpoolNFCForSpool %d: clear binding on %s: %v", spoolID, t.TagID, err)
		}
		if err := b.SetNFCTagStatus(t.TagID, "archived"); err != nil {
			log.Printf("ArchiveSpoolNFCForSpool %d: set archived on %s: %v", spoolID, t.TagID, err)
		}
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (b *FilamentBridge) storePendingForTag(tag *NFCTag) error {
	timeout, _ := b.getTapTimeout()
	return b.SetPendingTap(PendingTap{
		TagID:     tag.TagID,
		TagType:   tag.TagType,
		EntityID:  tag.BoundEntityID,
		ExpiresAt: time.Now().Add(timeout),
	})
}

func (b *FilamentBridge) getTapTimeout() (time.Duration, error) {
	val, err := b.GetConfigValue(ConfigKeyNFCTapTimeoutSeconds)
	if err != nil || val == "" {
		return 15 * time.Second, nil
	}
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		return 15 * time.Second, nil
	}
	return time.Duration(n) * time.Second, nil
}

// filamentNameByID looks up a filament name from Spoolman by ID (best effort; returns "" on error).
func filamentNameByID(b *FilamentBridge, filamentID int) string {
	fils, err := b.spoolman.GetAllFilaments()
	if err != nil {
		return ""
	}
	for _, f := range fils {
		if f.ID == filamentID {
			return f.Name
		}
	}
	return ""
}

func isLocationSpool(a, b string) bool {
	return (a == "location" && b == "spool") || (a == "spool" && b == "location")
}

func isSpoolFilament(a, b string) bool {
	return (a == "spool" && b == "filament") || (a == "filament" && b == "spool")
}
