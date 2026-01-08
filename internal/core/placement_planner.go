// Package core provides the PlacementPlanner for intelligent storage placement.
// Based on design.txt Section 8: Provider Selection.
//
// INVARIANTS:
// - Use LIVE GetUsage(), not cached DB values
// - NEVER exceed hard_limit
// - Encryption compatibility is a HARD constraint
// - Tentative placement at add, final revalidation at push
package core

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// PlacementPlanner determines optimal storage placement.
type PlacementPlanner struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewPlacementPlanner creates a new placement planner.
func NewPlacementPlanner(db *sql.DB) *PlacementPlanner {
	return &PlacementPlanner{db: db}
}

// PlannedPlacement describes a single placement decision.
type PlannedPlacement struct {
	ProviderID   string
	ProviderName string
	RemotePath   string
	Priority     int
	Reason       string // "most_free_space", "classification_match", etc
}

// RejectedProvider explains why a provider was not selected.
type RejectedProvider struct {
	ProviderID   string
	ProviderName string
	Reason       string // "hard_limit_exceeded", "encryption_incompatible", etc
}

// PlacementPlan describes where to store data.
type PlacementPlan struct {
	Placements        []PlannedPlacement
	RejectedProviders []RejectedProvider
	Rejected          bool
	Reason            string
	FileSize          int64
	FileName          string
}

// ProviderInfo contains provider details for planning.
type ProviderInfo struct {
	ID                 int64
	Name               string
	Type               string
	Status             string
	Priority           int
	SoftLimit          sql.NullInt64
	HardLimit          sql.NullInt64
	RequiresEncryption bool
	Remote             string
	FreeSpace          int64 // Live from GetUsage
}

// Plan creates a placement plan for a file.
// This computes TENTATIVE placement; must be revalidated at push time.
func (pp *PlacementPlanner) Plan(ctx context.Context, fileName string, fileSize int64, encrypted bool) (*PlacementPlan, error) {
	pp.mu.RLock()
	defer pp.mu.RUnlock()

	plan := &PlacementPlan{
		Placements:        make([]PlannedPlacement, 0),
		RejectedProviders: make([]RejectedProvider, 0),
		FileSize:          fileSize,
		FileName:          fileName,
	}

	// Get all active providers
	providers, err := pp.getActiveProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get providers: %w", err)
	}

	if len(providers) == 0 {
		plan.Rejected = true
		plan.Reason = "no active providers available"
		return plan, nil
	}

	// Fetch live free space for each provider
	for i := range providers {
		freeSpace, err := pp.getLiveFreeSpace(ctx, providers[i].Remote)
		if err != nil {
			providers[i].FreeSpace = 0 // Treat as unavailable
		} else {
			providers[i].FreeSpace = freeSpace
		}
	}

	// Evaluate each provider
	for _, p := range providers {
		// HARD CONSTRAINT 1: Encryption compatibility
		if p.RequiresEncryption && !encrypted {
			plan.RejectedProviders = append(plan.RejectedProviders, RejectedProvider{
				ProviderID:   fmt.Sprintf("%d", p.ID),
				ProviderName: p.Name,
				Reason:       "encryption_incompatible",
			})
			continue
		}

		// HARD CONSTRAINT 2: Hard limit
		if p.HardLimit.Valid && p.FreeSpace < fileSize {
			plan.RejectedProviders = append(plan.RejectedProviders, RejectedProvider{
				ProviderID:   fmt.Sprintf("%d", p.ID),
				ProviderName: p.Name,
				Reason:       fmt.Sprintf("hard_limit_exceeded (need %d, have %d)", fileSize, p.FreeSpace),
			})
			continue
		}

		// Calculate priority score
		reason := pp.calculateReason(p, fileSize)

		plan.Placements = append(plan.Placements, PlannedPlacement{
			ProviderID:   fmt.Sprintf("%d", p.ID),
			ProviderName: p.Name,
			RemotePath:   "/" + fileName,
			Priority:     p.Priority,
			Reason:       reason,
		})
	}

	// Sort by priority and free space
	pp.sortPlacements(plan.Placements)

	if len(plan.Placements) == 0 {
		plan.Rejected = true
		plan.Reason = "no suitable providers available"
	}

	return plan, nil
}

// Revalidate checks if a plan is still valid (called immediately before upload).
func (pp *PlacementPlanner) Revalidate(ctx context.Context, plan *PlacementPlan) error {
	pp.mu.RLock()
	defer pp.mu.RUnlock()

	for i, p := range plan.Placements {
		// Get provider remote
		var remote string
		err := pp.db.QueryRowContext(ctx, `
			SELECT value FROM provider_config 
			WHERE provider_id = ? AND key = 'remote'
		`, p.ProviderID).Scan(&remote)
		if err != nil {
			return fmt.Errorf("failed to get remote for %s: %w", p.ProviderName, err)
		}

		// Check live free space
		freeSpace, err := pp.getLiveFreeSpace(ctx, remote)
		if err != nil {
			return fmt.Errorf("failed to get free space for %s: %w", p.ProviderName, err)
		}

		if freeSpace < plan.FileSize {
			return fmt.Errorf("provider %s no longer has sufficient space (need %d, have %d)",
				p.ProviderName, plan.FileSize, freeSpace)
		}

		// Update reason with fresh data
		plan.Placements[i].Reason = fmt.Sprintf("revalidated: %d bytes free", freeSpace)
	}

	return nil
}

// Explain returns a human-readable explanation of the plan.
func (pp *PlacementPlanner) Explain(plan *PlacementPlan) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Placement Plan for: %s (%d bytes)\n", plan.FileName, plan.FileSize))
	sb.WriteString("═══════════════════════════════════════\n\n")

	if plan.Rejected {
		sb.WriteString(fmt.Sprintf("❌ REJECTED: %s\n", plan.Reason))
	} else {
		sb.WriteString("✓ Selected Providers:\n")
		for i, p := range plan.Placements {
			sb.WriteString(fmt.Sprintf("  %d. %s → %s\n     Reason: %s\n",
				i+1, p.ProviderName, p.RemotePath, p.Reason))
		}
	}

	if len(plan.RejectedProviders) > 0 {
		sb.WriteString("\n⚠️ Rejected Providers:\n")
		for _, r := range plan.RejectedProviders {
			sb.WriteString(fmt.Sprintf("  • %s: %s\n", r.ProviderName, r.Reason))
		}
	}

	return sb.String()
}

// getActiveProviders returns all active providers with their config.
func (pp *PlacementPlanner) getActiveProviders(ctx context.Context) ([]ProviderInfo, error) {
	rows, err := pp.db.QueryContext(ctx, `
		SELECT p.id, p.name, p.type, p.status, p.priority, p.soft_limit, p.hard_limit,
		       (SELECT value FROM provider_config WHERE provider_id = p.id AND key = 'remote') as remote
		FROM providers p
		WHERE p.status = 'active'
		ORDER BY p.priority
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []ProviderInfo
	for rows.Next() {
		var p ProviderInfo
		var remote sql.NullString
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.Status, &p.Priority,
			&p.SoftLimit, &p.HardLimit, &remote); err != nil {
			return nil, err
		}
		if remote.Valid {
			p.Remote = remote.String
		}
		providers = append(providers, p)
	}
	return providers, nil
}

// getLiveFreeSpace queries rclone for actual free space.
func (pp *PlacementPlanner) getLiveFreeSpace(ctx context.Context, remote string) (int64, error) {
	cmd := exec.CommandContext(ctx, "rclone", "about", remote, "--json")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	// Parse JSON output for "free" field
	// Simple parsing - in production use encoding/json
	outputStr := string(output)
	if idx := strings.Index(outputStr, `"free":`); idx != -1 {
		start := idx + 7
		end := strings.IndexAny(outputStr[start:], ",}")
		if end != -1 {
			freeStr := strings.TrimSpace(outputStr[start : start+end])
			free, err := strconv.ParseInt(freeStr, 10, 64)
			if err == nil {
				return free, nil
			}
		}
	}

	return 0, fmt.Errorf("failed to parse free space from rclone output")
}

// calculateReason determines the placement reason based on provider state.
func (pp *PlacementPlanner) calculateReason(p ProviderInfo, fileSize int64) string {
	if p.SoftLimit.Valid && (p.FreeSpace-fileSize) > p.SoftLimit.Int64 {
		return "under_soft_limit"
	}
	if p.FreeSpace > 1024*1024*1024*10 { // >10GB free
		return "most_free_space"
	}
	if p.Priority == 1 {
		return "primary_provider"
	}
	return "available"
}

// sortPlacements orders placements by priority (lower = better).
func (pp *PlacementPlanner) sortPlacements(placements []PlannedPlacement) {
	// Simple bubble sort for small N
	for i := 0; i < len(placements); i++ {
		for j := i + 1; j < len(placements); j++ {
			if placements[j].Priority < placements[i].Priority {
				placements[i], placements[j] = placements[j], placements[i]
			}
		}
	}
}
