// Package core provides the Request Queue for CloudFS.
// Based on design.txt Section: Multi-device (Phase 3).
//
// INVARIANTS:
// - Request-based sync (NO auto-sync)
// - Queue stored in index
// - One device executes operations at a time
// - Conflicts resolved via journal replay
// - NO background daemon
// - All operations explicit user action
package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// RequestQueue manages multi-device request-based sync.
type RequestQueue struct {
	db       *sql.DB
	journal  *JournalManager
	deviceID string
	mu       sync.RWMutex
}

// NewRequestQueue creates a new request queue.
func NewRequestQueue(db *sql.DB, journal *JournalManager, deviceID string) *RequestQueue {
	return &RequestQueue{
		db:       db,
		journal:  journal,
		deviceID: deviceID,
	}
}

// Request types
const (
	RequestTypePush = "push" // Push local changes to provider
	RequestTypePull = "pull" // Pull changes from provider
	RequestTypeSync = "sync" // Bidirectional sync
)

// Request states
const (
	RequestStatePending   = "pending"
	RequestStateRunning   = "running"
	RequestStateCompleted = "completed"
	RequestStateFailed    = "failed"
	RequestStateCancelled = "cancelled"
)

// SyncRequest represents a sync request in the queue.
type SyncRequest struct {
	ID          int64     `json:"id"`
	DeviceID    string    `json:"device_id"`
	RequestType string    `json:"request_type"`
	State       string    `json:"state"`
	Payload     string    `json:"payload"`
	Priority    int       `json:"priority"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// QueueStatus shows the current queue state.
type QueueStatus struct {
	DeviceID        string
	PendingRequests int
	RunningRequests int
	CompletedToday  int
	FailedToday     int
	OldestPending   *time.Time
}

// EnsureSchema creates the request_queue table if needed.
func (rq *RequestQueue) EnsureSchema(ctx context.Context) error {
	_, err := rq.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS request_queue (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			device_id       TEXT NOT NULL,
			request_type    TEXT NOT NULL CHECK(request_type IN ('push', 'pull', 'sync')),
			state           TEXT NOT NULL DEFAULT 'pending'
			                CHECK(state IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
			payload         TEXT NOT NULL DEFAULT '{}',
			priority        INTEGER NOT NULL DEFAULT 0,
			created_at      TEXT NOT NULL DEFAULT (datetime('now')),
			started_at      TEXT,
			completed_at    TEXT,
			error           TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_request_queue_state ON request_queue(state);
		CREATE INDEX IF NOT EXISTS idx_request_queue_device ON request_queue(device_id);
	`)
	return err
}

// CreatePushRequest creates a push request.
func (rq *RequestQueue) CreatePushRequest(ctx context.Context, entryIDs []int64) (*SyncRequest, error) {
	rq.mu.Lock()
	defer rq.mu.Unlock()

	payload, _ := json.Marshal(map[string]interface{}{
		"entry_ids": entryIDs,
	})

	// Journal the operation
	opID, err := rq.journal.BeginOperation(ctx, "request_push", string(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to begin journal: %w", err)
	}

	result, err := rq.db.ExecContext(ctx, `
		INSERT INTO request_queue (device_id, request_type, payload)
		VALUES (?, 'push', ?)
	`, rq.deviceID, string(payload))
	if err != nil {
		rq.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to create push request: %w", err)
	}

	id, _ := result.LastInsertId()

	rq.journal.CommitOperation(ctx, opID)
	rq.journal.SyncOperation(ctx, opID)

	return &SyncRequest{
		ID:          id,
		DeviceID:    rq.deviceID,
		RequestType: RequestTypePush,
		State:       RequestStatePending,
		Payload:     string(payload),
		CreatedAt:   time.Now(),
	}, nil
}

// CreatePullRequest creates a pull request.
func (rq *RequestQueue) CreatePullRequest(ctx context.Context, paths []string) (*SyncRequest, error) {
	rq.mu.Lock()
	defer rq.mu.Unlock()

	payload, _ := json.Marshal(map[string]interface{}{
		"paths": paths,
	})

	opID, err := rq.journal.BeginOperation(ctx, "request_pull", string(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to begin journal: %w", err)
	}

	result, err := rq.db.ExecContext(ctx, `
		INSERT INTO request_queue (device_id, request_type, payload)
		VALUES (?, 'pull', ?)
	`, rq.deviceID, string(payload))
	if err != nil {
		rq.journal.RollbackOperation(ctx, opID, err.Error())
		return nil, fmt.Errorf("failed to create pull request: %w", err)
	}

	id, _ := result.LastInsertId()

	rq.journal.CommitOperation(ctx, opID)
	rq.journal.SyncOperation(ctx, opID)

	return &SyncRequest{
		ID:          id,
		DeviceID:    rq.deviceID,
		RequestType: RequestTypePull,
		State:       RequestStatePending,
		Payload:     string(payload),
		CreatedAt:   time.Now(),
	}, nil
}

// GetStatus returns the current queue status.
func (rq *RequestQueue) GetStatus(ctx context.Context) (*QueueStatus, error) {
	rq.mu.RLock()
	defer rq.mu.RUnlock()

	status := &QueueStatus{DeviceID: rq.deviceID}

	// Count pending
	rq.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM request_queue WHERE state = 'pending'
	`).Scan(&status.PendingRequests)

	// Count running
	rq.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM request_queue WHERE state = 'running'
	`).Scan(&status.RunningRequests)

	// Count completed today
	rq.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM request_queue 
		WHERE state = 'completed' AND date(completed_at) = date('now')
	`).Scan(&status.CompletedToday)

	// Count failed today
	rq.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM request_queue 
		WHERE state = 'failed' AND date(completed_at) = date('now')
	`).Scan(&status.FailedToday)

	// Oldest pending
	var oldestStr sql.NullString
	rq.db.QueryRowContext(ctx, `
		SELECT MIN(created_at) FROM request_queue WHERE state = 'pending'
	`).Scan(&oldestStr)
	if oldestStr.Valid {
		t, _ := time.Parse(time.RFC3339, oldestStr.String)
		status.OldestPending = &t
	}

	return status, nil
}

// ListPending returns pending requests.
func (rq *RequestQueue) ListPending(ctx context.Context) ([]*SyncRequest, error) {
	rq.mu.RLock()
	defer rq.mu.RUnlock()

	return rq.listByState(ctx, RequestStatePending)
}

// ListAll returns all requests.
func (rq *RequestQueue) ListAll(ctx context.Context, limit int) ([]*SyncRequest, error) {
	rq.mu.RLock()
	defer rq.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	rows, err := rq.db.QueryContext(ctx, `
		SELECT id, device_id, request_type, state, payload, priority,
		       created_at, started_at, completed_at, error
		FROM request_queue
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list requests: %w", err)
	}
	defer rows.Close()

	return scanRequests(rows)
}

func (rq *RequestQueue) listByState(ctx context.Context, state string) ([]*SyncRequest, error) {
	rows, err := rq.db.QueryContext(ctx, `
		SELECT id, device_id, request_type, state, payload, priority,
		       created_at, started_at, completed_at, error
		FROM request_queue
		WHERE state = ?
		ORDER BY priority DESC, created_at ASC
	`, state)
	if err != nil {
		return nil, fmt.Errorf("failed to list requests: %w", err)
	}
	defer rows.Close()

	return scanRequests(rows)
}

func scanRequests(rows *sql.Rows) ([]*SyncRequest, error) {
	var requests []*SyncRequest
	for rows.Next() {
		var req SyncRequest
		var createdAt, startedAt, completedAt, errStr sql.NullString

		err := rows.Scan(
			&req.ID, &req.DeviceID, &req.RequestType, &req.State, &req.Payload,
			&req.Priority, &createdAt, &startedAt, &completedAt, &errStr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan request: %w", err)
		}

		req.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
		if startedAt.Valid {
			t, _ := time.Parse(time.RFC3339, startedAt.String)
			req.StartedAt = &t
		}
		if completedAt.Valid {
			t, _ := time.Parse(time.RFC3339, completedAt.String)
			req.CompletedAt = &t
		}
		if errStr.Valid {
			req.Error = errStr.String
		}

		requests = append(requests, &req)
	}

	return requests, nil
}

// GetDeviceID returns the current device ID.
func (rq *RequestQueue) GetDeviceID() string {
	return rq.deviceID
}

// GenerateDeviceID creates a unique device identifier.
func GenerateDeviceID() string {
	hostname, _ := os.Hostname()
	return fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano()%100000)
}

// CancelRequest cancels a pending request.
func (rq *RequestQueue) CancelRequest(ctx context.Context, requestID int64) error {
	rq.mu.Lock()
	defer rq.mu.Unlock()

	result, err := rq.db.ExecContext(ctx, `
		UPDATE request_queue SET state = 'cancelled', completed_at = datetime('now')
		WHERE id = ? AND state = 'pending'
	`, requestID)
	if err != nil {
		return fmt.Errorf("failed to cancel request: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("request not found or not pending: %d", requestID)
	}

	return nil
}
