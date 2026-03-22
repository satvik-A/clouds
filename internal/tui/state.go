// Package tui provides state management for CloudFS TUI.
package tui

import (
	"database/sql"
	"time"
)

// State represents the complete TUI application state
type State struct {
	// Navigation
	CurrentView ViewType
	NavIndex    int
	PrevView    ViewType

	// Data from database
	Entries      []EntryState
	Providers    []ProviderState
	Archives     []ArchiveState
	Snapshots    []SnapshotState
	TrashItems   []TrashItemState
	JournalOps   []JournalOpState
	QueueItems   []QueueItemState
	CacheEntries []CacheEntryState
	HealthData   HealthSummaryState

	// Dashboard aggregates
	Dashboard DashboardState

	// UI State
	SelectedIndex  int
	ScrollOffset   int
	FilterQuery    string
	SortColumn     string
	SortAscending  bool
	MultiSelect    map[int]bool

	// Modal State
	ModalActive bool
	ModalType   ModalType
	ModalData   interface{}
	ModalResult chan interface{}

	// Background Tasks
	ActiveTasks  []TaskState
	TaskProgress map[string]float64

	// Configuration
	Theme       Theme
	Styles      Styles
	WindowWidth  int
	WindowHeight int

	// Connection state
	DBPath     string
	Passphrase string
	Connected  bool
	LastRefresh time.Time
}

// DashboardState contains aggregated dashboard metrics
type DashboardState struct {
	TotalEntries    int
	TotalFiles      int
	TotalFolders    int
	TotalSize       int64
	CachedFiles     int
	CachedSize      int64
	PinnedFiles     int
	ArchivedFiles   int
	ArchiveSize     int64
	ActiveProviders int
	TotalPlacements int
	UnverifiedCount int
	HealthyFiles    int
	WarningFiles    int
	CriticalFiles   int
	SnapshotCount   int
	TrashCount      int
	PendingJournal  int
	PendingRequests int
}

// EntryState represents a file/folder from the index
type EntryState struct {
	ID             int64
	Name           string
	Type           string
	LogicalSize    int64
	PhysicalSize   int64
	Classification string
	CacheState     string
	IsPinned       bool
	IsPlaceholder  bool
	PlacementCount int
	VersionCount   int
	ActiveVersion  int64
	ContentHash    string
	HealthScore    float64
	InTrash        bool
	IsArchived     bool
	CreatedAt      time.Time
	ModifiedAt     time.Time
}

// ProviderState represents a cloud provider
type ProviderState struct {
	ID         int64
	Name       string
	Type       string
	Status     string
	Priority   int
	Remote     string
	Placements int
	TotalSize  int64
	IsHealthy  bool
	LastCheck  time.Time
}

// ArchiveState represents a cold archive
type ArchiveState struct {
	ID          int64
	EntryID     int64
	EntryName   string
	ArchivePath string
	Par2Path    string
	ArchiveSize int64
	ContentHash string
	CreatedAt   time.Time
	Verified    bool
	VerifiedAt  time.Time
}

// SnapshotState represents a metadata snapshot
type SnapshotState struct {
	ID          int64
	Name        string
	Description string
	EntryCount  int
	CreatedAt   time.Time
}

// TrashItemState represents a trashed item
type TrashItemState struct {
	ID           int64
	EntryID      int64
	OriginalPath string
	Size         int64
	DeletedAt    time.Time
	AutoPurgeAt  sql.NullTime
	DaysLeft     int
}

// JournalOpState represents a journal operation
type JournalOpState struct {
	ID        int64
	Operation string
	Payload   string
	State     string
	StartedAt time.Time
	Error     string
}

// QueueItemState represents a sync request
type QueueItemState struct {
	ID          int64
	DeviceID    string
	RequestType string
	State       string
	Payload     string
	CreatedAt   time.Time
}

// CacheEntryState represents a cached file
type CacheEntryState struct {
	ID           int64
	EntryID      int64
	EntryName    string
	Size         int64
	State        string
	IsPinned     bool
	LastAccessed time.Time
}

// HealthSummaryState contains health metrics
type HealthSummaryState struct {
	TotalFiles      int
	HealthyCount    int
	WarningCount    int
	CriticalCount   int
	AverageScore    float64
	UnverifiedCount int
	StaleCount      int
}

// TaskState represents a background task
type TaskState struct {
	ID          string
	Type        string
	Description string
	Progress    float64
	Status      string
	StartedAt   time.Time
	Error       string
	Cancellable bool
}

// NewState creates a new TUI state with defaults
func NewState() *State {
	theme := DarkTheme
	return &State{
		CurrentView:   ViewDashboard,
		MultiSelect:   make(map[int]bool),
		TaskProgress:  make(map[string]float64),
		Theme:         theme,
		Styles:        NewStyles(theme),
		SortAscending: true,
	}
}

// SetTheme updates the theme and regenerates styles
func (s *State) SetTheme(theme Theme) {
	s.Theme = theme
	s.Styles = NewStyles(theme)
}

// ToggleSelect toggles multi-selection for an item
func (s *State) ToggleSelect(index int) {
	if s.MultiSelect[index] {
		delete(s.MultiSelect, index)
	} else {
		s.MultiSelect[index] = true
	}
}

// ClearSelection clears all multi-selections
func (s *State) ClearSelection() {
	s.MultiSelect = make(map[int]bool)
}

// SelectAll selects all items in current view
func (s *State) SelectAll() {
	count := s.ItemCount()
	for i := 0; i < count; i++ {
		s.MultiSelect[i] = true
	}
}

// SelectedCount returns count of selected items
func (s *State) SelectedCount() int {
	return len(s.MultiSelect)
}

// ItemCount returns item count for current view
func (s *State) ItemCount() int {
	switch s.CurrentView {
	case ViewFiles:
		return len(s.Entries)
	case ViewProviders:
		return len(s.Providers)
	case ViewArchives:
		return len(s.Archives)
	case ViewSnapshots:
		return len(s.Snapshots)
	case ViewTrash:
		return len(s.TrashItems)
	case ViewJournal:
		return len(s.JournalOps)
	case ViewQueue:
		return len(s.QueueItems)
	case ViewCache:
		return len(s.CacheEntries)
	default:
		return 0
	}
}

// SelectedEntry returns currently selected entry (Files view)
func (s *State) SelectedEntry() *EntryState {
	if s.CurrentView != ViewFiles || s.SelectedIndex >= len(s.Entries) {
		return nil
	}
	return &s.Entries[s.SelectedIndex]
}

// SelectedProvider returns currently selected provider
func (s *State) SelectedProvider() *ProviderState {
	if s.CurrentView != ViewProviders || s.SelectedIndex >= len(s.Providers) {
		return nil
	}
	return &s.Providers[s.SelectedIndex]
}

// SelectedArchive returns currently selected archive
func (s *State) SelectedArchive() *ArchiveState {
	if s.CurrentView != ViewArchives || s.SelectedIndex >= len(s.Archives) {
		return nil
	}
	return &s.Archives[s.SelectedIndex]
}

// SelectedSnapshot returns currently selected snapshot
func (s *State) SelectedSnapshot() *SnapshotState {
	if s.CurrentView != ViewSnapshots || s.SelectedIndex >= len(s.Snapshots) {
		return nil
	}
	return &s.Snapshots[s.SelectedIndex]
}

// SelectedTrashItem returns currently selected trash item
func (s *State) SelectedTrashItem() *TrashItemState {
	if s.CurrentView != ViewTrash || s.SelectedIndex >= len(s.TrashItems) {
		return nil
	}
	return &s.TrashItems[s.SelectedIndex]
}
