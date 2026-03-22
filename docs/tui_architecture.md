# CloudFS TUI Architecture

## Overview

The CloudFS TUI is built using the **Bubble Tea** framework, implementing the Model-View-Update (MVU) architecture pattern inspired by Elm.

## Component Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              CloudFS TUI                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────┐     ┌─────────────────┐     ┌─────────────────────────┐  │
│  │   Model     │────▷│     Update      │────▷│         View            │  │
│  │  (State)    │◁────│  (Messages)     │◁────│       (Render)          │  │
│  └─────────────┘     └─────────────────┘     └─────────────────────────┘  │
│        │                     │                          │                  │
│        │                     │                          │                  │
│        ▼                     ▼                          ▼                  │
│  ┌─────────────┐     ┌─────────────────┐     ┌─────────────────────────┐  │
│  │   State     │     │    Actions      │     │        Styles           │  │
│  │  (state.go) │     │  (actions.go)   │     │      (theme.go)         │  │
│  └─────────────┘     └─────────────────┘     └─────────────────────────┘  │
│        │                     │                                             │
│        │                     │                                             │
│        ▼                     ▼                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐  │
│  │                    CloudFS Core Engine                               │  │
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────────┐   │  │
│  │  │  Index  │ │ Journal │ │  Cache  │ │Provider │ │  Archives   │   │  │
│  │  │ Manager │ │ Manager │ │ Manager │ │Registry │ │  Manager    │   │  │
│  │  └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────────┘   │  │
│  └─────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Files Structure

```
internal/tui/
├── model.go       # Main Bubble Tea model (Init, Update, View)
├── state.go       # Application state and data models
├── actions.go     # Action dispatcher (connects to CloudFS engine)
└── theme.go       # Themes, styles, and UI constants
```

## Data Flow

```
User Input
    │
    ▼
┌─────────────┐
│  tea.KeyMsg │
└─────────────┘
    │
    ▼
┌─────────────┐
│   Update()  │ ◀─────── Messages from async operations
└─────────────┘
    │
    ├─── Navigation (1-9 keys)
    ├─── Actions (H, D, P, T, X, etc.)
    └─── Modals (?, Esc, y/n)
    │
    ▼
┌─────────────┐
│   State     │ ◀─────── Data from ActionDispatcher
└─────────────┘
    │
    ▼
┌─────────────┐
│   View()    │
└─────────────┘
    │
    ▼
[Terminal Render]
```

## State Model

```go
type State struct {
    // Navigation
    CurrentView    ViewType    // Dashboard, Files, Providers, etc.
    SelectedIndex  int         // Currently highlighted item

    // Data (from database)
    Entries        []EntryState
    Providers      []ProviderState
    Archives       []ArchiveState
    Snapshots      []SnapshotState
    TrashItems     []TrashItemState
    JournalOps     []JournalOpState
    CacheEntries   []CacheEntryState

    // Modal state
    ModalActive    bool
    ModalType      ModalType
    ModalData      interface{}

    // UI config
    Theme          Theme
    Styles         Styles
}
```

## Message Types

| Message              | Description          | Triggers                |
| -------------------- | -------------------- | ----------------------- |
| `tea.KeyMsg`         | User keyboard input  | All key handlers        |
| `tea.WindowSizeMsg`  | Terminal resize      | Layout recalculation    |
| `dashboardLoadedMsg` | Dashboard data ready | Dashboard view render   |
| `entriesLoadedMsg`   | File entries loaded  | Files view render       |
| `providersLoadedMsg` | Providers loaded     | Providers view render   |
| `actionResultMsg`    | Action completed     | Status message, refresh |
| `errMsg`             | Error occurred       | Error display           |

## View Types

| View      | Key | Description                  |
| --------- | --- | ---------------------------- |
| Dashboard | 1   | Overview of all CloudFS data |
| Files     | 2   | File tree browser            |
| Providers | 3   | Cloud provider management    |
| Archives  | 4   | Cold archive management      |
| Snapshots | 5   | Snapshot timeline            |
| Trash     | 6   | Deleted items                |
| Journal   | 7   | Pending operations           |
| Queue     | 8   | Multi-device sync queue      |
| Health    | 9   | Health monitoring            |
| Cache     | 0   | Cache management             |

## Action Dispatcher

The `ActionDispatcher` connects the TUI to the CloudFS engine:

```go
type ActionDispatcher struct {
    dbPath     string           // Path to index.db
    passphrase string           // Encryption key
    configDir  string           // .cloudfs directory
}
```

### Methods

| Method             | Description                     |
| ------------------ | ------------------------------- |
| `LoadDashboard()`  | Aggregate all dashboard metrics |
| `LoadEntries()`    | List all indexed entries        |
| `LoadProviders()`  | List configured providers       |
| `HydrateEntry()`   | Download file from provider     |
| `DehydrateEntry()` | Remove local, keep placeholder  |
| `PinEntry()`       | Pin/unpin cache entry           |
| `TrashEntry()`     | Move to trash                   |

## Safety Invariants

1. **No auto-hydration** - Hydrate only on explicit `H` key
2. **Confirmation dialogs** - Destructive actions show modal first
3. **Dry-run support** - Preview actions before execution
4. **Journal-backed** - All mutations go through journal
5. **No background sync** - All operations are user-triggered

## Rendering Pipeline

```
View()
  │
  ├── renderHeader()      // Title + help hints
  │
  ├── renderSidebar()     // Navigation menu
  │
  ├── renderMainContent() // View-specific content
  │     ├── renderDashboard()
  │     ├── renderFiles()
  │     ├── renderProviders()
  │     └── ...
  │
  └── renderFooter()      // Status bar
```

## Theme System

```go
type Theme struct {
    Background  lipgloss.Color
    Foreground  lipgloss.Color
    Primary     lipgloss.Color    // Highlights
    Success     lipgloss.Color    // Green indicators
    Warning     lipgloss.Color    // Yellow alerts
    Error       lipgloss.Color    // Red errors
    Muted       lipgloss.Color    // Faded text
}
```

### Available Themes

- **Dark** (default) - Tokyo Night inspired
- **Light** - High contrast for accessibility

## Future Extensions

### Planned Features

1. **Progress bars** for long operations (upload/download)
2. **Background tasks panel** for running operations
3. **Search/filter** across all views
4. **Multi-select operations** (batch hydrate/dehydrate)
5. **Tab navigation** between panes

### Extension Points

- Web UI: Same state model, different render target
- macOS Finder extension: Bridge via IPC
- VS Code sidebar: Webview with state sync
