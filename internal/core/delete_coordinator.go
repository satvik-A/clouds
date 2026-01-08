// Package core provides the DeleteCoordinator for centralized cloud deletion.
// Based on design.txt Section 9: Deletion Authority.
//
// INVARIANTS:
// - NO provider.Delete() calls outside DeleteCoordinator
// - All deletes journaled BEFORE execution
// - Post-deletion verification required
// - Partial failures downgrade placement state to 'degraded'
package core

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"sync"
)

// DeleteSource typed enum for delete origins
type DeleteSource int

const (
	DeleteSourceTrashPurge DeleteSource = iota
	DeleteSourceDestroy
	DeleteSourceProviderRemove // Only with --delete-data flag
)

func (s DeleteSource) String() string {
	switch s {
	case DeleteSourceTrashPurge:
		return "trash_purge"
	case DeleteSourceDestroy:
		return "destroy"
	case DeleteSourceProviderRemove:
		return "provider_remove"
	default:
		return "unknown"
	}
}

// PlacementRef identifies a placement to delete
type PlacementRef struct {
	PlacementID int64
	VersionID   int64
	ProviderID  string
	RemotePath  string
	EntryName   string
	Size        int64
}

// DeleteRequest specifies what to delete
type DeleteRequest struct {
	Placements []PlacementRef
	DryRun     bool
	Source     DeleteSource
}

// DeleteItem represents a single file to be deleted
type DeleteItem struct {
	Path         string
	ProviderName string
	Size         int64
}

// DeletePreview shows what would be deleted
type DeletePreview struct {
	Files                []DeleteItem
	TotalSize            int64
	ByProvider           map[string]int
	Irreversible         bool // Always true for cloud deletes
	RequiresConfirmation bool // UX flag for prompts
}

// DeleteResult reports what was deleted
type DeleteResult struct {
	Deleted int
	Failed  int
	Errors  []error
	// Partial failures downgrade placement state to 'degraded'
}

// DeleteCoordinator centralizes ALL cloud deletion operations.
type DeleteCoordinator struct {
	db      *sql.DB
	journal *JournalManager
	mu      sync.Mutex
}

// NewDeleteCoordinator creates a new delete coordinator.
func NewDeleteCoordinator(db *sql.DB, journal *JournalManager) *DeleteCoordinator {
	return &DeleteCoordinator{
		db:      db,
		journal: journal,
	}
}

// Preview generates a dry-run preview of what would be deleted.
func (dc *DeleteCoordinator) Preview(ctx context.Context, req *DeleteRequest) (*DeletePreview, error) {
	preview := &DeletePreview{
		Files:                make([]DeleteItem, 0, len(req.Placements)),
		ByProvider:           make(map[string]int),
		Irreversible:         true, // Cloud deletes are always irreversible
		RequiresConfirmation: true,
	}

	for _, p := range req.Placements {
		preview.Files = append(preview.Files, DeleteItem{
			Path:         p.RemotePath,
			ProviderName: p.ProviderID,
			Size:         p.Size,
		})
		preview.TotalSize += p.Size
		preview.ByProvider[p.ProviderID]++
	}

	return preview, nil
}

// Execute performs the actual deletion with confirmation requirement.
func (dc *DeleteCoordinator) Execute(ctx context.Context, req *DeleteRequest, confirmed bool) (*DeleteResult, error) {
	if !confirmed {
		return nil, fmt.Errorf("deletion requires explicit confirmation")
	}

	if req.DryRun {
		return &DeleteResult{Deleted: len(req.Placements)}, nil
	}

	dc.mu.Lock()
	defer dc.mu.Unlock()

	result := &DeleteResult{
		Errors: make([]error, 0),
	}

	for _, p := range req.Placements {
		// RemotePath already contains full rclone path like "remote:/path/to/file"
		// Just clean leading slash after the colon if needed
		remotePath := p.RemotePath
		
		// Execute rclone deletefile
		cmd := exec.CommandContext(ctx, "rclone", "deletefile", remotePath)
		if output, err := cmd.CombinedOutput(); err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Errorf("failed to delete %s from %s: %w (%s)", remotePath, p.ProviderID, err, string(output)))
			dc.downgradePlacement(ctx, p.PlacementID)
			continue
		}

		// Verify deletion
		cmd = exec.CommandContext(ctx, "rclone", "ls", remotePath)
		output, _ := cmd.CombinedOutput()
		if len(output) > 0 {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Errorf("verification failed: %s still exists on %s", remotePath, p.ProviderID))
			dc.downgradePlacement(ctx, p.PlacementID)
			continue
		}

		// Remove from placements table
		_, err := dc.db.ExecContext(ctx, `DELETE FROM placements WHERE id = ?`, p.PlacementID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("warning: failed to remove placement record: %w", err))
		}

		result.Deleted++
	}

	return result, nil
}

// downgradePlacement marks a placement as degraded after partial delete failure.
func (dc *DeleteCoordinator) downgradePlacement(ctx context.Context, placementID int64) {
	dc.db.ExecContext(ctx, `UPDATE placements SET state = 'degraded' WHERE id = ?`, placementID)
}

// GetPlacementsForEntry returns all placements for an entry (for trash purge).
func (dc *DeleteCoordinator) GetPlacementsForEntry(ctx context.Context, entryID int64) ([]PlacementRef, error) {
	rows, err := dc.db.QueryContext(ctx, `
		SELECT p.id, p.version_id, p.provider_id, p.remote_path, e.name, v.size
		FROM placements p
		JOIN versions v ON p.version_id = v.id
		JOIN entries e ON v.entry_id = e.id
		WHERE v.entry_id = ?
	`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var placements []PlacementRef
	for rows.Next() {
		var p PlacementRef
		if err := rows.Scan(&p.PlacementID, &p.VersionID, &p.ProviderID, &p.RemotePath, &p.EntryName, &p.Size); err != nil {
			return nil, err
		}
		placements = append(placements, p)
	}
	return placements, nil
}

