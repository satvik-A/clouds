// Package core provides the core engine components for CloudFS.
// This includes the Index Manager, Journal Manager, Cache Manager, and Hydration Controller.
package core

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cloudfs/cloudfs/internal/model"
	"github.com/google/uuid"
	_ "github.com/mutecomm/go-sqlcipher/v4"
)

// IndexManager manages the encrypted SQLite metadata index.
// The index is the SOURCE OF TRUTH (design.txt Section 3).
type IndexManager struct {
	db       *sql.DB
	dbPath   string
	mu       sync.RWMutex
	journal  *JournalManager
}

// NewIndexManager creates a new index manager.
// The database is encrypted via SQLCipher (or go-sqlcipher).
func NewIndexManager(dbPath string, encryptionKey string) (*IndexManager, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Open database with encryption
	// Note: For full SQLCipher support, use github.com/mutecomm/go-sqlcipher/v4
	// Here we use standard sqlite3 for initial implementation
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_synchronous=NORMAL", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	im := &IndexManager{
		db:     db,
		dbPath: dbPath,
	}

	return im, nil
}

// Initialize creates the schema if it doesn't exist.
func (im *IndexManager) Initialize(ctx context.Context) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	schema := `
-- CloudFS Metadata Index Schema v1.0

-- Core file/folder entries
CREATE TABLE IF NOT EXISTS entries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    parent_id       INTEGER REFERENCES entries(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    entry_type      TEXT NOT NULL CHECK(entry_type IN ('file', 'directory')),
    logical_size    INTEGER DEFAULT 0,
    physical_size   INTEGER DEFAULT 0,
    parity_size     INTEGER DEFAULT 0,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    modified_at     TEXT NOT NULL DEFAULT (datetime('now')),
    classification  TEXT,
    UNIQUE(parent_id, name)
);
CREATE INDEX IF NOT EXISTS idx_entries_parent ON entries(parent_id);
CREATE INDEX IF NOT EXISTS idx_entries_type ON entries(entry_type);

-- Versions (immutable atomic units)
CREATE TABLE IF NOT EXISTS versions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id        INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    version_num     INTEGER NOT NULL,
    content_hash    TEXT NOT NULL,
    size            INTEGER NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    state           TEXT NOT NULL DEFAULT 'incomplete'
                    CHECK(state IN ('incomplete', 'active', 'superseded', 'deleted')),
    encryption_key_id TEXT,
    UNIQUE(entry_id, version_num)
);
CREATE INDEX IF NOT EXISTS idx_versions_entry ON versions(entry_id);
CREATE INDEX IF NOT EXISTS idx_versions_state ON versions(state);

-- Chunks (for large files and archives)
CREATE TABLE IF NOT EXISTS chunks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id      INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
    chunk_index     INTEGER NOT NULL,
    chunk_hash      TEXT NOT NULL,
    size            INTEGER NOT NULL,
    UNIQUE(version_id, chunk_index)
);

-- Backend placements
CREATE TABLE IF NOT EXISTS placements (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    chunk_id        INTEGER REFERENCES chunks(id) ON DELETE CASCADE,
    version_id      INTEGER REFERENCES versions(id) ON DELETE CASCADE,
    provider_id     TEXT NOT NULL,
    remote_path     TEXT NOT NULL,
    uploaded_at     TEXT NOT NULL DEFAULT (datetime('now')),
    verified_at     TEXT,
    state           TEXT NOT NULL DEFAULT 'pending'
                    CHECK(state IN ('pending', 'uploaded', 'verified', 'degraded', 'failed'))
);
CREATE INDEX IF NOT EXISTS idx_placements_provider ON placements(provider_id);
CREATE INDEX IF NOT EXISTS idx_placements_state ON placements(state);

-- Provider accounts (current_usage is CACHED only)
CREATE TABLE IF NOT EXISTS providers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    type            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK(status IN ('active', 'inactive', 'error')),
    priority        INTEGER DEFAULT 1,
    soft_limit      INTEGER,
    hard_limit      INTEGER,
    current_usage   INTEGER DEFAULT 0,
    capabilities    TEXT,
    preferences     TEXT,
    health_state    TEXT DEFAULT 'healthy'
                    CHECK(health_state IN ('healthy', 'degraded', 'unavailable')),
    last_health_check TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Provider configuration key-value store
CREATE TABLE IF NOT EXISTS provider_config (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id     INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    key             TEXT NOT NULL,
    value           TEXT NOT NULL,
    UNIQUE(provider_id, key)
);
CREATE INDEX IF NOT EXISTS idx_provider_config_provider ON provider_config(provider_id);

-- Cache state (SQLite is source of truth)
CREATE TABLE IF NOT EXISTS cache_entries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id        INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    version_id      INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
    cache_path      TEXT NOT NULL,
    cached_at       TEXT NOT NULL DEFAULT (datetime('now')),
    last_accessed   TEXT NOT NULL DEFAULT (datetime('now')),
    pinned          INTEGER NOT NULL DEFAULT 0,
    state           TEXT NOT NULL DEFAULT 'valid'
                    CHECK(state IN ('valid', 'stale', 'pending_eviction')),
    UNIQUE(entry_id, version_id)
);
CREATE INDEX IF NOT EXISTS idx_cache_last_accessed ON cache_entries(last_accessed);
CREATE INDEX IF NOT EXISTS idx_cache_pinned ON cache_entries(pinned);

-- Hydration state
CREATE TABLE IF NOT EXISTS hydration_state (
    entry_id        INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
    current_state   TEXT NOT NULL DEFAULT 'placeholder'
                    CHECK(current_state IN ('placeholder', 'hydrating', 'hydrated', 'partial')),
    hydrated_version_id INTEGER REFERENCES versions(id),
    hydration_progress INTEGER DEFAULT 0,
    last_hydrated   TEXT
);

-- Snapshots (metadata-only, Phase 2)
CREATE TABLE IF NOT EXISTS snapshots (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    description     TEXT
);

CREATE TABLE IF NOT EXISTS snapshot_versions (
    snapshot_id     INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    version_id      INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
    PRIMARY KEY (snapshot_id, version_id)
);

-- Trash (Phase 2)
CREATE TABLE IF NOT EXISTS trash (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    original_entry_id INTEGER NOT NULL,
    original_path   TEXT NOT NULL,
    deleted_at      TEXT NOT NULL DEFAULT (datetime('now')),
    version_id      INTEGER REFERENCES versions(id),
    auto_purge_after TEXT
);

-- Policies
CREATE TABLE IF NOT EXISTS policies (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    policy_type     TEXT NOT NULL,
    config          TEXT NOT NULL,
    priority        INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS entry_policies (
    entry_id        INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    policy_id       INTEGER NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    PRIMARY KEY (entry_id, policy_id)
);

-- Write-ahead journal
CREATE TABLE IF NOT EXISTS journal (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id    TEXT NOT NULL UNIQUE,
    operation_type  TEXT NOT NULL,
    payload         TEXT NOT NULL,
    state           TEXT NOT NULL DEFAULT 'pending'
                    CHECK(state IN ('pending', 'committed', 'synced', 'rolled_back')),
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at    TEXT
);
CREATE INDEX IF NOT EXISTS idx_journal_state ON journal(state);

-- Health metrics (observational only)
CREATE TABLE IF NOT EXISTS health_metrics (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id        INTEGER REFERENCES entries(id) ON DELETE CASCADE,
    measured_at     TEXT NOT NULL DEFAULT (datetime('now')),
    replication_count INTEGER NOT NULL DEFAULT 1,
    verification_age_days INTEGER,
    parity_available INTEGER NOT NULL DEFAULT 0,
    provider_health_score REAL,
    overall_score   REAL NOT NULL
);

-- Cold data archives (Phase 3)
CREATE TABLE IF NOT EXISTS archives (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id        INTEGER NOT NULL REFERENCES entries(id),
    archive_path    TEXT NOT NULL,
    par2_path       TEXT NOT NULL,
    original_size   INTEGER NOT NULL,
    archive_size    INTEGER NOT NULL,
    content_hash    TEXT NOT NULL,
    recovery_level  INTEGER NOT NULL DEFAULT 10,
    state           TEXT NOT NULL DEFAULT 'active'
                    CHECK(state IN ('active', 'verified', 'corrupt')),
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    verified_at     TEXT
);

-- Index metadata
CREATE TABLE IF NOT EXISTS index_meta (
    key             TEXT PRIMARY KEY,
    value           TEXT NOT NULL
);

INSERT OR IGNORE INTO index_meta (key, value) VALUES
    ('schema_version', '1.0'),
    ('created_at', datetime('now')),
    ('last_validated', datetime('now'));
`
	_, err := im.db.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("failed to initialize schema: %w", err)
	}

	return nil
}

// SetJournalManager sets the journal manager for atomic operations.
func (im *IndexManager) SetJournalManager(jm *JournalManager) {
	im.journal = jm
}

// Close closes the database connection.
func (im *IndexManager) Close() error {
	return im.db.Close()
}

// --- Entry Operations ---

// CreateEntry creates a new entry in the index.
// Requires journal entry to be created first.
func (im *IndexManager) CreateEntry(ctx context.Context, entry *model.Entry) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	query := `
		INSERT INTO entries (parent_id, name, entry_type, logical_size, physical_size, parity_size, classification)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	result, err := im.db.ExecContext(ctx, query,
		entry.ParentID, entry.Name, entry.Type,
		entry.LogicalSize, entry.PhysicalSize, entry.ParitySize,
		entry.Classification)
	if err != nil {
		return fmt.Errorf("failed to create entry: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get entry id: %w", err)
	}
	entry.ID = id

	return nil
}

// GetEntry retrieves an entry by ID.
func (im *IndexManager) GetEntry(ctx context.Context, id int64) (*model.Entry, error) {
	im.mu.RLock()
	defer im.mu.RUnlock()

	query := `
		SELECT id, parent_id, name, entry_type, logical_size, physical_size, parity_size,
		       created_at, modified_at, classification
		FROM entries WHERE id = ?
	`
	row := im.db.QueryRowContext(ctx, query, id)

	var entry model.Entry
	var createdAt, modifiedAt string
	err := row.Scan(
		&entry.ID, &entry.ParentID, &entry.Name, &entry.Type,
		&entry.LogicalSize, &entry.PhysicalSize, &entry.ParitySize,
		&createdAt, &modifiedAt, &entry.Classification)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get entry: %w", err)
	}

	entry.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	entry.ModifiedAt, _ = time.Parse(time.RFC3339, modifiedAt)

	return &entry, nil
}

// GetEntryByPath retrieves an entry by its full path.
func (im *IndexManager) GetEntryByPath(ctx context.Context, path string) (*model.Entry, error) {
	im.mu.RLock()
	defer im.mu.RUnlock()

	// For now, simple implementation: lookup by basename
	name := filepath.Base(path)
	
	var entry model.Entry
	var parentID sql.NullInt64
	var classification sql.NullString

	err := im.db.QueryRowContext(ctx, `
		SELECT id, parent_id, name, type, logical_size, physical_size, parity_size,
		       created_at, modified_at, classification
		FROM entries WHERE name = ?
	`, name).Scan(
		&entry.ID, &parentID, &entry.Name, &entry.Type,
		&entry.LogicalSize, &entry.PhysicalSize, &entry.ParitySize,
		&entry.CreatedAt, &entry.ModifiedAt, &classification,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entry not found: %s", path)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get entry: %w", err)
	}

	if parentID.Valid {
		entry.ParentID = &parentID.Int64
	}
	if classification.Valid {
		entry.Classification = classification.String
	}

	return &entry, nil
}

// ListEntries lists entries in a directory.
func (im *IndexManager) ListEntries(ctx context.Context, parentID *int64) ([]*model.Entry, error) {
	im.mu.RLock()
	defer im.mu.RUnlock()

	var query string
	var args []interface{}

	if parentID == nil {
		query = `
			SELECT id, parent_id, name, entry_type, logical_size, physical_size, parity_size,
			       created_at, modified_at, classification
			FROM entries WHERE parent_id IS NULL
			ORDER BY entry_type DESC, name ASC
		`
	} else {
		query = `
			SELECT id, parent_id, name, entry_type, logical_size, physical_size, parity_size,
			       created_at, modified_at, classification
			FROM entries WHERE parent_id = ?
			ORDER BY entry_type DESC, name ASC
		`
		args = append(args, *parentID)
	}

	rows, err := im.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list entries: %w", err)
	}
	defer rows.Close()

	var entries []*model.Entry
	for rows.Next() {
		var entry model.Entry
		var createdAt, modifiedAt string
		var classification sql.NullString
		err := rows.Scan(
			&entry.ID, &entry.ParentID, &entry.Name, &entry.Type,
			&entry.LogicalSize, &entry.PhysicalSize, &entry.ParitySize,
			&createdAt, &modifiedAt, &classification)
		if err != nil {
			return nil, fmt.Errorf("failed to scan entry: %w", err)
		}
		entry.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		entry.ModifiedAt, _ = time.Parse(time.RFC3339, modifiedAt)
		if classification.Valid {
			entry.Classification = classification.String
		}
		entries = append(entries, &entry)
	}

	return entries, nil
}

// --- Version Operations ---

// CreateVersion creates a new version for an entry.
func (im *IndexManager) CreateVersion(ctx context.Context, version *model.Version) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	query := `
		INSERT INTO versions (entry_id, version_num, content_hash, size, state, encryption_key_id)
		VALUES (?, ?, ?, ?, ?, ?)
	`
	result, err := im.db.ExecContext(ctx, query,
		version.EntryID, version.VersionNum, version.ContentHash,
		version.Size, version.State, version.EncryptionKeyID)
	if err != nil {
		return fmt.Errorf("failed to create version: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get version id: %w", err)
	}
	version.ID = id

	return nil
}

// GetActiveVersion returns the active version for an entry.
func (im *IndexManager) GetActiveVersion(ctx context.Context, entryID int64) (*model.Version, error) {
	im.mu.RLock()
	defer im.mu.RUnlock()

	query := `
		SELECT id, entry_id, version_num, content_hash, size, created_at, state, encryption_key_id
		FROM versions WHERE entry_id = ? AND state = 'active'
		ORDER BY version_num DESC LIMIT 1
	`
	row := im.db.QueryRowContext(ctx, query, entryID)

	var version model.Version
	var createdAt string
	err := row.Scan(
		&version.ID, &version.EntryID, &version.VersionNum,
		&version.ContentHash, &version.Size, &createdAt,
		&version.State, &version.EncryptionKeyID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get version: %w", err)
	}

	version.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

	return &version, nil
}

// --- Validation ---

// Validate performs index integrity validation.
// Based on design.txt Section 22.
func (im *IndexManager) Validate(ctx context.Context) error {
	im.mu.RLock()
	defer im.mu.RUnlock()

	// Check for orphaned versions
	query := `
		SELECT COUNT(*) FROM versions v
		LEFT JOIN entries e ON v.entry_id = e.id
		WHERE e.id IS NULL
	`
	var orphanedVersions int
	if err := im.db.QueryRowContext(ctx, query).Scan(&orphanedVersions); err != nil {
		return fmt.Errorf("failed to check orphaned versions: %w", err)
	}
	if orphanedVersions > 0 {
		return fmt.Errorf("found %d orphaned versions", orphanedVersions)
	}

	// Update last_validated timestamp
	update := `UPDATE index_meta SET value = datetime('now') WHERE key = 'last_validated'`
	if _, err := im.db.ExecContext(ctx, update); err != nil {
		return fmt.Errorf("failed to update validation timestamp: %w", err)
	}

	return nil
}

// --- JournalManager ---

// JournalManager manages the write-ahead journal for crash safety.
// Based on design.txt Section 7.
type JournalManager struct {
	db *sql.DB
	mu sync.Mutex
}

// NewJournalManager creates a new journal manager.
func NewJournalManager(db *sql.DB) *JournalManager {
	return &JournalManager{db: db}
}

// BeginOperation starts a new journaled operation.
// INVARIANT: Journal entry MUST be written and fsynced BEFORE any index mutation.
func (jm *JournalManager) BeginOperation(ctx context.Context, opType string, payload string) (string, error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	opID := uuid.New().String()

	query := `
		INSERT INTO journal (operation_id, operation_type, payload, state)
		VALUES (?, ?, ?, 'pending')
	`
	_, err := jm.db.ExecContext(ctx, query, opID, opType, payload)
	if err != nil {
		return "", fmt.Errorf("failed to begin operation: %w", err)
	}

	return opID, nil
}

// CommitOperation marks an operation as committed.
func (jm *JournalManager) CommitOperation(ctx context.Context, opID string) error {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	query := `UPDATE journal SET state = 'committed' WHERE operation_id = ?`
	_, err := jm.db.ExecContext(ctx, query, opID)
	if err != nil {
		return fmt.Errorf("failed to commit operation: %w", err)
	}

	return nil
}

// SyncOperation marks an operation as synced (fully complete).
func (jm *JournalManager) SyncOperation(ctx context.Context, opID string) error {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	query := `UPDATE journal SET state = 'synced', completed_at = datetime('now') WHERE operation_id = ?`
	_, err := jm.db.ExecContext(ctx, query, opID)
	if err != nil {
		return fmt.Errorf("failed to sync operation: %w", err)
	}

	return nil
}

// RollbackOperation marks an operation as rolled back.
func (jm *JournalManager) RollbackOperation(ctx context.Context, opID string, errMsg string) error {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	query := `UPDATE journal SET state = 'rolled_back', completed_at = datetime('now') WHERE operation_id = ?`
	_, err := jm.db.ExecContext(ctx, query, opID)
	if err != nil {
		return fmt.Errorf("failed to rollback operation: %w", err)
	}

	return nil
}

// GetPendingOperations returns operations that need recovery.
func (jm *JournalManager) GetPendingOperations(ctx context.Context) ([]*model.JournalEntry, error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	query := `
		SELECT id, operation_id, operation_type, payload, state, created_at
		FROM journal WHERE state IN ('pending', 'committed')
		ORDER BY created_at ASC
	`
	rows, err := jm.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending operations: %w", err)
	}
	defer rows.Close()

	var entries []*model.JournalEntry
	for rows.Next() {
		var entry model.JournalEntry
		var createdAt string
		err := rows.Scan(&entry.ID, &entry.OperationID, &entry.OperationType,
			&entry.Payload, &entry.State, &createdAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan journal entry: %w", err)
		}
		entry.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		entries = append(entries, &entry)
	}

	return entries, nil
}
