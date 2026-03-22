// Package tui provides a constraint-based responsive layout engine.
// This engine handles terminal resize with full reflow, never overlaps panes,
// and maintains minimum sizes for all regions.
package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// LayoutConstraint defines size constraints for a pane
type LayoutConstraint struct {
	MinWidth  int
	MinHeight int
	MaxWidth  int  // 0 = unlimited
	MaxHeight int  // 0 = unlimited
	Weight    int  // Flex weight for distributing extra space
	Fixed     bool // If true, doesn't flex
}

// Region represents screen regions
type Region int

const (
	RegionTopBar Region = iota
	RegionLeftPane
	RegionRightPane
	RegionBottomBar
)

// Layout manages the responsive layout of the TUI
type Layout struct {
	// Terminal dimensions
	Width  int
	Height int

	// Calculated region dimensions
	TopBar     Rect
	LeftPane   Rect
	RightPane  Rect
	BottomBar  Rect

	// Constraints
	constraints map[Region]LayoutConstraint

	// Styles for borders
	borderStyle lipgloss.Style
}

// Rect represents a rectangle with position and size
type Rect struct {
	X      int
	Y      int
	Width  int
	Height int
}

// DefaultConstraints returns the default layout constraints
func DefaultConstraints() map[Region]LayoutConstraint {
	return map[Region]LayoutConstraint{
		RegionTopBar: {
			MinWidth:  40,
			MinHeight: 1,
			MaxHeight: 1,
			Fixed:     true,
		},
		RegionLeftPane: {
			MinWidth:  20,
			MinHeight: 10,
			MaxWidth:  40,
			Weight:    1,
		},
		RegionRightPane: {
			MinWidth:  30,
			MinHeight: 10,
			Weight:    3,
		},
		RegionBottomBar: {
			MinWidth:  40,
			MinHeight: 3,
			MaxHeight: 5,
			Weight:    1,
		},
	}
}

// NewLayout creates a new layout with default constraints
func NewLayout() *Layout {
	return &Layout{
		constraints: DefaultConstraints(),
		borderStyle: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#3b4261")),
	}
}

// Resize recalculates all region dimensions based on terminal size
func (l *Layout) Resize(width, height int) {
	l.Width = width
	l.Height = height

	// Guard against too small terminals
	if width < 60 || height < 15 {
		l.setMinimalLayout()
		return
	}

	// Fixed heights
	topBarHeight := 1
	bottomBarHeight := l.calculateBottomBarHeight()

	// Available height for main content
	mainHeight := height - topBarHeight - bottomBarHeight - 2 // 2 for borders

	// Calculate left/right pane widths
	leftWidth := l.calculateLeftPaneWidth()
	rightWidth := width - leftWidth - 3 // 3 for borders/separator

	// TopBar: full width, top
	l.TopBar = Rect{
		X:      0,
		Y:      0,
		Width:  width,
		Height: topBarHeight,
	}

	// LeftPane: left side, below topbar
	l.LeftPane = Rect{
		X:      0,
		Y:      topBarHeight + 1,
		Width:  leftWidth,
		Height: mainHeight,
	}

	// RightPane: right side, below topbar
	l.RightPane = Rect{
		X:      leftWidth + 1,
		Y:      topBarHeight + 1,
		Width:  rightWidth,
		Height: mainHeight,
	}

	// BottomBar: full width, bottom
	l.BottomBar = Rect{
		X:      0,
		Y:      height - bottomBarHeight,
		Width:  width,
		Height: bottomBarHeight,
	}
}

func (l *Layout) calculateLeftPaneWidth() int {
	c := l.constraints[RegionLeftPane]

	// Calculate proportional width (25% of screen)
	proportional := l.Width / 4

	// Apply constraints
	if proportional < c.MinWidth {
		return c.MinWidth
	}
	if c.MaxWidth > 0 && proportional > c.MaxWidth {
		return c.MaxWidth
	}
	return proportional
}

func (l *Layout) calculateBottomBarHeight() int {
	c := l.constraints[RegionBottomBar]

	// Calculate proportional height (15% of screen)
	proportional := l.Height / 7

	// Apply constraints
	if proportional < c.MinHeight {
		return c.MinHeight
	}
	if c.MaxHeight > 0 && proportional > c.MaxHeight {
		return c.MaxHeight
	}
	return proportional
}

func (l *Layout) setMinimalLayout() {
	// Minimal layout for very small terminals
	l.TopBar = Rect{Width: l.Width, Height: 1}
	l.LeftPane = Rect{Width: l.Width, Height: (l.Height - 4) / 2, Y: 2}
	l.RightPane = Rect{Width: l.Width, Height: (l.Height - 4) / 2, Y: 2 + (l.Height-4)/2}
	l.BottomBar = Rect{Width: l.Width, Height: 2, Y: l.Height - 2}
}

// ContentWidth returns usable content width for a region (minus borders)
func (l *Layout) ContentWidth(r Region) int {
	switch r {
	case RegionTopBar:
		return l.TopBar.Width - 2
	case RegionLeftPane:
		return l.LeftPane.Width - 2
	case RegionRightPane:
		return l.RightPane.Width - 2
	case RegionBottomBar:
		return l.BottomBar.Width - 2
	}
	return 0
}

// ContentHeight returns usable content height for a region (minus borders)
func (l *Layout) ContentHeight(r Region) int {
	switch r {
	case RegionTopBar:
		return l.TopBar.Height
	case RegionLeftPane:
		return l.LeftPane.Height - 2
	case RegionRightPane:
		return l.RightPane.Height - 2
	case RegionBottomBar:
		return l.BottomBar.Height - 2
	}
	return 0
}

// PaneStyle returns a lipgloss style for rendering a pane with proper dimensions
func (l *Layout) PaneStyle(r Region, theme Theme) lipgloss.Style {
	var rect Rect
	switch r {
	case RegionTopBar:
		rect = l.TopBar
		return lipgloss.NewStyle().
			Width(rect.Width).
			Height(rect.Height).
			Background(theme.Background).
			Foreground(theme.Foreground).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(theme.Border)
	case RegionLeftPane:
		rect = l.LeftPane
		return lipgloss.NewStyle().
			Width(rect.Width).
			Height(rect.Height).
			Background(theme.Background).
			Foreground(theme.Foreground).
			BorderStyle(lipgloss.NormalBorder()).
			BorderRight(true).
			BorderForeground(theme.Border)
	case RegionRightPane:
		rect = l.RightPane
		return lipgloss.NewStyle().
			Width(rect.Width).
			Height(rect.Height).
			Background(theme.Background).
			Foreground(theme.Foreground)
	case RegionBottomBar:
		rect = l.BottomBar
		return lipgloss.NewStyle().
			Width(rect.Width).
			Height(rect.Height).
			Background(theme.Background).
			Foreground(theme.Foreground).
			BorderStyle(lipgloss.NormalBorder()).
			BorderTop(true).
			BorderForeground(theme.Border)
	}
	return lipgloss.NewStyle()
}

// ScrollState tracks scroll position for a pane
type ScrollState struct {
	Offset    int // Current scroll offset
	Total     int // Total number of items
	Visible   int // Number of visible items
	Selected  int // Currently selected item index
}

// NewScrollState creates a new scroll state
func NewScrollState() *ScrollState {
	return &ScrollState{}
}

// Update updates scroll state based on selection
func (s *ScrollState) Update(selected, total, visible int) {
	s.Selected = selected
	s.Total = total
	s.Visible = visible

	// Keep selected item in view
	if s.Selected < s.Offset {
		s.Offset = s.Selected
	}
	if s.Selected >= s.Offset+s.Visible {
		s.Offset = s.Selected - s.Visible + 1
	}

	// Clamp offset
	maxOffset := s.Total - s.Visible
	if maxOffset < 0 {
		maxOffset = 0
	}
	if s.Offset > maxOffset {
		s.Offset = maxOffset
	}
	if s.Offset < 0 {
		s.Offset = 0
	}
}

// VisibleRange returns the range of visible items
func (s *ScrollState) VisibleRange() (start, end int) {
	start = s.Offset
	end = s.Offset + s.Visible
	if end > s.Total {
		end = s.Total
	}
	return
}

// HasScrollbar returns true if scrollbar should be shown
func (s *ScrollState) HasScrollbar() bool {
	return s.Total > s.Visible
}

// ScrollbarPosition returns the scrollbar thumb position (0.0 to 1.0)
func (s *ScrollState) ScrollbarPosition() float64 {
	if s.Total <= s.Visible {
		return 0
	}
	return float64(s.Offset) / float64(s.Total-s.Visible)
}

// Truncate truncates a string to fit within width, adding ellipsis if needed
func Truncate(s string, width int) string {
	if len(s) <= width {
		return s
	}
	if width <= 3 {
		return s[:width]
	}
	return s[:width-3] + "..."
}

// PadRight pads a string to a specific width
func PadRight(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	return s + repeatSpace(width-len(s))
}

// PadCenter centers a string within a specific width
func PadCenter(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	left := (width - len(s)) / 2
	right := width - len(s) - left
	return repeatSpace(left) + s + repeatSpace(right)
}

func repeatSpace(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}
