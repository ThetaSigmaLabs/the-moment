// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidBackupFilename(t *testing.T) {
	valid := []string{
		"the-moment-backup-20260604-120000-db.tar.gz",
		"the-moment-backup-20260604-120000-all.tar.gz",
		"the-moment-backup-20260604-120000-gcode.tar.gz",
		"the-moment-backup-20260604-120000-uploads.tar.gz",
	}
	for _, name := range valid {
		if !validBackupFilename(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}

	invalid := []string{
		"the-moment-backup-20260604-120000-db.tar.gz.exe",
		"../../../etc/passwd",
		"the-moment-backup-2026-db.tar.gz",
		"backup-20260604-120000-db.tar.gz",
		"",
	}
	for _, name := range invalid {
		if validBackupFilename(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestParseScopeFromFilename(t *testing.T) {
	cases := map[string]string{
		"the-moment-backup-20260604-120000-db.tar.gz":      "db",
		"the-moment-backup-20260604-120000-all.tar.gz":     "all",
		"the-moment-backup-20260604-120000-gcode.tar.gz":   "gcode",
		"the-moment-backup-20260604-120000-uploads.tar.gz": "uploads",
	}
	for filename, wantScope := range cases {
		got := parseScopeFromFilename(filename)
		if got != wantScope {
			t.Errorf("parseScopeFromFilename(%q) = %q, want %q", filename, got, wantScope)
		}
	}
}

func TestDeleteBackup_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BACKUP_DIR", dir)

	err := DeleteBackup("../../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal attempt, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected 'invalid' in error, got %q", err.Error())
	}
}

func TestDeleteBackup_NotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BACKUP_DIR", dir)

	err := DeleteBackup("the-moment-backup-20260604-120000-db.tar.gz")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got %q", err.Error())
	}
}

func TestListBackups_Empty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BACKUP_DIR", dir)

	entries, err := ListBackups()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestListBackups_MultipleEntries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BACKUP_DIR", dir)

	names := []string{
		"the-moment-backup-20260604-090000-db.tar.gz",
		"the-moment-backup-20260604-120000-all.tar.gz",
		"ignored-file.txt",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := ListBackups()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (ignored .txt), got %d", len(entries))
	}
	scopes := map[string]bool{}
	for _, e := range entries {
		scopes[e.Scope] = true
	}
	if !scopes["db"] || !scopes["all"] {
		t.Errorf("unexpected scopes: %v", scopes)
	}
}

func TestSafeJoin_PathTraversal(t *testing.T) {
	base := "/data/gcode"
	dangerous := []string{
		"../etc/passwd",
		"../../root/.ssh/authorized_keys",
	}
	for _, rel := range dangerous {
		result, err := safeJoin(base, rel)
		if err == nil {
			t.Errorf("safeJoin(%q, %q) should have failed, got %q", base, rel, result)
		}
	}

	result, err := safeJoin(base, "subdir/file.gcode")
	if err != nil {
		t.Errorf("safeJoin safe path unexpected error: %v", err)
	}
	if result != filepath.Join(base, "subdir/file.gcode") {
		t.Errorf("safeJoin result %q, want %q", result, filepath.Join(base, "subdir/file.gcode"))
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, c := range cases {
		got := formatBytes(c.input)
		if got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestPreflightRestore_InvalidFilename(t *testing.T) {
	result, err := PreflightRestore("../../etc/passwd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Valid {
		t.Error("expected Valid=false for path traversal filename")
	}
}

func TestPreflightRestore_NotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BACKUP_DIR", dir)

	result, err := PreflightRestore("the-moment-backup-20260604-120000-db.tar.gz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Valid {
		t.Error("expected Valid=false for missing file")
	}
	if !strings.Contains(result.Message, "not found") {
		t.Errorf("expected 'not found' in message, got %q", result.Message)
	}
}

func TestPreflightRestore_ValidArchive(t *testing.T) {
	dir := t.TempDir()
	backupDir := t.TempDir()
	t.Setenv("BACKUP_DIR", backupDir)
	t.Setenv("THE_MOMENT_DB_PATH", dir)

	filename := "the-moment-backup-20260604-120000-db.tar.gz"
	if err := writeMinimalBackupArchive(filepath.Join(backupDir, filename), "db/the-moment.db", []byte("SQLITE")); err != nil {
		t.Fatal(err)
	}

	result, err := PreflightRestore(filename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Valid {
		t.Errorf("expected Valid=true, message: %s", result.Message)
	}
	if result.Scope != "db" {
		t.Errorf("expected scope=db, got %q", result.Scope)
	}
	if len(result.WillReplace) == 0 {
		t.Error("expected WillReplace to be non-empty for db scope")
	}
}

func TestPreflightRestore_CorruptArchive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BACKUP_DIR", dir)

	filename := "the-moment-backup-20260604-120000-db.tar.gz"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("not a real archive"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := PreflightRestore(filename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Valid {
		t.Error("expected Valid=false for corrupt archive")
	}
}

// ── Shared test helpers ───────────────────────────────────────────────────────

func writeMinimalBackupArchive(destPath, entryName string, content []byte) error {
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: entryName, Size: int64(len(content)), Mode: 0644}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(content); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
