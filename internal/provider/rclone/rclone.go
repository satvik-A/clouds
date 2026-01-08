// Package rclone provides an rclone-based storage provider implementation.
// This is the reference implementation as specified in design.txt Section 8.
package rclone

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudfs/cloudfs/internal/provider"
)

// Provider implements the storage provider interface using rclone.
// rclone is the reference implementation for CloudFS providers.
type Provider struct {
	id          string
	displayName string
	remoteName  string // rclone remote name (e.g., "gdrive:", "s3:")
	configPath  string
}

// NewProvider creates a new rclone-based provider.
func NewProvider(id, displayName, remoteName, configPath string) *Provider {
	return &Provider{
		id:          id,
		displayName: displayName,
		remoteName:  remoteName,
		configPath:  configPath,
	}
}

// ID returns the unique identifier for this provider instance.
func (p *Provider) ID() string {
	return p.id
}

// Type returns the provider type.
func (p *Provider) Type() string {
	return "rclone"
}

// DisplayName returns the human-readable name.
func (p *Provider) DisplayName() string {
	return p.displayName
}

// Init initializes the provider with configuration.
func (p *Provider) Init(ctx context.Context, config map[string]interface{}) error {
	// Verify rclone is installed
	if _, err := exec.LookPath("rclone"); err != nil {
		return fmt.Errorf("rclone not found in PATH: %w", err)
	}

	// Verify remote is configured
	cmd := p.rcloneCmd(ctx, "listremotes")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list rclone remotes: %w", err)
	}

	if !strings.Contains(string(output), p.remoteName) {
		return fmt.Errorf("rclone remote '%s' not configured", p.remoteName)
	}

	return nil
}

// Capabilities returns what this provider supports.
func (p *Provider) Capabilities(ctx context.Context) (*provider.Capabilities, error) {
	// Get provider features from rclone
	cmd := p.rcloneCmd(ctx, "backend", "features", p.remoteName)
	output, err := cmd.Output()
	if err != nil {
		// Return default capabilities if features query fails
		return &provider.Capabilities{
			MaxChunkSize:       100 * 1024 * 1024, // 100MB default
			SupportsVersioning: false,
			SupportsDirectUpload: true,
			RequiresEncryption: false,
			SupportsResume:     true,
			ConcurrentUploads:  4,
		}, nil
	}

	// Parse features JSON
	var features map[string]interface{}
	json.Unmarshal(output, &features)

	return &provider.Capabilities{
		MaxChunkSize:       100 * 1024 * 1024,
		SupportsVersioning: features["BucketBased"] == true,
		SupportsDirectUpload: true,
		RequiresEncryption: false,
		SupportsResume:     true,
		ConcurrentUploads:  4,
	}, nil
}

// GetUsage returns current usage statistics.
// This is AUTHORITATIVE for quota enforcement.
func (p *Provider) GetUsage(ctx context.Context) (*provider.Usage, error) {
	cmd := p.rcloneCmd(ctx, "about", p.remoteName, "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get usage: %w", err)
	}

	var about struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
		Free  int64 `json:"free"`
	}
	if err := json.Unmarshal(output, &about); err != nil {
		return nil, fmt.Errorf("failed to parse usage: %w", err)
	}

	return &provider.Usage{
		TotalBytes:     about.Total,
		UsedBytes:      about.Used,
		AvailableBytes: about.Free,
	}, nil
}

// Upload uploads a file to the provider.
func (p *Provider) Upload(ctx context.Context, localPath string, remotePath string, progress provider.ProgressFunc) (*provider.UploadResult, error) {
	// Verify local file exists
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, fmt.Errorf("local file not found: %w", err)
	}

	// Build remote path
	fullRemotePath := p.remoteName + remotePath

	// Execute rclone copy
	cmd := p.rcloneCmd(ctx, "copyto", localPath, fullRemotePath, "--progress")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("upload failed: %w", err)
	}

	// Get hash of uploaded file
	hash, err := p.getRemoteHash(ctx, remotePath)
	if err != nil {
		// Non-fatal, some backends don't support hashing
		hash = ""
	}

	return &provider.UploadResult{
		RemotePath:  remotePath,
		ContentHash: hash,
		UploadedAt:  time.Now(),
		Size:        info.Size(),
	}, nil
}

// Download downloads a file from the provider.
func (p *Provider) Download(ctx context.Context, remotePath string, localPath string, progress provider.ProgressFunc) (*provider.DownloadResult, error) {
	// Ensure local directory exists
	if err := os.MkdirAll(filepath.Dir(localPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Build remote path
	fullRemotePath := p.remoteName + remotePath

	// Execute rclone copy
	cmd := p.rcloneCmd(ctx, "copyto", fullRemotePath, localPath, "--progress")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}

	// Get file info
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat downloaded file: %w", err)
	}

	// Calculate local hash
	hash, _ := calculateFileHash(localPath)

	return &provider.DownloadResult{
		LocalPath:    localPath,
		ContentHash:  hash,
		DownloadedAt: time.Now(),
		Size:         info.Size(),
	}, nil
}

// Delete removes a file from the provider.
// NOTE: Only invoked during explicit purge or trash eviction after user confirmation.
func (p *Provider) Delete(ctx context.Context, remotePath string) error {
	fullRemotePath := p.remoteName + remotePath

	cmd := p.rcloneCmd(ctx, "delete", fullRemotePath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("delete failed: %w", err)
	}

	return nil
}

// Verify checks data integrity on provider.
func (p *Provider) Verify(ctx context.Context, remotePath string) (*provider.VerifyResult, error) {
	fullRemotePath := p.remoteName + remotePath

	// Check if file exists
	cmd := p.rcloneCmd(ctx, "lsjson", fullRemotePath)
	output, err := cmd.Output()
	if err != nil {
		return &provider.VerifyResult{
			IsValid:      false,
			ErrorMessage: "file not found or inaccessible",
		}, nil
	}

	var files []struct {
		Name string `json:"Name"`
		Size int64  `json:"Size"`
	}
	if err := json.Unmarshal(output, &files); err != nil || len(files) == 0 {
		return &provider.VerifyResult{
			IsValid:      false,
			ErrorMessage: "failed to parse file info",
		}, nil
	}

	// Get hash if available
	hash, _ := p.getRemoteHash(ctx, remotePath)

	return &provider.VerifyResult{
		IsValid:     true,
		ContentHash: hash,
	}, nil
}

// CheckHealth returns current health state.
// NOTE: Health is observational, not decision authority.
func (p *Provider) CheckHealth(ctx context.Context) provider.HealthState {
	// Try a simple operation to check health
	cmd := p.rcloneCmd(ctx, "lsd", p.remoteName, "--max-depth", "0")
	if err := cmd.Run(); err != nil {
		return provider.HealthStateUnavailable
	}

	return provider.HealthStateHealthy
}

// rcloneCmd creates an rclone command with common flags.
func (p *Provider) rcloneCmd(ctx context.Context, args ...string) *exec.Cmd {
	allArgs := args
	if p.configPath != "" {
		allArgs = append([]string{"--config", p.configPath}, args...)
	}
	cmd := exec.CommandContext(ctx, "rclone", allArgs...)
	return cmd
}

// getRemoteHash gets the hash of a remote file.
func (p *Provider) getRemoteHash(ctx context.Context, remotePath string) (string, error) {
	fullRemotePath := p.remoteName + remotePath
	cmd := p.rcloneCmd(ctx, "hashsum", "SHA-256", fullRemotePath)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// Output format: "hash  filename"
	parts := strings.Fields(string(output))
	if len(parts) >= 1 {
		return parts[0], nil
	}
	return "", fmt.Errorf("no hash in output")
}

// calculateFileHash calculates SHA-256 hash of a local file.
func calculateFileHash(path string) (string, error) {
	cmd := exec.Command("shasum", "-a", "256", path)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	parts := strings.Fields(string(output))
	if len(parts) >= 1 {
		return parts[0], nil
	}
	return "", fmt.Errorf("no hash in output")
}
