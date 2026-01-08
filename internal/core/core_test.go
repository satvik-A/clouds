// Package core provides tests for CloudFS core components.
package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudfs/cloudfs/internal/model"
)

func TestIndexManager_CreateEntry(t *testing.T) {
	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, err := NewIndexManager(dbPath, "")
	if err != nil {
		t.Fatalf("failed to create index manager: %v", err)
	}
	defer im.Close()

	ctx := context.Background()
	if err := im.Initialize(ctx); err != nil {
		t.Fatalf("failed to initialize: %v", err)
	}

	// Test creating an entry
	entry := &model.Entry{
		Name:         "test.txt",
		Type:         model.EntryTypeFile,
		LogicalSize:  1024,
		PhysicalSize: 1024,
	}

	if err := im.CreateEntry(ctx, entry); err != nil {
		t.Fatalf("failed to create entry: %v", err)
	}

	if entry.ID == 0 {
		t.Error("entry ID should be set after creation")
	}

	// Test retrieving the entry
	retrieved, err := im.GetEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("failed to get entry: %v", err)
	}

	if retrieved == nil {
		t.Fatal("entry should not be nil")
	}

	if retrieved.Name != "test.txt" {
		t.Errorf("expected name 'test.txt', got '%s'", retrieved.Name)
	}

	if retrieved.LogicalSize != 1024 {
		t.Errorf("expected size 1024, got %d", retrieved.LogicalSize)
	}
}

func TestIndexManager_CreateVersion(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, err := NewIndexManager(dbPath, "")
	if err != nil {
		t.Fatalf("failed to create index manager: %v", err)
	}
	defer im.Close()

	ctx := context.Background()
	im.Initialize(ctx)

	// Create an entry first
	entry := &model.Entry{
		Name: "test.txt",
		Type: model.EntryTypeFile,
	}
	im.CreateEntry(ctx, entry)

	// Create a version
	version := &model.Version{
		EntryID:     entry.ID,
		VersionNum:  1,
		ContentHash: "abc123",
		Size:        1024,
		State:       model.VersionStateActive,
	}

	if err := im.CreateVersion(ctx, version); err != nil {
		t.Fatalf("failed to create version: %v", err)
	}

	if version.ID == 0 {
		t.Error("version ID should be set after creation")
	}

	// Get active version
	active, err := im.GetActiveVersion(ctx, entry.ID)
	if err != nil {
		t.Fatalf("failed to get active version: %v", err)
	}

	if active == nil {
		t.Fatal("active version should not be nil")
	}

	if active.ContentHash != "abc123" {
		t.Errorf("expected hash 'abc123', got '%s'", active.ContentHash)
	}
}

func TestIndexManager_ListEntries(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, err := NewIndexManager(dbPath, "")
	if err != nil {
		t.Fatalf("failed to create index manager: %v", err)
	}
	defer im.Close()

	ctx := context.Background()
	im.Initialize(ctx)

	// Create multiple entries
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		entry := &model.Entry{
			Name: name,
			Type: model.EntryTypeFile,
		}
		im.CreateEntry(ctx, entry)
	}

	// List entries
	entries, err := im.ListEntries(ctx, nil)
	if err != nil {
		t.Fatalf("failed to list entries: %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

func TestIndexManager_Validate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, err := NewIndexManager(dbPath, "")
	if err != nil {
		t.Fatalf("failed to create index manager: %v", err)
	}
	defer im.Close()

	ctx := context.Background()
	im.Initialize(ctx)

	// Validation should pass on empty database
	if err := im.Validate(ctx); err != nil {
		t.Errorf("validation should pass: %v", err)
	}
}

func TestJournalManager_Operations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	db, err := OpenEncryptedDB(dbPath, "")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	// Initialize schema
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(context.Background())
	im.Close()

	// Reopen for journal
	db2, _ := OpenEncryptedDB(dbPath, "")
	defer db2.Close()

	jm := NewJournalManager(db2.DB())
	ctx := context.Background()

	// Begin operation
	opID, err := jm.BeginOperation(ctx, "upload", `{"file": "test.txt"}`)
	if err != nil {
		t.Fatalf("failed to begin operation: %v", err)
	}

	if opID == "" {
		t.Error("operation ID should not be empty")
	}

	// Get pending operations - should have one
	pending, err := jm.GetPendingOperations(ctx)
	if err != nil {
		t.Fatalf("failed to get pending: %v", err)
	}

	if len(pending) != 1 {
		t.Errorf("expected 1 pending operation, got %d", len(pending))
	}

	// Commit operation
	if err := jm.CommitOperation(ctx, opID); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Sync operation
	if err := jm.SyncOperation(ctx, opID); err != nil {
		t.Fatalf("failed to sync: %v", err)
	}

	// Get pending - should be empty now
	pending, _ = jm.GetPendingOperations(ctx)
	if len(pending) != 0 {
		t.Errorf("expected 0 pending operations after sync, got %d", len(pending))
	}
}

func TestJournalManager_Rollback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
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
	ctx := context.Background()

	// Begin and rollback
	opID, _ := jm.BeginOperation(ctx, "delete", `{"file": "test.txt"}`)
	if err := jm.RollbackOperation(ctx, opID, "simulated failure"); err != nil {
		t.Fatalf("failed to rollback: %v", err)
	}

	// Pending should be empty (rolled back is not pending)
	pending, _ := jm.GetPendingOperations(ctx)
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after rollback, got %d", len(pending))
	}
}

func TestCacheManager_PutAndGet(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
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

	// Create a temp file to cache
	testFile := filepath.Join(tmpDir, "testfile.txt")
	if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Put in cache
	entry, err := cm.Put(ctx, 1, 1, testFile)
	if err != nil {
		t.Fatalf("failed to put in cache: %v", err)
	}

	if entry == nil {
		t.Fatal("cache entry should not be nil")
	}

	// Get from cache
	cachePath, err := cm.Get(ctx, 1, 1)
	if err != nil {
		t.Fatalf("failed to get from cache: %v", err)
	}

	if cachePath == "" {
		t.Error("cache path should not be empty")
	}

	// Verify content
	content, _ := os.ReadFile(cachePath)
	if string(content) != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", string(content))
	}
}

func TestCacheManager_Pin(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
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

	cacheDir := filepath.Join(tmpDir, "cache")
	cm, _ := NewCacheManager(db.DB(), cacheDir)
	ctx := context.Background()

	// Add file to cache
	testFile := filepath.Join(tmpDir, "testfile.txt")
	os.WriteFile(testFile, []byte("test"), 0644)
	cm.Put(ctx, 1, 1, testFile)

	// Pin should succeed
	if err := cm.Pin(ctx, 1); err != nil {
		t.Fatalf("failed to pin: %v", err)
	}

	// Evict pinned should fail
	err = cm.Evict(ctx, 1, 1, true)
	if err == nil || err.Error() != "cannot evict pinned entry - unpin first" {
		t.Errorf("expected 'cannot evict pinned' error, got: %v", err)
	}

	// Unpin
	if err := cm.Unpin(ctx, 1); err != nil {
		t.Fatalf("failed to unpin: %v", err)
	}

	// Evict should succeed now
	if err := cm.Evict(ctx, 1, 1, true); err != nil {
		t.Fatalf("evict should succeed after unpin: %v", err)
	}
}

func TestCacheManager_Stats(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
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

	cacheDir := filepath.Join(tmpDir, "cache")
	cm, _ := NewCacheManager(db.DB(), cacheDir)
	ctx := context.Background()

	// Empty stats
	stats, err := cm.Stats(ctx)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}

	if stats.TotalEntries != 0 {
		t.Errorf("expected 0 entries, got %d", stats.TotalEntries)
	}
}

func TestPlaceholderManager_CreateAndRead(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pm, err := NewPlaceholderManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create placeholder manager: %v", err)
	}

	ctx := context.Background()

	entry := &model.Entry{
		ID:          1,
		Name:        "test.txt",
		Type:        model.EntryTypeFile,
		LogicalSize: 1024,
	}

	version := &model.Version{
		ID:          1,
		ContentHash: "abc123",
	}

	// Create placeholder
	if err := pm.CreatePlaceholder(ctx, entry, version, "", "provider1", "/remote/test.txt"); err != nil {
		t.Fatalf("failed to create placeholder: %v", err)
	}

	// Check placeholder exists
	placeholderPath := pm.GetPlaceholderPath(entry, "")
	if _, err := os.Stat(placeholderPath); os.IsNotExist(err) {
		t.Error("placeholder file should exist")
	}

	// Read placeholder
	meta, err := pm.ReadPlaceholder(placeholderPath)
	if err != nil {
		t.Fatalf("failed to read placeholder: %v", err)
	}

	if meta.EntryID != 1 {
		t.Errorf("expected entry ID 1, got %d", meta.EntryID)
	}

	if meta.LogicalSize != 1024 {
		t.Errorf("expected size 1024, got %d", meta.LogicalSize)
	}

	if !meta.IsPlaceholder {
		t.Error("IsPlaceholder should be true")
	}
}

func TestPlaceholderManager_AtomicSwap(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
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
		LogicalSize: 11, // "hello world" = 11 bytes
	}

	version := &model.Version{
		ID:          1,
		ContentHash: "", // Skip hash check for this test
	}

	// Create placeholder
	pm.CreatePlaceholder(ctx, entry, version, "", "", "")

	// Create cache file
	cacheFile := filepath.Join(tmpDir, "cache", "data")
	os.MkdirAll(filepath.Dir(cacheFile), 0755)
	os.WriteFile(cacheFile, []byte("hello world"), 0644)

	// Atomic swap
	if err := pm.AtomicSwap(ctx, entry, cacheFile, "", ""); err != nil {
		t.Fatalf("failed to swap: %v", err)
	}

	// Real file should exist
	realPath := pm.GetRealPath(entry, "")
	content, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("failed to read real file: %v", err)
	}

	if string(content) != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", string(content))
	}

	// Placeholder should not exist
	placeholderPath := pm.GetPlaceholderPath(entry, "")
	if _, err := os.Stat(placeholderPath); !os.IsNotExist(err) {
		t.Error("placeholder should be removed after swap")
	}
}

func TestPlaceholderManager_HashVerification(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cloudfs-test-*")
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
		LogicalSize: 11,
	}

	// Create cache file
	cacheFile := filepath.Join(tmpDir, "cache", "data")
	os.MkdirAll(filepath.Dir(cacheFile), 0755)
	os.WriteFile(cacheFile, []byte("hello world"), 0644)

	// Try swap with wrong hash - should fail
	err = pm.AtomicSwap(ctx, entry, cacheFile, "wronghash", "")
	if err == nil {
		t.Error("swap should fail with wrong hash")
	}

	if err.Error()[:13] != "hash mismatch" {
		t.Errorf("expected 'hash mismatch' error, got: %v", err)
	}
}

// Benchmark test for entry creation
func BenchmarkIndexManager_CreateEntry(b *testing.B) {
	tmpDir, _ := os.MkdirTemp("", "cloudfs-bench-*")
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(context.Background())
	defer im.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry := &model.Entry{
			Name: "test.txt",
			Type: model.EntryTypeFile,
		}
		im.CreateEntry(ctx, entry)
	}
}

// Test helper to verify no race conditions
func TestIndexManager_Concurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent test in short mode")
	}

	tmpDir, _ := os.MkdirTemp("", "cloudfs-test-*")
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "index.db")
	im, _ := NewIndexManager(dbPath, "")
	im.Initialize(context.Background())
	defer im.Close()

	ctx := context.Background()

	// Run concurrent operations
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(n int) {
			for j := 0; j < 10; j++ {
				entry := &model.Entry{
					Name: "test.txt",
					Type: model.EntryTypeFile,
				}
				im.CreateEntry(ctx, entry)
				im.ListEntries(ctx, nil)
				time.Sleep(time.Millisecond)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}
