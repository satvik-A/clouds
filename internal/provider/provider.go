// Package provider defines the storage provider interface and types.
// Based on design.txt Section 8: Providers are PLUGINS.
// No provider-specific logic in core.
package provider

import (
	"context"
	"time"
)

// Capabilities describes what a provider supports.
// Retrieved via the Capabilities() method, not assumed.
type Capabilities struct {
	MaxChunkSize       int64 `json:"max_chunk_size"`
	SupportsVersioning bool  `json:"supports_versioning"`
	SupportsDirectUpload bool `json:"supports_direct_upload"`
	RequiresEncryption bool  `json:"requires_encryption"`
	SupportsResume     bool  `json:"supports_resume"`
	ConcurrentUploads  int   `json:"concurrent_uploads"`
}

// Usage statistics from provider.
// This is AUTHORITATIVE for quota enforcement (fetched live via GetUsage).
// The cached value in providers.current_usage is for display only.
type Usage struct {
	TotalBytes     int64 `json:"total_bytes"`
	UsedBytes      int64 `json:"used_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}

// UploadResult returned after successful upload.
type UploadResult struct {
	RemotePath  string    `json:"remote_path"`
	ContentHash string    `json:"content_hash"` // SHA-256
	UploadedAt  time.Time `json:"uploaded_at"`
	Size        int64     `json:"size"`
}

// DownloadResult returned after successful download.
type DownloadResult struct {
	LocalPath    string    `json:"local_path"`
	ContentHash  string    `json:"content_hash"` // SHA-256
	DownloadedAt time.Time `json:"downloaded_at"`
	Size         int64     `json:"size"`
}

// VerifyResult returned after integrity check.
type VerifyResult struct {
	IsValid      bool   `json:"is_valid"`
	ContentHash  string `json:"content_hash,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// HealthState represents provider availability.
// NOTE: HealthState is OBSERVATIONAL only, not decision authority.
type HealthState string

const (
	HealthStateHealthy     HealthState = "healthy"
	HealthStateDegraded    HealthState = "degraded"
	HealthStateUnavailable HealthState = "unavailable"
)

// ProgressFunc callback for upload/download progress (0.0 to 1.0).
type ProgressFunc func(progress float64)

// Provider interface - all storage providers must implement this.
// Based on design.txt Section 8.
type Provider interface {
	// ID returns unique identifier for this provider instance.
	ID() string

	// Type returns provider type (gdrive, s3, webdav, rclone, etc.).
	Type() string

	// DisplayName returns human-readable name.
	DisplayName() string

	// Init initializes the provider with configuration.
	Init(ctx context.Context, config map[string]interface{}) error

	// Capabilities returns what this provider supports.
	Capabilities(ctx context.Context) (*Capabilities, error)

	// GetUsage returns current usage statistics.
	// This is AUTHORITATIVE for quota enforcement.
	GetUsage(ctx context.Context) (*Usage, error)

	// Upload uploads a file to the provider.
	Upload(ctx context.Context, localPath string, remotePath string, progress ProgressFunc) (*UploadResult, error)

	// Download downloads a file from the provider.
	Download(ctx context.Context, remotePath string, localPath string, progress ProgressFunc) (*DownloadResult, error)

	// Delete removes a file from the provider.
	// NOTE: Only invoked during explicit purge or trash eviction after user confirmation.
	Delete(ctx context.Context, remotePath string) error

	// Verify checks data integrity on provider.
	Verify(ctx context.Context, remotePath string) (*VerifyResult, error)

	// CheckHealth returns current health state.
	// NOTE: Health is observational, not decision authority.
	CheckHealth(ctx context.Context) HealthState
}

// Registry manages provider instances.
type Registry interface {
	// Register adds a new provider.
	Register(p Provider) error

	// Get returns provider by ID.
	Get(id string) (Provider, bool)

	// All returns all registered providers.
	All() []Provider

	// Primary returns the primary provider.
	Primary() Provider

	// SetPrimary sets the primary provider.
	SetPrimary(id string) error
}
