package upstream

import (
	"errors"
	"strings"
	"testing"
)

func baseReport() Report {
	return Report{
		ScannedCommits: 1,
		Commits:        []CommitRef{{SHA: strings.Repeat("a", 40), Absorbed: false}},
		Items: []ItemInput{{
			Title: "功能变化", Summary: "具体改动", Rationale: "与本地有关", ClusterKey: "pr-1", Verdict: "absorb", Confidence: "high",
			Presence: "absent", CommitSHAs: []string{strings.Repeat("a", 40)},
		}},
	}
}

func TestValidateReportAcceptsMinimalReport(t *testing.T) {
	if err := ValidateReport(baseReport()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateReportRejectsUnknownCommitReference(t *testing.T) {
	rep := baseReport()
	rep.Items[0].CommitSHAs = []string{strings.Repeat("b", 40)}
	err := ValidateReport(rep)
	var verr ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	if !strings.Contains(verr.Message, "未声明") {
		t.Fatalf("expected unknown-commit message, got %q", verr.Message)
	}
}

func TestValidateReportRejectsCommitClaimedTwice(t *testing.T) {
	rep := baseReport()
	rep.ScannedCommits = 1
	rep.Items = append(rep.Items, ItemInput{
		Title: "另一个变化", Summary: "具体改动", Rationale: "待核实", ClusterKey: "pr-2", Verdict: "review", Confidence: "low",
		Presence: "local", CommitSHAs: []string{strings.Repeat("a", 40)},
	})
	err := ValidateReport(rep)
	var verr ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	if !strings.Contains(verr.Message, "只能进一个簇") {
		t.Fatalf("unexpected message %q", verr.Message)
	}
}

func TestValidateReportRejectsScannedMismatch(t *testing.T) {
	rep := baseReport()
	rep.ScannedCommits = 5
	err := ValidateReport(rep)
	var verr ValidationError
	if !errors.As(err, &verr) || verr.Field != "scanned_commits" {
		t.Fatalf("expected scanned_commits error, got %v", err)
	}
}

func TestValidateReportRejectsOversizedBatch(t *testing.T) {
	rep := Report{}
	for i := 0; i < MaxCommitsPerReport+1; i++ {
		rep.Commits = append(rep.Commits, CommitRef{SHA: strings.Repeat("a", 7)})
	}
	err := ValidateReport(rep)
	if err == nil {
		t.Fatal("expected error for oversized batch")
	}
}

func TestValidateReportRejectsInvalidEnums(t *testing.T) {
	rep := baseReport()
	rep.Items[0].Verdict = "maybe"
	err := ValidateReport(rep)
	var verr ValidationError
	if !errors.As(err, &verr) || verr.Field != "items[0].verdict" {
		t.Fatalf("expected verdict error, got %v", err)
	}
}
