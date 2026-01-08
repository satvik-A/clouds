// Package core provides the Health Manager for CloudFS.
// Based on design.txt Section 18: Health scoring.
//
// INVARIANTS:
// - Health scoring is OBSERVATIONAL only
// - NO automatic remediation
// - NO auto-replication
// - NO data movement
// - Display only
package core

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// HealthManager provides health scoring visibility.
type HealthManager struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewHealthManager creates a new health manager.
func NewHealthManager(db *sql.DB) *HealthManager {
	return &HealthManager{db: db}
}

// EntryHealth represents the health status of an entry.
type EntryHealth struct {
	EntryID           int64
	EntryName         string
	HealthScore       float64 // 0.0 (critical) to 1.0 (excellent)
	ReplicationCount  int
	LastVerified      *time.Time
	VerificationAge   int // days since last verification
	Issues            []string
	Recommendations   []string
}

// OverallHealth represents the overall system health.
type OverallHealth struct {
	TotalEntries      int
	HealthyEntries    int
	WarningEntries    int
	CriticalEntries   int
	AverageScore      float64
	LastFullScan      *time.Time
	UnverifiedCount   int
	LowReplicationCount int
}

// GetOverallHealth returns the overall system health.
func (hm *HealthManager) GetOverallHealth(ctx context.Context) (*OverallHealth, error) {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	health := &OverallHealth{}

	// Count total entries
	err := hm.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM entries WHERE entry_type = 'file'
	`).Scan(&health.TotalEntries)
	if err != nil {
		return nil, fmt.Errorf("failed to count entries: %w", err)
	}

	// Count entries by replication status
	err = hm.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT v.entry_id)
		FROM versions v
		LEFT JOIN placements p ON v.id = p.version_id
		WHERE v.state = 'active'
		GROUP BY v.entry_id
		HAVING COUNT(p.id) < 1
	`).Scan(&health.LowReplicationCount)
	if err != nil && err != sql.ErrNoRows {
		// Not an error if no rows returned
		health.LowReplicationCount = 0
	}

	// Count unverified entries (no recent verification)
	verificationThreshold := time.Now().AddDate(0, 0, -30).Format(time.RFC3339)
	err = hm.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT v.entry_id)
		FROM versions v
		LEFT JOIN placements p ON v.id = p.version_id
		WHERE v.state = 'active'
		AND (p.verified_at IS NULL OR p.verified_at < ?)
	`, verificationThreshold).Scan(&health.UnverifiedCount)
	if err != nil && err != sql.ErrNoRows {
		health.UnverifiedCount = 0
	}

	// Calculate health categories
	// Healthy = has placement, verified recently
	// Warning = missing verification or low replication
	// Critical = no placement at all
	
	// Get placement counts per entry
	rows, err := hm.db.QueryContext(ctx, `
		SELECT 
			v.entry_id,
			COUNT(p.id) as placement_count,
			MAX(p.verified_at) as last_verified
		FROM versions v
		LEFT JOIN placements p ON v.id = p.version_id
		WHERE v.state = 'active'
		GROUP BY v.entry_id
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get health data: %w", err)
	}
	defer rows.Close()

	var scoreSum float64
	for rows.Next() {
		var entryID int64
		var placementCount int
		var lastVerified sql.NullString

		rows.Scan(&entryID, &placementCount, &lastVerified)

		score := calculateHealthScore(placementCount, lastVerified)
		scoreSum += score

		if score >= 0.8 {
			health.HealthyEntries++
		} else if score >= 0.5 {
			health.WarningEntries++
		} else {
			health.CriticalEntries++
		}
	}

	if health.TotalEntries > 0 {
		health.AverageScore = scoreSum / float64(health.TotalEntries)
	} else {
		health.AverageScore = 1.0 // No entries = healthy
	}

	return health, nil
}

// calculateHealthScore computes a health score for an entry.
func calculateHealthScore(replicationCount int, lastVerified sql.NullString) float64 {
	score := 1.0

	// No placements = critical
	if replicationCount == 0 {
		return 0.2
	}

	// Low replication
	if replicationCount < 2 {
		score -= 0.2
	}

	// Verification age
	if lastVerified.Valid {
		verifiedTime, err := time.Parse(time.RFC3339, lastVerified.String)
		if err == nil {
			daysSinceVerified := int(time.Since(verifiedTime).Hours() / 24)
			if daysSinceVerified > 30 {
				score -= 0.3
			} else if daysSinceVerified > 7 {
				score -= 0.1
			}
		}
	} else {
		// Never verified
		score -= 0.4
	}

	if score < 0 {
		score = 0
	}
	return score
}

// GetEntryHealth returns the health status of a specific entry.
func (hm *HealthManager) GetEntryHealth(ctx context.Context, entryID int64) (*EntryHealth, error) {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	// Get entry info
	var entryName string
	err := hm.db.QueryRowContext(ctx, `
		SELECT name FROM entries WHERE id = ?
	`, entryID).Scan(&entryName)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entry not found: %d", entryID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get entry: %w", err)
	}

	health := &EntryHealth{
		EntryID:   entryID,
		EntryName: entryName,
	}

	// Count placements
	err = hm.db.QueryRowContext(ctx, `
		SELECT COUNT(p.id)
		FROM versions v
		JOIN placements p ON v.id = p.version_id
		WHERE v.entry_id = ? AND v.state = 'active'
	`, entryID).Scan(&health.ReplicationCount)
	if err != nil {
		health.ReplicationCount = 0
	}

	// Get last verified time
	var lastVerified sql.NullString
	err = hm.db.QueryRowContext(ctx, `
		SELECT MAX(p.verified_at)
		FROM versions v
		JOIN placements p ON v.id = p.version_id
		WHERE v.entry_id = ? AND v.state = 'active'
	`, entryID).Scan(&lastVerified)
	if err == nil && lastVerified.Valid {
		t, _ := time.Parse(time.RFC3339, lastVerified.String)
		health.LastVerified = &t
		health.VerificationAge = int(time.Since(t).Hours() / 24)
	}

	// Calculate score
	health.HealthScore = calculateHealthScore(health.ReplicationCount, lastVerified)

	// Generate issues and recommendations
	if health.ReplicationCount == 0 {
		health.Issues = append(health.Issues, "No provider placements found")
		health.Recommendations = append(health.Recommendations, "Run 'cloudfs push' to upload to provider")
	} else if health.ReplicationCount < 2 {
		health.Issues = append(health.Issues, "Low replication count (1)")
		health.Recommendations = append(health.Recommendations, "Consider adding a second provider for redundancy")
	}

	if health.LastVerified == nil {
		health.Issues = append(health.Issues, "Never verified with provider")
		health.Recommendations = append(health.Recommendations, "Run 'cloudfs verify' to check provider data")
	} else if health.VerificationAge > 30 {
		health.Issues = append(health.Issues, fmt.Sprintf("Not verified in %d days", health.VerificationAge))
		health.Recommendations = append(health.Recommendations, "Run 'cloudfs verify' to refresh verification")
	}

	return health, nil
}

// GetHealthByPath returns health for an entry by its path.
func (hm *HealthManager) GetHealthByPath(ctx context.Context, path string) (*EntryHealth, error) {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	var entryID int64
	err := hm.db.QueryRowContext(ctx, `
		SELECT id FROM entries WHERE name = ?
	`, path).Scan(&entryID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entry not found: %s", path)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find entry: %w", err)
	}

	hm.mu.RUnlock()
	defer hm.mu.RLock()
	return hm.GetEntryHealth(ctx, entryID)
}

// GetCriticalEntries returns entries with critical health issues.
func (hm *HealthManager) GetCriticalEntries(ctx context.Context, limit int) ([]*EntryHealth, error) {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	// Find entries with no placements
	rows, err := hm.db.QueryContext(ctx, `
		SELECT e.id, e.name
		FROM entries e
		JOIN versions v ON e.id = v.entry_id AND v.state = 'active'
		LEFT JOIN placements p ON v.id = p.version_id
		WHERE e.entry_type = 'file'
		GROUP BY e.id
		HAVING COUNT(p.id) = 0
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to find critical entries: %w", err)
	}
	defer rows.Close()

	var results []*EntryHealth
	for rows.Next() {
		var id int64
		var name string
		rows.Scan(&id, &name)
		results = append(results, &EntryHealth{
			EntryID:     id,
			EntryName:   name,
			HealthScore: 0.2,
			Issues:      []string{"No provider placements"},
		})
	}

	return results, nil
}

// GetHealthScoreDescription returns a human-readable description.
func GetHealthScoreDescription(score float64) string {
	switch {
	case score >= 0.9:
		return "Excellent"
	case score >= 0.8:
		return "Good"
	case score >= 0.6:
		return "Fair"
	case score >= 0.4:
		return "Warning"
	default:
		return "Critical"
	}
}
