// Package core provides Scanners and Diagnostics for CloudFS.
// Phase 4: Operability & Trust.
//
// INVARIANTS:
// - All operations READ-ONLY
// - NO auto-fix
// - Report-only
// - NO data mutation
package core

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Scanner provides non-destructive consistency checks.
type Scanner struct {
	db       *sql.DB
	cacheDir string
	rootDir  string
	mu       sync.RWMutex
}

// NewScanner creates a new scanner.
func NewScanner(db *sql.DB, cacheDir, rootDir string) *Scanner {
	return &Scanner{
		db:       db,
		cacheDir: cacheDir,
		rootDir:  rootDir,
	}
}

// ScanResult contains scan findings.
type ScanResult struct {
	ScanType    string        `json:"scan_type"`
	ScanTime    time.Time     `json:"scan_time"`
	TotalItems  int           `json:"total_items"`
	OKCount     int           `json:"ok_count"`
	WarningCount int          `json:"warning_count"`
	ErrorCount  int           `json:"error_count"`
	Findings    []ScanFinding `json:"findings"`
}

// ScanFinding is an individual finding.
type ScanFinding struct {
	Severity    string `json:"severity"` // "ok", "warning", "error"
	Category    string `json:"category"`
	Description string `json:"description"`
	Path        string `json:"path,omitempty"`
	EntryID     int64  `json:"entry_id,omitempty"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// ScanIndex performs a read-only scan of the index.
func (s *Scanner) ScanIndex(ctx context.Context) (*ScanResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := &ScanResult{
		ScanType: "index",
		ScanTime: time.Now(),
	}

	// Check schema version
	var schemaVersion string
	s.db.QueryRowContext(ctx, `SELECT value FROM index_meta WHERE key = 'schema_version'`).Scan(&schemaVersion)
	if schemaVersion == "" {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "error",
			Category:    "schema",
			Description: "Missing schema version",
			Suggestion:  "Reinitialize index with 'cloudfs init'",
		})
		result.ErrorCount++
	} else {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "ok",
			Category:    "schema",
			Description: fmt.Sprintf("Schema version: %s", schemaVersion),
		})
		result.OKCount++
	}

	// Count entries
	var entryCount int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries`).Scan(&entryCount)
	result.TotalItems = entryCount
	result.Findings = append(result.Findings, ScanFinding{
		Severity:    "ok",
		Category:    "entries",
		Description: fmt.Sprintf("Total entries: %d", entryCount),
	})
	result.OKCount++

	// Check for orphaned versions
	var orphanedVersions int
	s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM versions v
		LEFT JOIN entries e ON v.entry_id = e.id
		WHERE e.id IS NULL
	`).Scan(&orphanedVersions)
	if orphanedVersions > 0 {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "warning",
			Category:    "versions",
			Description: fmt.Sprintf("%d orphaned versions (no parent entry)", orphanedVersions),
			Suggestion:  "Run 'cloudfs repair' to clean up",
		})
		result.WarningCount++
	}

	// Check for entries without active versions
	var noActiveVersion int
	s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM entries e
		LEFT JOIN versions v ON e.id = v.entry_id AND v.state = 'active'
		WHERE e.entry_type = 'file' AND v.id IS NULL
	`).Scan(&noActiveVersion)
	if noActiveVersion > 0 {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "warning",
			Category:    "versions",
			Description: fmt.Sprintf("%d entries without active version", noActiveVersion),
			Suggestion:  "Check if entries are in trash",
		})
		result.WarningCount++
	}

	// Check pending journal entries
	var pendingJournal int
	s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM journal WHERE state IN ('pending', 'committed')
	`).Scan(&pendingJournal)
	if pendingJournal > 0 {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "warning",
			Category:    "journal",
			Description: fmt.Sprintf("%d pending journal entries", pendingJournal),
			Suggestion:  "Run 'cloudfs journal resume' to complete",
		})
		result.WarningCount++
	} else {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "ok",
			Category:    "journal",
			Description: "No pending journal entries",
		})
		result.OKCount++
	}

	return result, nil
}

// ScanCache performs a read-only scan of the cache.
func (s *Scanner) ScanCache(ctx context.Context) (*ScanResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := &ScanResult{
		ScanType: "cache",
		ScanTime: time.Now(),
	}

	// Check cache directory exists
	if _, err := os.Stat(s.cacheDir); os.IsNotExist(err) {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "error",
			Category:    "cache",
			Description: "Cache directory does not exist",
			Path:        s.cacheDir,
			Suggestion:  "Run 'cloudfs init' to create cache",
		})
		result.ErrorCount++
		return result, nil
	}

	// Count cache entries in DB
	var dbCacheCount int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cache_entries WHERE state = 'valid'`).Scan(&dbCacheCount)

	// Count actual cache files
	entriesDir := filepath.Join(s.cacheDir, "entries")
	var fsCacheCount int
	filepath.Walk(entriesDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			fsCacheCount++
		}
		return nil
	})

	result.TotalItems = dbCacheCount
	result.Findings = append(result.Findings, ScanFinding{
		Severity:    "ok",
		Category:    "cache",
		Description: fmt.Sprintf("DB cache entries: %d", dbCacheCount),
	})
	result.OKCount++

	result.Findings = append(result.Findings, ScanFinding{
		Severity:    "ok",
		Category:    "cache",
		Description: fmt.Sprintf("Filesystem cache files: %d", fsCacheCount),
	})
	result.OKCount++

	// Check for mismatches
	if dbCacheCount != fsCacheCount {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "warning",
			Category:    "cache",
			Description: fmt.Sprintf("Cache mismatch: DB has %d, FS has %d", dbCacheCount, fsCacheCount),
			Suggestion:  "Run 'cloudfs repair' to reconcile",
		})
		result.WarningCount++
	}

	// Check cache size
	var totalSize int64
	s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM cache_entries WHERE state = 'ready'`).Scan(&totalSize)
	result.Findings = append(result.Findings, ScanFinding{
		Severity:    "ok",
		Category:    "cache",
		Description: fmt.Sprintf("Total cache size: %s", formatBytes(totalSize)),
	})
	result.OKCount++

	// Check pinned entries
	var pinnedCount int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cache_entries WHERE pinned = 1`).Scan(&pinnedCount)
	result.Findings = append(result.Findings, ScanFinding{
		Severity:    "ok",
		Category:    "cache",
		Description: fmt.Sprintf("Pinned entries: %d", pinnedCount),
	})
	result.OKCount++

	return result, nil
}

// ScanProviders performs a read-only scan of provider state.
func (s *Scanner) ScanProviders(ctx context.Context) (*ScanResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := &ScanResult{
		ScanType: "providers",
		ScanTime: time.Now(),
	}

	// Count providers
	var providerCount int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM providers WHERE status = 'active'`).Scan(&providerCount)
	result.TotalItems = providerCount

	if providerCount == 0 {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "warning",
			Category:    "providers",
			Description: "No active providers configured",
			Suggestion:  "Add a provider with 'cloudfs provider add'",
		})
		result.WarningCount++
	} else {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "ok",
			Category:    "providers",
			Description: fmt.Sprintf("Active providers: %d", providerCount),
		})
		result.OKCount++
	}

	// List each provider
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, type, status FROM providers
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var name, ptype, status string
			rows.Scan(&name, &ptype, &status)
			severity := "ok"
			if status != "active" {
				severity = "warning"
			}
			result.Findings = append(result.Findings, ScanFinding{
				Severity:    severity,
				Category:    "providers",
				Description: fmt.Sprintf("Provider '%s' (%s): %s", name, ptype, status),
			})
			if severity == "ok" {
				result.OKCount++
			} else {
				result.WarningCount++
			}
		}
	}

	// Check placements
	var placementCount int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM placements`).Scan(&placementCount)
	result.Findings = append(result.Findings, ScanFinding{
		Severity:    "ok",
		Category:    "placements",
		Description: fmt.Sprintf("Total placements: %d", placementCount),
	})
	result.OKCount++

	// Check unverified placements
	var unverifiedCount int
	s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM placements WHERE verified = 0
	`).Scan(&unverifiedCount)
	if unverifiedCount > 0 {
		result.Findings = append(result.Findings, ScanFinding{
			Severity:    "warning",
			Category:    "placements",
			Description: fmt.Sprintf("%d unverified placements", unverifiedCount),
			Suggestion:  "Run 'cloudfs verify' to verify",
		})
		result.WarningCount++
	}

	return result, nil
}

// Diagnostics contains comprehensive system diagnostics.
type Diagnostics struct {
	GeneratedAt      time.Time              `json:"generated_at"`
	CloudFSVersion   string                 `json:"cloudfs_version"`
	SchemaVersion    string                 `json:"schema_version"`
	IndexPath        string                 `json:"index_path"`
	CachePath        string                 `json:"cache_path"`
	RootPath         string                 `json:"root_path"`
	Providers        []ProviderDiagnostic   `json:"providers"`
	HealthSummary    HealthDiagnostic       `json:"health_summary"`
	JournalState     JournalDiagnostic      `json:"journal_state"`
	RequestQueue     RequestQueueDiagnostic `json:"request_queue"`
	Archives         ArchiveDiagnostic      `json:"archives"`
}

// ProviderDiagnostic is provider info (redacted).
type ProviderDiagnostic struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Placements int    `json:"placements"`
}

// HealthDiagnostic summarizes health.
type HealthDiagnostic struct {
	TotalEntries    int     `json:"total_entries"`
	HealthyCount    int     `json:"healthy_count"`
	WarningCount    int     `json:"warning_count"`
	CriticalCount   int     `json:"critical_count"`
	AverageScore    float64 `json:"average_score"`
}

// JournalDiagnostic shows journal state.
type JournalDiagnostic struct {
	PendingCount    int `json:"pending_count"`
	CommittedCount  int `json:"committed_count"`
	RolledBackCount int `json:"rolled_back_count"`
}

// RequestQueueDiagnostic shows queue state.
type RequestQueueDiagnostic struct {
	PendingRequests  int `json:"pending_requests"`
	RunningRequests  int `json:"running_requests"`
	CompletedToday   int `json:"completed_today"`
	FailedToday      int `json:"failed_today"`
}

// ArchiveDiagnostic shows archive state.
type ArchiveDiagnostic struct {
	TotalArchives int   `json:"total_archives"`
	TotalSize     int64 `json:"total_size"`
}

// ExportDiagnostics generates comprehensive diagnostics.
func (s *Scanner) ExportDiagnostics(ctx context.Context, indexPath string) (*Diagnostics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	diag := &Diagnostics{
		GeneratedAt:    time.Now(),
		CloudFSVersion: "1.0.0",
		IndexPath:      indexPath,
		CachePath:      s.cacheDir,
		RootPath:       s.rootDir,
	}

	// Schema version
	s.db.QueryRowContext(ctx, `SELECT value FROM index_meta WHERE key = 'schema_version'`).Scan(&diag.SchemaVersion)

	// Providers (redacted config)
	rows, _ := s.db.QueryContext(ctx, `
		SELECT p.name, p.type, p.status, COUNT(pl.id) as placements
		FROM providers p
		LEFT JOIN placements pl ON p.id = pl.provider_id
		GROUP BY p.id
	`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var pd ProviderDiagnostic
			rows.Scan(&pd.Name, &pd.Type, &pd.Status, &pd.Placements)
			diag.Providers = append(diag.Providers, pd)
		}
	}

	// Health summary
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE entry_type = 'file'`).Scan(&diag.HealthSummary.TotalEntries)

	// Journal state
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal WHERE state = 'pending'`).Scan(&diag.JournalState.PendingCount)
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal WHERE state = 'committed'`).Scan(&diag.JournalState.CommittedCount)
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal WHERE state = 'rolled_back'`).Scan(&diag.JournalState.RolledBackCount)

	// Request queue
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_queue WHERE state = 'pending'`).Scan(&diag.RequestQueue.PendingRequests)
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_queue WHERE state = 'running'`).Scan(&diag.RequestQueue.RunningRequests)

	// Archives
	s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(archive_size), 0) FROM archives`).Scan(&diag.Archives.TotalArchives, &diag.Archives.TotalSize)

	return diag, nil
}

// Helper for formatting bytes
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
