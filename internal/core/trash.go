// Package core provides the Trash Manager for CloudFS.
// Based on design.txt Section 15: Versioning & Trash.
//
// INVARIANTS:
// - rm moves entries to trash (NOT immediate deletion)
// - No immediate provider deletion
// - Provider Delete() ONLY on purge after user confirmation
// - Journal all trash operations
// - Respect repair boundaries
package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cloudfs/cloudfs/internal/model"
)

// TrashManager manages the trash system for safe deletion.
type TrashManager struct {
	db      *sql.DB
	journal *JournalManager
	mu      sync.RWMutex
}

// NewTrashManager creates a new trash manager.
func NewTrashManager(db *sql.DB, journal *JournalManager) *TrashManager {
	return &TrashManager{
		db:      db,
		journal: journal,
	}
}

// TrashInfo contains detailed trash entry information.
type TrashInfo struct {
	Entry       *model.TrashEntry
	OriginalName string
	Size        int64
	DaysInTrash int
}

// MoveToTrash moves an entry to trash.
// This does NOT delete provider data - only marks as deleted.
func (tm *TrashManager) MoveToTrash(ctx context.Context, entryID int64, originalPath string, autoPurgeDays int) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Begin journal operation
	payload, _ := json.Marshal(map[string]interface{}{
		"entry_id":      entryID,
		"original_path": originalPath,
	})
	opID, err := tm.journal.BeginOperation(ctx, "trash_move", string(payload))
	if err != nil {
		return fmt.Errorf("failed to begin journal: %w", err)
	}

	// Get active version ID
	var versionID sql.NullInt64
	err = tm.db.QueryRowContext(ctx, `
		SELECT id FROM versions WHERE entry_id = ? AND state = 'active'
	`, entryID).Scan(&versionID)
	if err != nil && err != sql.ErrNoRows {
		tm.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to get version: %w", err)
	}

	// Calculate auto-purge date if specified
	var autoPurgeAfter sql.NullString
	if autoPurgeDays > 0 {
		purgeDate := time.Now().AddDate(0, 0, autoPurgeDays)
		autoPurgeAfter = sql.NullString{String: purgeDate.Format(time.RFC3339), Valid: true}
	}

	// Insert into trash
	_, err = tm.db.ExecContext(ctx, `
		INSERT INTO trash (original_entry_id, original_path, version_id, auto_purge_after)
		VALUES (?, ?, ?, ?)
	`, entryID, originalPath, versionID, autoPurgeAfter)
	if err != nil {
		tm.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to add to trash: %w", err)
	}

	// Mark versions as deleted (but don't remove from DB)
	_, err = tm.db.ExecContext(ctx, `
		UPDATE versions SET state = 'deleted' WHERE entry_id = ?
	`, entryID)
	if err != nil {
		tm.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to mark versions deleted: %w", err)
	}

	// Complete journal
	tm.journal.CommitOperation(ctx, opID)
	tm.journal.SyncOperation(ctx, opID)

	return nil
}

// List returns all entries in trash.
func (tm *TrashManager) List(ctx context.Context) ([]*TrashInfo, error) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	rows, err := tm.db.QueryContext(ctx, `
		SELECT t.id, t.original_entry_id, t.original_path, t.deleted_at, t.version_id, t.auto_purge_after,
		       COALESCE(v.size, 0) as size
		FROM trash t
		LEFT JOIN versions v ON t.version_id = v.id
		ORDER BY t.deleted_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list trash: %w", err)
	}
	defer rows.Close()

	var items []*TrashInfo
	for rows.Next() {
		var entry model.TrashEntry
		var deletedAt string
		var versionID, size sql.NullInt64
		var autoPurge sql.NullString

		err := rows.Scan(
			&entry.ID, &entry.OriginalEntryID, &entry.OriginalPath, &deletedAt,
			&versionID, &autoPurge, &size,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan trash entry: %w", err)
		}

		entry.DeletedAt, _ = time.Parse(time.RFC3339, deletedAt)
		if versionID.Valid {
			entry.VersionID = &versionID.Int64
		}
		if autoPurge.Valid {
			t, _ := time.Parse(time.RFC3339, autoPurge.String)
			entry.AutoPurgeAfter = &t
		}

		daysInTrash := int(time.Since(entry.DeletedAt).Hours() / 24)

		items = append(items, &TrashInfo{
			Entry:        &entry,
			OriginalName: entry.OriginalPath,
			Size:         size.Int64,
			DaysInTrash:  daysInTrash,
		})
	}

	return items, nil
}

// Restore restores an entry from trash.
func (tm *TrashManager) Restore(ctx context.Context, trashID int64) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Get trash entry
	var entryID int64
	var versionID sql.NullInt64
	err := tm.db.QueryRowContext(ctx, `
		SELECT original_entry_id, version_id FROM trash WHERE id = ?
	`, trashID).Scan(&entryID, &versionID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("trash entry not found: %d", trashID)
	}
	if err != nil {
		return fmt.Errorf("failed to get trash entry: %w", err)
	}

	// Begin journal
	payload, _ := json.Marshal(map[string]interface{}{
		"trash_id": trashID,
		"entry_id": entryID,
	})
	opID, err := tm.journal.BeginOperation(ctx, "trash_restore", string(payload))
	if err != nil {
		return fmt.Errorf("failed to begin journal: %w", err)
	}

	// Restore version state to active
	if versionID.Valid {
		_, err = tm.db.ExecContext(ctx, `
			UPDATE versions SET state = 'active' WHERE id = ?
		`, versionID.Int64)
		if err != nil {
			tm.journal.RollbackOperation(ctx, opID, err.Error())
			return fmt.Errorf("failed to restore version: %w", err)
		}
	}

	// Remove from trash
	_, err = tm.db.ExecContext(ctx, `DELETE FROM trash WHERE id = ?`, trashID)
	if err != nil {
		tm.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to remove from trash: %w", err)
	}

	// Complete journal
	tm.journal.CommitOperation(ctx, opID)
	tm.journal.SyncOperation(ctx, opID)

	return nil
}

// RestoreByPath restores an entry by its original path.
func (tm *TrashManager) RestoreByPath(ctx context.Context, path string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	var trashID int64
	err := tm.db.QueryRowContext(ctx, `
		SELECT id FROM trash WHERE original_path = ?
	`, path).Scan(&trashID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("not found in trash: %s", path)
	}
	if err != nil {
		return fmt.Errorf("failed to find in trash: %w", err)
	}

	tm.mu.Unlock()
	defer tm.mu.Lock()
	return tm.Restore(ctx, trashID)
}

// PurgePreview returns what would be purged.
type PurgePreview struct {
	EntryCount int
	TotalSize  int64
	Entries    []string
}

// GetPurgePreview returns preview of what would be purged.
func (tm *TrashManager) GetPurgePreview(ctx context.Context, autoPurgeOnly bool) (*PurgePreview, error) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	var query string
	if autoPurgeOnly {
		query = `
			SELECT t.original_path, COALESCE(v.size, 0)
			FROM trash t
			LEFT JOIN versions v ON t.version_id = v.id
			WHERE t.auto_purge_after IS NOT NULL AND t.auto_purge_after <= datetime('now')
		`
	} else {
		query = `
			SELECT t.original_path, COALESCE(v.size, 0)
			FROM trash t
			LEFT JOIN versions v ON t.version_id = v.id
		`
	}

	rows, err := tm.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get purge preview: %w", err)
	}
	defer rows.Close()

	preview := &PurgePreview{}
	for rows.Next() {
		var path string
		var size int64
		rows.Scan(&path, &size)
		preview.Entries = append(preview.Entries, path)
		preview.TotalSize += size
		preview.EntryCount++
	}

	return preview, nil
}

// Purge permanently deletes entries from trash.
// This is the ONLY place where provider Delete() should be called.
// Requires explicit user confirmation.
func (tm *TrashManager) Purge(ctx context.Context, trashID int64, confirmed bool) error {
	if !confirmed {
		return fmt.Errorf("purge requires explicit user confirmation")
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Get trash entry info for journal
	var entryID int64
	var originalPath string
	err := tm.db.QueryRowContext(ctx, `
		SELECT original_entry_id, original_path FROM trash WHERE id = ?
	`, trashID).Scan(&entryID, &originalPath)
	if err == sql.ErrNoRows {
		return fmt.Errorf("trash entry not found: %d", trashID)
	}
	if err != nil {
		return fmt.Errorf("failed to get trash entry: %w", err)
	}

	// Begin journal
	payload, _ := json.Marshal(map[string]interface{}{
		"trash_id":      trashID,
		"entry_id":      entryID,
		"original_path": originalPath,
	})
	opID, err := tm.journal.BeginOperation(ctx, "trash_purge", string(payload))
	if err != nil {
		return fmt.Errorf("failed to begin journal: %w", err)
	}

	// Delete from trash
	_, err = tm.db.ExecContext(ctx, `DELETE FROM trash WHERE id = ?`, trashID)
	if err != nil {
		tm.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to purge from trash: %w", err)
	}

	// Delete from entries (cleanup zombie)
	_, err = tm.db.ExecContext(ctx, `DELETE FROM entries WHERE id = ?`, entryID)
	if err != nil {
		// Log warning but don't fail, as trash is already gone
		fmt.Printf("Warning: failed to delete entry %d: %v\n", entryID, err)
	}

	// Note: Actual provider deletion would happen here via provider.Delete()
	// This is the ONLY valid place for that call per design.txt

	// Complete journal
	tm.journal.CommitOperation(ctx, opID)
	tm.journal.SyncOperation(ctx, opID)

	return nil
}

// PurgeAll permanently deletes all entries from trash.
// Requires explicit user confirmation.
func (tm *TrashManager) PurgeAll(ctx context.Context, confirmed bool) (int, error) {
	if !confirmed {
		return 0, fmt.Errorf("purge requires explicit user confirmation")
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Get all trash IDs
	rows, err := tm.db.QueryContext(ctx, `SELECT id FROM trash`)
	if err != nil {
		return 0, fmt.Errorf("failed to list trash: %w", err)
	}
	defer rows.Close()

	var trashIDs []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		trashIDs = append(trashIDs, id)
	}

	// Begin journal for batch purge
	payload, _ := json.Marshal(map[string]interface{}{
		"count": len(trashIDs),
	})
	opID, err := tm.journal.BeginOperation(ctx, "trash_purge_all", string(payload))
	if err != nil {
		return 0, fmt.Errorf("failed to begin journal: %w", err)
	}

	// Delete all from entries (referenced by trash)
	_, err = tm.db.ExecContext(ctx, `DELETE FROM entries WHERE id IN (SELECT original_entry_id FROM trash)`)
	if err != nil {
		tm.journal.RollbackOperation(ctx, opID, err.Error())
		return 0, fmt.Errorf("failed to purge entries: %w", err)
	}

	// Delete all from trash
	result, err := tm.db.ExecContext(ctx, `DELETE FROM trash`)
	if err != nil {
		tm.journal.RollbackOperation(ctx, opID, err.Error())
		return 0, fmt.Errorf("failed to purge all: %w", err)
	}

	count, _ := result.RowsAffected()

	// Complete journal
	tm.journal.CommitOperation(ctx, opID)
	tm.journal.SyncOperation(ctx, opID)

	return int(count), nil
}

// PurgeExpired purges entries past their auto-purge date.
// Requires explicit user confirmation.
func (tm *TrashManager) PurgeExpired(ctx context.Context, confirmed bool) (int, error) {
	if !confirmed {
		return 0, fmt.Errorf("purge requires explicit user confirmation")
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Begin journal
	opID, err := tm.journal.BeginOperation(ctx, "trash_purge_expired", "{}")
	if err != nil {
		return 0, fmt.Errorf("failed to begin journal: %w", err)
	}

	// Delete expired entries
	result, err := tm.db.ExecContext(ctx, `
		DELETE FROM trash WHERE auto_purge_after IS NOT NULL AND auto_purge_after <= datetime('now')
	`)
	if err != nil {
		tm.journal.RollbackOperation(ctx, opID, err.Error())
		return 0, fmt.Errorf("failed to purge expired: %w", err)
	}

	count, _ := result.RowsAffected()

	// Complete journal
	tm.journal.CommitOperation(ctx, opID)
	tm.journal.SyncOperation(ctx, opID)

	return int(count), nil
}

// GetByPath finds a trash entry by its original path.
func (tm *TrashManager) GetByPath(ctx context.Context, path string) (*model.TrashEntry, error) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	var entry model.TrashEntry
	var deletedAt string
	var versionID sql.NullInt64
	var autoPurge sql.NullString

	err := tm.db.QueryRowContext(ctx, `
		SELECT id, original_entry_id, original_path, deleted_at, version_id, auto_purge_after
		FROM trash WHERE original_path = ?
	`, path).Scan(&entry.ID, &entry.OriginalEntryID, &entry.OriginalPath, &deletedAt, &versionID, &autoPurge)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get trash entry: %w", err)
	}

	entry.DeletedAt, _ = time.Parse(time.RFC3339, deletedAt)
	if versionID.Valid {
		entry.VersionID = &versionID.Int64
	}
	if autoPurge.Valid {
		t, _ := time.Parse(time.RFC3339, autoPurge.String)
		entry.AutoPurgeAfter = &t
	}

	return &entry, nil
}
