// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

//go:build integration

package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateBackup_DBOnly(t *testing.T) {
	dbDir := t.TempDir()
	backupDir := t.TempDir()
	t.Setenv("THE_MOMENT_DB_PATH", dbDir)
	t.Setenv("BACKUP_DIR", backupDir)

	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("failed to create bridge: %v", err)
	}
	defer bridge.db.Close()

	filename, err := bridge.CreateBackup(BackupScopeDB)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}
	if !validBackupFilename(filename) {
		t.Errorf("generated filename %q does not match expected pattern", filename)
	}
	if _, err := os.Stat(filepath.Join(backupDir, filename)); os.IsNotExist(err) {
		t.Fatalf("archive file not found at %s", filepath.Join(backupDir, filename))
	}
	if !archiveContainsPath(t, filepath.Join(backupDir, filename), "db/the-moment.db") {
		t.Error("archive does not contain db/the-moment.db")
	}
}

func TestCreateBackup_All(t *testing.T) {
	dbDir := t.TempDir()
	gcodeDir := t.TempDir()
	uploadsDir := t.TempDir()
	backupDir := t.TempDir()
	t.Setenv("THE_MOMENT_DB_PATH", dbDir)
	t.Setenv("THE_MOMENT_GCODE_PATH", gcodeDir)
	t.Setenv("THE_MOMENT_UPLOADS_PATH", uploadsDir)
	t.Setenv("BACKUP_DIR", backupDir)

	if err := os.WriteFile(filepath.Join(gcodeDir, "test.gcode"), []byte(";TIME:120"), 0644); err != nil {
		t.Fatal(err)
	}

	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("failed to create bridge: %v", err)
	}
	defer bridge.db.Close()

	filename, err := bridge.CreateBackup(BackupScopeAll)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	archivePath := filepath.Join(backupDir, filename)
	if !archiveContainsPath(t, archivePath, "db/the-moment.db") {
		t.Error("archive missing db/the-moment.db")
	}
	if !archiveContainsPath(t, archivePath, "gcode/test.gcode") {
		t.Error("archive missing gcode/test.gcode")
	}
}

func TestCreateBackup_InvalidScope(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("THE_MOMENT_DB_PATH", dbDir)

	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("failed to create bridge: %v", err)
	}
	defer bridge.db.Close()

	_, err = bridge.CreateBackup("invalid-scope")
	if err == nil {
		t.Fatal("expected error for invalid scope, got nil")
	}
}

func TestRestoreBackup_CleanReplace(t *testing.T) {
	dbDir := t.TempDir()
	gcodeDir := t.TempDir()
	uploadsDir := t.TempDir()
	backupDir := t.TempDir()
	t.Setenv("THE_MOMENT_DB_PATH", dbDir)
	t.Setenv("THE_MOMENT_GCODE_PATH", gcodeDir)
	t.Setenv("THE_MOMENT_UPLOADS_PATH", uploadsDir)
	t.Setenv("BACKUP_DIR", backupDir)

	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("failed to create bridge: %v", err)
	}
	defer bridge.db.Close()

	// Create a file that should be preserved in the backup
	keepFile := filepath.Join(gcodeDir, "keep.gcode")
	if err := os.WriteFile(keepFile, []byte(";TIME:60"), 0644); err != nil {
		t.Fatal(err)
	}

	filename, err := bridge.CreateBackup(BackupScopeGcode)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	// Add a zombie file after the backup — it must not survive the restore
	zombieFile := filepath.Join(gcodeDir, "zombie.gcode")
	if err := os.WriteFile(zombieFile, []byte("zombie"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := bridge.RestoreBackup(filename); err != nil {
		t.Fatalf("RestoreBackup failed: %v", err)
	}

	if _, err := os.Stat(zombieFile); !os.IsNotExist(err) {
		t.Error("zombie file still exists after restore — clean replace did not work")
	}
	if _, err := os.Stat(keepFile); os.IsNotExist(err) {
		t.Error("original file missing after restore")
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func archiveContainsPath(t *testing.T, archivePath, wantEntry string) bool {
	t.Helper()
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("failed to open archive: %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("failed to open gzip reader: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("error reading archive: %v", err)
		}
		if hdr.Name == wantEntry {
			return true
		}
	}
	return false
}
