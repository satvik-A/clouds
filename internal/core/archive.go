// Package core provides the Archive Manager for CloudFS.
// Based on design.txt Section 17: Cold data archival.
//
// INVARIANTS:
// - Archive is EXPLICIT USER ACTION (no auto-archival)
// - Format: 7z (compression) + PAR2 (error correction)
// - Archive is immutable once created
// - Original version remains recoverable
// - Archive placement tracked in index
// - Failure must NOT delete originals
// - Restore possible without CloudFS installed
package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ArchiveManager manages cold data archival.
type ArchiveManager struct {
	db         *sql.DB
	journal    *JournalManager
	cacheDir   string
	archiveDir string
	mu         sync.RWMutex
}

// NewArchiveManager creates a new archive manager.
func NewArchiveManager(db *sql.DB, journal *JournalManager, cacheDir, archiveDir string) (*ArchiveManager, error) {
	if err := os.MkdirAll(archiveDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create archive directory: %w", err)
	}

	return &ArchiveManager{
		db:         db,
		journal:    journal,
		cacheDir:   cacheDir,
		archiveDir: archiveDir,
	}, nil
}

// ArchiveInfo contains information about an archive.
type ArchiveInfo struct {
	EntryID       int64
	EntryName     string
	ArchivePath   string
	Par2Path      string
	OriginalSize  int64
	ArchiveSize   int64
	CreatedAt     time.Time
	ContentHash   string
	RecoveryLevel int // PAR2 redundancy percentage
}

// ArchivePreview shows what would be archived.
type ArchivePreview struct {
	EntryName       string
	OriginalSize    int64
	EstimatedSize   int64 // Rough estimate of compressed size
	ArchivePath     string
	Par2Path        string
	RecoveryLevel   int
	RequiredTools   []string
	ToolsAvailable  map[string]bool
}

// CheckTools verifies required tools are available.
func (am *ArchiveManager) CheckTools() map[string]bool {
	tools := map[string]bool{
		"7z":   false,
		"par2": false,
	}

	if _, err := exec.LookPath("7z"); err == nil {
		tools["7z"] = true
	}
	if _, err := exec.LookPath("par2"); err == nil {
		tools["par2"] = true
	}

	return tools
}

// GetArchivePreview returns a dry-run preview of archive creation.
func (am *ArchiveManager) GetArchivePreview(ctx context.Context, entryID int64) (*ArchivePreview, error) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	// Get entry info
	var entryName string
	var logicalSize int64
	err := am.db.QueryRowContext(ctx, `
		SELECT name, logical_size FROM entries WHERE id = ?
	`, entryID).Scan(&entryName, &logicalSize)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entry not found: %d", entryID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get entry: %w", err)
	}

	// Check tools
	tools := am.CheckTools()

	// Generate archive paths
	archiveName := fmt.Sprintf("archive_%d_%s.7z", entryID, time.Now().Format("20060102"))
	archivePath := filepath.Join(am.archiveDir, archiveName)
	par2Path := archivePath + ".par2"

	// Estimate compressed size (rough: 60% compression for typical data)
	estimatedSize := int64(float64(logicalSize) * 0.6)
	if estimatedSize < 1024 {
		estimatedSize = logicalSize // Don't estimate smaller than original for tiny files
	}

	return &ArchivePreview{
		EntryName:      entryName,
		OriginalSize:   logicalSize,
		EstimatedSize:  estimatedSize,
		ArchivePath:    archivePath,
		Par2Path:       par2Path,
		RecoveryLevel:  10, // Default 10% redundancy
		RequiredTools:  []string{"7z", "par2"},
		ToolsAvailable: tools,
	}, nil
}

// GetArchivePreviewByPath returns preview by entry path.
func (am *ArchiveManager) GetArchivePreviewByPath(ctx context.Context, path string) (*ArchivePreview, error) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	var entryID int64
	err := am.db.QueryRowContext(ctx, `SELECT id FROM entries WHERE name = ?`, path).Scan(&entryID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entry not found: %s", path)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find entry: %w", err)
	}

	am.mu.RUnlock()
	defer am.mu.RLock()
	return am.GetArchivePreview(ctx, entryID)
}

// CreateArchive creates a cold archive of an entry.
// This is an EXPLICIT USER ACTION, never called automatically.
func (am *ArchiveManager) CreateArchive(ctx context.Context, entryID int64, sourcePath string, recoveryLevel int) (*ArchiveInfo, error) {
	am.mu.Lock()
	defer am.mu.Unlock()

	// Check tools first
	tools := am.CheckTools()
	if !tools["7z"] {
		return nil, fmt.Errorf("7z not found - install with: brew install p7zip")
	}
	if !tools["par2"] {
		return nil, fmt.Errorf("par2 not found - install with: brew install par2")
	}

	// Get entry info
	var entryName string
	var logicalSize int64
	err := am.db.QueryRowContext(ctx, `
		SELECT name, logical_size FROM entries WHERE id = ?
	`, entryID).Scan(&entryName, &logicalSize)
	if err != nil {
		return nil, fmt.Errorf("failed to get entry: %w", err)
	}

	// Begin journal operation
	payload, _ := json.Marshal(map[string]interface{}{
		"entry_id":       entryID,
		"source_path":    sourcePath,
		"recovery_level": recoveryLevel,
	})
	opID, err := am.journal.BeginOperation(ctx, "archive_create", string(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to begin journal: %w", err)
	}

	// Verify source exists
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		am.journal.RollbackOperation(ctx, opID, "source not found")
		return nil, fmt.Errorf("source file not found: %w", err)
	}

	// Calculate content hash before archiving
	contentHash, err := calculateFileHash(sourcePath)
	if err != nil {
		am.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to hash source: %w", err)
	}

	// Generate archive paths
	timestamp := time.Now().Format("20060102_150405")
	archiveName := fmt.Sprintf("archive_%d_%s.7z", entryID, timestamp)
	archivePath := filepath.Join(am.archiveDir, archiveName)

	// Step 1: Create 7z archive
	cmd := exec.CommandContext(ctx, "7z", "a", "-t7z", "-mx=9", archivePath, sourcePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		am.journal.RollbackOperation(ctx, opID, fmt.Sprintf("7z failed: %s", string(output)))
		return nil, fmt.Errorf("7z compression failed: %w - %s", err, string(output))
	}

	// Verify archive was created
	archiveInfo, err := os.Stat(archivePath)
	if err != nil {
		am.journal.RollbackOperation(ctx, opID, "archive not created")
		return nil, fmt.Errorf("archive file not created: %w", err)
	}

	// Step 2: Create PAR2 recovery data
	par2Path := archivePath + ".par2"
	cmd = exec.CommandContext(ctx, "par2", "create",
		fmt.Sprintf("-r%d", recoveryLevel),
		"-n1", // Single recovery file
		par2Path,
		archivePath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		// Clean up archive on PAR2 failure
		os.Remove(archivePath)
		am.journal.RollbackOperation(ctx, opID, fmt.Sprintf("par2 failed: %s", string(output)))
		return nil, fmt.Errorf("PAR2 creation failed: %w - %s", err, string(output))
	}

	// Record in database
	_, err = am.db.ExecContext(ctx, `
		INSERT INTO archives (entry_id, archive_path, par2_path, original_size, archive_size, 
		                     content_hash, recovery_level, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
	`, entryID, archivePath, par2Path, sourceInfo.Size(), archiveInfo.Size(), contentHash, recoveryLevel)
	if err != nil {
		// Clean up files on DB failure
		os.Remove(archivePath)
		os.Remove(par2Path)
		// Remove any additional PAR2 files (par2 may create multiple)
		matches, _ := filepath.Glob(archivePath + ".vol*")
		for _, m := range matches {
			os.Remove(m)
		}
		am.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to record archive: %w", err)
	}

	// Complete journal
	am.journal.CommitOperation(ctx, opID)
	am.journal.SyncOperation(ctx, opID)

	return &ArchiveInfo{
		EntryID:       entryID,
		EntryName:     entryName,
		ArchivePath:   archivePath,
		Par2Path:      par2Path,
		OriginalSize:  sourceInfo.Size(),
		ArchiveSize:   archiveInfo.Size(),
		CreatedAt:     time.Now(),
		ContentHash:   contentHash,
		RecoveryLevel: recoveryLevel,
	}, nil
}

// calculateFileHash computes SHA256 hash of a file.
func calculateFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// InspectArchive returns information about an existing archive.
func (am *ArchiveManager) InspectArchive(ctx context.Context, entryID int64) (*ArchiveInfo, error) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	var info ArchiveInfo
	var createdAt string
	err := am.db.QueryRowContext(ctx, `
		SELECT a.entry_id, e.name, a.archive_path, a.par2_path, 
		       a.original_size, a.archive_size, a.content_hash, 
		       a.recovery_level, a.created_at
		FROM archives a
		JOIN entries e ON a.entry_id = e.id
		WHERE a.entry_id = ?
		ORDER BY a.created_at DESC
		LIMIT 1
	`, entryID).Scan(
		&info.EntryID, &info.EntryName, &info.ArchivePath, &info.Par2Path,
		&info.OriginalSize, &info.ArchiveSize, &info.ContentHash,
		&info.RecoveryLevel, &createdAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no archive found for entry %d", entryID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get archive info: %w", err)
	}

	info.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

	// Verify files still exist
	if _, err := os.Stat(info.ArchivePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("archive file missing: %s", info.ArchivePath)
	}

	return &info, nil
}

// InspectArchiveByPath returns archive info by entry path.
func (am *ArchiveManager) InspectArchiveByPath(ctx context.Context, path string) (*ArchiveInfo, error) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	var entryID int64
	err := am.db.QueryRowContext(ctx, `SELECT id FROM entries WHERE name = ?`, path).Scan(&entryID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entry not found: %s", path)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find entry: %w", err)
	}

	am.mu.RUnlock()
	defer am.mu.RLock()
	return am.InspectArchive(ctx, entryID)
}

// ListArchives returns all archives.
func (am *ArchiveManager) ListArchives(ctx context.Context) ([]*ArchiveInfo, error) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	rows, err := am.db.QueryContext(ctx, `
		SELECT a.entry_id, e.name, a.archive_path, a.par2_path,
		       a.original_size, a.archive_size, a.content_hash,
		       a.recovery_level, a.created_at
		FROM archives a
		JOIN entries e ON a.entry_id = e.id
		ORDER BY a.created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list archives: %w", err)
	}
	defer rows.Close()

	var archives []*ArchiveInfo
	for rows.Next() {
		var info ArchiveInfo
		var createdAt string
		err := rows.Scan(
			&info.EntryID, &info.EntryName, &info.ArchivePath, &info.Par2Path,
			&info.OriginalSize, &info.ArchiveSize, &info.ContentHash,
			&info.RecoveryLevel, &createdAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan archive: %w", err)
		}
		info.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		archives = append(archives, &info)
	}

	return archives, nil
}

// VerifyArchive verifies archive integrity using PAR2.
func (am *ArchiveManager) VerifyArchive(ctx context.Context, entryID int64) error {
	am.mu.RLock()
	defer am.mu.RUnlock()

	info, err := am.InspectArchive(ctx, entryID)
	if err != nil {
		return err
	}

	// Use PAR2 to verify
	cmd := exec.CommandContext(ctx, "par2", "verify", info.Par2Path)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("archive verification failed: %w - %s", err, string(output))
	}

	return nil
}

// GetEntryIDByPath helper to get entry ID from path.
func (am *ArchiveManager) GetEntryIDByPath(ctx context.Context, path string) (int64, error) {
	var entryID int64
	err := am.db.QueryRowContext(ctx, `SELECT id FROM entries WHERE name = ?`, path).Scan(&entryID)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("entry not found: %s", path)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to find entry: %w", err)
	}
	return entryID, nil
}

// ArchiveRestorePreview shows what would be restored from an archive.
type ArchiveRestorePreview struct {
	EntryName     string
	ArchivePath   string
	OriginalSize  int64
	ContentHash   string
	RestorePath   string
	ArchiveExists bool
	Par2Exists    bool
}

// GetRestorePreview returns a dry-run preview of archive restoration.
func (am *ArchiveManager) GetRestorePreview(ctx context.Context, entryID int64) (*ArchiveRestorePreview, error) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	info, err := am.InspectArchive(ctx, entryID)
	if err != nil {
		return nil, err
	}

	// Check if files exist
	archiveExists := true
	if _, err := os.Stat(info.ArchivePath); os.IsNotExist(err) {
		archiveExists = false
	}

	par2Exists := true
	if _, err := os.Stat(info.Par2Path); os.IsNotExist(err) {
		par2Exists = false
	}

	// Restore path in cache
	restorePath := filepath.Join(am.cacheDir, "entries", fmt.Sprintf("%d", entryID), info.EntryName)

	return &ArchiveRestorePreview{
		EntryName:     info.EntryName,
		ArchivePath:   info.ArchivePath,
		OriginalSize:  info.OriginalSize,
		ContentHash:   info.ContentHash,
		RestorePath:   restorePath,
		ArchiveExists: archiveExists,
		Par2Exists:    par2Exists,
	}, nil
}

// RestoreArchive extracts an archive to the cache and verifies hashes.
// Original archive is PRESERVED (never deleted).
func (am *ArchiveManager) RestoreArchive(ctx context.Context, entryID int64) (string, error) {
	am.mu.Lock()
	defer am.mu.Unlock()

	// Check tools
	tools := am.CheckTools()
	if !tools["7z"] {
		return "", fmt.Errorf("7z not found - install with: brew install p7zip")
	}

	// Get archive info
	info, err := am.InspectArchive(ctx, entryID)
	if err != nil {
		return "", err
	}

	// Verify archive exists
	if _, err := os.Stat(info.ArchivePath); os.IsNotExist(err) {
		return "", fmt.Errorf("archive file missing: %s", info.ArchivePath)
	}

	// Begin journal operation
	payload, _ := json.Marshal(map[string]interface{}{
		"entry_id":     entryID,
		"archive_path": info.ArchivePath,
	})
	opID, err := am.journal.BeginOperation(ctx, "archive_restore", string(payload))
	if err != nil {
		return "", fmt.Errorf("failed to begin journal: %w", err)
	}

	// Create restore directory
	restoreDir := filepath.Join(am.cacheDir, "entries", fmt.Sprintf("%d", entryID))
	if err := os.MkdirAll(restoreDir, 0700); err != nil {
		am.journal.RollbackOperation(ctx, opID, err.Error())
		return "", fmt.Errorf("failed to create restore directory: %w", err)
	}

	// Step 1: Verify archive integrity with PAR2 (if available)
	if tools["par2"] {
		if _, err := os.Stat(info.Par2Path); err == nil {
			cmd := exec.CommandContext(ctx, "par2", "verify", info.Par2Path)
			if output, err := cmd.CombinedOutput(); err != nil {
				// Try to repair
				cmd = exec.CommandContext(ctx, "par2", "repair", info.Par2Path)
				if repairOutput, repairErr := cmd.CombinedOutput(); repairErr != nil {
					am.journal.RollbackOperation(ctx, opID, fmt.Sprintf("archive corrupt and repair failed: %s", string(repairOutput)))
					return "", fmt.Errorf("archive corrupt and repair failed: %w - %s", repairErr, string(output))
				}
			}
		}
	}

	// Step 2: Extract archive to restore directory
	cmd := exec.CommandContext(ctx, "7z", "x", "-y", "-o"+restoreDir, info.ArchivePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		am.journal.RollbackOperation(ctx, opID, fmt.Sprintf("extraction failed: %s", string(output)))
		return "", fmt.Errorf("extraction failed: %w - %s", err, string(output))
	}

	// Step 3: Find the extracted file
	var extractedPath string
	err = filepath.Walk(restoreDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() && extractedPath == "" {
			extractedPath = path
		}
		return nil
	})
	if err != nil || extractedPath == "" {
		am.journal.RollbackOperation(ctx, opID, "no files extracted")
		return "", fmt.Errorf("no files extracted from archive")
	}

	// Step 4: Verify content hash
	actualHash, err := calculateFileHash(extractedPath)
	if err != nil {
		am.journal.RollbackOperation(ctx, opID, err.Error())
		return "", fmt.Errorf("failed to hash extracted file: %w", err)
	}

	if actualHash != info.ContentHash {
		// Hash mismatch - clean up and fail
		os.Remove(extractedPath)
		am.journal.RollbackOperation(ctx, opID, "hash mismatch")
		return "", fmt.Errorf("hash verification failed: expected %s, got %s", info.ContentHash[:16], actualHash[:16])
	}

	// Complete journal
	am.journal.CommitOperation(ctx, opID)
	am.journal.SyncOperation(ctx, opID)

	return extractedPath, nil
}

// Archive state constants
const (
	ArchiveStateActive   = "active"
	ArchiveStateVerified = "verified"
	ArchiveStateCorrupt  = "corrupt"
)

// Ensure archives table exists
func (am *ArchiveManager) EnsureSchema(ctx context.Context) error {
	_, err := am.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS archives (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			entry_id        INTEGER NOT NULL REFERENCES entries(id),
			archive_path    TEXT NOT NULL,
			par2_path       TEXT NOT NULL,
			original_size   INTEGER NOT NULL,
			archive_size    INTEGER NOT NULL,
			content_hash    TEXT NOT NULL,
			recovery_level  INTEGER NOT NULL DEFAULT 10,
			state           TEXT NOT NULL DEFAULT 'active',
			created_at      TEXT NOT NULL DEFAULT (datetime('now')),
			verified_at     TEXT
		)
	`)
	return err
}
