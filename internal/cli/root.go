// Package cli implements the CloudFS command-line interface.
// Built with cobra following design.txt operational rules:
// - No background daemon
// - Explicit intent only
// - All destructive actions require confirmation
package cli

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	// Global flags
	verbose   bool
	quiet     bool
	configDir string
	dryRun    bool
)

// rootCmd is the base command for CloudFS.
var rootCmd = &cobra.Command{
	Use:   "cloudfs",
	Short: "Provider-agnostic cloud storage control plane",
	Long: `CloudFS is a provider-agnostic, policy-driven cloud storage control plane.

It provides:
  • Encrypted metadata index (SQLite + SQLCipher)
  • Explicit intent-only hydration
  • Deterministic caching with manual control
  • Plugin-based storage backends
  • Human-readable disaster recovery

Source of truth: design.txt
Single writer: macOS only (authoritative)
Atomic unit: version`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Global flags available to all commands
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "Suppress non-essential output")
	rootCmd.PersistentFlags().StringVar(&configDir, "config", "", "Use alternate config directory")
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without doing it")

	// Add command groups
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(addCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(lsCmd)
	rootCmd.AddCommand(hydrateCmd)
	rootCmd.AddCommand(dehydrateCmd)
	rootCmd.AddCommand(pinCmd)
	rootCmd.AddCommand(unpinCmd)
	rootCmd.AddCommand(cacheCmd)
	rootCmd.AddCommand(providerCmd)
	rootCmd.AddCommand(pushCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(repairCmd)
	rootCmd.AddCommand(journalCmd)
	rootCmd.AddCommand(snapshotCmd)
	rootCmd.AddCommand(trashCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(healthCmd)
	rootCmd.AddCommand(archiveCmd)
	rootCmd.AddCommand(requestCmd)
	rootCmd.AddCommand(explainCmd)
	rootCmd.AddCommand(scanCmd)
	rootCmd.AddCommand(diagnosticsCmd)
	rootCmd.AddCommand(overviewCmd)
	rootCmd.AddCommand(destroyCmd)
}

// getConfigDir returns the configuration directory path.
// First checks current directory for .cloudfs (repo-local), then falls back to user home.
func getConfigDir() string {
	if configDir != "" {
		return configDir
	}
	
	// Check current directory first (repo-local CloudFS)
	cwd, err := os.Getwd()
	if err == nil {
		localConfig := filepath.Join(cwd, ".cloudfs")
		if _, err := os.Stat(localConfig); err == nil {
			return localConfig
		}
	}
	
	// Fall back to home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cloudfs"
	}
	return filepath.Join(home, ".cloudfs")
}

// --- Command Stubs ---
// These will be implemented in separate files

var initCmd = &cobra.Command{
	Use:   "init [path]",
	Short: "Initialize a new CloudFS repository",
	Long:  `Initialize a new CloudFS repository at the specified path.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}
		return RunInit(path)
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show repository status and health",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunStatus()
	},
}

var addCmd = &cobra.Command{
	Use:   "add <path>",
	Short: "Add file/directory to index",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunAdd(args[0])
	},
}

var rmCmd = &cobra.Command{
	Use:   "rm <path>",
	Short: "Remove from index (moves to trash)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunRm(args[0])
	},
}

var lsCmd = &cobra.Command{
	Use:   "ls [path]",
	Short: "List entries (from index)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := ""
		if len(args) > 0 {
			path = args[0]
		}
		return RunLs(path)
	},
}

var hydrateCmd = &cobra.Command{
	Use:   "hydrate <path>",
	Short: "Download and hydrate file(s)",
	Long: `Download and hydrate file(s) from the provider.

Hydration is triggered ONLY by explicit user commands.
Downloads write to cache, then atomically swap placeholder after hash verification.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunHydrate(args[0])
	},
}

var dehydrateCmd = &cobra.Command{
	Use:   "dehydrate <path>",
	Short: "Remove local data, keep placeholder",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunDehydrate(args[0])
	},
}

var pinCmd = &cobra.Command{
	Use:   "pin <path>",
	Short: "Pin file in cache (prevent eviction)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunPin(args[0])
	},
}

var unpinCmd = &cobra.Command{
	Use:   "unpin <path>",
	Short: "Remove pin from cached file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunUnpin(args[0])
	},
}

// Cache subcommands
var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Cache management commands",
}

var cacheListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all cached files",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunCacheList()
	},
}

var cacheStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show cache disk usage",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunCacheStatus()
	},
}

var cacheEvictCmd = &cobra.Command{
	Use:   "evict <path>",
	Short: "Manually evict from cache",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunCacheEvict(args[0])
	},
}

var cacheClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear unpinned cache entries (with confirmation)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunCacheClear()
	},
}

// Provider subcommands
var providerCmd = &cobra.Command{
	Use:   "provider",
	Short: "Provider management commands",
}

var providerAddCmd = &cobra.Command{
	Use:   "add <name> <type> <remote>",
	Short: "Add a new storage provider",
	Long: `Add a new storage provider.

Example:
  cloudfs provider add google rclone gdrive:backup`,
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunProviderAdd(args[0], args[1], args[2])
	},
}

var providerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunProviderList()
	},
}

var providerStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show provider health/usage",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunProviderStatus(args[0])
	},
}

var providerRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove provider (with safety checks)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunProviderRemove(args[0])
	},
}

// Commit operations
var pushCmd = &cobra.Command{
	Use:   "push",
	Short: "Push pending changes to providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunPush()
	},
}

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify index integrity",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunVerify()
	},
}

var repairCmd = &cobra.Command{
	Use:   "repair",
	Short: "Attempt to repair inconsistencies",
	Long: `Attempt to repair inconsistencies in the index.

repair MAY:
  • Rebuild missing placeholders from index
  • Retry failed uploads
  • Re-verify placements with providers
  • Restore cache entries from verified provider data

repair MUST NOT:
  • Delete remote data
  • Migrate data between providers
  • Drop versions from index
  • Perform destructive reconciliation`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunRepair()
	},
}

// Journal subcommands
var journalCmd = &cobra.Command{
	Use:   "journal",
	Short: "Journal management commands",
}

var journalListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show pending journal entries",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunJournalList()
	},
}

var journalResumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume incomplete operations",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunJournalResume()
	},
}

var journalRollbackCmd = &cobra.Command{
	Use:   "rollback <id>",
	Short: "Rollback a pending operation",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunJournalRollback(args[0])
	},
}

func init() {
	// Add cache subcommands
	cacheCmd.AddCommand(cacheListCmd)
	cacheCmd.AddCommand(cacheStatusCmd)
	cacheCmd.AddCommand(cacheEvictCmd)
	cacheCmd.AddCommand(cacheClearCmd)

	// Add provider subcommands
	providerCmd.AddCommand(providerAddCmd)
	providerCmd.AddCommand(providerListCmd)
	providerCmd.AddCommand(providerStatusCmd)
	providerCmd.AddCommand(providerRemoveCmd)

	// Add journal subcommands
	journalCmd.AddCommand(journalListCmd)
	journalCmd.AddCommand(journalResumeCmd)
	journalCmd.AddCommand(journalRollbackCmd)

	// Add snapshot subcommands
	snapshotCmd.AddCommand(snapshotCreateCmd)
	snapshotCmd.AddCommand(snapshotListCmd)
	snapshotCmd.AddCommand(snapshotInspectCmd)
	snapshotCmd.AddCommand(snapshotRestoreCmd)
	snapshotCmd.AddCommand(snapshotDeleteCmd)
}

// Snapshot commands
var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Snapshot management commands",
	Long: `Snapshots capture a point-in-time view of the index.

Snapshots are METADATA-ONLY - no file data is copied.
Use snapshots to save and restore index state.`,
}

var snapshotCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new snapshot",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		desc, _ := cmd.Flags().GetString("description")
		return RunSnapshotCreate(args[0], desc)
	},
}

var snapshotListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all snapshots",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunSnapshotList()
	},
}

var snapshotInspectCmd = &cobra.Command{
	Use:   "inspect <name>",
	Short: "Show snapshot details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunSnapshotInspect(args[0])
	},
}

var snapshotRestoreCmd = &cobra.Command{
	Use:   "restore <name>",
	Short: "Restore index to snapshot state",
	Long: `Restore the index to match a snapshot.

This changes version states only - NO cloud data is deleted.
Use --dry-run to preview changes before applying.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunSnapshotRestore(args[0])
	},
}

var snapshotDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a snapshot",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunSnapshotDelete(args[0])
	},
}

func init() {
	snapshotCreateCmd.Flags().StringP("description", "d", "", "Snapshot description")
	snapshotRestoreCmd.Flags().Bool("dry-run", false, "Preview changes without applying")

	// Register trash subcommands
	trashCmd.AddCommand(trashListCmd)
	trashCmd.AddCommand(trashRestoreCmd)
	trashCmd.AddCommand(trashPurgeCmd)
}

// Trash commands
var trashCmd = &cobra.Command{
	Use:   "trash",
	Short: "Trash management commands",
	Long: `Manage deleted files in the trash.

Files are moved to trash on deletion, not immediately purged.
Purge permanently deletes files and their provider data.`,
}

var trashListCmd = &cobra.Command{
	Use:   "list",
	Short: "List entries in trash",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunTrashList()
	},
}

var trashRestoreCmd = &cobra.Command{
	Use:   "restore <path>",
	Short: "Restore an entry from trash",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunTrashRestore(args[0])
	},
}

var trashPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Permanently delete all trash entries",
	Long: `Permanently delete all entries in trash.

This is IRREVERSIBLE. Provider data will be deleted.
Use --dry-run to preview what will be deleted.
Use --force to skip confirmation prompt.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		force, _ := cmd.Flags().GetBool("force")
		return RunTrashPurge(force)
	},
}

func init() {
	trashPurgeCmd.Flags().Bool("force", false, "Skip confirmation prompt (dangerous)")
}

// Search command
var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search entries in the index",
	Long: `Search for entries in the metadata index.

This is an INDEX-ONLY search. No provider or filesystem access.
Search matches entry names using case-insensitive pattern matching.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := ""
		if len(args) > 0 {
			query = args[0]
		}
		entryType, _ := cmd.Flags().GetString("type")
		classification, _ := cmd.Flags().GetString("classification")
		return RunSearch(query, entryType, classification)
	},
}

func init() {
	searchCmd.Flags().StringP("type", "t", "", "Filter by type (file, folder)")
	searchCmd.Flags().StringP("classification", "c", "", "Filter by classification")
}

// Health command
var healthCmd = &cobra.Command{
	Use:   "health [path]",
	Short: "Show health status",
	Long: `Show health scoring for the repository or a specific entry.

Health scoring is OBSERVATIONAL only. No automatic remediation.
Scores are computed from replication count and verification age.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return RunHealthEntry(args[0])
		}
		return RunHealth()
	},
}

// Archive commands
var archiveCmd = &cobra.Command{
	Use:   "archive",
	Short: "Cold data archival commands",
	Long: `Create and manage cold data archives.

Archives use 7z compression + PAR2 error correction.
Archives are immutable once created.
Original data is preserved (never deleted during archival).`,
}

var archiveCreateCmd = &cobra.Command{
	Use:   "create <path>",
	Short: "Create a cold archive",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunArchiveCreate(args[0])
	},
}

var archiveInspectCmd = &cobra.Command{
	Use:   "inspect <path>",
	Short: "Inspect an existing archive",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunArchiveInspect(args[0])
	},
}

var archiveListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all archives",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunArchiveList()
	},
}

var archiveRestoreCmd = &cobra.Command{
	Use:   "restore <path>",
	Short: "Restore from a cold archive",
	Long: `Restore an entry from a cold archive.

Extracts archive to cache, verifies hash, and restores to cache.
Original archive is PRESERVED (never deleted).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunArchiveRestore(args[0])
	},
}

func init() {
	archiveCmd.AddCommand(archiveCreateCmd)
	archiveCmd.AddCommand(archiveInspectCmd)
	archiveCmd.AddCommand(archiveListCmd)
	archiveCmd.AddCommand(archiveRestoreCmd)

	// Request subcommands
	requestCmd.AddCommand(requestPushCmd)
	requestCmd.AddCommand(requestPullCmd)
	requestCmd.AddCommand(requestStatusCmd)
	requestCmd.AddCommand(requestListCmd)
}

// Request commands
var requestCmd = &cobra.Command{
	Use:   "request",
	Short: "Multi-device sync request queue",
	Long: `Manage sync requests for multi-device operation.

Request-based sync ensures explicit user control.
No automatic synchronization occurs.`,
}

var requestPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Queue a push request",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunRequestPush()
	},
}

var requestPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Queue a pull request",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunRequestPull()
	},
}

var requestStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show request queue status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunRequestStatus()
	},
}

var requestListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all requests",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunRequestList()
	},
}

// Explain command
var explainCmd = &cobra.Command{
	Use:   "explain <path>",
	Short: "Show comprehensive explanation of an entry",
	Long: `Show detailed information about where data lives.

This is a READ-ONLY command with NO side effects.
Shows versions, locations, cache state, archive state, health signals.

Use --archive to focus on archive details.
Use --health to focus on health signals.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		archiveOnly, _ := cmd.Flags().GetBool("archive")
		healthOnly, _ := cmd.Flags().GetBool("health")
		return RunExplain(args[0], archiveOnly, healthOnly)
	},
}

func init() {
	explainCmd.Flags().Bool("archive", false, "Focus on archive state")
	explainCmd.Flags().Bool("health", false, "Focus on health signals")

	// Scan subcommands
	scanCmd.AddCommand(scanIndexCmd)
	scanCmd.AddCommand(scanCacheCmd)
	scanCmd.AddCommand(scanProvidersCmd)
}

// Scan commands
var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Non-destructive consistency scanners",
	Long: `Run read-only scans to check consistency.

All scans are READ-ONLY with NO auto-fix.
Report-only output.`,
}

var scanIndexCmd = &cobra.Command{
	Use:   "index",
	Short: "Scan index for consistency",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunScanIndex()
	},
}

var scanCacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Scan cache for consistency",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunScanCache()
	},
}

var scanProvidersCmd = &cobra.Command{
	Use:   "providers",
	Short: "Scan provider state",
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunScanProviders()
	},
}

// Diagnostics command
var diagnosticsCmd = &cobra.Command{
	Use:   "diagnostics",
	Short: "Export machine-readable diagnostics",
	RunE: func(cmd *cobra.Command, args []string) error {
		outputPath, _ := cmd.Flags().GetString("output")
		return RunDiagnosticsExport(outputPath)
	},
}

func init() {
	diagnosticsCmd.Flags().StringP("output", "o", "", "Output file path (default: stdout)")
}

// Overview command
var overviewCmd = &cobra.Command{
	Use:   "overview",
	Short: "Show a complete dashboard of CloudFS state",
	Long: `Display a read-only summary of the entire CloudFS system.

Shows:
  • Total data (files, folders, size)
  • Cache status (cached files, pinned)
  • Archives (count, total size)
  • Providers (active, placements)
  • Health (healthy, warning, critical)
  • Pending operations (requests, journal)
  • Trash items

This is a READ-ONLY command with NO side effects.

Examples:
  cloudfs overview           # Show complete dashboard
  cloudfs overview --json    # Export as JSON`,
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")
		return RunOverview(jsonOutput)
	},
}

func init() {
	overviewCmd.Flags().Bool("json", false, "Output as JSON")
}

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Completely remove all local CloudFS data",
	Long: `Permanently delete all local CloudFS data including:
  • Encrypted metadata database (index.db)
  • Local file cache
  • All .cloudfs placeholder files in the current repository

This command is IRREVERSIBLE. No data is deleted from cloud providers.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		force, _ := cmd.Flags().GetBool("force")
		return RunDestroy(force)
	},
}

func init() {
	destroyCmd.Flags().Bool("force", false, "Skip confirmation prompts")
}
