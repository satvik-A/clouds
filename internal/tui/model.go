// Package tui provides the main TUI application with responsive layout and engine bindings.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// App is the main TUI application model
type App struct {
	// Layout engine
	layout *Layout

	// State
	state *AppState

	// Bindings and executor
	bindings *KeyBindings
	executor *ActionExecutor

	// UI components
	spinner spinner.Model

	// Status
	ready     bool
	err       error
	statusMsg string
	mode      Mode

	// Pending action (for confirmation)
	pendingAction  Action
	pendingParams  ActionParams
	confirmMessage string

	// Theme
	theme  Theme
	styles Styles
}

// AppState contains the application state synchronized with the database
type AppState struct {
	// Current view
	CurrentView ViewType

	// Data loaded from database
	Dashboard  DashboardState
	Entries    []EntryState
	Providers  []ProviderState
	Archives   []ArchiveState
	Snapshots  []SnapshotState
	TrashItems []TrashItemState
	JournalOps []JournalOpState
	CacheItems []CacheEntryState
	QueueItems []QueueItemState

	// Navigation state per pane
	LeftPaneScroll  *ScrollState
	RightPaneScroll *ScrollState

	// Selection
	SelectedIndex int
	MultiSelect   map[int]bool

	// Filter/search
	FilterQuery string

	// Invariant check results
	Violations []InvariantViolation
}

// Messages for async operations
type (
	dbLoadedMsg struct {
		state *AppState
		err   error
	}
	actionCompleteMsg struct {
		result ExecuteResult
	}
	invariantCheckMsg struct {
		violations []InvariantViolation
		err        error
	}
	tickMsg struct{}
)

// NewApp creates a new TUI application
func NewApp() *App {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7"))

	theme := DarkTheme
	configDir := GetConfigDir()
	passphrase := os.Getenv("CLOUDFS_PASSPHRASE")

	return &App{
		layout:   NewLayout(),
		state:    newAppState(),
		bindings: NewKeyBindings(),
		executor: NewActionExecutor(configDir, passphrase),
		spinner:  s,
		mode:     ModeNormal,
		theme:    theme,
		styles:   NewStyles(theme),
	}
}

func newAppState() *AppState {
	return &AppState{
		CurrentView:     ViewDashboard,
		LeftPaneScroll:  NewScrollState(),
		RightPaneScroll: NewScrollState(),
		MultiSelect:     make(map[int]bool),
	}
}

// Init implements tea.Model
func (a *App) Init() tea.Cmd {
	return tea.Batch(
		a.spinner.Tick,
		a.loadFromDatabase(),
	)
}

// Update implements tea.Model
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.layout.Resize(msg.Width, msg.Height)
		a.updateScrollStates()
		a.ready = true
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		a.spinner, cmd = a.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case dbLoadedMsg:
		if msg.err != nil {
			a.err = msg.err
			a.statusMsg = fmt.Sprintf("Error: %v", msg.err)
		} else {
			a.state = msg.state
			a.updateScrollStates()
			a.statusMsg = "Data loaded"
		}

	case actionCompleteMsg:
		a.handleActionResult(msg.result)
		if msg.result.NeedsRefresh {
			cmds = append(cmds, a.loadFromDatabase())
		}

	case invariantCheckMsg:
		if msg.err != nil {
			a.statusMsg = fmt.Sprintf("Invariant check failed: %v", msg.err)
		} else {
			a.state.Violations = msg.violations
			if len(msg.violations) > 0 {
				a.statusMsg = fmt.Sprintf("⚠ %d invariant violations found", len(msg.violations))
			} else {
				a.statusMsg = "✓ All invariants satisfied"
			}
		}
	}

	return a, tea.Batch(cmds...)
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle confirmation mode
	if a.mode == ModeConfirm {
		return a.handleConfirmKey(msg)
	}

	// Map key to action
	switch msg.String() {
	// Quit
	case "q", "ctrl+c":
		return a, tea.Quit

	// Help
	case "?":
		a.mode = ModeInspect
		return a, nil

	// Back/Escape
	case "esc":
		if a.mode != ModeNormal {
			a.mode = ModeNormal
		}
		return a, nil

	// Navigation
	case "j", "down":
		a.moveSelection(1)
		return a, nil
	case "k", "up":
		a.moveSelection(-1)
		return a, nil
	case "g", "home":
		a.state.SelectedIndex = 0
		a.updateScrollStates()
		return a, nil
	case "G", "end":
		a.state.SelectedIndex = a.itemCount() - 1
		if a.state.SelectedIndex < 0 {
			a.state.SelectedIndex = 0
		}
		a.updateScrollStates()
		return a, nil

	// View switching
	case "1":
		return a.switchView(ViewDashboard)
	case "2":
		return a.switchView(ViewFiles)
	case "3":
		return a.switchView(ViewProviders)
	case "4":
		return a.switchView(ViewArchives)
	case "5":
		return a.switchView(ViewSnapshots)
	case "6":
		return a.switchView(ViewTrash)
	case "7":
		return a.switchView(ViewJournal)
	case "8":
		return a.switchView(ViewQueue)
	case "9":
		return a.switchView(ViewHealth)
	case "0":
		return a.switchView(ViewCache)

	// Refresh
	case "ctrl+r", "r":
		return a, a.loadFromDatabase()

	// File actions (require selection)
	case "H":
		return a.triggerAction(ActionHydrate, true)
	case "D":
		return a.triggerAction(ActionDehydrate, true)
	case "P":
		return a.triggerAction(ActionPin, false)
	case "T":
		return a.triggerAction(ActionRemove, true)
	case "X":
		return a.triggerAction(ActionExplain, false)
	case "V":
		return a.triggerAction(ActionVerifyFile, false)
	case "U":
		return a.triggerAction(ActionPush, true)
	case "S":
		return a.triggerAction(ActionScanIndex, false)

	// Toggle selection
	case " ":
		a.toggleSelect(a.state.SelectedIndex)
		return a, nil
	}

	return a, nil
}

func (a *App) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		a.mode = ModeNormal
		return a, a.executeAction(a.pendingAction, a.pendingParams)
	case "n", "N", "esc":
		a.mode = ModeNormal
		a.pendingAction = ActionNone
		a.statusMsg = "Cancelled"
		return a, nil
	case "d":
		// Show dry-run
		params := a.pendingParams
		params.DryRun = true
		return a, a.executeAction(a.pendingAction, params)
	}
	return a, nil
}

func (a *App) triggerAction(action Action, needsConfirm bool) (tea.Model, tea.Cmd) {
	if a.state.CurrentView != ViewFiles && action != ActionPush && action != ActionScanIndex {
		a.statusMsg = "Action only available in Files view"
		return a, nil
	}

	entry := a.selectedEntry()
	if entry == nil && action != ActionPush && action != ActionScanIndex {
		a.statusMsg = "No file selected"
		return a, nil
	}

	params := ActionParams{}
	if entry != nil {
		params.Path = entry.Name
		params.EntryID = entry.ID
	}

	if needsConfirm {
		a.mode = ModeConfirm
		a.pendingAction = action
		a.pendingParams = params
		a.confirmMessage = a.getConfirmMessage(action, params)
		return a, nil
	}

	return a, a.executeAction(action, params)
}

func (a *App) getConfirmMessage(action Action, params ActionParams) string {
	switch action {
	case ActionHydrate:
		return fmt.Sprintf("Download '%s' from cloud? (y/n, d=dry-run)", params.Path)
	case ActionDehydrate:
		return fmt.Sprintf("Remove local copy of '%s'? (y/n, d=dry-run)", params.Path)
	case ActionRemove:
		return fmt.Sprintf("Move '%s' to trash? (y/n, d=dry-run)", params.Path)
	case ActionPush:
		return "Push all pending files to providers? (y/n, d=dry-run)"
	default:
		return "Confirm action? (y/n)"
	}
}

func (a *App) executeAction(action Action, params ActionParams) tea.Cmd {
	return func() tea.Msg {
		result := a.executor.Execute(context.Background(), action, params)
		return actionCompleteMsg{result: result}
	}
}

func (a *App) handleActionResult(result ExecuteResult) {
	if result.Error != nil {
		a.statusMsg = fmt.Sprintf("✗ Error: %v", result.Error)
	} else if result.DryRunDiff != "" {
		a.statusMsg = result.DryRunDiff
	} else {
		a.statusMsg = fmt.Sprintf("✓ %s", result.Message)
	}
}

func (a *App) switchView(v ViewType) (tea.Model, tea.Cmd) {
	a.state.CurrentView = v
	a.state.SelectedIndex = 0
	a.state.MultiSelect = make(map[int]bool)
	a.updateScrollStates()
	return a, a.loadFromDatabase()
}

func (a *App) moveSelection(delta int) {
	a.state.SelectedIndex += delta
	max := a.itemCount() - 1
	if a.state.SelectedIndex < 0 {
		a.state.SelectedIndex = 0
	}
	if a.state.SelectedIndex > max {
		a.state.SelectedIndex = max
	}
	if a.state.SelectedIndex < 0 {
		a.state.SelectedIndex = 0
	}
	a.updateScrollStates()
}

func (a *App) toggleSelect(index int) {
	if a.state.MultiSelect[index] {
		delete(a.state.MultiSelect, index)
	} else {
		a.state.MultiSelect[index] = true
	}
}

func (a *App) itemCount() int {
	switch a.state.CurrentView {
	case ViewFiles:
		return len(a.state.Entries)
	case ViewProviders:
		return len(a.state.Providers)
	case ViewArchives:
		return len(a.state.Archives)
	case ViewSnapshots:
		return len(a.state.Snapshots)
	case ViewTrash:
		return len(a.state.TrashItems)
	case ViewJournal:
		return len(a.state.JournalOps)
	case ViewCache:
		return len(a.state.CacheItems)
	case ViewQueue:
		return len(a.state.QueueItems)
	}
	return 0
}

func (a *App) selectedEntry() *EntryState {
	if a.state.CurrentView != ViewFiles || a.state.SelectedIndex >= len(a.state.Entries) {
		return nil
	}
	return &a.state.Entries[a.state.SelectedIndex]
}

func (a *App) updateScrollStates() {
	visibleHeight := a.layout.ContentHeight(RegionLeftPane)
	a.state.LeftPaneScroll.Update(a.state.SelectedIndex, a.itemCount(), visibleHeight)
}

func (a *App) loadFromDatabase() tea.Cmd {
	return func() tea.Msg {
		configDir := GetConfigDir()
		passphrase := os.Getenv("CLOUDFS_PASSPHRASE")

		dispatcher, err := NewActionDispatcher(configDir, passphrase)
		if err != nil {
			return dbLoadedMsg{err: err}
		}

		ctx := context.Background()
		state := newAppState()

		// Load dashboard
		dash, err := dispatcher.LoadDashboard(ctx)
		if err == nil && dash != nil {
			state.Dashboard = *dash
		}

		// Load entries
		entries, err := dispatcher.LoadEntries(ctx, "")
		if err == nil {
			state.Entries = entries
		}

		// Load providers
		providers, err := dispatcher.LoadProviders(ctx)
		if err == nil {
			state.Providers = providers
		}

		// Load archives
		archives, err := dispatcher.LoadArchives(ctx)
		if err == nil {
			state.Archives = archives
		}

		// Load snapshots
		snapshots, err := dispatcher.LoadSnapshots(ctx)
		if err == nil {
			state.Snapshots = snapshots
		}

		// Load trash
		trash, err := dispatcher.LoadTrash(ctx)
		if err == nil {
			state.TrashItems = trash
		}

		// Load journal
		journal, err := dispatcher.LoadJournal(ctx)
		if err == nil {
			state.JournalOps = journal
		}

		// Load cache
		cache, err := dispatcher.LoadCache(ctx)
		if err == nil {
			state.CacheItems = cache
		}

		// Load queue
		queue, err := dispatcher.LoadQueue(ctx)
		if err == nil {
			state.QueueItems = queue
		}

		return dbLoadedMsg{state: state}
	}
}

// View implements tea.Model - renders the entire UI
func (a *App) View() string {
	if !a.ready {
		return a.spinner.View() + " Initializing CloudFS TUI..."
	}

	if a.err != nil {
		return a.styles.Error.Render(fmt.Sprintf("Error: %v\n\nPress 'q' to quit.", a.err))
	}

	var b strings.Builder

	// Render each region
	topBar := a.renderTopBar()
	leftPane := a.renderLeftPane()
	rightPane := a.renderRightPane()
	bottomBar := a.renderBottomBar()

	// Compose layout
	b.WriteString(topBar)
	b.WriteString("\n")

	// Main content: left and right panes side by side
	mainContent := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	b.WriteString(mainContent)
	b.WriteString("\n")

	b.WriteString(bottomBar)

	// Overlay confirm dialog if in confirm mode
	if a.mode == ModeConfirm {
		return a.overlayConfirmDialog(b.String())
	}

	// Overlay help if in inspect mode
	if a.mode == ModeInspect {
		return a.overlayHelp(b.String())
	}

	return b.String()
}

func (a *App) renderTopBar() string {
	style := a.layout.PaneStyle(RegionTopBar, a.theme)
	width := a.layout.TopBar.Width

	// Left: repo info
	repoInfo := "CloudFS"

	// Center: view name
	viewName := a.state.CurrentView.String()

	// Right: status indicators
	providerCount := len(a.state.Providers)
	cacheInfo := formatBytes(a.state.Dashboard.CachedSize)
	health := "●"
	if len(a.state.Violations) > 0 {
		health = "⚠"
	}

	right := fmt.Sprintf("%s │ %d prov │ Cache: %s", health, providerCount, cacheInfo)

	// Calculate spacing
	spacing := width - len(repoInfo) - len(viewName) - len(right) - 8
	if spacing < 1 {
		spacing = 1
	}

	content := fmt.Sprintf(" %s%s%s%s%s ",
		repoInfo,
		strings.Repeat(" ", spacing/2),
		viewName,
		strings.Repeat(" ", spacing/2),
		right,
	)

	return style.Render(content)
}

func (a *App) renderLeftPane() string {
	style := a.layout.PaneStyle(RegionLeftPane, a.theme)
	width := a.layout.LeftPane.Width
	height := a.layout.LeftPane.Height

	var lines []string

	// View navigation
	views := []ViewType{
		ViewDashboard, ViewFiles, ViewProviders, ViewArchives,
		ViewSnapshots, ViewTrash, ViewJournal, ViewQueue, ViewHealth, ViewCache,
	}

	for _, v := range views {
		prefix := "  "
		if v == a.state.CurrentView {
			prefix = "▶ "
		}
		label := fmt.Sprintf("%s[%s] %s", prefix, v.Key(), v.String())
		if v == a.state.CurrentView {
			label = a.styles.Selected.Render(label)
		}
		lines = append(lines, Truncate(label, width-4))
	}

	// Pad to height
	for len(lines) < height-2 {
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")
	return style.Width(width).Height(height).Render(content)
}

func (a *App) renderRightPane() string {
	style := a.layout.PaneStyle(RegionRightPane, a.theme)
	width := a.layout.RightPane.Width
	height := a.layout.RightPane.Height

	var content string

	switch a.state.CurrentView {
	case ViewDashboard:
		content = a.renderDashboardContent(width, height)
	case ViewFiles:
		content = a.renderFilesContent(width, height)
	case ViewProviders:
		content = a.renderProvidersContent(width, height)
	case ViewArchives:
		content = a.renderArchivesContent(width, height)
	case ViewSnapshots:
		content = a.renderSnapshotsContent(width, height)
	case ViewTrash:
		content = a.renderTrashContent(width, height)
	case ViewJournal:
		content = a.renderJournalContent(width, height)
	case ViewQueue:
		content = a.renderQueueContent(width, height)
	case ViewHealth:
		content = a.renderHealthContent(width, height)
	case ViewCache:
		content = a.renderCacheContent(width, height)
	default:
		content = "View not implemented"
	}

	return style.Width(width).Height(height).Render(content)
}

func (a *App) renderBottomBar() string {
	style := a.layout.PaneStyle(RegionBottomBar, a.theme)
	width := a.layout.BottomBar.Width
	height := a.layout.BottomBar.Height

	// Line 1: Status message
	status := a.statusMsg
	if status == "" {
		status = "Ready"
	}
	status = Truncate(status, width-4)

	// Line 2: Key hints
	hints := "[?] Help  [H]ydrate  [D]ehydrate  [P]in  [T]rash  [U]push  [S]can  [q]Quit"
	hints = Truncate(hints, width-4)

	// Line 3: Selection info
	selInfo := fmt.Sprintf("View: %s │ Items: %d", a.state.CurrentView.String(), a.itemCount())
	if len(a.state.MultiSelect) > 0 {
		selInfo += fmt.Sprintf(" │ Selected: %d", len(a.state.MultiSelect))
	}
	if a.state.SelectedIndex < a.itemCount() {
		selInfo += fmt.Sprintf(" │ [%d]", a.state.SelectedIndex+1)
	}
	selInfo = Truncate(selInfo, width-4)

	var lines []string
	lines = append(lines, status)
	lines = append(lines, hints)
	if height > 3 {
		lines = append(lines, selInfo)
	}

	content := strings.Join(lines, "\n")
	return style.Width(width).Height(height).Render(content)
}

// Content renderers

func (a *App) renderDashboardContent(width, height int) string {
	d := a.state.Dashboard
	var lines []string

	lines = append(lines, a.styles.Title.Render("Dashboard Overview"))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("Files:      %5d    │  Size: %s", d.TotalFiles, formatBytes(d.TotalSize)))
	lines = append(lines, fmt.Sprintf("Cached:     %5d    │  Size: %s", d.CachedFiles, formatBytes(d.CachedSize)))
	lines = append(lines, fmt.Sprintf("Pinned:     %5d    │  Providers: %d", d.PinnedFiles, d.ActiveProviders))
	lines = append(lines, fmt.Sprintf("Archived:   %5d    │  Size: %s", d.ArchivedFiles, formatBytes(d.ArchiveSize)))
	lines = append(lines, fmt.Sprintf("Snapshots:  %5d    │  Trash: %d", d.SnapshotCount, d.TrashCount))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("Pending Journal: %d", d.PendingJournal))
	lines = append(lines, fmt.Sprintf("Pending Requests: %d", d.PendingRequests))

	if len(a.state.Violations) > 0 {
		lines = append(lines, "")
		lines = append(lines, a.styles.Warning.Render(fmt.Sprintf("⚠ %d invariant violations", len(a.state.Violations))))
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderFilesContent(width, height int) string {
	if len(a.state.Entries) == 0 {
		return a.styles.Muted.Render("No files. Use 'cloudfs add <path>' to add files.")
	}

	var lines []string

	// Header
	header := fmt.Sprintf("%-4s %-20s %10s %6s %4s", "Type", "Name", "Size", "Cache", "Prov")
	lines = append(lines, a.styles.TableHeader.Render(Truncate(header, width-4)))

	// Entries
	scroll := a.state.LeftPaneScroll
	scroll.Update(a.state.SelectedIndex, len(a.state.Entries), height-4)
	start, end := scroll.VisibleRange()

	for i := start; i < end && i < len(a.state.Entries); i++ {
		e := a.state.Entries[i]

		icon := "📄"
		if e.Type == "folder" {
			icon = "📁"
		}

		cacheState := "○"
		if e.CacheState == "ready" {
			cacheState = "●"
		}
		if e.IsPinned {
			cacheState = "📌"
		}

		name := Truncate(e.Name, 20)
		line := fmt.Sprintf("%s %-20s %10s %6s %4d",
			icon, name, formatBytes(e.LogicalSize), cacheState, e.PlacementCount)

		if i == a.state.SelectedIndex {
			line = a.styles.Selected.Render(line)
		} else if a.state.MultiSelect[i] {
			line = a.styles.Info.Render(line)
		}

		lines = append(lines, Truncate(line, width-4))
	}

	// Scrollbar indicator
	if scroll.HasScrollbar() {
		lines = append(lines, a.styles.Muted.Render(
			fmt.Sprintf("↕ %d-%d of %d", start+1, end, len(a.state.Entries))))
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderProvidersContent(width, height int) string {
	if len(a.state.Providers) == 0 {
		return a.styles.Muted.Render("No providers. Use 'cloudfs provider add'.")
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("Providers"))
	lines = append(lines, "")

	for i, p := range a.state.Providers {
		status := a.styles.Success.Render("●")
		if p.Status != "active" {
			status = a.styles.Warning.Render("○")
		}

		line := fmt.Sprintf("%s %-12s │ %s │ %d files",
			status, Truncate(p.Name, 12), formatBytes(p.TotalSize), p.Placements)

		if i == a.state.SelectedIndex {
			line = a.styles.Selected.Render(line)
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderArchivesContent(width, height int) string {
	if len(a.state.Archives) == 0 {
		return a.styles.Muted.Render("No archives. Use 'cloudfs archive create'.")
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("Archives"))
	lines = append(lines, "")

	for i, arch := range a.state.Archives {
		line := fmt.Sprintf("%-20s │ %s │ %s",
			Truncate(arch.EntryName, 20), formatBytes(arch.ArchiveSize),
			arch.CreatedAt.Format("2006-01-02"))

		if i == a.state.SelectedIndex {
			line = a.styles.Selected.Render(line)
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderSnapshotsContent(width, height int) string {
	if len(a.state.Snapshots) == 0 {
		return a.styles.Muted.Render("No snapshots. Use 'cloudfs snapshot create'.")
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("Snapshots"))
	lines = append(lines, "")

	for i, snap := range a.state.Snapshots {
		line := fmt.Sprintf("%-16s │ %4d entries │ %s",
			Truncate(snap.Name, 16), snap.EntryCount, snap.CreatedAt.Format("2006-01-02 15:04"))

		if i == a.state.SelectedIndex {
			line = a.styles.Selected.Render(line)
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderTrashContent(width, height int) string {
	if len(a.state.TrashItems) == 0 {
		return a.styles.Muted.Render("Trash is empty.")
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("Trash"))
	lines = append(lines, "")

	for i, item := range a.state.TrashItems {
		days := ""
		if item.DaysLeft > 0 {
			days = fmt.Sprintf("(%dd)", item.DaysLeft)
		}

		line := fmt.Sprintf("%-24s │ %s %s",
			Truncate(item.OriginalPath, 24), formatBytes(item.Size), days)

		if i == a.state.SelectedIndex {
			line = a.styles.Selected.Render(line)
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderJournalContent(width, height int) string {
	if len(a.state.JournalOps) == 0 {
		return a.styles.Muted.Render("No journal operations.")
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("Journal Operations"))
	lines = append(lines, "")

	for i, op := range a.state.JournalOps {
		stateStyle := a.styles.Muted
		switch op.State {
		case "pending":
			stateStyle = a.styles.Warning
		case "committed":
			stateStyle = a.styles.Success
		case "failed":
			stateStyle = a.styles.Error
		}

		line := fmt.Sprintf("%-12s │ %s │ %s",
			Truncate(op.Operation, 12), stateStyle.Render(op.State), op.StartedAt.Format("15:04:05"))

		if i == a.state.SelectedIndex {
			line = a.styles.Selected.Render(line)
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderQueueContent(width, height int) string {
	if len(a.state.QueueItems) == 0 {
		return a.styles.Muted.Render("No pending requests.")
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("Request Queue"))
	lines = append(lines, "")

	for i, item := range a.state.QueueItems {
		line := fmt.Sprintf("%-10s │ %-8s │ %s │ %s",
			Truncate(item.DeviceID, 10), item.RequestType, item.State, item.CreatedAt.Format("15:04:05"))

		if i == a.state.SelectedIndex {
			line = a.styles.Selected.Render(line)
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderHealthContent(width, height int) string {
	var lines []string
	lines = append(lines, a.styles.Title.Render("Health Monitor"))
	lines = append(lines, "")

	if len(a.state.Violations) == 0 {
		lines = append(lines, a.styles.Success.Render("✓ All invariants satisfied"))
	} else {
		for _, v := range a.state.Violations {
			icon := "•"
			style := a.styles.Muted
			switch v.Severity {
			case SeverityCritical, SeverityError:
				icon = "✗"
				style = a.styles.Error
			case SeverityWarning:
				icon = "⚠"
				style = a.styles.Warning
			}
			line := style.Render(fmt.Sprintf("%s [%s] %s", icon, v.Invariant, v.Description))
			lines = append(lines, Truncate(line, width-4))
		}
	}

	return strings.Join(lines, "\n")
}

func (a *App) renderCacheContent(width, height int) string {
	if len(a.state.CacheItems) == 0 {
		return a.styles.Muted.Render("Cache is empty.")
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("Cache"))
	lines = append(lines, "")

	for i, e := range a.state.CacheItems {
		pin := "  "
		if e.IsPinned {
			pin = "📌"
		}

		line := fmt.Sprintf("%s %-20s │ %10s │ %s",
			pin, Truncate(e.EntryName, 20), formatBytes(e.Size), e.State)

		if i == a.state.SelectedIndex {
			line = a.styles.Selected.Render(line)
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (a *App) overlayConfirmDialog(base string) string {
	// Create confirm dialog content
	dialog := fmt.Sprintf(`
┌─────────────────────────────────────────┐
│          Confirm Action                 │
├─────────────────────────────────────────┤
│                                         │
│  %s
│                                         │
│  [y] Yes   [n] No   [d] Dry-run        │
└─────────────────────────────────────────┘
`, a.confirmMessage)

	dialogContent := a.styles.Modal.Render(dialog)

	// Center on screen
	x := (a.layout.Width - 45) / 2
	y := (a.layout.Height - 8) / 2

	return a.overlayAt(base, dialogContent, x, y)
}

func (a *App) overlayHelp(base string) string {
	help := `┌──────────────────────────────────────────────────────────────┐
│                        Help - Press ESC to close             │
├──────────────────────────────────────────────────────────────┤
│  Navigation           │  Actions                              │
│  ─────────────────────┼────────────────────────────            │
│  j/k, ↑/↓   Move      │  H  Hydrate (download)                │
│  1-9, 0     Views     │  D  Dehydrate (remove local)          │
│  Enter      Select    │  P  Pin/Unpin                        │
│  Space      Multi     │  T  Move to Trash                    │
│  r, Ctrl+R  Refresh   │  U  Push to providers                │
│  q          Quit      │  S  Scan index                       │
│  ?          Help      │  X  Explain entry                    │
│  Esc        Back      │  V  Verify                           │
└──────────────────────────────────────────────────────────────┘`

	helpContent := a.styles.Modal.Render(help)
	x := (a.layout.Width - 66) / 2
	y := (a.layout.Height - 14) / 2

	return a.overlayAt(base, helpContent, x, y)
}

func (a *App) overlayAt(base, overlay string, x, y int) string {
	baseLines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")

	for i, oLine := range overlayLines {
		lineIdx := y + i
		if lineIdx >= 0 && lineIdx < len(baseLines) {
			baseLine := baseLines[lineIdx]
			// Simple overlay: replace chars
			newLine := baseLine
			if x >= 0 && x < len(baseLine) {
				prefix := ""
				if x > 0 && x < len(baseLine) {
					prefix = baseLine[:x]
				}
				newLine = prefix + oLine
				if len(newLine) < len(baseLine) {
					newLine += baseLine[len(newLine):]
				}
			} else if x >= 0 {
				newLine = strings.Repeat(" ", x) + oLine
			}
			baseLines[lineIdx] = newLine
		}
	}

	return strings.Join(baseLines, "\n")
}
