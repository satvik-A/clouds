// Package core provides the Snapshot Manager for CloudFS.
// Based on design.txt Section 16: Snapshots & Restore.
//
// INVARIANTS:
// - Snapshots are METADATA-ONLY (no data duplication)
// - Snapshots are immutable once created
// - Restore = roll index state back to snapshot
// - Restoring does NOT delete cloud data
// - All snapshot operations are journaled
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

// SnapshotManager manages metadata-only snapshots.
type SnapshotManager struct {
	db      *sql.DB
	journal *JournalManager
	mu      sync.RWMutex
}

// NewSnapshotManager creates a new snapshot manager.
func NewSnapshotManager(db *sql.DB, journal *JournalManager) *SnapshotManager {
	return &SnapshotManager{
		db:      db,
		journal: journal,
	}
}

// SnapshotInfo contains detailed snapshot information.
type SnapshotInfo struct {
	Snapshot     *model.Snapshot
	EntryCount   int
	VersionCount int
	TotalSize    int64
}

// Create creates a new snapshot capturing current index state.
// This is METADATA-ONLY - no file data is copied.
func (sm *SnapshotManager) Create(ctx context.Context, name string, description string) (*model.Snapshot, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Begin journal operation
	payload, _ := json.Marshal(map[string]string{
		"name":        name,
		"description": description,
	})
	opID, err := sm.journal.BeginOperation(ctx, "snapshot_create", string(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to begin journal: %w", err)
	}

	// Start transaction
	tx, err := sm.db.BeginTx(ctx, nil)
	if err != nil {
		sm.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Create snapshot record
	result, err := tx.ExecContext(ctx, `
		INSERT INTO snapshots (name, description) VALUES (?, ?)
	`, name, description)
	if err != nil {
		sm.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to create snapshot: %w", err)
	}

	snapshotID, err := result.LastInsertId()
	if err != nil {
		sm.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to get snapshot ID: %w", err)
	}

	// Capture all active versions (metadata-only)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO snapshot_versions (snapshot_id, version_id)
		SELECT ?, v.id
		FROM versions v
		WHERE v.state = 'active'
	`, snapshotID)
	if err != nil {
		sm.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to capture versions: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		sm.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to commit snapshot: %w", err)
	}

	// Complete journal
	sm.journal.CommitOperation(ctx, opID)
	sm.journal.SyncOperation(ctx, opID)

	// Get created snapshot
	var snapshot model.Snapshot
	var createdAt string
	err = sm.db.QueryRowContext(ctx, `
		SELECT id, name, created_at, description FROM snapshots WHERE id = ?
	`, snapshotID).Scan(&snapshot.ID, &snapshot.Name, &createdAt, &snapshot.Description)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve snapshot: %w", err)
	}
	snapshot.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

	return &snapshot, nil
}

// List returns all snapshots.
func (sm *SnapshotManager) List(ctx context.Context) ([]*model.Snapshot, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	rows, err := sm.db.QueryContext(ctx, `
		SELECT id, name, created_at, description
		FROM snapshots
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []*model.Snapshot
	for rows.Next() {
		var s model.Snapshot
		var createdAt string
		if err := rows.Scan(&s.ID, &s.Name, &createdAt, &s.Description); err != nil {
			return nil, fmt.Errorf("failed to scan snapshot: %w", err)
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		snapshots = append(snapshots, &s)
	}

	return snapshots, nil
}

// Inspect returns detailed information about a snapshot.
func (sm *SnapshotManager) Inspect(ctx context.Context, name string) (*SnapshotInfo, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Get snapshot
	var snapshot model.Snapshot
	var createdAt string
	err := sm.db.QueryRowContext(ctx, `
		SELECT id, name, created_at, description FROM snapshots WHERE name = ?
	`, name).Scan(&snapshot.ID, &snapshot.Name, &createdAt, &snapshot.Description)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("snapshot not found: %s", name)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get snapshot: %w", err)
	}
	snapshot.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

	// Get stats
	var entryCount, versionCount int
	var totalSize int64

	err = sm.db.QueryRowContext(ctx, `
		SELECT 
			COUNT(DISTINCT e.id) as entry_count,
			COUNT(sv.version_id) as version_count,
			COALESCE(SUM(v.size), 0) as total_size
		FROM snapshot_versions sv
		JOIN versions v ON sv.version_id = v.id
		JOIN entries e ON v.entry_id = e.id
		WHERE sv.snapshot_id = ?
	`, snapshot.ID).Scan(&entryCount, &versionCount, &totalSize)
	if err != nil {
		return nil, fmt.Errorf("failed to get snapshot stats: %w", err)
	}

	return &SnapshotInfo{
		Snapshot:     &snapshot,
		EntryCount:   entryCount,
		VersionCount: versionCount,
		TotalSize:    totalSize,
	}, nil
}

// RestorePreview returns what would change if restoring a snapshot.
type RestorePreview struct {
	SnapshotName    string
	EntriesToAdd    []string
	EntriesToRemove []string
	VersionChanges  int
}

// GetRestorePreview shows what would change without making changes.
func (sm *SnapshotManager) GetRestorePreview(ctx context.Context, name string) (*RestorePreview, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Get snapshot ID
	var snapshotID int64
	err := sm.db.QueryRowContext(ctx, `SELECT id FROM snapshots WHERE name = ?`, name).Scan(&snapshotID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("snapshot not found: %s", name)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get snapshot: %w", err)
	}

	preview := &RestorePreview{SnapshotName: name}

	// Find entries in snapshot but not currently active
	rows, err := sm.db.QueryContext(ctx, `
		SELECT DISTINCT e.name
		FROM snapshot_versions sv
		JOIN versions v ON sv.version_id = v.id
		JOIN entries e ON v.entry_id = e.id
		WHERE sv.snapshot_id = ?
		AND e.id NOT IN (
			SELECT entry_id FROM versions WHERE state = 'active'
		)
	`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("failed to find additions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		rows.Scan(&name)
		preview.EntriesToAdd = append(preview.EntriesToAdd, name)
	}

	// Find entries currently active but not in snapshot
	rows2, err := sm.db.QueryContext(ctx, `
		SELECT DISTINCT e.name
		FROM versions v
		JOIN entries e ON v.entry_id = e.id
		WHERE v.state = 'active'
		AND v.id NOT IN (
			SELECT version_id FROM snapshot_versions WHERE snapshot_id = ?
		)
	`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("failed to find removals: %w", err)
	}
	defer rows2.Close()

	for rows2.Next() {
		var name string
		rows2.Scan(&name)
		preview.EntriesToRemove = append(preview.EntriesToRemove, name)
	}

	// Count version changes
	err = sm.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM versions v
		WHERE v.state = 'active'
		AND v.id NOT IN (SELECT version_id FROM snapshot_versions WHERE snapshot_id = ?)
	`, snapshotID).Scan(&preview.VersionChanges)
	if err != nil {
		return nil, fmt.Errorf("failed to count changes: %w", err)
	}

	return preview, nil
}

// Restore rolls the index state back to match a snapshot.
// This does NOT delete cloud data - only changes version states.
func (sm *SnapshotManager) Restore(ctx context.Context, name string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Get snapshot ID
	var snapshotID int64
	err := sm.db.QueryRowContext(ctx, `SELECT id FROM snapshots WHERE name = ?`, name).Scan(&snapshotID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("snapshot not found: %s", name)
	}
	if err != nil {
		return fmt.Errorf("failed to get snapshot: %w", err)
	}

	// Begin journal operation
	payload, _ := json.Marshal(map[string]interface{}{
		"snapshot_name": name,
		"snapshot_id":   snapshotID,
	})
	opID, err := sm.journal.BeginOperation(ctx, "snapshot_restore", string(payload))
	if err != nil {
		return fmt.Errorf("failed to begin journal: %w", err)
	}

	// Start transaction
	tx, err := sm.db.BeginTx(ctx, nil)
	if err != nil {
		sm.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Step 1: Mark all currently active versions as superseded
	// (except those in the snapshot)
	_, err = tx.ExecContext(ctx, `
		UPDATE versions SET state = 'superseded'
		WHERE state = 'active'
		AND id NOT IN (SELECT version_id FROM snapshot_versions WHERE snapshot_id = ?)
	`, snapshotID)
	if err != nil {
		sm.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to supersede versions: %w", err)
	}

	// Step 2: Restore snapshot versions to active
	_, err = tx.ExecContext(ctx, `
		UPDATE versions SET state = 'active'
		WHERE id IN (SELECT version_id FROM snapshot_versions WHERE snapshot_id = ?)
	`, snapshotID)
	if err != nil {
		sm.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to restore versions: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		sm.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to commit restore: %w", err)
	}

	// Complete journal
	sm.journal.CommitOperation(ctx, opID)
	sm.journal.SyncOperation(ctx, opID)

	return nil
}

// Delete removes a snapshot (metadata only).
func (sm *SnapshotManager) Delete(ctx context.Context, name string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Begin journal operation
	payload, _ := json.Marshal(map[string]string{"name": name})
	opID, err := sm.journal.BeginOperation(ctx, "snapshot_delete", string(payload))
	if err != nil {
		return fmt.Errorf("failed to begin journal: %w", err)
	}

	result, err := sm.db.ExecContext(ctx, `DELETE FROM snapshots WHERE name = ?`, name)
	if err != nil {
		sm.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		sm.journal.RollbackOperation(ctx, opID, "snapshot not found")
		return fmt.Errorf("snapshot not found: %s", name)
	}

	sm.journal.CommitOperation(ctx, opID)
	sm.journal.SyncOperation(ctx, opID)

	return nil
}

// GetByName retrieves a snapshot by name.
func (sm *SnapshotManager) GetByName(ctx context.Context, name string) (*model.Snapshot, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var snapshot model.Snapshot
	var createdAt string
	err := sm.db.QueryRowContext(ctx, `
		SELECT id, name, created_at, description FROM snapshots WHERE name = ?
	`, name).Scan(&snapshot.ID, &snapshot.Name, &createdAt, &snapshot.Description)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get snapshot: %w", err)
	}
	snapshot.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

	return &snapshot, nil
}

// GetVersionsInSnapshot returns all versions captured in a snapshot.
func (sm *SnapshotManager) GetVersionsInSnapshot(ctx context.Context, snapshotID int64) ([]*model.Version, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	rows, err := sm.db.QueryContext(ctx, `
		SELECT v.id, v.entry_id, v.version_num, v.content_hash, v.size, v.created_at, v.state, v.encryption_key_id
		FROM snapshot_versions sv
		JOIN versions v ON sv.version_id = v.id
		WHERE sv.snapshot_id = ?
		ORDER BY v.entry_id, v.version_num
	`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("failed to get versions: %w", err)
	}
	defer rows.Close()

	var versions []*model.Version
	for rows.Next() {
		var v model.Version
		var createdAt string
		if err := rows.Scan(&v.ID, &v.EntryID, &v.VersionNum, &v.ContentHash, &v.Size, &createdAt, &v.State, &v.EncryptionKeyID); err != nil {
			return nil, fmt.Errorf("failed to scan version: %w", err)
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		versions = append(versions, &v)
	}

	return versions, nil
}
