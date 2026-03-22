# CloudFS TUI Design Document

## Overview

The CloudFS TUI is a full-featured, keyboard-driven terminal interface that exposes **every capability** of CloudFS through an intuitive, real-time interface. Built on the Bubble Tea framework (Model-View-Update architecture), it provides:

- **Complete Control**: All CLI operations accessible via keyboard
- **Live State**: Real-time sync with SQLite index and providers
- **Safety First**: Dry-run previews, confirmations, journal integrity
- **Non-blocking**: Async workers for long operations with progress bars

## Design Principles

1. **Explicit Intent Only** - No auto-hydration, no background sync
2. **Preview Before Action** - Every destructive operation shows dry-run first
3. **Keyboard-First** - Full vim-style + arrow navigation
4. **State-Driven** - Model → View → Update cycle
5. **Crash-Safe** - Journal-backed, resumable operations

---

## Screen Layout

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ CloudFS TUI                                          [H]elp [Q]uit        │
├─────────────────────────────────────────────────────────────────────────────┤
│ ┌─ Navigation ─┐  ┌─ Main View ─────────────────────────────────────────┐  │
│ │ > Dashboard  │  │                                                     │  │
│ │   Files      │  │  ╔═══════════════════════════════════════════════╗ │  │
│ │   Providers  │  │  ║             DASHBOARD OVERVIEW               ║ │  │
│ │   Archives   │  │  ╠═══════════════════════════════════════════════╣ │  │
│ │   Snapshots  │  │  ║ Entries:    1,234 files    │   56.7 GB total  ║ │  │
│ │   Trash      │  │  ║ Cache:      12.3 GB used   │   5 pinned       ║ │  │
│ │   Journal    │  │  ║ Providers:  3 active       │   89% healthy    ║ │  │
│ │   Queue      │  │  ║ Archives:   45 cold        │   234 GB         ║ │  │
│ │   Health     │  │  ║ Snapshots:  12             │                  ║ │  │
│ │   Cache      │  │  ║ Trash:      3 items        │   exp. 25 days   ║ │  │
│ │   Settings   │  │  ║ Journal:    2 pending      │                  ║ │  │
│ └──────────────┘  │  ╚═══════════════════════════════════════════════╝ │  │
│                   │                                                     │  │
│                   └─────────────────────────────────────────────────────┘  │
├─────────────────────────────────────────────────────────────────────────────┤
│ Status: Ready │ Provider: dublicate2 ● │ Encryption: ON │ 2:28 AM        │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## View Hierarchy

```
TUI Root
├── Dashboard View
│   ├── Summary Panel
│   ├── Health Widget
│   ├── Quick Actions
│   └── Alerts Panel
│
├── Files View
│   ├── Tree Browser
│   ├── File Details Pane
│   ├── Action Bar
│   └── Preview Panel
│
├── Providers View
│   ├── Provider List
│   ├── Provider Details
│   ├── Quota Gauge
│   └── Health Status
│
├── Archives View
│   ├── Archive List
│   ├── Archive Inspector
│   ├── PAR2 Verifier
│   └── Restore Dialog
│
├── Snapshots View
│   ├── Timeline
│   ├── Snapshot Diff
│   └── Restore Preview
│
├── Trash View
│   ├── Deleted Items
│   ├── Restore Dialog
│   └── Purge Confirmation
│
├── Journal View
│   ├── Pending Operations
│   ├── Operation Details
│   └── Replay/Rollback Actions
│
├── Queue View (Multi-device)
│   ├── Push Requests
│   ├── Pull Requests
│   └── Status Monitor
│
├── Health View
│   ├── Per-File Health
│   ├── Per-Provider Health
│   └── Risk Matrix
│
├── Cache View
│   ├── LRU Table
│   ├── Pinned Items
│   ├── Eviction Planner
│   └── Size Calculator
│
└── Settings View
    ├── Theme Selection
    ├── Keybindings
    └── Export Config
```

---

## State Model

```go
type TUIState struct {
    // Navigation
    CurrentView    ViewType
    NavIndex       int

    // Data (refreshed from DB)
    Entries        []Entry
    Providers      []Provider
    Archives       []Archive
    Snapshots      []Snapshot
    TrashItems     []TrashItem
    JournalOps     []JournalOp
    QueueRequests  []Request
    CacheEntries   []CacheEntry
    HealthData     HealthSummary

    // UI State
    SelectedIndex  int
    ScrollOffset   int
    FilterQuery    string
    SortColumn     string
    SortAsc        bool

    // Modal State
    ModalActive    bool
    ModalType      ModalType
    ModalData      interface{}

    // Background Tasks
    ActiveTasks    []Task
    TaskProgress   map[string]float64

    // Configuration
    Theme          Theme
    KeyBindings    KeyMap
}
```

---

## Concurrency Model

```
┌─────────────────────────────────────────────────────────────────┐
│                         Main Thread                             │
│  ┌─────────────┐   ┌─────────────┐   ┌─────────────┐          │
│  │   Render    │ → │   Update    │ → │   Dispatch  │          │
│  └─────────────┘   └─────────────┘   └─────────────┘          │
│         ↑                                   ↓                   │
│         │              ┌────────────────────┼────────────────┐ │
│         │              ↓                    ↓                ↓ │
│         │    ┌─────────────┐    ┌─────────────┐    ┌─────────┐│
│         └────│ State Store │←───│  DB Watcher │    │ Workers ││
│              └─────────────┘    └─────────────┘    └─────────┘│
│                                                          ↓     │
│                                                    ┌─────────┐ │
│                                                    │ Rclone  │ │
│                                                    │ Process │ │
│                                                    └─────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

### Worker Pools

1. **Database Watcher** - Polls SQLite for state changes (500ms)
2. **Provider Workers** - Handles uploads/downloads via rclone
3. **Archive Workers** - 7z compression and PAR2 verification
4. **Progress Ticker** - Updates progress bars (100ms)

---

## Error Handling UX

| Error Type       | Display                        | Action            |
| ---------------- | ------------------------------ | ----------------- |
| Network Error    | Red banner + retry button      | Retry / Cancel    |
| Auth Error       | Modal with credentials prompt  | Re-auth           |
| Disk Full        | Warning modal + cleanup wizard | Evict cache       |
| Corrupt Index    | Critical alert + repair button | Run repair        |
| Journal Conflict | Diff viewer + resolve options  | Rollback / Commit |

---

## Accessibility

1. **High Contrast Mode** - For visibility
2. **Color-Blind Safe** - Uses patterns + labels, not just color
3. **Screen Reader** - All widgets have text labels
4. **Large Text Mode** - Configurable font scaling (where possible in terminal)

---

## Theme System

```go
type Theme struct {
    Name            string
    Background      lipgloss.Color
    Foreground      lipgloss.Color
    Primary         lipgloss.Color
    Secondary       lipgloss.Color
    Success         lipgloss.Color
    Warning         lipgloss.Color
    Error           lipgloss.Color
    Muted           lipgloss.Color
    Border          lipgloss.Color
    HighlightBg     lipgloss.Color
    HighlightFg     lipgloss.Color
}

var DarkTheme = Theme{
    Name:        "dark",
    Background:  "#1a1b26",
    Foreground:  "#c0caf5",
    Primary:     "#7aa2f7",
    Secondary:   "#bb9af7",
    Success:     "#9ece6a",
    Warning:     "#e0af68",
    Error:       "#f7768e",
    Muted:       "#565f89",
    Border:      "#3b4261",
    HighlightBg: "#283457",
    HighlightFg: "#c0caf5",
}

var LightTheme = Theme{
    Name:        "light",
    Background:  "#f5f5f5",
    Foreground:  "#1a1b26",
    Primary:     "#2e7de9",
    Secondary:   "#7847bd",
    Success:     "#4d7f17",
    Warning:     "#b15c00",
    Error:       "#c64343",
    Muted:       "#6c7086",
    Border:      "#d0d0d0",
    HighlightBg: "#e0e0e0",
    HighlightFg: "#1a1b26",
}
```

---

## Framework Choice: Bubble Tea + Bubbles + Lip Gloss

**Why Bubble Tea?**

- Pure Go, no CGO required
- Model-View-Update (Elm-like) architecture
- Composable components via Bubbles
- Beautiful styling via Lip Gloss
- Async-safe message passing
- Active community, well-maintained

**Dependencies:**

```go
github.com/charmbracelet/bubbletea
github.com/charmbracelet/bubbles
github.com/charmbracelet/lipgloss
```
