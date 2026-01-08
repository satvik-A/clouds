// Package model defines the core domain models for CloudFS.
// Based on design.txt Sections 3, 6, and other specifications.
package model

import (
	"time"
)

// EntryType represents the type of an entry.
type EntryType string

const (
	EntryTypeFile      EntryType = "file"
	EntryTypeDirectory EntryType = "directory"
)

// Entry represents a file or directory in the index.
// The index is the source of truth (design.txt Section 3).
type Entry struct {
	ID             int64     `json:"id"`
	ParentID       *int64    `json:"parent_id,omitempty"`
	Name           string    `json:"name"`
	Type           EntryType `json:"entry_type"`
	LogicalSize    int64     `json:"logical_size"`
	PhysicalSize   int64     `json:"physical_size"`
	ParitySize     int64     `json:"parity_size"`
	CreatedAt      time.Time `json:"created_at"`
	ModifiedAt     time.Time `json:"modified_at"`
	Classification string    `json:"classification,omitempty"`
}

// VersionState represents the state of a version.
type VersionState string

const (
	VersionStateIncomplete VersionState = "incomplete"
	VersionStateActive     VersionState = "active"
	VersionStateSuperseded VersionState = "superseded"
	VersionStateDeleted    VersionState = "deleted"
)

// Version represents an immutable atomic unit of data.
// Based on design.txt Section 6: Atomic unit = VERSION.
type Version struct {
	ID              int64        `json:"id"`
	EntryID         int64        `json:"entry_id"`
	VersionNum      int          `json:"version_num"`
	ContentHash     string       `json:"content_hash"` // SHA-256
	Size            int64        `json:"size"`
	CreatedAt       time.Time    `json:"created_at"`
	State           VersionState `json:"state"`
	EncryptionKeyID string       `json:"encryption_key_id,omitempty"`
}

// Chunk represents a portion of a large file or archive.
type Chunk struct {
	ID         int64  `json:"id"`
	VersionID  int64  `json:"version_id"`
	ChunkIndex int    `json:"chunk_index"`
	ChunkHash  string `json:"chunk_hash"` // SHA-256
	Size       int64  `json:"size"`
}

// PlacementState represents the state of a placement.
type PlacementState string

const (
	PlacementStatePending  PlacementState = "pending"
	PlacementStateUploaded PlacementState = "uploaded"
	PlacementStateVerified PlacementState = "verified"
	PlacementStateDegraded PlacementState = "degraded"
	PlacementStateFailed   PlacementState = "failed"
)

// Placement represents where data is stored on a provider.
type Placement struct {
	ID         int64          `json:"id"`
	ChunkID    *int64         `json:"chunk_id,omitempty"`
	VersionID  *int64         `json:"version_id,omitempty"`
	ProviderID string         `json:"provider_id"`
	RemotePath string         `json:"remote_path"`
	UploadedAt time.Time      `json:"uploaded_at"`
	VerifiedAt *time.Time     `json:"verified_at,omitempty"`
	State      PlacementState `json:"state"`
}

// HydrationState represents the hydration state of an entry.
type HydrationState string

const (
	HydrationStatePlaceholder HydrationState = "placeholder"
	HydrationStateHydrating   HydrationState = "hydrating"
	HydrationStateHydrated    HydrationState = "hydrated"
	HydrationStatePartial     HydrationState = "partial"
)

// Hydration represents the hydration state of an entry.
type Hydration struct {
	EntryID            int64          `json:"entry_id"`
	CurrentState       HydrationState `json:"current_state"`
	HydratedVersionID  *int64         `json:"hydrated_version_id,omitempty"`
	HydrationProgress  int            `json:"hydration_progress"` // 0-100
	LastHydrated       *time.Time     `json:"last_hydrated,omitempty"`
}

// CacheState represents the state of a cache entry.
type CacheState string

const (
	CacheStateValid           CacheState = "valid"
	CacheStateStale           CacheState = "stale"
	CacheStatePendingEviction CacheState = "pending_eviction"
)

// CacheEntry represents a cached file.
// SQLite is the source of truth for cache state (not metadata.json).
type CacheEntry struct {
	ID           int64      `json:"id"`
	EntryID      int64      `json:"entry_id"`
	VersionID    int64      `json:"version_id"`
	CachePath    string     `json:"cache_path"`
	CachedAt     time.Time  `json:"cached_at"`
	LastAccessed time.Time  `json:"last_accessed"`
	Pinned       bool       `json:"pinned"`
	State        CacheState `json:"state"`
}

// JournalState represents the state of a journal entry.
type JournalState string

const (
	JournalStatePending    JournalState = "pending"
	JournalStateCommitted  JournalState = "committed"
	JournalStateSynced     JournalState = "synced"
	JournalStateRolledBack JournalState = "rolled_back"
)

// JournalEntry represents a write-ahead journal entry.
// Based on design.txt Section 7.
type JournalEntry struct {
	ID            int64        `json:"id"`
	OperationID   string       `json:"operation_id"` // UUID
	OperationType string       `json:"operation_type"`
	Payload       string       `json:"payload"` // JSON
	State         JournalState `json:"state"`
	CreatedAt     time.Time    `json:"created_at"`
	CompletedAt   *time.Time   `json:"completed_at,omitempty"`
}

// ProviderHealthState mirrors provider.HealthState for model layer.
type ProviderHealthState string

const (
	ProviderHealthStateHealthy     ProviderHealthState = "healthy"
	ProviderHealthStateDegraded    ProviderHealthState = "degraded"
	ProviderHealthStateUnavailable ProviderHealthState = "unavailable"
)

// ProviderConfig represents a configured provider.
// NOTE: CurrentUsage is CACHED, not authoritative for quota checks.
type ProviderConfig struct {
	ID              string              `json:"id"`
	ProviderType    string              `json:"provider_type"`
	DisplayName     string              `json:"display_name"`
	ConfigPath      string              `json:"config_path,omitempty"`
	SoftLimit       *int64              `json:"soft_limit,omitempty"`
	HardLimit       *int64              `json:"hard_limit,omitempty"`
	CurrentUsage    int64               `json:"current_usage"` // CACHED only
	Capabilities    string              `json:"capabilities,omitempty"` // JSON
	Preferences     string              `json:"preferences,omitempty"`  // JSON
	HealthState     ProviderHealthState `json:"health_state"`
	LastHealthCheck *time.Time          `json:"last_health_check,omitempty"`
}

// Policy represents a policy configuration.
type Policy struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PolicyType string `json:"policy_type"` // versioning, encryption, replication, lifecycle
	Config     string `json:"config"`      // JSON
	Priority   int    `json:"priority"`
}

// HealthMetric represents a health measurement.
// NOTE: Health scores are OBSERVATIONAL METRICS, not decision authority.
type HealthMetric struct {
	ID                   int64     `json:"id"`
	EntryID              *int64    `json:"entry_id,omitempty"`
	MeasuredAt           time.Time `json:"measured_at"`
	ReplicationCount     int       `json:"replication_count"`
	VerificationAgeDays  *int      `json:"verification_age_days,omitempty"`
	ParityAvailable      bool      `json:"parity_available"`
	ProviderHealthScore  *float64  `json:"provider_health_score,omitempty"`
	OverallScore         float64   `json:"overall_score"`
}

// Snapshot represents a named reference to a set of versions.
// NOTE: Snapshots are METADATA-ONLY, no data duplication (Phase 2).
type Snapshot struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	Description string    `json:"description,omitempty"`
}

// TrashEntry represents a deleted entry awaiting permanent removal.
type TrashEntry struct {
	ID              int64      `json:"id"`
	OriginalEntryID int64      `json:"original_entry_id"`
	OriginalPath    string     `json:"original_path"`
	DeletedAt       time.Time  `json:"deleted_at"`
	VersionID       *int64     `json:"version_id,omitempty"`
	AutoPurgeAfter  *time.Time `json:"auto_purge_after,omitempty"`
}
