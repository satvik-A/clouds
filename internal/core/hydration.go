// Package core provides the Hydration Controller for CloudFS.
// Based on design.txt Section 14: Hydration is explicit intent only.
//
// INVARIANTS:
// - Hydration triggered ONLY by explicit CLI commands (hydrate, pin)
// - Downloads go to cache ONLY, never directly to filesystem
// - Hash verification BEFORE atomic placeholder swap
// - Partial downloads NEVER appear in filesystem view
// - All operations recorded in journal
package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cloudfs/cloudfs/internal/model"
	"github.com/cloudfs/cloudfs/internal/provider"
)

// HydrationController manages the hydration workflow.
// Hydration = downloading real file data and making it available locally.
type HydrationController struct {
	index       *IndexManager
	cache       *CacheManager
	placeholder *PlaceholderManager
	journal     *JournalManager
	registry    provider.Registry
	db          *sql.DB
	mu          sync.Mutex
}

// HydrationResult contains the result of a hydration operation.
type HydrationResult struct {
	EntryID     int64
	VersionID   int64
	Success     bool
	Cancelled   bool
	Error       string
	BytesLoaded int64
	Duration    time.Duration
}

// HydrationOptions configures a hydration operation.
type HydrationOptions struct {
	Pin          bool                         // Pin after hydration
	ProgressFunc func(entryID int64, percent int) // Progress callback
}

// NewHydrationController creates a new hydration controller.
func NewHydrationController(
	index *IndexManager,
	cache *CacheManager,
	placeholder *PlaceholderManager,
	journal *JournalManager,
	registry provider.Registry,
	db *sql.DB,
) *HydrationController {
	return &HydrationController{
		index:       index,
		cache:       cache,
		placeholder: placeholder,
		journal:     journal,
		registry:    registry,
		db:          db,
	}
}

// Hydrate downloads and materializes a file.
// This is the ONLY path for hydration - no filesystem-triggered downloads.
//
// Flow:
// 1. Journal entry (pending)
// 2. Update hydration_state to 'hydrating'
// 3. Download to cache
// 4. Verify hash
// 5. Atomic placeholder swap
// 6. Update hydration_state to 'hydrated'
// 7. Journal entry (synced)
func (hc *HydrationController) Hydrate(ctx context.Context, entryID int64, opts *HydrationOptions) (*HydrationResult, error) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	start := time.Now()
	result := &HydrationResult{EntryID: entryID}

	// Step 1: Get entry from index
	entry, err := hc.index.GetEntry(ctx, entryID)
	if err != nil {
		return nil, fmt.Errorf("failed to get entry: %w", err)
	}
	if entry == nil {
		return nil, fmt.Errorf("entry not found: %d", entryID)
	}
	if entry.Type == model.EntryTypeDirectory {
		return nil, fmt.Errorf("cannot hydrate directory")
	}

	// Step 2: Get active version
	version, err := hc.index.GetActiveVersion(ctx, entryID)
	if err != nil {
		return nil, fmt.Errorf("failed to get version: %w", err)
	}
	if version == nil {
		return nil, fmt.Errorf("no active version for entry: %d", entryID)
	}
	result.VersionID = version.ID

	// Step 3: Check if already hydrated
	hydrationState, err := hc.getHydrationState(ctx, entryID)
	if err == nil && hydrationState != nil && hydrationState.CurrentState == model.HydrationStateHydrated {
		// Already hydrated
		result.Success = true
		result.Duration = time.Since(start)
		return result, nil
	}

	// Step 4: Get placement info (where is the data?)
	placement, err := hc.getPlacement(ctx, version.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get placement: %w", err)
	}
	if placement == nil {
		return nil, fmt.Errorf("no placement found for version: %d", version.ID)
	}

	// Step 5: Get provider
	prov, ok := hc.registry.Get(placement.ProviderID)
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", placement.ProviderID)
	}

	// Step 6: Begin journal operation
	payload, _ := json.Marshal(map[string]interface{}{
		"entry_id":    entryID,
		"version_id":  version.ID,
		"provider_id": placement.ProviderID,
		"remote_path": placement.RemotePath,
	})
	opID, err := hc.journal.BeginOperation(ctx, "hydrate", string(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to begin journal: %w", err)
	}

	// Step 7: Update hydration state to 'hydrating'
	if err := hc.setHydrationState(ctx, entryID, model.HydrationStateHydrating, nil, 0); err != nil {
		hc.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to update hydration state: %w", err)
	}

	// Step 8: Download to cache (temp file)
	tempPath := fmt.Sprintf("%s/.cloudfs/temp/%d_%d_%d", os.Getenv("HOME"), entryID, version.ID, time.Now().UnixNano())
	if err := os.MkdirAll(os.Getenv("HOME")+"/.cloudfs/temp", 0700); err != nil {
		hc.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	// Progress wrapper
	var progressFunc provider.ProgressFunc
	if opts != nil && opts.ProgressFunc != nil {
		progressFunc = func(p float64) {
			percent := int(p * 100)
			opts.ProgressFunc(entryID, percent)
			hc.setHydrationState(ctx, entryID, model.HydrationStateHydrating, nil, percent)
		}
	}

	downloadResult, err := prov.Download(ctx, placement.RemotePath, tempPath, progressFunc)
	if err != nil {
		hc.setHydrationState(ctx, entryID, model.HydrationStatePlaceholder, nil, 0)
		hc.journal.RollbackOperation(ctx, opID, err.Error())
		os.Remove(tempPath)
		return nil, fmt.Errorf("download failed: %w", err)
	}
	result.BytesLoaded = downloadResult.Size

	// Step 9: Verify hash BEFORE any filesystem changes
	if downloadResult.ContentHash != "" && version.ContentHash != "" {
		if downloadResult.ContentHash != version.ContentHash {
			hc.setHydrationState(ctx, entryID, model.HydrationStatePlaceholder, nil, 0)
			hc.journal.RollbackOperation(ctx, opID, "hash mismatch")
			os.Remove(tempPath)
			return nil, fmt.Errorf("hash verification failed: expected %s, got %s", version.ContentHash, downloadResult.ContentHash)
		}
	}

	// Step 10: Add to cache
	cacheEntry, err := hc.cache.Put(ctx, entryID, version.ID, tempPath)
	if err != nil {
		hc.setHydrationState(ctx, entryID, model.HydrationStatePlaceholder, nil, 0)
		hc.journal.RollbackOperation(ctx, opID, err.Error())
		os.Remove(tempPath)
		return nil, fmt.Errorf("failed to cache file: %w", err)
	}

	// Step 11: Atomic placeholder swap
	err = hc.placeholder.AtomicSwap(ctx, entry, cacheEntry.CachePath, version.ContentHash, "")
	if err != nil {
		hc.setHydrationState(ctx, entryID, model.HydrationStatePlaceholder, nil, 0)
		hc.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to swap placeholder: %w", err)
	}

	// Step 12: Update hydration state to 'hydrated'
	now := time.Now()
	if err := hc.setHydrationState(ctx, entryID, model.HydrationStateHydrated, &version.ID, 100); err != nil {
		// Non-fatal - file is already swapped
		fmt.Printf("warning: failed to update hydration state: %v\n", err)
	}
	_ = now // Used for LastHydrated

	// Step 13: Pin if requested
	if opts != nil && opts.Pin {
		if err := hc.cache.Pin(ctx, entryID); err != nil {
			fmt.Printf("warning: failed to pin entry: %v\n", err)
		}
	}

	// Step 14: Complete journal
	if err := hc.journal.CommitOperation(ctx, opID); err != nil {
		fmt.Printf("warning: failed to commit journal: %v\n", err)
	}
	if err := hc.journal.SyncOperation(ctx, opID); err != nil {
		fmt.Printf("warning: failed to sync journal: %v\n", err)
	}

	result.Success = true
	result.Duration = time.Since(start)
	return result, nil
}

// Dehydrate removes local file data, keeping the placeholder.
func (hc *HydrationController) Dehydrate(ctx context.Context, entryID int64) error {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Get entry
	entry, err := hc.index.GetEntry(ctx, entryID)
	if err != nil {
		return fmt.Errorf("failed to get entry: %w", err)
	}
	if entry == nil {
		return fmt.Errorf("entry not found: %d", entryID)
	}

	// Get version
	version, err := hc.index.GetActiveVersion(ctx, entryID)
	if err != nil {
		return fmt.Errorf("failed to get version: %w", err)
	}
	if version == nil {
		return fmt.Errorf("no active version for entry: %d", entryID)
	}

	// Get placement
	placement, err := hc.getPlacement(ctx, version.ID)
	if err != nil || placement == nil {
		return fmt.Errorf("no placement found for version: %d", version.ID)
	}

	// Begin journal
	payload, _ := json.Marshal(map[string]interface{}{
		"entry_id":   entryID,
		"version_id": version.ID,
	})
	opID, err := hc.journal.BeginOperation(ctx, "dehydrate", string(payload))
	if err != nil {
		return fmt.Errorf("failed to begin journal: %w", err)
	}

	// Dehydrate (reverts to placeholder)
	err = hc.placeholder.Dehydrate(ctx, entry, version, "", placement.ProviderID, placement.RemotePath)
	if err != nil {
		hc.journal.RollbackOperation(ctx, opID, err.Error())
		return fmt.Errorf("failed to dehydrate: %w", err)
	}

	// Update hydration state
	if err := hc.setHydrationState(ctx, entryID, model.HydrationStatePlaceholder, nil, 0); err != nil {
		fmt.Printf("warning: failed to update hydration state: %v\n", err)
	}

	// Complete journal
	hc.journal.CommitOperation(ctx, opID)
	hc.journal.SyncOperation(ctx, opID)

	return nil
}

// GetHydrationState returns the current hydration state for an entry.
func (hc *HydrationController) GetHydrationState(ctx context.Context, entryID int64) (*model.Hydration, error) {
	return hc.getHydrationState(ctx, entryID)
}

// getHydrationState reads hydration state from database.
func (hc *HydrationController) getHydrationState(ctx context.Context, entryID int64) (*model.Hydration, error) {
	query := `
		SELECT entry_id, current_state, hydrated_version_id, hydration_progress, last_hydrated
		FROM hydration_state WHERE entry_id = ?
	`
	row := hc.db.QueryRowContext(ctx, query, entryID)

	var h model.Hydration
	var lastHydrated sql.NullString
	var versionID sql.NullInt64
	err := row.Scan(&h.EntryID, &h.CurrentState, &versionID, &h.HydrationProgress, &lastHydrated)
	if err == sql.ErrNoRows {
		// Default state is placeholder
		return &model.Hydration{
			EntryID:           entryID,
			CurrentState:      model.HydrationStatePlaceholder,
			HydrationProgress: 0,
		}, nil
	}
	if err != nil {
		return nil, err
	}

	if versionID.Valid {
		h.HydratedVersionID = &versionID.Int64
	}
	if lastHydrated.Valid {
		t, _ := time.Parse(time.RFC3339, lastHydrated.String)
		h.LastHydrated = &t
	}

	return &h, nil
}

// setHydrationState updates hydration state in database.
func (hc *HydrationController) setHydrationState(ctx context.Context, entryID int64, state model.HydrationState, versionID *int64, progress int) error {
	query := `
		INSERT INTO hydration_state (entry_id, current_state, hydrated_version_id, hydration_progress, last_hydrated)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(entry_id) DO UPDATE SET
			current_state = excluded.current_state,
			hydrated_version_id = excluded.hydrated_version_id,
			hydration_progress = excluded.hydration_progress,
			last_hydrated = excluded.last_hydrated
	`
	_, err := hc.db.ExecContext(ctx, query, entryID, state, versionID, progress)
	return err
}

// getPlacement gets the primary placement for a version.
func (hc *HydrationController) getPlacement(ctx context.Context, versionID int64) (*model.Placement, error) {
	query := `
		SELECT id, chunk_id, version_id, provider_id, remote_path, uploaded_at, verified_at, state
		FROM placements WHERE version_id = ? AND state IN ('uploaded', 'verified')
		ORDER BY verified_at DESC LIMIT 1
	`
	row := hc.db.QueryRowContext(ctx, query, versionID)

	var p model.Placement
	var chunkID, verID sql.NullInt64
	var verifiedAt sql.NullString
	var uploadedAt string

	err := row.Scan(&p.ID, &chunkID, &verID, &p.ProviderID, &p.RemotePath, &uploadedAt, &verifiedAt, &p.State)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if chunkID.Valid {
		p.ChunkID = &chunkID.Int64
	}
	if verID.Valid {
		p.VersionID = &verID.Int64
	}
	p.UploadedAt, _ = time.Parse(time.RFC3339, uploadedAt)
	if verifiedAt.Valid {
		t, _ := time.Parse(time.RFC3339, verifiedAt.String)
		p.VerifiedAt = &t
	}

	return &p, nil
}

// HydrateBatch hydrates multiple entries.
func (hc *HydrationController) HydrateBatch(ctx context.Context, entryIDs []int64, opts *HydrationOptions) ([]*HydrationResult, error) {
	var results []*HydrationResult

	for _, id := range entryIDs {
		select {
		case <-ctx.Done():
			// Cancelled
			for _, remaining := range entryIDs[len(results):] {
				results = append(results, &HydrationResult{
					EntryID:   remaining,
					Cancelled: true,
				})
			}
			return results, ctx.Err()
		default:
		}

		result, err := hc.Hydrate(ctx, id, opts)
		if err != nil {
			results = append(results, &HydrationResult{
				EntryID: id,
				Success: false,
				Error:   err.Error(),
			})
		} else {
			results = append(results, result)
		}
	}

	return results, nil
}
