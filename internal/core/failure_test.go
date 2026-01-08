// Package core provides failure simulation tests for CloudFS crash safety.
// These tests verify the journal replay and recovery mechanisms.
package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudfs/cloudfs/internal/model"
)

// TestJournalCrashDuringIndexMutation simulates a crash during index mutation.
// The journal entry should allow recovery.
func TestJournalCrashDuringIndexMutation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-failure-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	ctx := context.Background()

	// Phase 1: Begin operation but don't complete
	im, err := NewIndexManager(dbPath, "")
	if err != nil {
		t.Fatalf("failed to create index manager: %v", err)
	}
	im.Initialize(ctx)

	db, _ := OpenEncryptedDB(dbPath, "")
	jm := NewJournalManager(db.DB())
	im.SetJournalManager(jm)

	// Begin an upload operation (simulate start)
	opID, err := jm.BeginOperation(ctx, "upload", `{"path": "test.txt", "size": 1024}`)
	if err != nil {
		t.Fatalf("failed to begin operation: %v", err)
	}

	// Create entry (this would normally happen during upload)
	entry := &model.Entry{
		Name:        "test.txt",
		Type:        model.EntryTypeFile,
		LogicalSize: 1024,
	}
	im.CreateEntry(ctx, entry)

	// Commit but NOT sync (simulating crash before sync)
	jm.CommitOperation(ctx, opID)

	// Close (simulate crash)
	im.Close()
	db.Close()

	// Phase 2: Recover and verify
	im2, err := NewIndexManager(dbPath, "")
	if err != nil {
		t.Fatalf("failed to reopen index manager: %v", err)
	}
	defer im2.Close()

	db2, _ := OpenEncryptedDB(dbPath, "")
	defer db2.Close()
	jm2 := NewJournalManager(db2.DB())

	// Get pending operations (should find the uncommitted one)
	pending, err := jm2.GetPendingOperations(ctx)
	if err != nil {
		t.Fatalf("failed to get pending operations: %v", err)
	}

	// Should have 1 pending committed operation
	foundOp := false
	for _, op := range pending {
		if op.OperationID == opID && op.State == model.JournalStateCommitted {
			foundOp = true
			break
		}
	}

	if !foundOp {
		t.Error("should find the committed but not synced operation")
	}

	// Verify entry exists
	entries, err := im2.ListEntries(ctx, nil)
	if err != nil {
		t.Fatalf("failed to list entries: %v", err)
	}

	if len(entries) == 0 {
		t.Error("entry should exist after recovery")
	}
}

// TestCacheWriteInterruption simulates cache write interruption.
func TestCacheWriteInterruption(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-cache-failure-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Setup
	dbPath := filepath.Join(tmpDir, "index.db")
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(context.Background())
	im.Close()

	db, _ := OpenEncryptedDB(dbPath, "")
	defer db.Close()

	cacheDir := filepath.Join(tmpDir, "cache")
	cm, err := NewCacheManager(db.DB(), cacheDir)
	if err != nil {
		t.Fatalf("failed to create cache manager: %v", err)
	}

	ctx := context.Background()

	// Create a file to cache
	testFile := filepath.Join(tmpDir, "testfile.txt")
	content := []byte("test content for cache")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Put in cache
	_, err = cm.Put(ctx, 1, 1, testFile)
	if err != nil {
		t.Fatalf("failed to put in cache: %v", err)
	}

	// Simulate interrupt by not finishing properly
	// Just close and reopen

	db.Close()

	// Reopen
	db2, _ := OpenEncryptedDB(dbPath, "")
	defer db2.Close()

	cm2, _ := NewCacheManager(db2.DB(), cacheDir)

	// Verify cache entry survived
	cachePath, err := cm2.Get(ctx, 1, 1)
	if err != nil {
		t.Fatalf("failed to get from cache: %v", err)
	}

	if cachePath == "" {
		t.Error("cache entry should exist after recovery")
	}

	// Verify content
	if cachePath != "" {
		data, _ := os.ReadFile(cachePath)
		if string(data) != "test content for cache" {
			t.Error("cache content should be intact")
		}
	}
}

// TestPlaceholderPartialSwap verifies no partial data exposure during swap.
func TestPlaceholderPartialSwap(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-placeholder-failure-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pm, _ := NewPlaceholderManager(tmpDir)
	ctx := context.Background()

	entry := &model.Entry{
		ID:          1,
		Name:        "test.txt",
		Type:        model.EntryTypeFile,
		LogicalSize: 11, // "hello world"
	}

	version := &model.Version{
		ID:          1,
		ContentHash: "wrong-hash",
	}

	// Create placeholder
	pm.CreatePlaceholder(ctx, entry, version, "", "prov1", "/remote/test.txt")

	// Create cache file
	cacheDir := filepath.Join(tmpDir, "cache")
	os.MkdirAll(cacheDir, 0755)
	cacheFile := filepath.Join(cacheDir, "data")
	os.WriteFile(cacheFile, []byte("hello world"), 0644)

	// Try swap with wrong hash - should fail
	err = pm.AtomicSwap(ctx, entry, cacheFile, "correct-hash", "")
	if err == nil {
		t.Error("swap should fail with wrong hash")
	}

	// Verify placeholder still exists (no partial exposure)
	placeholderPath := pm.GetPlaceholderPath(entry, "")
	if _, err := os.Stat(placeholderPath); os.IsNotExist(err) {
		t.Error("placeholder should still exist after failed swap")
	}

	// Verify no real file created
	realPath := pm.GetRealPath(entry, "")
	if _, err := os.Stat(realPath); !os.IsNotExist(err) {
		t.Error("real file should not exist after failed swap")
	}
}

// TestJournalRollbackRecovery verifies rolled-back operations don't resurrect.
func TestJournalRollbackRecovery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-rollback-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	ctx := context.Background()

	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(ctx)

	db, _ := OpenEncryptedDB(dbPath, "")
	jm := NewJournalManager(db.DB())

	// Begin and rollback
	opID, _ := jm.BeginOperation(ctx, "delete", `{"path": "important.txt"}`)
	jm.RollbackOperation(ctx, opID, "simulated failure")

	im.Close()
	db.Close()

	// Reopen and verify
	db2, _ := OpenEncryptedDB(dbPath, "")
	defer db2.Close()
	jm2 := NewJournalManager(db2.DB())

	pending, _ := jm2.GetPendingOperations(ctx)

	// Rolled back operation should NOT appear in pending
	for _, op := range pending {
		if op.OperationID == opID {
			t.Error("rolled back operation should not be pending")
		}
	}
}

// TestIndexIntegrityAfterCrash verifies index remains valid after crash.
func TestIndexIntegrityAfterCrash(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-integrity-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	ctx := context.Background()

	// Phase 1: Create entries
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(ctx)

	for i := 0; i < 10; i++ {
		entry := &model.Entry{
			Name:        "test.txt",
			Type:        model.EntryTypeFile,
			LogicalSize: int64(i * 100),
		}
		im.CreateEntry(ctx, entry)
	}

	// Close abruptly (simulate crash)
	im.Close()

	// Phase 2: Reopen and validate
	im2, err := NewIndexManager(dbPath, "")
	if err != nil {
		t.Fatalf("failed to reopen: %v", err)
	}
	defer im2.Close()

	// Validate index integrity
	if err := im2.Validate(ctx); err != nil {
		t.Errorf("index validation failed: %v", err)
	}

	// All entries should exist
	entries, _ := im2.ListEntries(ctx, nil)
	if len(entries) != 10 {
		t.Errorf("expected 10 entries, got %d", len(entries))
	}
}

// TestNoAutoHydrationOnAccess verifies filesystem access doesn't trigger hydration.
// This is a design invariant test, not a crash test.
func TestNoAutoHydrationOnAccess(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-no-auto-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pm, _ := NewPlaceholderManager(tmpDir)
	ctx := context.Background()

	entry := &model.Entry{
		ID:          1,
		Name:        "never-hydrate.txt",
		Type:        model.EntryTypeFile,
		LogicalSize: 1024,
	}

	version := &model.Version{
		ID:          1,
		ContentHash: "abc123",
	}

	// Create placeholder
	pm.CreatePlaceholder(ctx, entry, version, "", "prov1", "/remote/test.txt")

	// Try to read the placeholder (simulating filesystem access)
	placeholderPath := pm.GetPlaceholderPath(entry, "")
	_, err = os.ReadFile(placeholderPath)
	if err != nil {
		t.Fatalf("failed to read placeholder: %v", err)
	}

	// Verify file is STILL a placeholder (not auto-hydrated)
	if !pm.IsPlaceholder(placeholderPath) {
		t.Error("file should still be a placeholder after access")
	}

	// Real file should NOT exist
	realPath := pm.GetRealPath(entry, "")
	if _, err := os.Stat(realPath); !os.IsNotExist(err) {
		t.Error("real file should not exist - no auto-hydration allowed")
	}
}
