// Package core provides the Cache Manager for CloudFS.
// Based on design.txt Sections 12, 13.
package core

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cloudfs/cloudfs/internal/model"
)

// CacheManager manages the persistent cache.
// Cache rules (design.txt Section 12):
// - Persistent (survives restarts)
// - NO automatic eviction
// - Files only deleted on explicit user action
// - Files only overwritten on re-download
//
// SQLite is the SOURCE OF TRUTH for cache state.
// metadata.json is derived/disposable.
type CacheManager struct {
	db       *sql.DB
	cacheDir string
	mu       sync.RWMutex
}

// NewCacheManager creates a new cache manager.
func NewCacheManager(db *sql.DB, cacheDir string) (*CacheManager, error) {
	// Ensure cache directory exists
	entriesDir := filepath.Join(cacheDir, "entries")
	if err := os.MkdirAll(entriesDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	return &CacheManager{
		db:       db,
		cacheDir: cacheDir,
	}, nil
}

// CacheDir returns the cache directory path.
func (cm *CacheManager) CacheDir() string {
	return cm.cacheDir
}

// CacheStats holds cache statistics.
type CacheStats struct {
	TotalEntries    int
	TotalSize       int64
	PinnedEntries   int
	PinnedSize      int64
	StaleEntries    int
	DiskUsage       int64
	DiskAvailable   int64
}

// Get returns the cache path for an entry version, or empty if not cached.
func (cm *CacheManager) Get(ctx context.Context, entryID, versionID int64) (string, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	query := `
		SELECT cache_path FROM cache_entries
		WHERE entry_id = ? AND version_id = ? AND state != 'pending_eviction'
	`
	var cachePath string
	err := cm.db.QueryRowContext(ctx, query, entryID, versionID).Scan(&cachePath)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get cache entry: %w", err)
	}

	// Verify file exists
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		// Cache entry exists in DB but file is missing - mark stale
		return "", nil
	}

	// Update last accessed time
	go cm.updateLastAccessed(ctx, entryID, versionID)

	return cachePath, nil
}

// updateLastAccessed updates the last accessed timestamp.
func (cm *CacheManager) updateLastAccessed(ctx context.Context, entryID, versionID int64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	query := `UPDATE cache_entries SET last_accessed = datetime('now') WHERE entry_id = ? AND version_id = ?`
	cm.db.ExecContext(ctx, query, entryID, versionID)
}

// Put adds a file to the cache.
// The caller must have already written the data to dataPath.
func (cm *CacheManager) Put(ctx context.Context, entryID, versionID int64, dataPath string) (*model.CacheEntry, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Calculate cache path
	cacheDir := filepath.Join(cm.cacheDir, "entries", fmt.Sprintf("%d", entryID), fmt.Sprintf("%d", versionID))
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create cache entry directory: %w", err)
	}

	cachePath := filepath.Join(cacheDir, "data")

	// Move or copy the file to cache location
	if err := os.Rename(dataPath, cachePath); err != nil {
		// Try copy if rename fails (cross-device)
		if err := copyFile(dataPath, cachePath); err != nil {
			return nil, fmt.Errorf("failed to move file to cache: %w", err)
		}
		os.Remove(dataPath)
	}

	// Insert or update cache entry
	query := `
		INSERT INTO cache_entries (entry_id, version_id, cache_path, state)
		VALUES (?, ?, ?, 'valid')
		ON CONFLICT(entry_id, version_id) DO UPDATE SET
			cache_path = excluded.cache_path,
			cached_at = datetime('now'),
			last_accessed = datetime('now'),
			state = 'valid'
	`
	result, err := cm.db.ExecContext(ctx, query, entryID, versionID, cachePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache entry: %w", err)
	}

	id, _ := result.LastInsertId()

	return &model.CacheEntry{
		ID:           id,
		EntryID:      entryID,
		VersionID:    versionID,
		CachePath:    cachePath,
		CachedAt:     time.Now(),
		LastAccessed: time.Now(),
		Pinned:       false,
		State:        model.CacheStateValid,
	}, nil
}

// Evict removes an entry from the cache.
// Based on design.txt: eviction requires explicit user action.
// This function should only be called after user confirmation.
func (cm *CacheManager) Evict(ctx context.Context, entryID, versionID int64, confirmed bool) error {
	if !confirmed {
		return fmt.Errorf("eviction requires explicit user confirmation")
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Check if pinned
	var pinned int
	query := `SELECT pinned FROM cache_entries WHERE entry_id = ? AND version_id = ?`
	err := cm.db.QueryRowContext(ctx, query, entryID, versionID).Scan(&pinned)
	if err == sql.ErrNoRows {
		return nil // Already not in cache
	}
	if err != nil {
		return fmt.Errorf("failed to check cache entry: %w", err)
	}

	if pinned == 1 {
		return fmt.Errorf("cannot evict pinned entry - unpin first")
	}

	// Get cache path and delete file
	var cachePath string
	query = `SELECT cache_path FROM cache_entries WHERE entry_id = ? AND version_id = ?`
	cm.db.QueryRowContext(ctx, query, entryID, versionID).Scan(&cachePath)

	if cachePath != "" {
		os.RemoveAll(filepath.Dir(cachePath)) // Remove version directory
	}

	// Delete from database
	query = `DELETE FROM cache_entries WHERE entry_id = ? AND version_id = ?`
	_, err = cm.db.ExecContext(ctx, query, entryID, versionID)
	if err != nil {
		return fmt.Errorf("failed to delete cache entry: %w", err)
	}

	return nil
}

// Pin marks an entry as pinned (prevent eviction).
func (cm *CacheManager) Pin(ctx context.Context, entryID int64) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	query := `UPDATE cache_entries SET pinned = 1 WHERE entry_id = ?`
	result, err := cm.db.ExecContext(ctx, query, entryID)
	if err != nil {
		return fmt.Errorf("failed to pin entry: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("entry not in cache")
	}

	return nil
}

// Unpin removes the pin from an entry.
func (cm *CacheManager) Unpin(ctx context.Context, entryID int64) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	query := `UPDATE cache_entries SET pinned = 0 WHERE entry_id = ?`
	_, err := cm.db.ExecContext(ctx, query, entryID)
	if err != nil {
		return fmt.Errorf("failed to unpin entry: %w", err)
	}

	return nil
}

// List returns cached entries matching the filter.
type CacheFilter struct {
	PinnedOnly bool
	StaleOnly  bool
}

func (cm *CacheManager) List(ctx context.Context, filter *CacheFilter) ([]*model.CacheEntry, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	query := `
		SELECT id, entry_id, version_id, cache_path, cached_at, last_accessed, pinned, state
		FROM cache_entries
		WHERE 1=1
	`
	var args []interface{}

	if filter != nil {
		if filter.PinnedOnly {
			query += " AND pinned = 1"
		}
		if filter.StaleOnly {
			query += " AND state = 'stale'"
		}
	}

	query += " ORDER BY last_accessed DESC"

	rows, err := cm.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list cache entries: %w", err)
	}
	defer rows.Close()

	var entries []*model.CacheEntry
	for rows.Next() {
		var entry model.CacheEntry
		var cachedAt, lastAccessed string
		var pinned int
		err := rows.Scan(
			&entry.ID, &entry.EntryID, &entry.VersionID,
			&entry.CachePath, &cachedAt, &lastAccessed,
			&pinned, &entry.State)
		if err != nil {
			return nil, fmt.Errorf("failed to scan cache entry: %w", err)
		}
		entry.CachedAt, _ = time.Parse(time.RFC3339, cachedAt)
		entry.LastAccessed, _ = time.Parse(time.RFC3339, lastAccessed)
		entry.Pinned = pinned == 1
		entries = append(entries, &entry)
	}

	return entries, nil
}

// Stats returns cache statistics.
func (cm *CacheManager) Stats(ctx context.Context) (*CacheStats, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	stats := &CacheStats{}

	// Count total entries and pinned entries
	query := `
		SELECT 
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN pinned = 1 THEN 1 ELSE 0 END), 0) as pinned,
			COALESCE(SUM(CASE WHEN state = 'stale' THEN 1 ELSE 0 END), 0) as stale
		FROM cache_entries
	`
	err := cm.db.QueryRowContext(ctx, query).Scan(&stats.TotalEntries, &stats.PinnedEntries, &stats.StaleEntries)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get cache stats: %w", err)
	}

	// Calculate actual disk usage
	stats.DiskUsage = cm.calculateDiskUsage()

	return stats, nil
}

// calculateDiskUsage walks the cache directory to calculate total size.
func (cm *CacheManager) calculateDiskUsage() int64 {
	var total int64
	filepath.Walk(cm.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// GetEvictionCandidates returns entries ranked for eviction.
// Ranking algorithm (design.txt Section 12):
// 1. last_accessed (oldest first)
// 2. cache_policy (deletable before pinned)
// 3. hydration_cost (cheap-to-redownload first)
func (cm *CacheManager) GetEvictionCandidates(ctx context.Context, limit int) ([]*model.CacheEntry, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	query := `
		SELECT id, entry_id, version_id, cache_path, cached_at, last_accessed, pinned, state
		FROM cache_entries
		WHERE pinned = 0 AND state != 'pending_eviction'
		ORDER BY last_accessed ASC
		LIMIT ?
	`
	rows, err := cm.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get eviction candidates: %w", err)
	}
	defer rows.Close()

	var entries []*model.CacheEntry
	for rows.Next() {
		var entry model.CacheEntry
		var cachedAt, lastAccessed string
		var pinned int
		err := rows.Scan(
			&entry.ID, &entry.EntryID, &entry.VersionID,
			&entry.CachePath, &cachedAt, &lastAccessed,
			&pinned, &entry.State)
		if err != nil {
			return nil, fmt.Errorf("failed to scan cache entry: %w", err)
		}
		entry.CachedAt, _ = time.Parse(time.RFC3339, cachedAt)
		entry.LastAccessed, _ = time.Parse(time.RFC3339, lastAccessed)
		entry.Pinned = pinned == 1
		entries = append(entries, &entry)
	}

	return entries, nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = destFile.ReadFrom(sourceFile)
	return err
}
