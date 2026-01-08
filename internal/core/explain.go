// Package core provides the Explainer for CloudFS.
// Phase 4: Audit & Explainability.
//
// INVARIANTS:
// - Read-only operations only
// - NO side effects
// - Human-readable output
// - Shows where data lives, versions, states
package core

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"
)

// Explainer provides human-readable explanations of entry state.
type Explainer struct {
	db       *sql.DB
	cacheDir string
	rootDir  string
	mu       sync.RWMutex
}

// NewExplainer creates a new explainer.
func NewExplainer(db *sql.DB, cacheDir, rootDir string) *Explainer {
	return &Explainer{
		db:       db,
		cacheDir: cacheDir,
		rootDir:  rootDir,
	}
}

// EntryExplanation contains comprehensive information about an entry.
type EntryExplanation struct {
	// Basic Info
	EntryID        int64
	Name           string
	Type           string
	Classification string
	LogicalSize    int64
	
	// Version Info
	ActiveVersion   *VersionInfo
	VersionHistory  []*VersionInfo
	TotalVersions   int
	
	// Location Info
	Locations       []LocationInfo
	
	// Cache State
	CacheState      *CacheStateInfo
	
	// Archive State
	ArchiveState    *ArchiveStateInfo
	
	// Health Info
	HealthInfo      *HealthStateInfo
	
	// Pending Operations
	PendingOps      []PendingOpInfo
	
	// Trash State
	InTrash         bool
	TrashInfo       *TrashStateInfo
}

// VersionInfo describes a version.
type VersionInfo struct {
	VersionID   int64
	VersionNum  int
	ContentHash string
	Size        int64
	State       string
	CreatedAt   time.Time
}

// LocationInfo describes where data is stored.
type LocationInfo struct {
	LocationType string // "cache", "provider", "archive", "placeholder"
	Path         string
	ProviderName string
	Verified     bool
	LastVerified *time.Time
}

// CacheStateInfo describes cache state.
type CacheStateInfo struct {
	IsCached     bool
	CachePath    string
	Pinned       bool
	LastAccessed *time.Time
	State        string
}

// ArchiveStateInfo describes archive state.
type ArchiveStateInfo struct {
	IsArchived    bool
	ArchivePath   string
	Par2Path      string
	OriginalSize  int64
	ArchiveSize   int64
	RecoveryLevel int
	CreatedAt     time.Time
	Verified      bool
}

// HealthStateInfo describes health.
type HealthStateInfo struct {
	Score            float64
	ScoreDescription string
	ReplicationCount int
	LastVerified     *time.Time
	Issues           []string
	Recommendations  []string
}

// TrashStateInfo describes trash state.
type TrashStateInfo struct {
	DeletedAt      time.Time
	AutoPurgeAfter *time.Time
	DaysInTrash    int
}

// PendingOpInfo describes a pending operation.
type PendingOpInfo struct {
	OperationID   string
	OperationType string
	State         string
	CreatedAt     time.Time
}

// Explain returns a comprehensive explanation of an entry.
func (e *Explainer) Explain(ctx context.Context, path string) (*EntryExplanation, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Get entry
	var entryID int64
	var name, entryType string
	var classification sql.NullString
	var logicalSize, physicalSize int64

	err := e.db.QueryRowContext(ctx, `
		SELECT id, name, entry_type, classification, logical_size, physical_size
		FROM entries WHERE name = ?
	`, path).Scan(&entryID, &name, &entryType, &classification, &logicalSize, &physicalSize)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entry not found: %s", path)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get entry: %w", err)
	}

	exp := &EntryExplanation{
		EntryID:        entryID,
		Name:           name,
		Type:           entryType,
		Classification: classification.String,
		LogicalSize:    logicalSize,
	}

	// Get versions
	exp.ActiveVersion, exp.VersionHistory, exp.TotalVersions = e.getVersionInfo(ctx, entryID)

	// Get locations
	exp.Locations = e.getLocations(ctx, entryID, path)

	// Get cache state
	exp.CacheState = e.getCacheState(ctx, entryID)

	// Get archive state
	exp.ArchiveState = e.getArchiveState(ctx, entryID)

	// Get health info
	exp.HealthInfo = e.getHealthInfo(ctx, entryID)

	// Get pending operations
	exp.PendingOps = e.getPendingOps(ctx, entryID)

	// Get trash state
	exp.InTrash, exp.TrashInfo = e.getTrashState(ctx, entryID)

	return exp, nil
}

func (e *Explainer) getVersionInfo(ctx context.Context, entryID int64) (*VersionInfo, []*VersionInfo, int) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT id, version_num, content_hash, size, state, created_at
		FROM versions WHERE entry_id = ?
		ORDER BY version_num DESC
	`, entryID)
	if err != nil {
		return nil, nil, 0
	}
	defer rows.Close()

	var active *VersionInfo
	var history []*VersionInfo
	for rows.Next() {
		var v VersionInfo
		var createdAt string
		rows.Scan(&v.VersionID, &v.VersionNum, &v.ContentHash, &v.Size, &v.State, &createdAt)
		v.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

		if v.State == "active" && active == nil {
			active = &v
		}
		history = append(history, &v)
	}

	return active, history, len(history)
}

func (e *Explainer) getLocations(ctx context.Context, entryID int64, path string) []LocationInfo {
	var locations []LocationInfo

	// Check placeholder
	placeholderPath := e.rootDir + "/" + path + ".cloudfs"
	if _, err := os.Stat(placeholderPath); err == nil {
		locations = append(locations, LocationInfo{
			LocationType: "placeholder",
			Path:         placeholderPath,
		})
	}

	// Check real file
	realPath := e.rootDir + "/" + path
	if _, err := os.Stat(realPath); err == nil {
		locations = append(locations, LocationInfo{
			LocationType: "local",
			Path:         realPath,
		})
	}

	// Check cache
	var cacheState string
	e.db.QueryRowContext(ctx, `
		SELECT state FROM cache_entries WHERE entry_id = ?
	`, entryID).Scan(&cacheState)
	if cacheState == "ready" {
		cachePath := fmt.Sprintf("%s/entries/%d", e.cacheDir, entryID)
		locations = append(locations, LocationInfo{
			LocationType: "cache",
			Path:         cachePath,
		})
	}

	// Check providers
	rows, err := e.db.QueryContext(ctx, `
		SELECT pr.name, p.remote_path, p.verified, p.last_verified
		FROM placements p
		JOIN versions v ON p.version_id = v.id
		JOIN providers pr ON p.provider_id = pr.id
		WHERE v.entry_id = ? AND v.state = 'active'
	`, entryID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var loc LocationInfo
			var lastVerified sql.NullString
			var verified int
			rows.Scan(&loc.ProviderName, &loc.Path, &verified, &lastVerified)
			loc.LocationType = "provider"
			loc.Verified = verified == 1
			if lastVerified.Valid {
				t, _ := time.Parse(time.RFC3339, lastVerified.String)
				loc.LastVerified = &t
			}
			locations = append(locations, loc)
		}
	}

	return locations
}

func (e *Explainer) getCacheState(ctx context.Context, entryID int64) *CacheStateInfo {
	var state CacheStateInfo
	var lastAccessed sql.NullString
	var pinned int

	err := e.db.QueryRowContext(ctx, `
		SELECT state, pinned, last_accessed
		FROM cache_entries WHERE entry_id = ?
	`, entryID).Scan(&state.State, &pinned, &lastAccessed)
	if err != nil {
		return nil
	}

	state.IsCached = state.State == "ready"
	state.Pinned = pinned == 1
	state.CachePath = fmt.Sprintf("%s/entries/%d", e.cacheDir, entryID)
	if lastAccessed.Valid {
		t, _ := time.Parse(time.RFC3339, lastAccessed.String)
		state.LastAccessed = &t
	}

	return &state
}

func (e *Explainer) getArchiveState(ctx context.Context, entryID int64) *ArchiveStateInfo {
	var state ArchiveStateInfo
	var createdAt string

	err := e.db.QueryRowContext(ctx, `
		SELECT archive_path, par2_path, original_size, archive_size, 
		       recovery_level, created_at
		FROM archives WHERE entry_id = ?
		ORDER BY created_at DESC LIMIT 1
	`, entryID).Scan(
		&state.ArchivePath, &state.Par2Path, &state.OriginalSize,
		&state.ArchiveSize, &state.RecoveryLevel, &createdAt,
	)
	if err != nil {
		return nil
	}

	state.IsArchived = true
	state.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

	// Check if archive file exists
	if _, err := os.Stat(state.ArchivePath); err == nil {
		state.Verified = true
	}

	return &state
}

func (e *Explainer) getHealthInfo(ctx context.Context, entryID int64) *HealthStateInfo {
	health := &HealthStateInfo{
		Issues:          []string{},
		Recommendations: []string{},
	}

	// Count placements
	e.db.QueryRowContext(ctx, `
		SELECT COUNT(p.id)
		FROM placements p
		JOIN versions v ON p.version_id = v.id
		WHERE v.entry_id = ? AND v.state = 'active'
	`, entryID).Scan(&health.ReplicationCount)

	// Get last verified
	var lastVerified sql.NullString
	e.db.QueryRowContext(ctx, `
		SELECT MAX(p.last_verified)
		FROM placements p
		JOIN versions v ON p.version_id = v.id
		WHERE v.entry_id = ? AND v.state = 'active'
	`, entryID).Scan(&lastVerified)
	if lastVerified.Valid {
		t, _ := time.Parse(time.RFC3339, lastVerified.String)
		health.LastVerified = &t
	}

	// Calculate score
	health.Score = 1.0
	if health.ReplicationCount == 0 {
		health.Score = 0.2
		health.Issues = append(health.Issues, "No provider placements")
		health.Recommendations = append(health.Recommendations, "Run 'cloudfs push' to upload")
	} else if health.ReplicationCount < 2 {
		health.Score = 0.7
		health.Issues = append(health.Issues, "Low replication (1 copy)")
		health.Recommendations = append(health.Recommendations, "Add second provider for redundancy")
	}

	if health.LastVerified == nil {
		health.Score -= 0.3
		if health.Score < 0 {
			health.Score = 0
		}
		health.Issues = append(health.Issues, "Never verified with provider")
		health.Recommendations = append(health.Recommendations, "Run 'cloudfs verify'")
	}

	health.ScoreDescription = GetHealthScoreDescription(health.Score)

	return health
}

func (e *Explainer) getPendingOps(ctx context.Context, entryID int64) []PendingOpInfo {
	rows, err := e.db.QueryContext(ctx, `
		SELECT operation_id, operation_type, state, created_at
		FROM journal
		WHERE state IN ('pending', 'committed')
		AND payload LIKE ?
		ORDER BY created_at DESC
	`, fmt.Sprintf("%%\"entry_id\":%d%%", entryID))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var ops []PendingOpInfo
	for rows.Next() {
		var op PendingOpInfo
		var createdAt string
		rows.Scan(&op.OperationID, &op.OperationType, &op.State, &createdAt)
		op.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		ops = append(ops, op)
	}

	return ops
}

func (e *Explainer) getTrashState(ctx context.Context, entryID int64) (bool, *TrashStateInfo) {
	var state TrashStateInfo
	var deletedAt string
	var autoPurge sql.NullString

	err := e.db.QueryRowContext(ctx, `
		SELECT deleted_at, auto_purge_after
		FROM trash WHERE original_entry_id = ?
	`, entryID).Scan(&deletedAt, &autoPurge)
	if err != nil {
		return false, nil
	}

	state.DeletedAt, _ = time.Parse(time.RFC3339, deletedAt)
	state.DaysInTrash = int(time.Since(state.DeletedAt).Hours() / 24)
	if autoPurge.Valid {
		t, _ := time.Parse(time.RFC3339, autoPurge.String)
		state.AutoPurgeAfter = &t
	}

	return true, &state
}

// ExplainByID explains an entry by ID.
func (e *Explainer) ExplainByID(ctx context.Context, entryID int64) (*EntryExplanation, error) {
	var name string
	err := e.db.QueryRowContext(ctx, `SELECT name FROM entries WHERE id = ?`, entryID).Scan(&name)
	if err != nil {
		return nil, fmt.Errorf("entry not found: %d", entryID)
	}
	return e.Explain(ctx, name)
}
