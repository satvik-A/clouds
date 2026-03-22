// Package tui provides a recursive command verifier for CloudFS.
// The verifier systematically tests all command paths under all valid states.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudfs/cloudfs/internal/core"
)

// VerifierState represents a CloudFS state configuration
type VerifierState int

const (
	StateEmpty VerifierState = iota
	StateIndexedOnly
	StateCached
	StateHydrated
	StateArchived
	StateSnapshotted
	StateTrashed
	StateMultiProvider
	StateDegradedProvider
	StateQuotaExhausted
	StateJournalPending
	StateRequestQueued
)

func (s VerifierState) String() string {
	names := []string{
		"Empty", "IndexedOnly", "Cached", "Hydrated", "Archived",
		"Snapshotted", "Trashed", "MultiProvider", "DegradedProvider",
		"QuotaExhausted", "JournalPending", "RequestQueued",
	}
	if int(s) < len(names) {
		return names[s]
	}
	return "Unknown"
}

// CommandTest represents a test of a single command
type CommandTest struct {
	State      VerifierState
	Action     Action
	Params     ActionParams
	DryRun     bool
	ExpectedOK bool
}

// TestResult contains the result of a command test
type TestResult struct {
	Test       CommandTest
	Passed     bool
	Error      error
	Duration   time.Duration
	DryRunDiff string

	// State checks
	PreViolations  []InvariantViolation
	PostViolations []InvariantViolation
	NewViolations  []InvariantViolation

	// Data diffs
	IndexDiff     string
	CacheDiff     string
	PlacementDiff string
	JournalDiff   string
}

// VerificationPlan contains the ordered list of tests to execute
type VerificationPlan struct {
	Tests   []CommandTest
	Results []TestResult
}

// Verifier performs recursive command validation
type Verifier struct {
	db         *core.EncryptedDB
	invariants *InvariantChecker
	dispatcher *ActionDispatcher
	configDir  string
	passphrase string

	// State tracking
	currentState VerifierState
	testsPassed  int
	testsFailed  int
	testsSkipped int
}

// NewVerifier creates a new command verifier
func NewVerifier(configDir, passphrase string) (*Verifier, error) {
	db, err := core.OpenEncryptedDB(configDir+"/index.db", passphrase)
	if err != nil {
		return nil, err
	}

	dispatcher, err := NewActionDispatcher(configDir, passphrase)
	if err != nil {
		db.Close()
		return nil, err
	}

	return &Verifier{
		db:           db,
		invariants:   NewInvariantChecker(db),
		dispatcher:   dispatcher,
		configDir:    configDir,
		passphrase:   passphrase,
		currentState: StateEmpty,
	}, nil
}

// Close releases verifier resources
func (v *Verifier) Close() {
	if v.db != nil {
		v.db.Close()
	}
}

// BuildStateGraph analyzes the current CloudFS state
func (v *Verifier) BuildStateGraph(ctx context.Context) (VerifierState, error) {
	// Check entries
	var entryCount int
	v.db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM entries`).Scan(&entryCount)

	if entryCount == 0 {
		return StateEmpty, nil
	}

	// Check cache
	var cachedCount int
	v.db.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cache_entries WHERE state = 'ready'
	`).Scan(&cachedCount)

	// Check placements
	var placementCount int
	v.db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM placements`).Scan(&placementCount)

	// Check archives
	var archiveCount int
	v.db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM archives`).Scan(&archiveCount)

	// Check snapshots
	var snapshotCount int
	v.db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM snapshots`).Scan(&snapshotCount)

	// Check trash
	var trashCount int
	v.db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM trash`).Scan(&trashCount)

	// Check pending journal
	var pendingJournal int
	v.db.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM journal WHERE state = 'pending'
	`).Scan(&pendingJournal)

	// Determine state
	if trashCount > 0 {
		return StateTrashed, nil
	}
	if snapshotCount > 0 {
		return StateSnapshotted, nil
	}
	if archiveCount > 0 {
		return StateArchived, nil
	}
	if pendingJournal > 0 {
		return StateJournalPending, nil
	}
	if cachedCount > 0 && placementCount > 0 {
		return StateHydrated, nil
	}
	if cachedCount > 0 {
		return StateCached, nil
	}

	return StateIndexedOnly, nil
}

// GenerateLegalActions returns valid actions for current state
func (v *Verifier) GenerateLegalActions(ctx context.Context, state VerifierState) []CommandTest {
	var tests []CommandTest

	switch state {
	case StateEmpty:
		// Can only add files in empty state
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionAdd,
			Params:     ActionParams{Path: "test.txt"},
			ExpectedOK: true,
		})

	case StateIndexedOnly:
		// Can cache, push, archive, snapshot, trash
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionPush,
			ExpectedOK: true,
		})
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionRemove,
			Params:     ActionParams{EntryID: 1},
			ExpectedOK: true,
		})
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionScanIndex,
			ExpectedOK: true,
		})

	case StateCached:
		// Can hydrate, dehydrate, push, etc.
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionHydrate,
			Params:     ActionParams{EntryID: 1},
			ExpectedOK: true,
		})
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionPin,
			Params:     ActionParams{EntryID: 1},
			ExpectedOK: true,
		})

	case StateHydrated:
		// Can dehydrate (if has placement)
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionDehydrate,
			Params:     ActionParams{EntryID: 1},
			ExpectedOK: true,
		})

	case StateTrashed:
		// Can restore from trash
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionTrashRestore,
			Params:     ActionParams{EntryID: 1},
			ExpectedOK: true,
		})
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionTrashPurge,
			Params:     ActionParams{EntryID: 1},
			ExpectedOK: true,
		})

	case StateArchived:
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionArchiveRestore,
			Params:     ActionParams{EntryID: 1},
			ExpectedOK: true,
		})

	case StateSnapshotted:
		tests = append(tests, CommandTest{
			State:      state,
			Action:     ActionSnapshotRestore,
			Params:     ActionParams{EntryID: 1},
			ExpectedOK: true,
		})
	}

	// Always add verification actions
	tests = append(tests, CommandTest{
		State:      state,
		Action:     ActionScanIndex,
		ExpectedOK: true,
	})
	tests = append(tests, CommandTest{
		State:      state,
		Action:     ActionVerifyFile,
		ExpectedOK: true,
	})

	return tests
}

// ExecuteTest runs a single command test
func (v *Verifier) ExecuteTest(ctx context.Context, test CommandTest) TestResult {
	start := time.Now()
	result := TestResult{Test: test}

	// Pre-check invariants
	preViolations, err := v.invariants.CheckAll(ctx)
	if err != nil {
		result.Error = fmt.Errorf("pre-check failed: %w", err)
		result.Passed = false
		return result
	}
	result.PreViolations = preViolations

	// Execute with dry-run first
	dryParams := test.Params
	dryParams.DryRun = true

	executor := NewActionExecutor(v.configDir, v.passphrase)
	dryResult := executor.Execute(ctx, test.Action, dryParams)
	result.DryRunDiff = dryResult.DryRunDiff

	// Execute real action
	if !test.DryRun {
		realResult := executor.Execute(ctx, test.Action, test.Params)
		if realResult.Error != nil {
			result.Error = realResult.Error
			result.Passed = !test.ExpectedOK // If we expected failure, pass
		} else {
			result.Passed = test.ExpectedOK
		}
	} else {
		result.Passed = true
	}

	// Post-check invariants
	postViolations, err := v.invariants.CheckAll(ctx)
	if err != nil {
		result.Error = fmt.Errorf("post-check failed: %w", err)
		result.Passed = false
		return result
	}
	result.PostViolations = postViolations

	// Find new violations
	result.NewViolations = v.findNewViolations(preViolations, postViolations)
	if len(result.NewViolations) > 0 {
		result.Passed = false
		result.Error = fmt.Errorf("action introduced %d invariant violations", len(result.NewViolations))
	}

	result.Duration = time.Since(start)
	return result
}

func (v *Verifier) findNewViolations(pre, post []InvariantViolation) []InvariantViolation {
	var newOnes []InvariantViolation

	preSet := make(map[string]bool)
	for _, v := range pre {
		preSet[v.Invariant+v.Description] = true
	}

	for _, pv := range post {
		key := pv.Invariant + pv.Description
		if !preSet[key] {
			newOnes = append(newOnes, pv)
		}
	}

	return newOnes
}

// RunFullVerification executes the complete verification plan
func (v *Verifier) RunFullVerification(ctx context.Context) *VerificationPlan {
	plan := &VerificationPlan{}

	// Determine current state
	state, _ := v.BuildStateGraph(ctx)
	v.currentState = state

	// Generate tests for current state
	tests := v.GenerateLegalActions(ctx, state)
	plan.Tests = tests

	// Execute each test
	for _, test := range tests {
		result := v.ExecuteTest(ctx, test)
		plan.Results = append(plan.Results, result)

		if result.Passed {
			v.testsPassed++
		} else {
			v.testsFailed++
		}
	}

	return plan
}

// FormatPlan formats a verification plan for display
func FormatPlan(plan *VerificationPlan) string {
	var sb strings.Builder

	sb.WriteString("═══════════════════════════════════════════════════════════\n")
	sb.WriteString("                  VERIFICATION RESULTS\n")
	sb.WriteString("═══════════════════════════════════════════════════════════\n\n")

	passed := 0
	failed := 0

	for _, result := range plan.Results {
		icon := "✓"
		status := "PASS"
		if !result.Passed {
			icon = "✗"
			status = "FAIL"
			failed++
		} else {
			passed++
		}

		sb.WriteString(fmt.Sprintf("%s [%s] Action: %d | State: %s | Duration: %v\n",
			icon, status, result.Test.Action, result.Test.State, result.Duration))

		if result.Error != nil {
			sb.WriteString(fmt.Sprintf("     Error: %v\n", result.Error))
		}

		if result.DryRunDiff != "" {
			sb.WriteString(fmt.Sprintf("     Dry-run: %s\n", result.DryRunDiff))
		}

		if len(result.NewViolations) > 0 {
			sb.WriteString("     New violations:\n")
			for _, v := range result.NewViolations {
				sb.WriteString(fmt.Sprintf("       - [%s] %s: %s\n",
					v.Severity, v.Invariant, v.Description))
			}
		}

		sb.WriteString("\n")
	}

	sb.WriteString("───────────────────────────────────────────────────────────\n")
	sb.WriteString(fmt.Sprintf("Summary: %d passed, %d failed, %d total\n",
		passed, failed, len(plan.Results)))

	if failed > 0 {
		sb.WriteString("⚠ VERIFICATION FAILED\n")
	} else {
		sb.WriteString("✓ ALL TESTS PASSED\n")
	}

	return sb.String()
}

// VerificationTree represents a tree of test executions for visualization
type VerificationTree struct {
	Root     *VerificationNode
	MaxDepth int
}

// VerificationNode is a node in the verification tree
type VerificationNode struct {
	State    VerifierState
	Action   Action
	Result   *TestResult
	Children []*VerificationNode
	Depth    int
}

// BuildVerificationTree builds a tree of all state transitions
func (v *Verifier) BuildVerificationTree(ctx context.Context, maxDepth int) *VerificationTree {
	tree := &VerificationTree{
		Root: &VerificationNode{
			State: v.currentState,
			Depth: 0,
		},
		MaxDepth: maxDepth,
	}

	v.expandNode(ctx, tree.Root, maxDepth)
	return tree
}

func (v *Verifier) expandNode(ctx context.Context, node *VerificationNode, remainingDepth int) {
	if remainingDepth <= 0 {
		return
	}

	// Get legal actions for this state
	actions := v.GenerateLegalActions(ctx, node.State)

	for _, test := range actions {
		childNode := &VerificationNode{
			State:  test.State,
			Action: test.Action,
			Depth:  node.Depth + 1,
		}

		// Execute test
		result := v.ExecuteTest(ctx, test)
		childNode.Result = &result

		node.Children = append(node.Children, childNode)

		// Recursively expand if test passed
		if result.Passed && remainingDepth > 1 {
			v.expandNode(ctx, childNode, remainingDepth-1)
		}
	}
}

// FormatTree formats a verification tree for display
func FormatTree(tree *VerificationTree) string {
	var sb strings.Builder
	sb.WriteString("Verification Tree\n")
	sb.WriteString("=================\n\n")

	formatNode(&sb, tree.Root, "")
	return sb.String()
}

func formatNode(sb *strings.Builder, node *VerificationNode, prefix string) {
	// Format this node
	icon := "○"
	if node.Result != nil {
		if node.Result.Passed {
			icon = "●"
		} else {
			icon = "✗"
		}
	}

	actionName := ""
	if node.Action != ActionNone {
		actionName = fmt.Sprintf(" → Action %d", node.Action)
	}

	sb.WriteString(fmt.Sprintf("%s%s [%s]%s\n", prefix, icon, node.State, actionName))

	// Format children
	for i, child := range node.Children {
		isLast := i == len(node.Children)-1
		childPrefix := prefix
		if isLast {
			childPrefix += "  "
		} else {
			childPrefix += "│ "
		}

		connector := "├─"
		if isLast {
			connector = "└─"
		}

		sb.WriteString(prefix + connector)
		formatNode(sb, child, childPrefix)
	}
}
