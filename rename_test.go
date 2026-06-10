// SPDX-License-Identifier: GPL-3.0-or-later

package main

// =============================================================================
// rename_test.go
// =============================================================================
// Unit tests for RenamePrint and RenameAttachment, verifying that the DB records
// and physical files are updated atomically and that the two fields stay in sync.
//
// Run with:
//   go test ./... -v -run TestRename
// =============================================================================

import (
	"os"
	"path/filepath"
	"testing"
)

// testBridgeWithGcode returns a bridge whose GcodePath is set to a temp directory
// and inserts a fake print record plus a gcode attachment on disk.
// Returns (bridge, printID, attachmentID, relPath).
func testBridgeWithGcode(t *testing.T) (*FilamentBridge, int, int, string) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("THE_MOMENT_DB_PATH", tmpDir)
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })

	// Override gcodePath to the same tmpDir so files are co-located with the DB.
	cfg := &Config{GcodePath: tmpDir}
	bridge.config = cfg

	// Insert a print history record.
	res, err := bridge.db.Exec(
		`INSERT INTO print_history (printer_name, toolhead_id, spool_id, filament_used, job_name, print_started, print_finished)
		 VALUES ('TestPrinter', 0, 1, 10.0, 'original-name', datetime('now'), datetime('now'))`,
	)
	if err != nil {
		t.Fatalf("insert print_history: %v", err)
	}
	printID64, _ := res.LastInsertId()
	printID := int(printID64)

	// Create the physical file and DB attachment record.
	subDir := filepath.Join(tmpDir, "print-files", "2026", "01")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	relPath := filepath.Join("print-files", "2026", "01", "1_original-name.gcode")
	absPath := filepath.Join(tmpDir, relPath)
	if err := os.WriteFile(absPath, []byte("; gcode content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	attachID64, err := bridge.SavePrintAttachment(printID, "gcode", "original-name.gcode", relPath, "", 15)
	if err != nil {
		t.Fatalf("SavePrintAttachment: %v", err)
	}

	return bridge, printID, int(attachID64), relPath
}

// jobName reads print_history.job_name for the given print ID.
func jobName(t *testing.T, bridge *FilamentBridge, printID int) string {
	t.Helper()
	var name string
	if err := bridge.db.QueryRow(`SELECT job_name FROM print_history WHERE id = ?`, printID).Scan(&name); err != nil {
		t.Fatalf("read job_name: %v", err)
	}
	return name
}

// attachFilename reads print_attachments.filename for the given attachment ID.
func attachFilename(t *testing.T, bridge *FilamentBridge, attachID int) string {
	t.Helper()
	var name string
	if err := bridge.db.QueryRow(`SELECT filename FROM print_attachments WHERE id = ?`, attachID).Scan(&name); err != nil {
		t.Fatalf("read filename: %v", err)
	}
	return name
}

// attachFilePath reads print_attachments.file_path for the given attachment ID.
func attachFilePath(t *testing.T, bridge *FilamentBridge, attachID int) string {
	t.Helper()
	var path string
	if err := bridge.db.QueryRow(`SELECT file_path FROM print_attachments WHERE id = ?`, attachID).Scan(&path); err != nil {
		t.Fatalf("read file_path: %v", err)
	}
	return path
}

// ─── RenamePrint ──────────────────────────────────────────────────────────────

// TestRenamePrint_NoAttachment verifies that renaming is rejected when no gcode
// attachment exists for the record.
func TestRenamePrint_NoAttachment(t *testing.T) {
	bridge := testBridge(t)
	res, err := bridge.db.Exec(
		`INSERT INTO print_history (printer_name, toolhead_id, spool_id, filament_used, job_name, print_started, print_finished)
		 VALUES ('P', 0, 0, 0, 'bare', datetime('now'), datetime('now'))`,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	id64, _ := res.LastInsertId()
	if err := bridge.RenamePrint(int(id64), "new-name"); err == nil {
		t.Fatal("expected error when no gcode attached, got nil")
	}
}

// TestRenamePrint_EmptyName verifies that an empty (or whitespace-stripped) name is rejected.
func TestRenamePrint_EmptyName(t *testing.T) {
	bridge, printID, _, _ := testBridgeWithGcode(t)
	if err := bridge.RenamePrint(printID, ""); err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
	if err := bridge.RenamePrint(printID, ".gcode"); err == nil {
		t.Fatal("expected error when name is only extension, got nil")
	}
}

// TestRenamePrint_RenamesFileAndDB verifies that the DB job_name, attachment filename,
// attachment file_path, and the physical file on disk are all updated correctly.
func TestRenamePrint_RenamesFileAndDB(t *testing.T) {
	bridge, printID, attachID, oldRelPath := testBridgeWithGcode(t)

	if err := bridge.RenamePrint(printID, "my-cool-print"); err != nil {
		t.Fatalf("RenamePrint: %v", err)
	}

	if got := jobName(t, bridge, printID); got != "my-cool-print" {
		t.Errorf("job_name = %q; want %q", got, "my-cool-print")
	}
	if got := attachFilename(t, bridge, attachID); got != "my-cool-print.gcode" {
		t.Errorf("attachment filename = %q; want %q", got, "my-cool-print.gcode")
	}

	newRelPath := attachFilePath(t, bridge, attachID)
	oldAbs := filepath.Join(bridge.gcodePath(), oldRelPath)
	newAbs := filepath.Join(bridge.gcodePath(), newRelPath)

	if _, err := os.Stat(oldAbs); err == nil {
		t.Errorf("old file still exists at %s", oldAbs)
	}
	if _, err := os.Stat(newAbs); err != nil {
		t.Errorf("new file not found at %s: %v", newAbs, err)
	}
}

// TestRenamePrint_StripsExtension verifies that passing a name with an extension
// strips it so job_name stays clean.
func TestRenamePrint_StripsExtension(t *testing.T) {
	bridge, printID, attachID, _ := testBridgeWithGcode(t)

	if err := bridge.RenamePrint(printID, "trimmed-name.gcode"); err != nil {
		t.Fatalf("RenamePrint: %v", err)
	}
	if got := jobName(t, bridge, printID); got != "trimmed-name" {
		t.Errorf("job_name = %q; want \"trimmed-name\"", got)
	}
	if got := attachFilename(t, bridge, attachID); got != "trimmed-name.gcode" {
		t.Errorf("attachment filename = %q; want \"trimmed-name.gcode\"", got)
	}
}

// ─── RenameAttachment ─────────────────────────────────────────────────────────

// TestRenameAttachment_UpdatesJobName verifies that renaming a gcode attachment
// strips the extension and updates print_history.job_name.
func TestRenameAttachment_UpdatesJobName(t *testing.T) {
	bridge, printID, attachID, _ := testBridgeWithGcode(t)

	if err := bridge.RenameAttachment(attachID, "renamed-file.gcode"); err != nil {
		t.Fatalf("RenameAttachment: %v", err)
	}
	if got := attachFilename(t, bridge, attachID); got != "renamed-file.gcode" {
		t.Errorf("attachment filename = %q; want \"renamed-file.gcode\"", got)
	}
	if got := jobName(t, bridge, printID); got != "renamed-file" {
		t.Errorf("job_name = %q; want \"renamed-file\"", got)
	}
}

// TestRenameAttachment_NonGcode verifies that renaming a non-gcode attachment
// does NOT touch print_history.job_name.
func TestRenameAttachment_NonGcode(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("THE_MOMENT_DB_PATH", tmpDir)
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })
	bridge.config = &Config{GcodePath: tmpDir}

	res, _ := bridge.db.Exec(
		`INSERT INTO print_history (printer_name, toolhead_id, spool_id, filament_used, job_name, print_started, print_finished)
		 VALUES ('P', 0, 0, 0, 'original-job', datetime('now'), datetime('now'))`,
	)
	printID64, _ := res.LastInsertId()
	printID := int(printID64)

	subDir := filepath.Join(tmpDir, "print-files", "2026", "01")
	os.MkdirAll(subDir, 0755)
	relPath := filepath.Join("print-files", "2026", "01", "1_model.3mf")
	os.WriteFile(filepath.Join(tmpDir, relPath), []byte("data"), 0644)

	attachID64, _ := bridge.SavePrintAttachment(printID, "slicer", "model.3mf", relPath, "", 4)
	attachID := int(attachID64)

	if err := bridge.RenameAttachment(attachID, "renamed.3mf"); err != nil {
		t.Fatalf("RenameAttachment: %v", err)
	}
	if got := jobName(t, bridge, printID); got != "original-job" {
		t.Errorf("job_name changed unexpectedly to %q", got)
	}
	if got := attachFilename(t, bridge, attachID); got != "renamed.3mf" {
		t.Errorf("attachment filename = %q; want \"renamed.3mf\"", got)
	}
}

// TestRenameAttachment_RenamesFileOnDisk verifies the physical file is moved.
func TestRenameAttachment_RenamesFileOnDisk(t *testing.T) {
	bridge, _, attachID, oldRelPath := testBridgeWithGcode(t)

	if err := bridge.RenameAttachment(attachID, "disk-rename.gcode"); err != nil {
		t.Fatalf("RenameAttachment: %v", err)
	}

	newRelPath := attachFilePath(t, bridge, attachID)
	oldAbs := filepath.Join(bridge.gcodePath(), oldRelPath)
	newAbs := filepath.Join(bridge.gcodePath(), newRelPath)

	if _, err := os.Stat(oldAbs); err == nil {
		t.Errorf("old file still exists at %s", oldAbs)
	}
	if _, err := os.Stat(newAbs); err != nil {
		t.Errorf("new file not found at %s: %v", newAbs, err)
	}
}
