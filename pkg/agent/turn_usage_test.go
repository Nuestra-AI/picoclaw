package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// Tests for turnState's turn-wide token accounting. Paired with the
// GetLastUsage -> GetTotalUsage fix in pipeline_streaming.go; both are
// upstream-shaped so they can be sent to sipeed/picoclaw as one change.

func TestGetTotalUsage_NilUntilAnyCallReportsUsage(t *testing.T) {
	ts := &turnState{}

	if usage := ts.GetTotalUsage(); usage != nil {
		t.Fatalf("expected nil total usage before any LLM call, got %+v", usage)
	}

	ts.SetLastUsage(nil)
	if usage := ts.GetTotalUsage(); usage != nil {
		t.Fatalf("expected nil total usage after a nil report, got %+v", usage)
	}
	if last := ts.GetLastUsage(); last != nil {
		t.Fatalf("expected a nil report to clear last usage, got %+v", last)
	}
}

func TestSetLastUsage_AccumulatesAcrossTurnButKeepsLastCallSemantics(t *testing.T) {
	ts := &turnState{}

	calls := []providers.UsageInfo{
		{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		{PromptTokens: 300, CompletionTokens: 40, TotalTokens: 340},
		{PromptTokens: 500, CompletionTokens: 60, TotalTokens: 560},
	}
	for i := range calls {
		ts.SetLastUsage(&calls[i])
	}

	last := ts.GetLastUsage()
	if last == nil || last.TotalTokens != 560 {
		t.Fatalf("expected last usage to keep the final call (560 total), got %+v", last)
	}

	total := ts.GetTotalUsage()
	if total == nil {
		t.Fatal("expected a running total after three reporting calls")
	}
	if total.PromptTokens != 900 || total.CompletionTokens != 120 || total.TotalTokens != 1020 {
		t.Fatalf("expected 900/120/1020 across the turn, got %+v", total)
	}
	// The defect this guards: a multi-iteration turn must report strictly more
	// than its last call alone.
	if total.TotalTokens <= last.TotalTokens {
		t.Fatalf("turn total %d must exceed last-call total %d", total.TotalTokens, last.TotalTokens)
	}
}

func TestGetTotalUsage_ReturnsCopy(t *testing.T) {
	ts := &turnState{}
	ts.SetLastUsage(&providers.UsageInfo{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})

	snapshot := ts.GetTotalUsage()
	snapshot.TotalTokens = 9999

	if again := ts.GetTotalUsage(); again.TotalTokens != 15 {
		t.Fatalf("mutating a snapshot must not change turn state, got %d", again.TotalTokens)
	}
}

// A nil report after real usage must not discard the accumulated total.
func TestSetLastUsage_NilReportPreservesRunningTotal(t *testing.T) {
	ts := &turnState{}
	ts.SetLastUsage(&providers.UsageInfo{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	ts.SetLastUsage(nil)

	total := ts.GetTotalUsage()
	if total == nil || total.TotalTokens != 15 {
		t.Fatalf("expected the running total to survive a nil report, got %+v", total)
	}
}
