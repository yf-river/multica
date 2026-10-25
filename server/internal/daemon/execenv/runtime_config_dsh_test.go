package execenv

import (
	"os"
	"strings"
	"testing"
)

func dshTestContext() TaskContextForEnv {
	return TaskContextForEnv{
		IssueID:           "issue-1",
		AgentID:           "agent-1",
		AgentName:         "Builder",
		AgentInstructions: "You are the workspace builder. Prefer small commits and report evidence.",
		Repos: []RepoContextForEnv{
			{URL: "https://github.com/example/app"},
		},
		ProjectID:          "project-1",
		ProjectTitle:       "Workbench",
		ProjectDescription: strings.Repeat("durable project context ", 40),
		ProjectResources: []ProjectResourceForEnv{
			{ID: "resource-1", ResourceType: "github_repo"},
		},
		WorkspaceContext: "Reply in the member's language.",
		AgentSkills: []SkillContextForEnv{
			{Name: "multica-working-on-issues"},
			{Name: "delivery"},
		},
	}
}

// TestDshBriefIsSlim pins the contract behind the DSH-specific runtime brief:
// DSH already supplies tools, planning and compaction, so the brief only keeps
// the platform facts and must stay inside its byte budget.
func TestDshBriefIsSlim(t *testing.T) {
	t.Parallel()
	ctx := dshTestContext()
	content := buildMetaSkillContent("dsh", ctx)

	for _, banned := range []string{
		"## Instruction Precedence",
		"## Available Commands",
		"## Issue Metadata",
		"## Sub-issue Creation",
		"## Attachments",
		"## Comment Formatting",
		"## Mentions",
		"## Issue Body Formatting",
		"## Background Task Safety",
	} {
		if strings.Contains(content, banned) {
			t.Errorf("DSH brief must not render the shared section %q\n---\n%s", banned, content)
		}
	}

	for _, want := range []string{
		"## Contract",
		"## CLI",
		"## Workflow",
		"never ask the user a question",
		"--content-file",
		"in_review",
		"watch CI",
		"Never terminate `multica`",
		"multica issue status <id>",
		"## Agent Identity",
		"You are the workspace builder.",
		".multica/project/resources.json",
		"Reply in the member's language.",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("DSH brief must contain %q\n---\n%s", want, content)
		}
	}

	withoutIdentity := strings.Replace(content, ctx.AgentInstructions, "", 1)
	if got := len(withoutIdentity); got > dshBriefBudget {
		t.Errorf("DSH brief is %d bytes without agent identity, budget is %d\n---\n%s", got, dshBriefBudget, content)
	}
	shared := buildMetaSkillContent("claude", ctx)
	t.Logf("brief bytes: dsh=%d shared=%d (without agent instructions)", len(withoutIdentity), len(strings.Replace(shared, ctx.AgentInstructions, "", 1)))
}

// TestDshBriefKeepsChatOnSharedPath guards the scope of the DSH branch: chat
// runs still need their surface instructions, so they keep the shared brief.
func TestDshBriefKeepsChatOnSharedPath(t *testing.T) {
	t.Parallel()
	content := buildMetaSkillContent("dsh", TaskContextForEnv{
		ChatSessionID: "chat-1",
		AgentName:     "Mika",
	})
	if !strings.Contains(content, "## Available Commands") {
		t.Fatalf("chat runs must keep the shared brief\n---\n%s", content)
	}
	if strings.Contains(content, "## Contract") {
		t.Fatalf("chat runs must not take the DSH issue brief\n---\n%s", content)
	}
}

// TestDshBriefRendersCustomStatuses keeps workspace-defined statuses usable in
// the slim brief, since the status line is the only status documentation it has.
func TestDshBriefRendersCustomStatuses(t *testing.T) {
	t.Parallel()
	ctx := dshTestContext()
	ctx.IssueStatuses = []IssueStatusForEnv{
		{Key: "human_review", Name: "Human Review", Category: "in_review", Description: "Waiting for a person"},
	}
	content := buildMetaSkillContent("dsh", ctx)
	for _, want := range []string{"human_review", "Human Review", "in_review"} {
		if !strings.Contains(content, want) {
			t.Errorf("DSH brief must render custom status %q\n---\n%s", want, content)
		}
	}
}

// TestDshBriefDump writes the rendered brief to DSH_BRIEF_OUT when set. It
// exists so a real DSH run can consume the exact brief a task would receive
// without deploying a candidate environment.
func TestDshBriefDump(t *testing.T) {
	out := os.Getenv("DSH_BRIEF_OUT")
	if out == "" {
		t.Skip("set DSH_BRIEF_OUT to dump the rendered DSH brief")
	}
	if err := os.WriteFile(out, []byte(buildDshMetaSkillContent(dshTestContext())), 0o600); err != nil {
		t.Fatal(err)
	}
}
