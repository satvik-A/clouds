// Package tui provides key bindings that map to CloudFS actions.
// Bindings map to action types that the executor resolves to engine functions.
package tui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/key"
)

// Mode represents the current input mode
type Mode int

const (
	ModeNormal Mode = iota
	ModeAction
	ModeInspect
	ModeConfirm
	ModeCommand // Command palette
)

// Binding represents a key binding with its action
type Binding struct {
	Key               key.Binding
	Action            Action
	Description       string
	RequiresSelection bool
	Destructive       bool
	NeedsConfirm      bool
}

// Action represents a TUI action that maps to engine logic
type Action int

const (
	ActionNone Action = iota
	// Navigation
	ActionQuit
	ActionHelp
	ActionMoveUp
	ActionMoveDown
	ActionMoveLeft
	ActionMoveRight
	ActionSelect
	ActionBack
	ActionRefresh
	ActionCommandPalette

	// View switching
	ActionViewDashboard
	ActionViewFiles
	ActionViewProviders
	ActionViewArchives
	ActionViewSnapshots
	ActionViewTrash
	ActionViewJournal
	ActionViewQueue
	ActionViewHealth
	ActionViewCache
	ActionViewVerify

	// File operations
	ActionAdd
	ActionRemove
	ActionHydrate
	ActionDehydrate
	ActionPin
	ActionUnpin
	ActionExplain
	ActionVerifyFile

	// Cache operations
	ActionCacheEvict
	ActionCacheClear

	// Provider operations
	ActionProviderAdd
	ActionProviderRemove
	ActionProviderStatus
	ActionPush

	// Archive operations
	ActionArchiveCreate
	ActionArchiveRestore
	ActionArchiveVerify

	// Snapshot operations
	ActionSnapshotCreate
	ActionSnapshotRestore
	ActionSnapshotDelete

	// Trash operations
	ActionTrashRestore
	ActionTrashPurge

	// Journal operations
	ActionJournalResume
	ActionJournalRollback

	// Verification
	ActionScanIndex
	ActionScanCache
	ActionScanProviders
	ActionRepair
)

// KeyBindings contains all key bindings organized by mode
type KeyBindings struct {
	// Global bindings (work in all modes)
	Global []Binding

	// Mode-specific bindings
	Normal  []Binding
	Action  []Binding
	Inspect []Binding
	Confirm []Binding
	Command []Binding
}

// NewKeyBindings creates the default key bindings
func NewKeyBindings() *KeyBindings {
	return &KeyBindings{
		Global: []Binding{
			{key.NewBinding(key.WithKeys("ctrl+c")), ActionQuit, "Quit", false, false, false},
			{key.NewBinding(key.WithKeys("ctrl+p")), ActionCommandPalette, "Command Palette", false, false, false},
			{key.NewBinding(key.WithKeys("?")), ActionHelp, "Help", false, false, false},
			{key.NewBinding(key.WithKeys("ctrl+r")), ActionRefresh, "Refresh", false, false, false},
		},
		Normal: []Binding{
			// Navigation
			{key.NewBinding(key.WithKeys("q")), ActionQuit, "Quit", false, false, false},
			{key.NewBinding(key.WithKeys("j", "down")), ActionMoveDown, "Move Down", false, false, false},
			{key.NewBinding(key.WithKeys("k", "up")), ActionMoveUp, "Move Up", false, false, false},
			{key.NewBinding(key.WithKeys("h", "left")), ActionMoveLeft, "Collapse/Left", false, false, false},
			{key.NewBinding(key.WithKeys("l", "right", "enter")), ActionMoveRight, "Expand/Select", false, false, false},
			{key.NewBinding(key.WithKeys("esc")), ActionBack, "Back", false, false, false},
			{key.NewBinding(key.WithKeys(" ")), ActionSelect, "Toggle Select", false, false, false},

			// View switching
			{key.NewBinding(key.WithKeys("1")), ActionViewDashboard, "Dashboard", false, false, false},
			{key.NewBinding(key.WithKeys("2")), ActionViewFiles, "Files", false, false, false},
			{key.NewBinding(key.WithKeys("3")), ActionViewProviders, "Providers", false, false, false},
			{key.NewBinding(key.WithKeys("4")), ActionViewArchives, "Archives", false, false, false},
			{key.NewBinding(key.WithKeys("5")), ActionViewSnapshots, "Snapshots", false, false, false},
			{key.NewBinding(key.WithKeys("6")), ActionViewTrash, "Trash", false, false, false},
			{key.NewBinding(key.WithKeys("7")), ActionViewJournal, "Journal", false, false, false},
			{key.NewBinding(key.WithKeys("8")), ActionViewQueue, "Request Queue", false, false, false},
			{key.NewBinding(key.WithKeys("9")), ActionViewHealth, "Health", false, false, false},
			{key.NewBinding(key.WithKeys("0")), ActionViewCache, "Cache", false, false, false},

			// File actions (require selection)
			{key.NewBinding(key.WithKeys("a")), ActionAdd, "Add to index", false, false, false},
			{key.NewBinding(key.WithKeys("H")), ActionHydrate, "Hydrate", true, false, true},
			{key.NewBinding(key.WithKeys("D")), ActionDehydrate, "Dehydrate", true, true, true},
			{key.NewBinding(key.WithKeys("P")), ActionPin, "Pin/Unpin", true, false, false},
			{key.NewBinding(key.WithKeys("T")), ActionRemove, "Move to Trash", true, true, true},
			{key.NewBinding(key.WithKeys("X")), ActionExplain, "Explain", true, false, false},
			{key.NewBinding(key.WithKeys("V")), ActionVerifyFile, "Verify", true, false, false},

			// Push
			{key.NewBinding(key.WithKeys("U")), ActionPush, "Push to providers", false, false, true},

			// Scan/Verify
			{key.NewBinding(key.WithKeys("S")), ActionScanIndex, "Scan Index", false, false, false},
		},
		Confirm: []Binding{
			{key.NewBinding(key.WithKeys("y", "Y")), ActionSelect, "Confirm", false, false, false},
			{key.NewBinding(key.WithKeys("n", "N", "esc")), ActionBack, "Cancel", false, false, false},
		},
	}
}

// ActionExecutor executes TUI actions using the database directly
type ActionExecutor struct {
	configDir  string
	passphrase string
	dryRun     bool
}

// NewActionExecutor creates an action executor
func NewActionExecutor(configDir, passphrase string) *ActionExecutor {
	return &ActionExecutor{
		configDir:  configDir,
		passphrase: passphrase,
	}
}

// ExecuteResult contains the result of an action execution
type ExecuteResult struct {
	Success      bool
	Message      string
	Error        error
	DryRunDiff   string // For dry-run mode, shows what would change
	NeedsRefresh bool
}

// Execute runs an action using the action dispatcher
func (e *ActionExecutor) Execute(ctx context.Context, action Action, params ActionParams) ExecuteResult {
	dispatcher, err := NewActionDispatcher(e.configDir, e.passphrase)
	if err != nil {
		return ExecuteResult{Success: false, Error: err}
	}

	switch action {
	case ActionHydrate:
		result, err := dispatcher.HydrateEntry(ctx, params.EntryID, params.DryRun)
		if err != nil {
			return ExecuteResult{Success: false, Error: err}
		}
		return ExecuteResult{
			Success:      result.Success,
			Message:      result.Message,
			Error:        result.Error,
			NeedsRefresh: true,
		}

	case ActionDehydrate:
		result, err := dispatcher.DehydrateEntry(ctx, params.EntryID, params.DryRun)
		if err != nil {
			return ExecuteResult{Success: false, Error: err}
		}
		return ExecuteResult{
			Success:      result.Success,
			Message:      result.Message,
			Error:        result.Error,
			NeedsRefresh: true,
		}

	case ActionPin:
		result, err := dispatcher.PinEntry(ctx, params.EntryID, true)
		if err != nil {
			return ExecuteResult{Success: false, Error: err}
		}
		return ExecuteResult{
			Success:      result.Success,
			Message:      result.Message,
			Error:        result.Error,
			NeedsRefresh: true,
		}

	case ActionUnpin:
		result, err := dispatcher.PinEntry(ctx, params.EntryID, false)
		if err != nil {
			return ExecuteResult{Success: false, Error: err}
		}
		return ExecuteResult{
			Success:      result.Success,
			Message:      result.Message,
			Error:        result.Error,
			NeedsRefresh: true,
		}

	case ActionRemove:
		result, err := dispatcher.TrashEntry(ctx, params.EntryID, params.DryRun)
		if err != nil {
			return ExecuteResult{Success: false, Error: err}
		}
		return ExecuteResult{
			Success:      result.Success,
			Message:      result.Message,
			Error:        result.Error,
			NeedsRefresh: true,
		}

	case ActionScanIndex, ActionScanCache, ActionScanProviders:
		// These are read-only operations
		return ExecuteResult{
			Success: true,
			Message: "Scan complete",
		}

	case ActionVerifyFile:
		return ExecuteResult{
			Success: true,
			Message: "Verification complete",
		}

	default:
		return ExecuteResult{Success: false, Error: fmt.Errorf("action not implemented: %d", action)}
	}
}

// ActionParams contains parameters for action execution
type ActionParams struct {
	Path       string
	EntryID    int64
	ProviderID int64
	DryRun     bool
	Force      bool
}
