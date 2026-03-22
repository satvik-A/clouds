// Package tui provides a full-featured terminal user interface for CloudFS.
// Built on Bubble Tea (Elm-like MVU architecture) with Bubbles components.
package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// ViewType represents different TUI views
type ViewType int

const (
	ViewDashboard ViewType = iota
	ViewFiles
	ViewProviders
	ViewArchives
	ViewSnapshots
	ViewTrash
	ViewJournal
	ViewQueue
	ViewHealth
	ViewCache
	ViewSettings
)

// ModalType represents different modal dialogs
type ModalType int

const (
	ModalNone ModalType = iota
	ModalConfirm
	ModalProgress
	ModalInput
	ModalHelp
	ModalError
	ModalExplain
	ModalDryRun
)

// Theme defines the color scheme for the TUI
type Theme struct {
	Name        string
	Background  lipgloss.Color
	Foreground  lipgloss.Color
	Primary     lipgloss.Color
	Secondary   lipgloss.Color
	Success     lipgloss.Color
	Warning     lipgloss.Color
	Error       lipgloss.Color
	Muted       lipgloss.Color
	Border      lipgloss.Color
	HighlightBg lipgloss.Color
	HighlightFg lipgloss.Color
}

// DarkTheme is the default dark color scheme
var DarkTheme = Theme{
	Name:        "dark",
	Background:  lipgloss.Color("#1a1b26"),
	Foreground:  lipgloss.Color("#c0caf5"),
	Primary:     lipgloss.Color("#7aa2f7"),
	Secondary:   lipgloss.Color("#bb9af7"),
	Success:     lipgloss.Color("#9ece6a"),
	Warning:     lipgloss.Color("#e0af68"),
	Error:       lipgloss.Color("#f7768e"),
	Muted:       lipgloss.Color("#565f89"),
	Border:      lipgloss.Color("#3b4261"),
	HighlightBg: lipgloss.Color("#283457"),
	HighlightFg: lipgloss.Color("#c0caf5"),
}

// LightTheme is an alternative light color scheme
var LightTheme = Theme{
	Name:        "light",
	Background:  lipgloss.Color("#f5f5f5"),
	Foreground:  lipgloss.Color("#1a1b26"),
	Primary:     lipgloss.Color("#2e7de9"),
	Secondary:   lipgloss.Color("#7847bd"),
	Success:     lipgloss.Color("#4d7f17"),
	Warning:     lipgloss.Color("#b15c00"),
	Error:       lipgloss.Color("#c64343"),
	Muted:       lipgloss.Color("#6c7086"),
	Border:      lipgloss.Color("#d0d0d0"),
	HighlightBg: lipgloss.Color("#e0e0e0"),
	HighlightFg: lipgloss.Color("#1a1b26"),
}

// Styles contains all UI styles derived from the theme
type Styles struct {
	Theme Theme

	// Layout styles
	App         lipgloss.Style
	Header      lipgloss.Style
	Footer      lipgloss.Style
	Sidebar     lipgloss.Style
	MainPane    lipgloss.Style
	StatusBar   lipgloss.Style

	// Component styles
	Title       lipgloss.Style
	Subtitle    lipgloss.Style
	Label       lipgloss.Style
	Value       lipgloss.Style
	Muted       lipgloss.Style
	Selected    lipgloss.Style
	Focused     lipgloss.Style

	// State styles
	Success     lipgloss.Style
	Warning     lipgloss.Style
	Error       lipgloss.Style
	Info        lipgloss.Style

	// Table styles
	TableHeader lipgloss.Style
	TableRow    lipgloss.Style
	TableRowAlt lipgloss.Style

	// Modal styles
	Modal       lipgloss.Style
	ModalTitle  lipgloss.Style
	Button      lipgloss.Style
	ButtonActive lipgloss.Style

	// Icons
	IconFile        string
	IconFolder      string
	IconPlaceholder string
	IconCached      string
	IconPinned      string
	IconArchived    string
	IconHealthy     string
	IconWarning     string
	IconError       string
	IconProvider    string
}

// NewStyles creates styles from a theme
func NewStyles(theme Theme) Styles {
	return Styles{
		Theme: theme,

		App: lipgloss.NewStyle().
			Background(theme.Background).
			Foreground(theme.Foreground),

		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Primary).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(theme.Border).
			Padding(0, 1),

		Footer: lipgloss.NewStyle().
			Foreground(theme.Muted).
			BorderStyle(lipgloss.NormalBorder()).
			BorderTop(true).
			BorderForeground(theme.Border).
			Padding(0, 1),

		Sidebar: lipgloss.NewStyle().
			BorderStyle(lipgloss.NormalBorder()).
			BorderRight(true).
			BorderForeground(theme.Border).
			Padding(1, 1),

		MainPane: lipgloss.NewStyle().
			Padding(1, 2),

		StatusBar: lipgloss.NewStyle().
			Foreground(theme.Muted).
			Padding(0, 1),

		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Primary),

		Subtitle: lipgloss.NewStyle().
			Foreground(theme.Secondary),

		Label: lipgloss.NewStyle().
			Foreground(theme.Muted),

		Value: lipgloss.NewStyle().
			Foreground(theme.Foreground),

		Muted: lipgloss.NewStyle().
			Foreground(theme.Muted),

		Selected: lipgloss.NewStyle().
			Background(theme.HighlightBg).
			Foreground(theme.HighlightFg),

		Focused: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(theme.Primary),

		Success: lipgloss.NewStyle().
			Foreground(theme.Success),

		Warning: lipgloss.NewStyle().
			Foreground(theme.Warning),

		Error: lipgloss.NewStyle().
			Foreground(theme.Error),

		Info: lipgloss.NewStyle().
			Foreground(theme.Primary),

		TableHeader: lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Primary).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(theme.Border),

		TableRow: lipgloss.NewStyle().
			Foreground(theme.Foreground),

		TableRowAlt: lipgloss.NewStyle().
			Foreground(theme.Foreground).
			Background(lipgloss.Color("#1e1f2a")),

		Modal: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.Primary).
			Padding(1, 2).
			Background(theme.Background),

		ModalTitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Primary).
			MarginBottom(1),

		Button: lipgloss.NewStyle().
			Foreground(theme.Foreground).
			Background(theme.Border).
			Padding(0, 2).
			MarginRight(1),

		ButtonActive: lipgloss.NewStyle().
			Foreground(theme.Background).
			Background(theme.Primary).
			Padding(0, 2).
			MarginRight(1),

		// Icons (using Unicode)
		IconFile:        "📄",
		IconFolder:      "📁",
		IconPlaceholder: "☁️",
		IconCached:      "💾",
		IconPinned:      "📌",
		IconArchived:    "🗄️",
		IconHealthy:     "✓",
		IconWarning:     "⚠",
		IconError:       "✗",
		IconProvider:    "☁",
	}
}

// ViewName returns the display name for a view
func (v ViewType) String() string {
	names := []string{
		"Dashboard",
		"Files",
		"Providers",
		"Archives",
		"Snapshots",
		"Trash",
		"Journal",
		"Queue",
		"Health",
		"Cache",
		"Settings",
	}
	if int(v) < len(names) {
		return names[v]
	}
	return "Unknown"
}

// Key returns the keyboard shortcut for a view
func (v ViewType) Key() string {
	keys := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "0", "s"}
	if int(v) < len(keys) {
		return keys[v]
	}
	return ""
}
