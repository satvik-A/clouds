// Package core provides the Overview Dashboard for CloudFS.
// Phase 5: Productization & Trust.
//
// INVARIANTS:
// - Read-only operations only
// - NO side effects
// - Human-readable summary
package core

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Dashboard provides a read-only overview of the CloudFS state.
type Dashboard struct {
	db       *sql.DB
	cacheDir string
	mu       sync.RWMutex
}

// NewDashboard creates a new dashboard.
func NewDashboard(db *sql.DB, cacheDir string) *Dashboard {
	return &Dashboard{
		db:       db,
		cacheDir: cacheDir,
	}
}

// Overview contains the complete system overview.
type Overview struct {
	GeneratedAt time.Time

	// Data Summary
	TotalEntries  int
	TotalFiles    int
	TotalFolders  int
	TotalSize     int64

	// Cache Summary
	CachedFiles   int
	CachedSize    int64
	PinnedFiles   int

	// Archive Summary
	ArchivedFiles int
	ArchiveSize   int64

	// Provider Summary
	ActiveProviders int
	TotalPlacements int
	UnverifiedPlacements int

	// Health Summary
	HealthyFiles  int
	WarningFiles  int
	CriticalFiles int
	AvgHealthScore float64

	// Queue Summary
	PendingRequests int
	RunningRequests int

	// Journal Summary
	PendingJournal int

	// Trash Summary
	TrashItems    int
	TrashSize     int64
}

// GetOverview returns a complete read-only overview.
func (d *Dashboard) GetOverview(ctx context.Context) (*Overview, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	o := &Overview{
		GeneratedAt: time.Now(),
	}

	// Data summary
	d.db.QueryRowContext(ctx, `
		SELECT 
			COUNT(*),
			COALESCE(SUM(CASE WHEN entry_type = 'file' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN entry_type = 'folder' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(logical_size), 0)
		FROM entries
	`).Scan(&o.TotalEntries, &o.TotalFiles, &o.TotalFolders, &o.TotalSize)

	// Cache summary
	d.db.QueryRowContext(ctx, `
		SELECT 
			COUNT(*),
			COALESCE(SUM(size), 0),
			COALESCE(SUM(CASE WHEN pinned = 1 THEN 1 ELSE 0 END), 0)
		FROM cache_entries WHERE state = 'valid'
	`).Scan(&o.CachedFiles, &o.CachedSize, &o.PinnedFiles)

	// Archive summary
	d.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(archive_size), 0)
		FROM archives
	`).Scan(&o.ArchivedFiles, &o.ArchiveSize)

	// Provider summary
	d.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM providers WHERE status = 'active'
	`).Scan(&o.ActiveProviders)

	d.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN verified = 0 THEN 1 ELSE 0 END), 0)
		FROM placements
	`).Scan(&o.TotalPlacements, &o.UnverifiedPlacements)

	// Health summary (calculated from placement count)
	d.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT e.id)
		FROM entries e
		JOIN versions v ON e.id = v.entry_id AND v.state = 'active'
		JOIN placements p ON v.id = p.version_id
		WHERE e.entry_type = 'file'
		GROUP BY e.id
		HAVING COUNT(p.id) >= 2
	`).Scan(&o.HealthyFiles)

	d.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT e.id)
		FROM entries e
		JOIN versions v ON e.id = v.entry_id AND v.state = 'active'
		LEFT JOIN placements p ON v.id = p.version_id
		WHERE e.entry_type = 'file'
		GROUP BY e.id
		HAVING COUNT(p.id) = 1
	`).Scan(&o.WarningFiles)

	d.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT e.id)
		FROM entries e
		JOIN versions v ON e.id = v.entry_id AND v.state = 'active'
		LEFT JOIN placements p ON v.id = p.version_id
		WHERE e.entry_type = 'file'
		GROUP BY e.id
		HAVING COUNT(p.id) = 0
	`).Scan(&o.CriticalFiles)

	// Queue summary
	d.db.QueryRowContext(ctx, `
		SELECT 
			COALESCE(SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'running' THEN 1 ELSE 0 END), 0)
		FROM request_queue
	`).Scan(&o.PendingRequests, &o.RunningRequests)

	// Journal summary
	d.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM journal WHERE state IN ('pending', 'committed')
	`).Scan(&o.PendingJournal)

	// Trash summary
	d.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trash
	`).Scan(&o.TrashItems)

	return o, nil
}

// FormatSize formats bytes to human-readable format.
func FormatSize(b int64) string {
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
