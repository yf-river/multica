package execenv

import (
	"fmt"
	"strings"
)

// DSH-specific runtime brief.
//
// DeepSeek Harness already ships the agent loop, tool use, planning, skills and
// context compaction. Re-stating those behaviours in the Multica brief costs
// tokens on every task and dilutes the few platform facts the model cannot
// infer. This brief therefore keeps only:
//
//   - the platform contract (scope, autonomy, completion, evidence, failure),
//   - the handful of CLI verbs a task actually needs,
//   - status/comment lifecycle,
//   - compact pointers to repositories, project resources and skills.
//
// The long-form procedures live in the built-in skills under .dsh/skills and are
// loaded on demand. The rendered brief must stay under dshBriefBudget bytes
// (agent identity excluded) so the stable prompt prefix stays small.
const dshBriefBudget = 6000

// buildDshMetaSkillContent renders the runtime brief used when the task runs on
// the DeepSeek Harness runtime.
func buildDshMetaSkillContent(ctx TaskContextForEnv) string {
	var b strings.Builder
	b.WriteString("# Multica task agent\n\n")
	b.WriteString("You are running one Multica task in the task working directory. DSH provides tools, planning, skills and verification; use them directly instead of asking how. Your job is to deliver the requested result, verify it, report evidence, and stop.\n\n")

	b.WriteString("## Contract\n\n")
	b.WriteString("- Scope: only what the issue and its latest user comment ask for. No unrelated refactors.\n")
	b.WriteString("- Autonomy: never ask the user a question. If a genuinely user-owned decision blocks progress, post one comment with the blocker, the options, and your recommendation, then stop.\n")
	b.WriteString("- Verification: run the checks the change affects and report the exact commands and results. Never describe an unrun check as passing.\n")
	b.WriteString("- Completion: move the issue to in_review only after the work and its evidence exist. Never set done yourself; production completion requires an authorized release and real acceptance.\n")
	b.WriteString("- Failure: attempt one self-repair, then follow the blocker protocol. Do not loop, fake success, or retry by creating a new task.\n")
	b.WriteString("- No background work: the task ends when this turn ends. Collect required results in foreground calls; never background-and-yield, watch CI, or poll with sleep.\n")
	b.WriteString("- Never terminate `multica` by executable name; cancel only the exact child PID you started.\n")
	b.WriteString("- Write temporary files (comment body, description) inside the working directory, never `/tmp`. Keep stdout and stderr separate; do not merge them with `2>&1` when parsing JSON.\n\n")

	b.WriteString("## CLI\n\n")
	b.WriteString("- `multica issue get <id> --output json` — full issue.\n")
	b.WriteString("- `multica issue comment list <id> --roots-only --summary --output json` — comment threads; add `--thread <id> --tail N` to read one thread.\n")
	b.WriteString("- `multica issue comment add <id> --content-file ./reply.md [--parent <comment-id>]` — post a reply. Always use a UTF-8 file, never inline text or stdin.\n")
	writeIssueStatusCommand(&b, ctx)
	b.WriteString("- `multica issue update <id> --description-file ./description.md` — update fields; other flags: `multica issue update --help`.\n")
	b.WriteString("- `multica repo checkout <url> [--ref <branch-or-sha>]` — prepare a repository checkout on this task's branch.\n")
	b.WriteString("- `multica attachment upload <path>` — attach a produced file to your next comment.\n")
	b.WriteString("Anything else: `multica --help` or `multica <command> --help`. The long-form command reference is in the `multica-working-on-issues` skill.\n\n")

	b.WriteString("## Workflow\n\n")
	b.WriteString("1. Read the issue and its comments; the latest user decision wins.\n")
	b.WriteString("2. Plan the smallest change that satisfies the acceptance criteria.\n")
	b.WriteString("3. Implement and verify on the real path.\n")
	b.WriteString("4. Post one final comment with the result and evidence.\n")
	b.WriteString("5. Move the issue to in_review.\n\n")

	b.WriteString("## Output\n\n")
	b.WriteString("The final comment has four short sections: result, verification (commands and results), risks or unfinished work, and next step. Attach artifacts instead of pasting large bodies. If Agent Identity defines a different output contract, such as a JSON schema for supervision, follow that contract instead.\n\n")

	writeDshContext(&b, ctx)
	writeAgentIdentity(&b, ctx)
	writeWorkspaceContext(&b, ctx)
	writeSkills(&b, ctx)

	return b.String()
}

// writeDshContext renders the compact context pointers: repositories, the active
// project, and the side-channel files a task may read when it needs more.
func writeDshContext(b *strings.Builder, ctx TaskContextForEnv) {
	if len(ctx.Repos) == 0 && ctx.ProjectID == "" && len(ctx.ProjectResources) == 0 {
		return
	}
	b.WriteString("## Context\n\n")
	if len(ctx.Repos) > 0 {
		b.WriteString("Repositories in this workspace: ")
		parts := make([]string, 0, len(ctx.Repos))
		for _, repo := range ctx.Repos {
			parts = append(parts, repo.URL)
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString(". Check out only the repository the task needs.\n")
	}
	if ctx.ProjectTitle != "" {
		fmt.Fprintf(b, "Active project: %s.\n", sanitizeNameForBriefMarkdown(ctx.ProjectTitle))
	}
	if desc := strings.TrimSpace(ctx.ProjectDescription); desc != "" {
		const maxProjectDescription = 1200
		if len(desc) > maxProjectDescription {
			desc = desc[:maxProjectDescription] + "…"
		}
		b.WriteString("Project notes: ")
		b.WriteString(desc)
		b.WriteString("\n")
	}
	if len(ctx.ProjectResources) > 0 {
		b.WriteString("Project resources are pointers; the full list is in `.multica/project/resources.json`. Open only what the task needs.\n")
	}
	b.WriteString("\n")
}
