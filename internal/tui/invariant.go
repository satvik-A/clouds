// Package tui provides invariant checking for CloudFS TUI.
// All mutations must pass invariant checks before and after execution.
package tui

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/cloudfs/cloudfs/internal/core"
)

// InvariantChecker verifies CloudFS invariants
type InvariantChecker struct {
	db *core.EncryptedDB
}

// InvariantViolation represents a detected invariant violation
type InvariantViolation struct {
	Invariant   string
	Description string
	Severity    Severity
	EntryID     int64
	ProviderID  int64
	Details     string
}

// Severity of an invariant violation
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityError
	SeverityCritical
)

func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "INFO"
	case SeverityWarning:
		return "WARNING"
	case SeverityError:
		return "ERROR"
	case SeverityCritical:
		return "CRITICAL"
	}
	return "UNKNOWN"
}

// NewInvariantChecker creates a new invariant checker
func NewInvariantChecker(db *core.EncryptedDB) *InvariantChecker {
	return &InvariantChecker{db: db}
}

// CheckAll runs all invariant checks
func (ic *InvariantChecker) CheckAll(ctx context.Context) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	// Check each invariant category
	checks := []func(context.Context) ([]InvariantViolation, error){
		ic.checkIndexIntegrity,
		ic.checkVersionConsistency,
		ic.checkPlacementValidity,
		ic.checkCacheConsistency,
		ic.checkJournalState,
		ic.checkTrashConsistency,
		ic.checkProviderHealth,
	}

	for _, check := range checks {
		v, err := check(ctx)
		if err != nil {
			return nil, err
		}
		violations = append(violations, v...)
	}

	return violations, nil
}

// Pre-mutation check: verify prerequisites are met
func (ic *InvariantChecker) PreCheck(ctx context.Context, action Action, params ActionParams) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	switch action {
	case ActionDehydrate:
		// Must have at least one provider placement before dehydrating
		v, err := ic.checkDehydratePrereq(ctx, params.EntryID)
		if err != nil {
			return nil, err
		}
		violations = append(violations, v...)

	case ActionRemove:
		// Entry must exist
		v, err := ic.checkEntryExists(ctx, params.EntryID)
		if err != nil {
			return nil, err
		}
		violations = append(violations, v...)

	case ActionCacheEvict:
		// Must not be pinned
		v, err := ic.checkNotPinned(ctx, params.EntryID)
		if err != nil {
			return nil, err
		}
		violations = append(violations, v...)
	}

	return violations, nil
}

// Post-mutation check: verify expected state was achieved
func (ic *InvariantChecker) PostCheck(ctx context.Context, action Action, params ActionParams) ([]InvariantViolation, error) {
	// After any mutation, verify core invariants
	return ic.CheckAll(ctx)
}

// Individual invariant checks

func (ic *InvariantChecker) checkIndexIntegrity(ctx context.Context) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	// Check for orphaned versions (versions without entries)
	rows, err := ic.db.DB().QueryContext(ctx, `
		SELECT v.id, v.entry_id 
		FROM versions v 
		LEFT JOIN entries e ON v.entry_id = e.id 
		WHERE e.id IS NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var versionID, entryID int64
		rows.Scan(&versionID, &entryID)
		violations = append(violations, InvariantViolation{
			Invariant:   "INDEX_ORPHAN_VERSION",
			Description: fmt.Sprintf("Version %d references non-existent entry %d", versionID, entryID),
			Severity:    SeverityError,
			EntryID:     entryID,
		})
	}

	// Check for entries without any version
	rows2, err := ic.db.DB().QueryContext(ctx, `
		SELECT e.id, e.name 
		FROM entries e 
		LEFT JOIN versions v ON e.id = v.entry_id 
		WHERE v.id IS NULL AND e.type = 'file'
	`)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()

	for rows2.Next() {
		var entryID int64
		var name string
		rows2.Scan(&entryID, &name)
		violations = append(violations, InvariantViolation{
			Invariant:   "INDEX_NO_VERSION",
			Description: fmt.Sprintf("File entry '%s' has no versions", name),
			Severity:    SeverityWarning,
			EntryID:     entryID,
		})
	}

	return violations, nil
}

func (ic *InvariantChecker) checkVersionConsistency(ctx context.Context) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	// Check for multiple active versions per entry
	rows, err := ic.db.DB().QueryContext(ctx, `
		SELECT entry_id, COUNT(*) as active_count 
		FROM versions 
		WHERE state = 'active' 
		GROUP BY entry_id 
		HAVING active_count > 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var entryID int64
		var count int
		rows.Scan(&entryID, &count)
		violations = append(violations, InvariantViolation{
			Invariant:   "VERSION_MULTIPLE_ACTIVE",
			Description: fmt.Sprintf("Entry %d has %d active versions (should be 1)", entryID, count),
			Severity:    SeverityCritical,
			EntryID:     entryID,
		})
	}

	return violations, nil
}

func (ic *InvariantChecker) checkPlacementValidity(ctx context.Context) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	// Check for placements on inactive providers
	rows, err := ic.db.DB().QueryContext(ctx, `
		SELECT p.id, p.version_id, pr.name, pr.status
		FROM placements p
		JOIN providers pr ON p.provider_id = pr.id
		WHERE pr.status != 'active'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var placementID, versionID int64
		var providerName, status string
		rows.Scan(&placementID, &versionID, &providerName, &status)
		violations = append(violations, InvariantViolation{
			Invariant:   "PLACEMENT_INACTIVE_PROVIDER",
			Description: fmt.Sprintf("Placement %d on inactive provider '%s' (status: %s)", placementID, providerName, status),
			Severity:    SeverityWarning,
			Details:     fmt.Sprintf("version_id=%d", versionID),
		})
	}

	// Check for orphaned placements (placements without versions)
	rows2, err := ic.db.DB().QueryContext(ctx, `
		SELECT p.id, p.version_id
		FROM placements p
		LEFT JOIN versions v ON p.version_id = v.id
		WHERE v.id IS NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()

	for rows2.Next() {
		var placementID, versionID int64
		rows2.Scan(&placementID, &versionID)
		violations = append(violations, InvariantViolation{
			Invariant:   "PLACEMENT_ORPHAN",
			Description: fmt.Sprintf("Placement %d references non-existent version %d", placementID, versionID),
			Severity:    SeverityError,
		})
	}

	return violations, nil
}

func (ic *InvariantChecker) checkCacheConsistency(ctx context.Context) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	// Check for cache entries without index entries
	rows, err := ic.db.DB().QueryContext(ctx, `
		SELECT c.id, c.entry_id
		FROM cache_entries c
		LEFT JOIN entries e ON c.entry_id = e.id
		WHERE e.id IS NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var cacheID, entryID int64
		rows.Scan(&cacheID, &entryID)
		violations = append(violations, InvariantViolation{
			Invariant:   "CACHE_ORPHAN",
			Description: fmt.Sprintf("Cache entry %d references non-existent entry %d", cacheID, entryID),
			Severity:    SeverityError,
			EntryID:     entryID,
		})
	}

	return violations, nil
}

func (ic *InvariantChecker) checkJournalState(ctx context.Context) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	// Check for stuck journal entries (pending for too long)
	rows, err := ic.db.DB().QueryContext(ctx, `
		SELECT id, operation, started_at
		FROM journal
		WHERE state = 'pending'
		AND datetime(started_at) < datetime('now', '-1 hour')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var operation, startedAt string
		rows.Scan(&id, &operation, &startedAt)
		violations = append(violations, InvariantViolation{
			Invariant:   "JOURNAL_STUCK",
			Description: fmt.Sprintf("Journal operation %d (%s) stuck since %s", id, operation, startedAt),
			Severity:    SeverityWarning,
		})
	}

	return violations, nil
}

func (ic *InvariantChecker) checkTrashConsistency(ctx context.Context) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	// Check for trash items with missing original entries
	rows, err := ic.db.DB().QueryContext(ctx, `
		SELECT t.id, t.original_entry_id, t.original_path
		FROM trash t
		LEFT JOIN entries e ON t.original_entry_id = e.id
		WHERE e.id IS NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var trashID, entryID int64
		var path string
		rows.Scan(&trashID, &entryID, &path)
		violations = append(violations, InvariantViolation{
			Invariant:   "TRASH_ORPHAN",
			Description: fmt.Sprintf("Trash item %d (%s) references non-existent entry", trashID, path),
			Severity:    SeverityWarning,
			EntryID:     entryID,
		})
	}

	return violations, nil
}

func (ic *InvariantChecker) checkProviderHealth(ctx context.Context) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	// Check for providers with no remote configured
	rows, err := ic.db.DB().QueryContext(ctx, `
		SELECT p.id, p.name
		FROM providers p
		LEFT JOIN provider_config pc ON p.id = pc.provider_id AND pc.key = 'remote'
		WHERE pc.value IS NULL OR pc.value = ''
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var name string
		rows.Scan(&id, &name)
		violations = append(violations, InvariantViolation{
			Invariant:   "PROVIDER_NO_REMOTE",
			Description: fmt.Sprintf("Provider '%s' has no remote configured", name),
			Severity:    SeverityError,
			ProviderID:  id,
		})
	}

	return violations, nil
}

// Prerequisite checks for specific actions

func (ic *InvariantChecker) checkDehydratePrereq(ctx context.Context, entryID int64) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	var count int
	err := ic.db.DB().QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM placements p
		JOIN versions v ON p.version_id = v.id
		WHERE v.entry_id = ? AND v.state = 'active'
	`, entryID).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	if count == 0 {
		violations = append(violations, InvariantViolation{
			Invariant:   "DEHYDRATE_NO_PLACEMENT",
			Description: "Cannot dehydrate: no provider placement exists",
			Severity:    SeverityError,
			EntryID:     entryID,
		})
	}

	return violations, nil
}

func (ic *InvariantChecker) checkEntryExists(ctx context.Context, entryID int64) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	var exists int
	err := ic.db.DB().QueryRowContext(ctx, `SELECT 1 FROM entries WHERE id = ?`, entryID).Scan(&exists)
	if err == sql.ErrNoRows {
		violations = append(violations, InvariantViolation{
			Invariant:   "ENTRY_NOT_FOUND",
			Description: fmt.Sprintf("Entry %d does not exist", entryID),
			Severity:    SeverityError,
			EntryID:     entryID,
		})
	} else if err != nil {
		return nil, err
	}

	return violations, nil
}

func (ic *InvariantChecker) checkNotPinned(ctx context.Context, entryID int64) ([]InvariantViolation, error) {
	var violations []InvariantViolation

	var pinned int
	err := ic.db.DB().QueryRowContext(ctx, `
		SELECT COALESCE(pinned, 0) FROM cache_entries WHERE entry_id = ?
	`, entryID).Scan(&pinned)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	if pinned == 1 {
		violations = append(violations, InvariantViolation{
			Invariant:   "CACHE_PINNED",
			Description: "Cannot evict: entry is pinned",
			Severity:    SeverityError,
			EntryID:     entryID,
		})
	}

	return violations, nil
}

// FormatViolations formats violations for display
func FormatViolations(violations []InvariantViolation) string {
	if len(violations) == 0 {
		return "✓ All invariants satisfied"
	}

	var sb strings.Builder
	for _, v := range violations {
		icon := "•"
		switch v.Severity {
		case SeverityCritical:
			icon = "✗"
		case SeverityError:
			icon = "✗"
		case SeverityWarning:
			icon = "⚠"
		case SeverityInfo:
			icon = "ℹ"
		}
		sb.WriteString(fmt.Sprintf("%s [%s] %s: %s\n", icon, v.Severity, v.Invariant, v.Description))
	}
	return sb.String()
}
