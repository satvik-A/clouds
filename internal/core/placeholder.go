// Package core provides the Placeholder Manager for CloudFS.
// Based on design.txt Section 14: Placeholders & Hydration.
//
// INVARIANTS:
// - Filesystem view is a DERIVED PROJECTION, never authoritative
// - Placeholders represent indexed files as lightweight stubs on disk
// - NO file contents stored in placeholder (only metadata)
// - Hydration triggered ONLY by explicit CLI commands
// - Atomic replacement when hydration completes
// - Never expose partial data
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudfs/cloudfs/internal/model"
)

// PlaceholderManager manages the filesystem projection.
// Filesystem is NEVER authoritative - index is source of truth.
type PlaceholderManager struct {
	rootDir string // Root directory for CloudFS filesystem projection
	mu      sync.RWMutex
}

// PlaceholderMetadata is the human-readable content of a placeholder file.
// This is written as JSON inside the placeholder for manual recovery.
type PlaceholderMetadata struct {
	CloudFSVersion  string    `json:"cloudfs_version"`
	EntryID         int64     `json:"entry_id"`
	VersionID       int64     `json:"version_id"`
	ContentHash     string    `json:"content_hash"`
	LogicalSize     int64     `json:"logical_size"`
	IsPlaceholder   bool      `json:"is_placeholder"`
	OriginalName    string    `json:"original_name"`
	CreatedAt       time.Time `json:"created_at"`
	ProviderID      string    `json:"provider_id,omitempty"`
	RemotePath      string    `json:"remote_path,omitempty"`
}

// PlaceholderSuffix is appended to identify CloudFS placeholders.
// We use .cloudfs extension for the placeholder marker file.
const PlaceholderSuffix = ".cloudfs"

// NewPlaceholderManager creates a new placeholder manager.
func NewPlaceholderManager(rootDir string) (*PlaceholderManager, error) {
	// Ensure root directory exists
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create root directory: %w", err)
	}

	return &PlaceholderManager{
		rootDir: rootDir,
	}, nil
}

// RootDir returns the root directory for the filesystem projection.
func (pm *PlaceholderManager) RootDir() string {
	return pm.rootDir
}

// GetPlaceholderPath returns the path for a placeholder file.
// Placeholder files have a .cloudfs extension.
func (pm *PlaceholderManager) GetPlaceholderPath(entry *model.Entry, parentPath string) string {
	if parentPath == "" {
		parentPath = pm.rootDir
	}
	return filepath.Join(parentPath, entry.Name+PlaceholderSuffix)
}

// GetRealPath returns the path for the real (hydrated) file.
func (pm *PlaceholderManager) GetRealPath(entry *model.Entry, parentPath string) string {
	if parentPath == "" {
		parentPath = pm.rootDir
	}
	return filepath.Join(parentPath, entry.Name)
}

// CreatePlaceholder creates a placeholder file for an entry.
// The placeholder is a small JSON file that marks the file as remote.
// It does NOT contain file contents.
func (pm *PlaceholderManager) CreatePlaceholder(ctx context.Context, entry *model.Entry, version *model.Version, parentPath string, providerID string, remotePath string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if entry.Type == model.EntryTypeDirectory {
		return pm.createDirectoryPlaceholder(entry, parentPath)
	}

	placeholderPath := pm.GetPlaceholderPath(entry, parentPath)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(placeholderPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	// Build placeholder metadata
	metadata := PlaceholderMetadata{
		CloudFSVersion: "1.0",
		EntryID:        entry.ID,
		VersionID:      version.ID,
		ContentHash:    version.ContentHash,
		LogicalSize:    entry.LogicalSize,
		IsPlaceholder:  true,
		OriginalName:   entry.Name,
		CreatedAt:      time.Now(),
		ProviderID:     providerID,
		RemotePath:     remotePath,
	}

	// Write placeholder content (human-readable JSON)
	content, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal placeholder: %w", err)
	}

	// Write atomically using temp file + rename
	tempPath := placeholderPath + ".tmp"
	if err := os.WriteFile(tempPath, content, 0644); err != nil {
		return fmt.Errorf("failed to write placeholder: %w", err)
	}

	if err := os.Rename(tempPath, placeholderPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to finalize placeholder: %w", err)
	}

	return nil
}

// createDirectoryPlaceholder creates a directory.
func (pm *PlaceholderManager) createDirectoryPlaceholder(entry *model.Entry, parentPath string) error {
	dirPath := pm.GetRealPath(entry, parentPath)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return nil
}

// IsPlaceholder checks if a path is a CloudFS placeholder.
func (pm *PlaceholderManager) IsPlaceholder(path string) bool {
	return strings.HasSuffix(path, PlaceholderSuffix)
}

// ReadPlaceholder reads and parses a placeholder file.
func (pm *PlaceholderManager) ReadPlaceholder(placeholderPath string) (*PlaceholderMetadata, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	content, err := os.ReadFile(placeholderPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read placeholder: %w", err)
	}

	var metadata PlaceholderMetadata
	if err := json.Unmarshal(content, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse placeholder: %w", err)
	}

	return &metadata, nil
}

// AtomicSwap performs an atomic swap from placeholder to real file.
// INVARIANTS:
// - Cache file must be verified before swap
// - Swap must be atomic (rename operation)
// - Partial data never exposed
// - Placeholder removed only after successful swap
func (pm *PlaceholderManager) AtomicSwap(ctx context.Context, entry *model.Entry, cachePath string, expectedHash string, parentPath string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	placeholderPath := pm.GetPlaceholderPath(entry, parentPath)
	realPath := pm.GetRealPath(entry, parentPath)

	// Step 1: Verify the cache file exists
	cacheInfo, err := os.Stat(cachePath)
	if err != nil {
		return fmt.Errorf("cache file not found: %w", err)
	}

	// Step 2: Verify hash BEFORE swap (critical for safety)
	if expectedHash != "" {
		actualHash, err := pm.calculateFileHash(cachePath)
		if err != nil {
			return fmt.Errorf("failed to verify hash: %w", err)
		}
		if actualHash != expectedHash {
			return fmt.Errorf("hash mismatch: expected %s, got %s", expectedHash, actualHash)
		}
	}

	// Step 3: Verify size matches
	if cacheInfo.Size() != entry.LogicalSize {
		return fmt.Errorf("size mismatch: expected %d, got %d", entry.LogicalSize, cacheInfo.Size())
	}

	// Step 4: Copy cache to temp location near target (for atomic rename)
	tempPath := realPath + ".cloudfs.tmp"
	if err := copyFileAtomic(cachePath, tempPath); err != nil {
		return fmt.Errorf("failed to prepare file: %w", err)
	}

	// Step 5: Atomic rename temp to real path
	if err := os.Rename(tempPath, realPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to swap file: %w", err)
	}

	// Step 6: Remove placeholder (only after successful swap)
	if placeholderPath != realPath {
		os.Remove(placeholderPath)
	}

	return nil
}

// Dehydrate reverts a hydrated file to placeholder state.
// This removes the local data but keeps the placeholder.
func (pm *PlaceholderManager) Dehydrate(ctx context.Context, entry *model.Entry, version *model.Version, parentPath string, providerID string, remotePath string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	realPath := pm.GetRealPath(entry, parentPath)
	placeholderPath := pm.GetPlaceholderPath(entry, parentPath)

	// Check if real file exists
	if _, err := os.Stat(realPath); os.IsNotExist(err) {
		// Already dehydrated or placeholder
		return nil
	}

	// Create placeholder first (before removing real file)
	metadata := PlaceholderMetadata{
		CloudFSVersion: "1.0",
		EntryID:        entry.ID,
		VersionID:      version.ID,
		ContentHash:    version.ContentHash,
		LogicalSize:    entry.LogicalSize,
		IsPlaceholder:  true,
		OriginalName:   entry.Name,
		CreatedAt:      time.Now(),
		ProviderID:     providerID,
		RemotePath:     remotePath,
	}

	content, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal placeholder: %w", err)
	}

	tempPath := placeholderPath + ".tmp"
	if err := os.WriteFile(tempPath, content, 0644); err != nil {
		return fmt.Errorf("failed to write placeholder: %w", err)
	}

	if err := os.Rename(tempPath, placeholderPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to finalize placeholder: %w", err)
	}

	// Remove real file only after placeholder is in place
	if realPath != placeholderPath {
		if err := os.Remove(realPath); err != nil && !os.IsNotExist(err) {
			// Log but don't fail - placeholder is in place
			fmt.Printf("warning: failed to remove real file %s: %v\n", realPath, err)
		}
	}

	return nil
}

// GetEntryPath builds the full filesystem path for an entry by traversing parents.
func (pm *PlaceholderManager) GetEntryPath(ctx context.Context, entry *model.Entry, getParent func(int64) (*model.Entry, error)) (string, error) {
	var parts []string
	current := entry

	for current != nil {
		parts = append([]string{current.Name}, parts...)
		if current.ParentID == nil {
			break
		}
		parent, err := getParent(*current.ParentID)
		if err != nil {
			return "", fmt.Errorf("failed to get parent: %w", err)
		}
		current = parent
	}

	return filepath.Join(append([]string{pm.rootDir}, parts...)...), nil
}

// SyncPlaceholders ensures all indexed entries have corresponding placeholders.
// This is used for initial projection and repair.
func (pm *PlaceholderManager) SyncPlaceholders(ctx context.Context, entries []*model.Entry, getVersion func(int64) (*model.Version, error), getPlacement func(int64) (string, string, error)) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, entry := range entries {
		if entry.Type == model.EntryTypeDirectory {
			// Create directory
			dirPath := filepath.Join(pm.rootDir, entry.Name)
			if err := os.MkdirAll(dirPath, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", entry.Name, err)
			}
			continue
		}

		// Get active version
		version, err := getVersion(entry.ID)
		if err != nil {
			return fmt.Errorf("failed to get version for %s: %w", entry.Name, err)
		}
		if version == nil {
			continue // No active version
		}

		// Get placement info
		providerID, remotePath, err := getPlacement(version.ID)
		if err != nil {
			// No placement yet - skip
			continue
		}

		// Check if placeholder or real file exists
		realPath := filepath.Join(pm.rootDir, entry.Name)
		placeholderPath := realPath + PlaceholderSuffix

		if _, err := os.Stat(realPath); err == nil {
			continue // Real file exists
		}
		if _, err := os.Stat(placeholderPath); err == nil {
			continue // Placeholder exists
		}

		// Create placeholder
		metadata := PlaceholderMetadata{
			CloudFSVersion: "1.0",
			EntryID:        entry.ID,
			VersionID:      version.ID,
			ContentHash:    version.ContentHash,
			LogicalSize:    entry.LogicalSize,
			IsPlaceholder:  true,
			OriginalName:   entry.Name,
			CreatedAt:      time.Now(),
			ProviderID:     providerID,
			RemotePath:     remotePath,
		}

		content, _ := json.MarshalIndent(metadata, "", "  ")
		if err := os.WriteFile(placeholderPath, content, 0644); err != nil {
			return fmt.Errorf("failed to create placeholder for %s: %w", entry.Name, err)
		}
	}

	return nil
}

// calculateFileHash computes SHA-256 hash of a file.
func (pm *PlaceholderManager) calculateFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFileAtomic copies a file atomically using a temp file.
func copyFileAtomic(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		destFile.Close()
		os.Remove(dst)
		return err
	}

	// Sync to disk before closing
	if err := destFile.Sync(); err != nil {
		destFile.Close()
		os.Remove(dst)
		return err
	}

	return destFile.Close()
}

// ListFilesystemState returns the current state of files in the projection.
type FilesystemEntry struct {
	Path          string
	IsPlaceholder bool
	IsDirectory   bool
	Size          int64
	EntryID       int64
}

func (pm *PlaceholderManager) ListFilesystemState(ctx context.Context) ([]FilesystemEntry, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var entries []FilesystemEntry

	err := filepath.Walk(pm.rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Skip root and hidden files
		if path == pm.rootDir {
			return nil
		}
		relPath, _ := filepath.Rel(pm.rootDir, path)
		if strings.HasPrefix(relPath, ".") {
			return nil
		}

		entry := FilesystemEntry{
			Path:        relPath,
			IsDirectory: info.IsDir(),
			Size:        info.Size(),
		}

		if strings.HasSuffix(path, PlaceholderSuffix) {
			entry.IsPlaceholder = true
			entry.Path = strings.TrimSuffix(relPath, PlaceholderSuffix)

			// Read metadata for entry ID
			if meta, err := pm.ReadPlaceholder(path); err == nil {
				entry.EntryID = meta.EntryID
				entry.Size = meta.LogicalSize
			}
		}

		entries = append(entries, entry)
		return nil
	})

	return entries, err
}
