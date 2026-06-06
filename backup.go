// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// BackupEntry describes a single backup archive in the backup store.
type BackupEntry struct {
	Filename  string    `json:"filename"`
	Scope     string    `json:"scope"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

// PreflightResult is the result of a pre-restore validation check.
type PreflightResult struct {
	Scope             string   `json:"scope"`
	UncompressedBytes int64    `json:"uncompressed_bytes"`
	AvailableBytes    int64    `json:"available_bytes"`
	WillReplace       []string `json:"will_replace"`
	SpaceOK           bool     `json:"space_ok"`
	Valid             bool     `json:"valid"`
	Message           string   `json:"message"`
}

var backupFilenameRE = regexp.MustCompile(`^the-moment-backup-\d{8}-\d{6}-(all|db|gcode|uploads)\.tar\.gz$`)

func validBackupFilename(name string) bool {
	return backupFilenameRE.MatchString(filepath.Base(name))
}

func parseScopeFromFilename(filename string) string {
	name := strings.TrimSuffix(filepath.Base(filename), ".tar.gz")
	parts := strings.Split(name, "-")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "unknown"
}

// CreateBackup creates a new .tar.gz archive in BACKUP_DIR covering the given scope.
// Uses VACUUM INTO for a consistent database snapshot without stopping the service.
func (b *FilamentBridge) CreateBackup(scope string) (string, error) {
	switch scope {
	case BackupScopeAll, BackupScopeDB, BackupScopeGcode, BackupScopeUploads:
	default:
		return "", fmt.Errorf("invalid scope %q: must be all, db, gcode, or uploads", scope)
	}

	backupDir := getBackupDir()
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	ts := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("the-moment-backup-%s-%s.tar.gz", ts, scope)
	destPath := filepath.Join(backupDir, filename)

	f, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create backup file: %w", err)
	}

	writeErr := func() error {
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)

		if scope == BackupScopeAll || scope == BackupScopeDB {
			snapshotPath := filepath.Join(os.TempDir(), fmt.Sprintf("the-moment-snap-%s.db", ts))
			if err := b.createDatabaseSnapshot(snapshotPath); err != nil {
				return fmt.Errorf("database snapshot failed: %w", err)
			}
			defer os.Remove(snapshotPath)

			if err := addFileToTar(tw, snapshotPath, "db/"+DefaultDBFileName); err != nil {
				return fmt.Errorf("failed to add database to archive: %w", err)
			}
		}

		if scope == BackupScopeAll || scope == BackupScopeGcode {
			if err := addDirToTar(tw, getGcodePath(), "gcode"); err != nil {
				return fmt.Errorf("failed to add gcode directory to archive: %w", err)
			}
		}

		if scope == BackupScopeAll || scope == BackupScopeUploads {
			if err := addDirToTar(tw, getUploadsPath(), "uploads"); err != nil {
				return fmt.Errorf("failed to add uploads directory to archive: %w", err)
			}
		}

		if err := tw.Close(); err != nil {
			return fmt.Errorf("failed to finalize tar: %w", err)
		}
		return gz.Close()
	}()

	f.Close()

	if writeErr != nil {
		os.Remove(destPath)
		return "", writeErr
	}

	_ = b.SetConfigValue(ConfigKeyLastBackupTime, time.Now().UTC().Format(time.RFC3339))
	return filename, nil
}

// createDatabaseSnapshot writes a consistent copy of the database to destPath using VACUUM INTO.
func (b *FilamentBridge) createDatabaseSnapshot(destPath string) error {
	_, err := b.db.Exec("VACUUM INTO ?", destPath)
	return err
}

// ListBackups returns all backup archives found in BACKUP_DIR, newest first.
func ListBackups() ([]BackupEntry, error) {
	backupDir := getBackupDir()
	entries, err := os.ReadDir(backupDir)
	if os.IsNotExist(err) {
		return []BackupEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	var backups []BackupEntry
	for _, entry := range entries {
		if entry.IsDir() || !validBackupFilename(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		backups = append(backups, BackupEntry{
			Filename:  entry.Name(),
			Scope:     parseScopeFromFilename(entry.Name()),
			SizeBytes: info.Size(),
			CreatedAt: info.ModTime(),
		})
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})
	return backups, nil
}

// DeleteBackup removes a backup archive by filename. Rejects path traversal attempts.
func DeleteBackup(filename string) error {
	if !validBackupFilename(filename) {
		return fmt.Errorf("invalid backup filename")
	}
	path := filepath.Join(getBackupDir(), filepath.Base(filename))
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("backup not found")
		}
		return fmt.Errorf("failed to delete backup: %w", err)
	}
	return nil
}

// PreflightRestore validates a backup archive and checks whether sufficient disk space exists.
// Returns a PreflightResult describing what will be replaced and whether the restore is safe to proceed.
func PreflightRestore(filename string) (PreflightResult, error) {
	if !validBackupFilename(filename) {
		return PreflightResult{Valid: false, Message: "invalid backup filename"}, nil
	}
	path := filepath.Join(getBackupDir(), filepath.Base(filename))

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PreflightResult{Valid: false, Message: "backup file not found"}, nil
		}
		return PreflightResult{}, fmt.Errorf("failed to open backup: %w", err)
	}
	defer f.Close()

	scope := parseScopeFromFilename(filename)

	gr, err := gzip.NewReader(f)
	if err != nil {
		return PreflightResult{Valid: false, Message: "not a valid gzip archive"}, nil
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	var uncompressedBytes int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return PreflightResult{Valid: false, Message: "archive is corrupt or truncated"}, nil
		}
		uncompressedBytes += hdr.Size
	}

	var willReplace []string
	if scope == BackupScopeAll || scope == BackupScopeDB {
		willReplace = append(willReplace, fmt.Sprintf("Database (%s)", getDBFilePath()))
	}
	if scope == BackupScopeAll || scope == BackupScopeGcode {
		willReplace = append(willReplace, fmt.Sprintf("G-code files (%s)", getGcodePath()))
	}
	if scope == BackupScopeAll || scope == BackupScopeUploads {
		willReplace = append(willReplace, fmt.Sprintf("Uploads (%s)", getUploadsPath()))
	}

	available, _ := availableDiskSpace(getBackupDir())
	needed := uncompressedBytes * 2
	spaceOK := available < 0 || available >= needed

	msg := ""
	if !spaceOK {
		msg = fmt.Sprintf("insufficient disk space: need %s, have %s",
			formatBytes(needed), formatBytes(available))
	}

	return PreflightResult{
		Scope:             scope,
		UncompressedBytes: uncompressedBytes,
		AvailableBytes:    available,
		WillReplace:       willReplace,
		SpaceOK:           spaceOK,
		Valid:             true,
		Message:           msg,
	}, nil
}

// clearDirContents removes all entries inside dir without removing dir itself.
// This is required for Docker bind-mount points: os.RemoveAll on the mount point
// returns EBUSY on Linux even though deleting its contents succeeds.
func clearDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(dir, 0755)
		}
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// RestoreBackup performs a files-only restore: wipes each target directory and extracts the archive.
// The running process continues on pre-restore data until restarted. After this call returns,
// restart the service (Docker: make down && make up; native: stop and restart the binary).
func (b *FilamentBridge) RestoreBackup(filename string) error {
	result, err := PreflightRestore(filename)
	if err != nil {
		return err
	}
	if !result.Valid {
		return fmt.Errorf("backup validation failed: %s", result.Message)
	}
	if !result.SpaceOK {
		return fmt.Errorf("restore aborted: %s", result.Message)
	}

	scope := parseScopeFromFilename(filename)

	// Wipe target directory contents before extraction to avoid zombie files.
	// clearDirContents is used instead of RemoveAll because these paths are Docker
	// bind-mount points; removing the mount point directory itself returns EBUSY on Linux.
	if scope == BackupScopeAll || scope == BackupScopeDB {
		if err := clearDirContents(filepath.Dir(getDBFilePath())); err != nil {
			return fmt.Errorf("failed to clear DB directory: %w", err)
		}
	}
	if scope == BackupScopeAll || scope == BackupScopeGcode {
		if err := clearDirContents(getGcodePath()); err != nil {
			return fmt.Errorf("failed to clear gcode directory: %w", err)
		}
	}
	if scope == BackupScopeAll || scope == BackupScopeUploads {
		if err := clearDirContents(getUploadsPath()); err != nil {
			return fmt.Errorf("failed to clear uploads directory: %w", err)
		}
	}

	path := filepath.Join(getBackupDir(), filepath.Base(filename))
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open backup: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to open gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading archive: %w", err)
		}

		destFile, err := archivePathToFS(hdr.Name)
		if err != nil {
			// Unrecognised or root-level entry — skip
			continue
		}

		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(destFile, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", destFile, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destFile), 0755); err != nil {
			return fmt.Errorf("failed to create parent for %s: %w", destFile, err)
		}

		out, err := os.Create(destFile)
		if err != nil {
			return fmt.Errorf("failed to create %s: %w", destFile, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return fmt.Errorf("failed to write %s: %w", destFile, err)
		}
		out.Close()
	}

	_ = b.SetConfigValue(ConfigKeyRestorePending, "true")
	return nil
}

// archivePathToFS maps an archive entry path (e.g. "gcode/subdir/file.gcode")
// to an absolute filesystem path, rejecting path traversal attempts.
func archivePathToFS(archivePath string) (string, error) {
	parts := strings.SplitN(filepath.ToSlash(archivePath), "/", 2)
	if len(parts) < 2 || parts[1] == "" {
		return "", fmt.Errorf("root entry or empty path, skip")
	}
	switch parts[0] {
	case "db":
		return safeJoin(filepath.Dir(getDBFilePath()), parts[1])
	case "gcode":
		return safeJoin(getGcodePath(), parts[1])
	case "uploads":
		return safeJoin(getUploadsPath(), parts[1])
	default:
		return "", fmt.Errorf("unknown archive prefix %q", parts[0])
	}
}

// safeJoin joins base and rel, returning an error if the result would escape base.
func safeJoin(base, rel string) (string, error) {
	candidate := filepath.Join(base, rel)
	base = filepath.Clean(base)
	if candidate != base && !strings.HasPrefix(candidate, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("path traversal detected in %q", rel)
	}
	return candidate, nil
}

// addFileToTar writes a single file into the archive under the given archive name.
func addFileToTar(tw *tar.Writer, srcPath, archiveName string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = archiveName

	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// addDirToTar walks srcDir and writes all contents into the archive under archivePrefix/.
// Missing or empty directories are silently skipped.
func addDirToTar(tw *tar.Writer, srcDir, archivePrefix string) error {
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return nil
	}
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		var archiveName string
		if rel == "." {
			archiveName = archivePrefix + "/"
		} else {
			archiveName = archivePrefix + "/" + filepath.ToSlash(rel)
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = archiveName

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

// formatBytes formats a byte count as a human-readable string (e.g. "1.4 GB").
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
