package protocol

import (
	"strings"
	"testing"
)

func TestInteractionContinuationDecisions(t *testing.T) {
	plan := InteractionPrompt{Kind: "plan", Title: "Plan", PlanMarkdown: "# Plan\n\nRead then write."}
	question := InteractionPrompt{Kind: "question", Title: "Choose", Questions: []InteractionQuestion{{ID: "folder", Question: "Which folder?"}}}
	for _, tc := range []struct {
		name   string
		prompt InteractionPrompt
		answer InteractionAnswer
		valid  bool
	}{
		{"approve", plan, InteractionAnswer{Decision: "approved"}, true},
		{"keep planning with feedback", plan, InteractionAnswer{Decision: "keep_planning", Feedback: "Add rollback steps."}, true},
		{"cancel", plan, InteractionAnswer{Decision: "cancelled"}, true},
		{"cancel with hidden feedback", plan, InteractionAnswer{Decision: "cancelled", Feedback: "execute"}, false},
		{"live allow is not plan approval", plan, InteractionAnswer{Decision: "allow"}, false},
		{"oversize feedback", plan, InteractionAnswer{Decision: "keep_planning", Feedback: strings.Repeat("x", 32769)}, false},
		{"answer", question, InteractionAnswer{Decision: "allow", Answers: map[string][]string{"folder": {"docs"}}}, true},
		{"question cannot approve plan", question, InteractionAnswer{Decision: "approved"}, false},
		{"question answer missing", question, InteractionAnswer{Decision: "allow"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.prompt.ValidateContinuation(tc.answer); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
	if err := plan.ValidateAnswer(InteractionAnswer{Decision: "allow", Feedback: "discarded feedback"}); err == nil {
		t.Fatal("live response silently discarded continuation feedback")
	}
}
