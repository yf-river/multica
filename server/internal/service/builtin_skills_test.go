package service

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"gopkg.in/yaml.v3"
)

// Tests stay outside embedded skill directories so they are not shipped to agents.

const (
	// maxSkillBodyLines is Anthropic's L2 budget for a SKILL.md body
	// (~5k tokens). Past this, content belongs in one-level-deep supporting
	// files, not the always-loaded body.
	maxSkillBodyLines = 500
	// maxDescriptionChars is the frontmatter description cap — it is the only
	// thing an agent sees when deciding whether to load the skill.
	maxDescriptionChars = 1024
)

func TestBuiltinSkillsConformToTemplate(t *testing.T) {
	skills := loadBuiltinSkills()
	if len(skills) == 0 {
		t.Fatal("no built-in skills loaded; embed or layout is broken")
	}

	for _, skill := range skills {
		t.Run(skill.Name, func(t *testing.T) {
			if !strings.HasPrefix(skill.Name, "multica-") {
				t.Errorf("skill name %q must carry the multica- prefix", skill.Name)
			}

			fm, body, ok := splitFrontmatter(skill.Content)
			if !ok {
				t.Fatalf("SKILL.md must lead with a --- frontmatter block")
			}
			if strings.TrimSpace(fm["name"]) == "" {
				t.Errorf("frontmatter is missing a non-empty name")
			}
			desc := strings.TrimSpace(fm["description"])
			if desc == "" {
				t.Errorf("frontmatter is missing a description (the only thing an agent sees when deciding to load the skill)")
			}
			if len(desc) > maxDescriptionChars {
				t.Errorf("description is %d chars, over the %d cap", len(desc), maxDescriptionChars)
			}
			if n := strings.Count(body, "\n") + 1; n > maxSkillBodyLines {
				t.Errorf("SKILL.md body is %d lines, over the %d-line L2 budget; move detail into one-level-deep supporting files", n, maxSkillBodyLines)
			}

			// Evals must never ride along to agent machines as supporting files.
			for _, f := range skill.Files {
				lower := strings.ToLower(f.Path)
				if strings.Contains(lower, "eval") || strings.HasSuffix(lower, "_test.go") || strings.HasSuffix(lower, "_test.md") {
					t.Errorf("supporting file %q looks like an eval/test; evals belong in _test.go, not the shipped skill payload", f.Path)
				}
			}
		})
	}
}

// Strict runtimes reject malformed YAML that the lightweight scalar reader accepts.
func TestBuiltinSkillsFrontmatterIsStrictYAML(t *testing.T) {
	skills := loadBuiltinSkills()
	if len(skills) == 0 {
		t.Fatal("no built-in skills loaded; embed or layout is broken")
	}

	for _, skill := range skills {
		t.Run(skill.Name, func(t *testing.T) {
			content := skill.Content
			if !strings.HasPrefix(content, "---\n") {
				t.Fatalf("SKILL.md must lead with a --- frontmatter block")
			}
			rest := content[len("---\n"):]
			end := strings.Index(rest, "\n---")
			if end < 0 {
				t.Fatalf("frontmatter has no closing --- delimiter")
			}

			var fm map[string]any
			if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
				t.Fatalf("frontmatter is not valid YAML — a strict runtime (e.g. Codex) "+
					"will drop this skill on load; quote values containing ': ': %v", err)
			}

			if name, ok := fm["name"].(string); !ok || strings.TrimSpace(name) == "" {
				t.Errorf("frontmatter name must parse as a non-empty string, got %#v", fm["name"])
			}
			if desc, ok := fm["description"].(string); !ok || strings.TrimSpace(desc) == "" {
				t.Errorf("frontmatter description must parse as a non-empty string, got %#v", fm["description"])
			}
		})
	}
}

func TestMentioningSkillFollowsContractFrontmatter(t *testing.T) {
	skill, ok := findSkill(t, "multica-mentioning")
	if !ok {
		return
	}
	fm, _, _ := splitFrontmatter(skill.Content)

	if got := strings.TrimSpace(fm["user-invocable"]); got != "false" {
		t.Errorf("user-invocable = %q, want false (a platform-contract skill triggers from context, not a slash command)", got)
	}
	if got := strings.TrimSpace(fm["allowed-tools"]); got != "Bash(multica *)" {
		t.Errorf("allowed-tools = %q, want Bash(multica *) (fence the skill to the CLI it teaches)", got)
	}
}

func TestMentioningSkillTeachesTheParserContract(t *testing.T) {
	const uuid = "7f3a1b2c-0000-4000-8000-000000000abc"

	cases := []struct {
		name    string
		content string
		want    []util.Mention
	}{
		{
			name:    "name where a uuid belongs is silently dead",
			content: "[@Alice](mention://member/Alice) please review",
			want:    nil,
		},
		{
			name:    "bare @name is plain text",
			content: "@alice please review",
			want:    nil,
		},
		{
			name:    "real uuid with matching type fires",
			content: "[@Alice](mention://member/" + uuid + ") please review",
			want:    []util.Mention{{Type: "member", ID: uuid}},
		},
		{
			name:    "all uses the literal all",
			content: "[@all](mention://all/all) heads up",
			want:    []util.Mention{{Type: "all", ID: "all"}},
		},
		{
			name:    "wrong type still parses (points at wrong entity)",
			content: "[@Bot](mention://member/" + uuid + ")",
			want:    []util.Mention{{Type: "member", ID: uuid}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := util.ParseMentions(tc.content)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseMentions(%q) = %+v, want %+v", tc.content, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("mention[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestWorkingOnIssuesSkillCoversIssueLoopContracts(t *testing.T) {
	skill, ok := findSkill(t, "multica-working-on-issues")
	if !ok {
		return
	}
	fm, body, _ := splitFrontmatter(skill.Content)

	if got := strings.TrimSpace(fm["user-invocable"]); got != "false" {
		t.Errorf("user-invocable = %q, want false (issue workflow guidance triggers from context)", got)
	}
	if got := strings.TrimSpace(fm["allowed-tools"]); !strings.Contains(got, "Bash(multica *)") {
		t.Errorf("allowed-tools = %q, want access to the Multica CLI", got)
	}

	mustContain := []string{
		"multica issue mr create <issue-id>",
		"multica issue mr list <issue-id> --output json",
		"Default for code-changing issue work",
		"create the MR through the Multica platform before posting the final issue",
		"This is a default, not",
		"Do not rely on MR title, body, or branch identifiers as the primary",
		"include the MR URL when an MR exists",
		"multica issue mr link",
		"--status backlog",
		"mr_url",
		"references/working-on-issues-source-map.md",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("working-on-issues skill missing %q", want)
		}
	}

	mustNotContain := []string{
		"Start from the trigger, not from memory",
		"multica issue get <issue-id> --output json",
		"multica issue metadata list <issue-id> --output json",
		"multica issue comment list <issue-id> --thread <trigger-comment-id>",
		"multica issue comment add <issue-id> --parent <trigger-comment-id>",
	}
	for _, forbidden := range mustNotContain {
		if strings.Contains(body, forbidden) {
			t.Errorf("working-on-issues skill duplicates runtime prompt contract %q", forbidden)
		}
	}

	if !skillHasFile(skill, "references/working-on-issues-source-map.md") {
		t.Errorf("working-on-issues skill missing supporting file references/working-on-issues-source-map.md")
	}
}

func TestSkillImportingSkillCoversWorkspaceImportContracts(t *testing.T) {
	skill, ok := findSkill(t, "multica-skill-importing")
	if !ok {
		return
	}
	fm, body, _ := splitFrontmatter(skill.Content)

	if got := strings.TrimSpace(fm["user-invocable"]); got != "false" {
		t.Errorf("user-invocable = %q, want false (skill import guidance triggers from context)", got)
	}
	if got := strings.TrimSpace(fm["allowed-tools"]); !strings.Contains(got, "Bash(multica *)") {
		t.Errorf("allowed-tools = %q, want access to the Multica CLI", got)
	}

	mustContain := []string{
		"multica skill import --url <url> --output json",
		"/api/skills/import",
		"clawhub.ai",
		"skills.sh",
		"github.com",
		"config.origin",
		"--on-conflict fail",
		"--on-conflict overwrite",
		"--on-conflict rename",
		"--on-conflict skip",
		"status",
		"conflict",
		"skipped",
		"409",
		"existing_skill",
		"Idempotency-Key",
		"idempotency_conflict",
		"id",
		"name",
		"npx skills add",
		"multica agent skills add <agent-id> --skill-ids <skill-id> --output json",
		"multica agent skills list <agent-id> --output json",
		"replace-all",
		"`set` is the replacement path",
		"references/skill-importing-source-map.md",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("skill-importing skill missing %q", want)
		}
	}

	mustNotContain := []string{
		"multica agent skills set <agent-id> --skill-ids <skill-id>",
		"merge the new skill id with the existing ids",
	}
	for _, forbidden := range mustNotContain {
		if strings.Contains(body, forbidden) {
			t.Errorf("skill-importing skill should not teach stale or destructive binding command %q", forbidden)
		}
	}

	if !skillHasFile(skill, "references/skill-importing-source-map.md") {
		t.Errorf("skill-importing skill missing supporting file references/skill-importing-source-map.md")
	}
}

func TestCreatingAgentsSkillCoversAgentCreationContracts(t *testing.T) {
	skill, ok := findSkill(t, "multica-creating-agents")
	if !ok {
		return
	}
	fm, body, _ := splitFrontmatter(skill.Content)

	if got := strings.TrimSpace(fm["user-invocable"]); got != "false" {
		t.Errorf("user-invocable = %q, want false (agent creation guidance triggers from context)", got)
	}
	if got := strings.TrimSpace(fm["allowed-tools"]); !strings.Contains(got, "Bash(multica *)") {
		t.Errorf("allowed-tools = %q, want access to the Multica CLI", got)
	}

	mustContain := []string{
		"not a parameter manual",
		"`description` is a catalog summary",
		"`instructions` is the runtime behavior contract",
		"multica agent create --name <name> --runtime-id <runtime-id>",
		"`model` is a first-class persisted column",
		"custom_env",
		"--custom-env-stdin",
		"--custom-env-file",
		"multica agent skills add <agent-id> --skill-ids <skill-id> --output json",
		"multica agent skills list <agent-id> --output json",
		"multica agent get <agent-id> --output json",
		"255",
		"references/creating-agents-source-map.md",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("creating-agents skill missing %q", want)
		}
	}

	mustNotContain := []string{
		"copy this parameter list",
		"Define the job first",
		"Run a low-risk task",
		"Decision flow",
	}
	for _, forbidden := range mustNotContain {
		if strings.Contains(body, forbidden) {
			t.Errorf("creating-agents skill should not teach immature template content or generic how-to coaching %q", forbidden)
		}
	}

	if !skillHasFile(skill, "references/creating-agents-source-map.md") {
		t.Errorf("creating-agents skill missing supporting file references/creating-agents-source-map.md")
	}
}

func TestSquadsSkillCoversLeaderRoutingContract(t *testing.T) {
	skill, ok := findSkill(t, "multica-squads")
	if !ok {
		return
	}
	fm, body, _ := splitFrontmatter(skill.Content)

	if got := strings.TrimSpace(fm["user-invocable"]); got != "false" {
		t.Errorf("user-invocable = %q, want false (squad guidance triggers from context)", got)
	}
	if got := strings.TrimSpace(fm["allowed-tools"]); !strings.Contains(got, "Bash(multica *)") {
		t.Errorf("allowed-tools = %q, want access to the Multica CLI", got)
	}

	mustContain := []string{
		"A squad is not an agent",
		"squad's `leader_id` agent",
		"squad members are not automatically fanned out",
		"multica squad member set-role",
		"mention://squad/<squad-id>",
		"recording squad activity",
		"references/squad-source-map.md",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("squads skill missing %q", want)
		}
	}

	if !skillHasFile(skill, "references/squad-source-map.md") {
		t.Errorf("squads skill missing supporting file references/squad-source-map.md")
	}
}

func TestAutopilotsSkillCoversDispatchAndSideEffects(t *testing.T) {
	skill, ok := findSkill(t, "multica-autopilots")
	if !ok {
		return
	}
	fm, body, _ := splitFrontmatter(skill.Content)

	if got := strings.TrimSpace(fm["user-invocable"]); got != "false" {
		t.Errorf("user-invocable = %q, want false", got)
	}
	if got := strings.TrimSpace(fm["allowed-tools"]); !strings.Contains(got, "Bash(multica *)") {
		t.Errorf("allowed-tools = %q, want access to the Multica CLI", got)
	}

	mustContain := []string{
		"An autopilot is not an agent",
		"create_issue",
		"run_only",
		"multica autopilot trigger-add <autopilot-id> --kind schedule",
		"multica autopilot trigger <autopilot-id> --output json",
		"Do not run `trigger`",
		"webhook tokens",
		"{{date}}",
		"squad's leader agent",
		"references/autopilots-source-map.md",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("autopilots skill missing %q", want)
		}
	}
	if !skillHasFile(skill, "references/autopilots-source-map.md") {
		t.Errorf("autopilots skill missing supporting file references/autopilots-source-map.md")
	}
}

func TestRuntimesAndReposSkillCoversClaimAndCheckoutChain(t *testing.T) {
	skill, ok := findSkill(t, "multica-runtimes-and-repos")
	if !ok {
		return
	}
	fm, body, _ := splitFrontmatter(skill.Content)

	if got := strings.TrimSpace(fm["user-invocable"]); got != "false" {
		t.Errorf("user-invocable = %q, want false", got)
	}
	if got := strings.TrimSpace(fm["allowed-tools"]); !strings.Contains(got, "Bash(multica *)") {
		t.Errorf("allowed-tools = %q, want access to the Multica CLI", got)
	}

	mustContain := []string{
		"agent_task_queue",
		"daemon polls/claims the task",
		"multica runtime list --output json",
		"multica repo checkout <url>",
		"MULTICA_DAEMON_PORT",
		"github_repo",
		"local_directory",
		"Runtime and repo commands affect active agent execution",
		"references/runtimes-and-repos-source-map.md",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("runtimes-and-repos skill missing %q", want)
		}
	}
	if !skillHasFile(skill, "references/runtimes-and-repos-source-map.md") {
		t.Errorf("runtimes-and-repos skill missing supporting file references/runtimes-and-repos-source-map.md")
	}
}

func TestProjectsAndResourcesSkillCoversDurableContext(t *testing.T) {
	skill, ok := findSkill(t, "multica-projects-and-resources")
	if !ok {
		return
	}
	fm, body, _ := splitFrontmatter(skill.Content)

	if got := strings.TrimSpace(fm["user-invocable"]); got != "false" {
		t.Errorf("user-invocable = %q, want false", got)
	}
	if got := strings.TrimSpace(fm["allowed-tools"]); !strings.Contains(got, "Bash(multica *)") {
		t.Errorf("allowed-tools = %q, want access to the Multica CLI", got)
	}

	mustContain := []string{
		"Projects are durable context containers",
		".multica/project/resources.json",
		"multica project resource list <project-id> --output json",
		"multica project resource add <project-id> --type github_repo --url <github-url> --output json",
		"multica project resource add <project-id> --type local_directory",
		"Project resources are durable and affect future tasks",
		"github_repo.resource_ref.url",
		"references/projects-and-resources-source-map.md",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("projects-and-resources skill missing %q", want)
		}
	}
	if !skillHasFile(skill, "references/projects-and-resources-source-map.md") {
		t.Errorf("projects-and-resources skill missing supporting file references/projects-and-resources-source-map.md")
	}
}

func findSkill(t *testing.T, name string) (protocol.TaskSkill, bool) {
	t.Helper()
	for _, s := range loadBuiltinSkills() {
		if s.Name == name {
			return s, true
		}
	}
	t.Errorf("built-in skill %q not found", name)
	return protocol.TaskSkill{}, false
}

func skillHasFile(skill protocol.TaskSkill, path string) bool {
	for _, f := range skill.Files {
		if f.Path == path {
			return true
		}
	}
	return false
}

// splitFrontmatter returns the top-level scalar keys of a leading YAML
// frontmatter block, the body after it, and whether a block was found. It only
// understands flat `key: value` lines — enough for the template's frontmatter.
func splitFrontmatter(content string) (map[string]string, string, bool) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, content, false
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, content, false
	}
	block := rest[:end]
	body := rest[end:]
	if nl := strings.Index(body, "\n"); nl >= 0 {
		body = body[nl+1:] // drop the closing --- line
	}

	fm := make(map[string]string)
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue // nested value; the template uses only flat scalars
		}
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fm[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(val), `"'`)
	}
	return fm, body, true
}
