package review

import "testing"

func TestStopReason(t *testing.T) {
	cases := []struct {
		name    string
		round   int
		verdict Verdict
		want    string
	}{
		{"stalled always stops", 1, VerdictStalled, "上一轮问题在最新提交里没有任何改动"},
		{"reject below cap continues", 1, VerdictReject, ""},
		{"reject at cap stops", MaxRounds, VerdictReject, "累计审查轮次达到上限"},
		{"pass never stops", 1, VerdictPass, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StopReason(tc.round, tc.verdict); got != tc.want {
				t.Fatalf("StopReason(%d, %q) = %q, want %q", tc.round, tc.verdict, got, tc.want)
			}
		})
	}
}

func TestNormalizeFiltersIncompleteFindings(t *testing.T) {
	got := Findings{
		Items: []Finding{
			{Level: "BLOCKING", File: " server/a.go ", Detail: " 缺少错误处理 "},
			{Level: "quality", Detail: ""},
			{Level: "", File: "server/b.go", Detail: "无等级"},
		},
		UnchangedPrevious: []string{" server/a.go ", "", "server/a.go"},
	}.Normalize(VerdictReject)

	if len(got.Items) != 1 {
		t.Fatalf("items = %d, want 1: %+v", len(got.Items), got.Items)
	}
	if got.Items[0].Level != "blocking" || got.Items[0].File != "server/a.go" || got.Items[0].Detail != "缺少错误处理" {
		t.Fatalf("unexpected normalized finding: %+v", got.Items[0])
	}
	if len(got.UnchangedPrevious) != 1 || got.UnchangedPrevious[0] != "server/a.go" {
		t.Fatalf("unchanged_previous = %v, want deduped file", got.UnchangedPrevious)
	}
	if got.Summary != "发现需要修改的问题" {
		t.Fatalf("default summary = %q", got.Summary)
	}
}

func TestParseVerdict(t *testing.T) {
	for _, raw := range []string{"pass", " PASS ", "Reject", "stalled"} {
		if ParseVerdict(raw) == "" {
			t.Fatalf("ParseVerdict(%q) returned empty", raw)
		}
	}
	if ParseVerdict("approved") != "" {
		t.Fatal("unknown verdict should be rejected")
	}
}

func TestEffectiveInstructionsFollowsDefault(t *testing.T) {
	if EffectiveInstructions("   ") != DefaultInstructions() {
		t.Fatal("blank custom instructions must fall back to the built-in prompt")
	}
	if EffectiveInstructions("自定义") != "自定义" {
		t.Fatal("custom instructions must win")
	}
	if DefaultInstructions() == "" || InstructionsVersion() == "" {
		t.Fatal("built-in prompt and its version must not be empty")
	}
}
