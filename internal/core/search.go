// Package core provides the Search Manager for CloudFS.
// Based on design.txt Section 19: Search (Phase 2).
//
// INVARIANTS:
// - Index-only queries (NO provider access)
// - NO filesystem access during search
// - Fast metadata search
package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudfs/cloudfs/internal/model"
)

// SearchManager provides fast index-only search.
type SearchManager struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewSearchManager creates a new search manager.
func NewSearchManager(db *sql.DB) *SearchManager {
	return &SearchManager{db: db}
}

// SearchFilter defines search criteria.
type SearchFilter struct {
	Query          string // Name/path search
	Type           string // file, folder
	Classification string // archive, media, document, etc.
	MinSize        int64  // Minimum size in bytes
	MaxSize        int64  // Maximum size in bytes
	State          string // active, deleted, etc.
	Limit          int    // Max results
}

// SearchResult contains a search match.
type SearchResult struct {
	Entry   *model.Entry
	Version *model.Version
	Score   float64 // Relevance score
}

// Search performs an index-only search.
// NO provider access. NO filesystem access.
func (sm *SearchManager) Search(ctx context.Context, filter *SearchFilter) ([]*SearchResult, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Build query
	query := `
		SELECT 
			e.id, e.parent_id, e.name, e.entry_type, e.classification, 
			e.logical_size, e.physical_size, e.created_at, e.modified_at,
			v.id, v.version_num, v.content_hash, v.size, v.state
		FROM entries e
		LEFT JOIN versions v ON e.id = v.entry_id AND v.state = 'active'
		WHERE 1=1
	`
	var args []interface{}

	// Name/path search (case-insensitive LIKE)
	if filter.Query != "" {
		query += " AND (e.name LIKE ? COLLATE NOCASE)"
		args = append(args, "%"+filter.Query+"%")
	}

	// Type filter
	if filter.Type != "" {
		query += " AND e.entry_type = ?"
		args = append(args, filter.Type)
	}

	// Classification filter
	if filter.Classification != "" {
		query += " AND e.classification = ?"
		args = append(args, filter.Classification)
	}

	// Size filters
	if filter.MinSize > 0 {
		query += " AND e.logical_size >= ?"
		args = append(args, filter.MinSize)
	}
	if filter.MaxSize > 0 {
		query += " AND e.logical_size <= ?"
		args = append(args, filter.MaxSize)
	}

	// State filter (defaults to active)
	if filter.State != "" {
		query += " AND v.state = ?"
		args = append(args, filter.State)
	}

	// Order by relevance (name match first)
	query += " ORDER BY e.name ASC"

	// Limit
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query += " LIMIT ?"
	args = append(args, limit)

	rows, err := sm.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	defer rows.Close()

	var results []*SearchResult
	for rows.Next() {
		var entry model.Entry
		var version model.Version
		var parentID sql.NullInt64
		var classification, createdAt, modifiedAt sql.NullString
		var versionID, versionNum sql.NullInt64
		var contentHash, versionState sql.NullString
		var versionSize sql.NullInt64

		err := rows.Scan(
			&entry.ID, &parentID, &entry.Name, &entry.Type, &classification,
			&entry.LogicalSize, &entry.PhysicalSize, &createdAt, &modifiedAt,
			&versionID, &versionNum, &contentHash, &versionSize, &versionState,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan result: %w", err)
		}

		if parentID.Valid {
			entry.ParentID = &parentID.Int64
		}
		if classification.Valid {
			entry.Classification = classification.String
		}

		if versionID.Valid {
			version.ID = versionID.Int64
			version.EntryID = entry.ID
			version.VersionNum = int(versionNum.Int64)
			version.ContentHash = contentHash.String
			version.Size = versionSize.Int64
			version.State = model.VersionState(versionState.String)
		}

		// Calculate simple relevance score
		score := calculateRelevance(entry.Name, filter.Query)

		results = append(results, &SearchResult{
			Entry:   &entry,
			Version: &version,
			Score:   score,
		})
	}

	return results, nil
}

// calculateRelevance computes a simple relevance score.
func calculateRelevance(name, query string) float64 {
	if query == "" {
		return 1.0
	}

	nameLower := strings.ToLower(name)
	queryLower := strings.ToLower(query)

	// Exact match
	if nameLower == queryLower {
		return 1.0
	}

	// Starts with query
	if strings.HasPrefix(nameLower, queryLower) {
		return 0.9
	}

	// Contains query
	if strings.Contains(nameLower, queryLower) {
		return 0.7
	}

	return 0.5
}

// SearchByName searches for entries by name.
func (sm *SearchManager) SearchByName(ctx context.Context, name string, limit int) ([]*SearchResult, error) {
	return sm.Search(ctx, &SearchFilter{
		Query: name,
		Limit: limit,
	})
}

// SearchByType searches for entries of a specific type.
func (sm *SearchManager) SearchByType(ctx context.Context, entryType string, limit int) ([]*SearchResult, error) {
	return sm.Search(ctx, &SearchFilter{
		Type:  entryType,
		Limit: limit,
	})
}

// SearchByClassification searches for entries by classification.
func (sm *SearchManager) SearchByClassification(ctx context.Context, classification string, limit int) ([]*SearchResult, error) {
	return sm.Search(ctx, &SearchFilter{
		Classification: classification,
		Limit:          limit,
	})
}

// SearchLargeFiles finds files above a size threshold.
func (sm *SearchManager) SearchLargeFiles(ctx context.Context, minSize int64, limit int) ([]*SearchResult, error) {
	return sm.Search(ctx, &SearchFilter{
		Type:    string(model.EntryTypeFile),
		MinSize: minSize,
		Limit:   limit,
	})
}

// GetStats returns search-related statistics.
type SearchStats struct {
	TotalEntries     int
	TotalFiles       int
	TotalFolders     int
	TotalSize        int64
	Classifications  map[string]int
}

func (sm *SearchManager) GetStats(ctx context.Context) (*SearchStats, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	stats := &SearchStats{
		Classifications: make(map[string]int),
	}

	// Count entries by type
	var files, folders int
	err := sm.db.QueryRowContext(ctx, `
		SELECT 
			COALESCE(SUM(CASE WHEN entry_type = 'file' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN entry_type = 'folder' THEN 1 ELSE 0 END), 0)
		FROM entries
	`).Scan(&files, &folders)
	if err != nil {
		return nil, fmt.Errorf("failed to count entries: %w", err)
	}

	stats.TotalFiles = files
	stats.TotalFolders = folders
	stats.TotalEntries = files + folders

	// Total size
	err = sm.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(logical_size), 0) FROM entries WHERE entry_type = 'file'
	`).Scan(&stats.TotalSize)
	if err != nil {
		return nil, fmt.Errorf("failed to sum size: %w", err)
	}

	// Count by classification
	rows, err := sm.db.QueryContext(ctx, `
		SELECT classification, COUNT(*)
		FROM entries
		WHERE classification IS NOT NULL AND classification != ''
		GROUP BY classification
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to count classifications: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var class string
		var count int
		rows.Scan(&class, &count)
		stats.Classifications[class] = count
	}

	return stats, nil
}
