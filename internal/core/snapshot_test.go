// Package core provides tests for the Snapshot Manager.
package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudfs/cloudfs/internal/model"
)

func TestSnapshotManager_Create(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-snapshot-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Setup
	dbPath := filepath.Join(tmpDir, "index.db")
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(context.Background())

	db, _ := OpenEncryptedDB(dbPath, "")
	defer db.Close()

	jm := NewJournalManager(db.DB())
	ctx := context.Background()

	// Create some entries and versions
	entry := &model.Entry{Name: "test.txt", Type: model.EntryTypeFile}
	im.CreateEntry(ctx, entry)

	version := &model.Version{
		EntryID:     entry.ID,
		VersionNum:  1,
		ContentHash: "abc123",
		Size:        1024,
		State:       model.VersionStateActive,
	}
	im.CreateVersion(ctx, version)
	im.Close()

	// Create snapshot
	sm := NewSnapshotManager(db.DB(), jm)
	snapshot, err := sm.Create(ctx, "v1.0", "Initial release")
	if err != nil {
		t.Fatalf("failed to create snapshot: %v", err)
	}

	if snapshot.Name != "v1.0" {
		t.Errorf("expected name 'v1.0', got '%s'", snapshot.Name)
	}
}

func TestSnapshotManager_List(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-snapshot-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(context.Background())
	im.Close()

	db, _ := OpenEncryptedDB(dbPath, "")
	defer db.Close()

	jm := NewJournalManager(db.DB())
	sm := NewSnapshotManager(db.DB(), jm)
	ctx := context.Background()

	// Create multiple snapshots
	sm.Create(ctx, "v1.0", "First")
	sm.Create(ctx, "v2.0", "Second")
	sm.Create(ctx, "v3.0", "Third")

	// List
	snapshots, err := sm.List(ctx)
	if err != nil {
		t.Fatalf("failed to list: %v", err)
	}

	if len(snapshots) != 3 {
		t.Errorf("expected 3 snapshots, got %d", len(snapshots))
	}
}

func TestSnapshotManager_Inspect(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-snapshot-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(context.Background())

	db, _ := OpenEncryptedDB(dbPath, "")
	defer db.Close()

	ctx := context.Background()

	// Create entries with versions
	for i := 0; i < 5; i++ {
		entry := &model.Entry{Name: "test.txt", Type: model.EntryTypeFile, LogicalSize: 100}
		im.CreateEntry(ctx, entry)
		version := &model.Version{
			EntryID:     entry.ID,
			VersionNum:  1,
			ContentHash: "hash",
			Size:        int64(100 * (i + 1)),
			State:       model.VersionStateActive,
		}
		im.CreateVersion(ctx, version)
	}
	im.Close()

	jm := NewJournalManager(db.DB())
	sm := NewSnapshotManager(db.DB(), jm)

	sm.Create(ctx, "test", "Test snapshot")

	info, err := sm.Inspect(ctx, "test")
	if err != nil {
		t.Fatalf("failed to inspect: %v", err)
	}

	if info.EntryCount != 5 {
		t.Errorf("expected 5 entries, got %d", info.EntryCount)
	}

	if info.VersionCount != 5 {
		t.Errorf("expected 5 versions, got %d", info.VersionCount)
	}

	expectedSize := int64(100 + 200 + 300 + 400 + 500)
	if info.TotalSize != expectedSize {
		t.Errorf("expected size %d, got %d", expectedSize, info.TotalSize)
	}
}

func TestSnapshotManager_Restore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-snapshot-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(context.Background())

	db, _ := OpenEncryptedDB(dbPath, "")
	defer db.Close()

	ctx := context.Background()
	jm := NewJournalManager(db.DB())

	// Create entry v1
	entry := &model.Entry{Name: "file.txt", Type: model.EntryTypeFile}
	im.CreateEntry(ctx, entry)
	v1 := &model.Version{
		EntryID:     entry.ID,
		VersionNum:  1,
		ContentHash: "hash1",
		Size:        100,
		State:       model.VersionStateActive,
	}
	im.CreateVersion(ctx, v1)

	// Create snapshot at v1
	sm := NewSnapshotManager(db.DB(), jm)
	sm.Create(ctx, "before-change", "Before modification")

	// Modify to v2
	db.DB().Exec("UPDATE versions SET state = 'superseded' WHERE id = ?", v1.ID)
	v2 := &model.Version{
		EntryID:     entry.ID,
		VersionNum:  2,
		ContentHash: "hash2",
		Size:        200,
		State:       model.VersionStateActive,
	}
	im.CreateVersion(ctx, v2)
	im.Close()

	// Verify current state is v2
	var currentHash string
	db.DB().QueryRow(`
		SELECT content_hash FROM versions WHERE entry_id = ? AND state = 'active'
	`, entry.ID).Scan(&currentHash)
	if currentHash != "hash2" {
		t.Errorf("expected current hash 'hash2', got '%s'", currentHash)
	}

	// Restore to snapshot
	if err := sm.Restore(ctx, "before-change"); err != nil {
		t.Fatalf("failed to restore: %v", err)
	}

	// Verify state is now v1
	db.DB().QueryRow(`
		SELECT content_hash FROM versions WHERE entry_id = ? AND state = 'active'
	`, entry.ID).Scan(&currentHash)
	if currentHash != "hash1" {
		t.Errorf("expected restored hash 'hash1', got '%s'", currentHash)
	}
}

func TestSnapshotManager_Delete(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-snapshot-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(context.Background())
	im.Close()

	db, _ := OpenEncryptedDB(dbPath, "")
	defer db.Close()

	jm := NewJournalManager(db.DB())
	sm := NewSnapshotManager(db.DB(), jm)
	ctx := context.Background()

	sm.Create(ctx, "to-delete", "Will be deleted")

	// Delete
	if err := sm.Delete(ctx, "to-delete"); err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// Verify deleted
	s, _ := sm.GetByName(ctx, "to-delete")
	if s != nil {
		t.Error("snapshot should be deleted")
	}
}

func TestSnapshotManager_RestorePreview(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-snapshot-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(context.Background())

	db, _ := OpenEncryptedDB(dbPath, "")
	defer db.Close()

	ctx := context.Background()
	jm := NewJournalManager(db.DB())

	// Create entry and snapshot
	entry := &model.Entry{Name: "original.txt", Type: model.EntryTypeFile}
	im.CreateEntry(ctx, entry)
	v1 := &model.Version{
		EntryID:     entry.ID,
		VersionNum:  1,
		ContentHash: "hash1",
		Size:        100,
		State:       model.VersionStateActive,
	}
	im.CreateVersion(ctx, v1)
	im.Close()

	sm := NewSnapshotManager(db.DB(), jm)
	sm.Create(ctx, "snapshot1", "")

	// Get preview (should show no changes since state matches)
	preview, err := sm.GetRestorePreview(ctx, "snapshot1")
	if err != nil {
		t.Fatalf("failed to get preview: %v", err)
	}

	if preview.VersionChanges != 0 {
		t.Errorf("expected 0 changes for matching state, got %d", preview.VersionChanges)
	}
}
