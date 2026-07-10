package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/util"
)

func resolveAssignee(ctx context.Context, client *cli.APIClient, name string, kinds assigneeKinds) (string, string, error) {
	if client.WorkspaceID == "" {
		return "", "", fmt.Errorf("workspace ID is required to resolve assignees; use --workspace-id or set MULTICA_WORKSPACE_ID")
	}

	input := normalizeAssigneeLookupInput(name)
	if input == "" {
		return "", "", fmt.Errorf("no %s found matching %q", kinds.describe(), name)
	}
	inputLower := strings.ToLower(input)

	// Matches are collected into three priority buckets. Higher-priority buckets
	// short-circuit lower-priority matching so that, e.g., an exact name match
	// always wins over a substring collision with another candidate.
	//   1. idMatches        — full UUID or 8-char ShortID (as shown by `truncateID`).
	//   2. exactMatches     — case-insensitive full name equality.
	//   3. substringMatches — preserves the existing partial-name UX.
	var idMatches, exactMatches, substringMatches []assigneeMatch
	var errs []error
	var fetchAttempts int

	classify := func(entityType, id, displayName string) {
		match := assigneeMatch{Type: entityType, ID: id, Name: displayName}
		if id != "" && (strings.EqualFold(id, input) || strings.EqualFold(truncateID(id), input)) {
			idMatches = append(idMatches, match)
			return
		}
		if strings.EqualFold(displayName, input) {
			exactMatches = append(exactMatches, match)
			return
		}
		if strings.Contains(strings.ToLower(displayName), inputLower) {
			substringMatches = append(substringMatches, match)
		}
	}

	// Search members.
	if kinds.member {
		fetchAttempts++
		var members []map[string]any
		if err := client.GetJSON(ctx, "/api/workspaces/"+client.WorkspaceID+"/members", &members); err != nil {
			errs = append(errs, fmt.Errorf("fetch members: %w", err))
		} else {
			for _, m := range members {
				classify("member", strVal(m, "user_id"), strVal(m, "name"))
			}
		}
	}

	// Search agents.
	if kinds.agent {
		fetchAttempts++
		var agents []map[string]any
		agentPath := "/api/agents?" + url.Values{"workspace_id": {client.WorkspaceID}}.Encode()
		if err := client.GetJSON(ctx, agentPath, &agents); err != nil {
			errs = append(errs, fmt.Errorf("fetch agents: %w", err))
		} else {
			for _, a := range agents {
				classify("agent", strVal(a, "id"), strVal(a, "name"))
			}
		}
	}

	// Search squads. The platform allows issues to be assigned to a squad
	// (the leader agent then coordinates delegation), so squad names must
	// resolve here too for issue-assignee callers — otherwise a user saying
	// "assign to <SquadName>" silently falls through and the autopilot
	// prompt emits "Unrecognized assignee: <SquadName>" (MUL-2165). Callers
	// whose target schema is member-or-agent only (project lead, subscriber)
	// must opt out via `kinds.squad = false`.
	if kinds.squad {
		fetchAttempts++
		var squads []map[string]any
		if err := client.GetJSON(ctx, "/api/squads", &squads); err != nil {
			errs = append(errs, fmt.Errorf("fetch squads: %w", err))
		} else {
			for _, s := range squads {
				if strVal(s, "archived_at") != "" {
					continue
				}
				classify("squad", strVal(s, "id"), strVal(s, "name"))
			}
		}
	}

	// If every fetch failed, report the errors instead of a misleading "not found".
	if fetchAttempts > 0 && len(errs) == fetchAttempts {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return "", "", fmt.Errorf("failed to resolve assignee: %s", strings.Join(msgs, "; "))
	}

	for _, bucket := range [][]assigneeMatch{idMatches, exactMatches, substringMatches} {
		switch len(bucket) {
		case 0:
			continue
		case 1:
			return bucket[0].Type, bucket[0].ID, nil
		default:
			return "", "", ambiguousAssigneeError(input, bucket)
		}
	}
	return "", "", fmt.Errorf("no %s found matching %q", kinds.describe(), input)
}

func normalizeAssigneeLookupInput(raw string) string {
	input := strings.TrimSpace(raw)
	if m := util.MentionRe.FindStringSubmatch(input); len(m) == 4 && m[0] == input {
		switch m[2] {
		case "member", "agent", "squad":
			return m[3]
		}
	}
	input = strings.TrimLeftFunc(input, func(r rune) bool {
		return r == '@' || r == '＠'
	})
	return strings.TrimSpace(input)
}

func ambiguousAssigneeError(input string, matches []assigneeMatch) error {
	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		parts = append(parts, fmt.Sprintf("  %s %q (%s)", m.Type, m.Name, truncateID(m.ID)))
	}
	return fmt.Errorf("ambiguous assignee %q; matches:\n%s", input, strings.Join(parts, "\n"))
}

// resolveAssigneeByID strictly resolves a canonical UUID to (assignee_type,
// assignee_id) by looking it up against the workspace's members, agents, and
// (when allowed) squads. It is the deterministic counterpart to
// resolveAssignee: callers that already hold a UUID (e.g. agents reading IDs
// from `multica workspace member list --output json`) should use this instead of
// round-tripping through name matching, which can be ambiguous in workspaces
// with overlapping names.
func resolveAssigneeByID(ctx context.Context, client *cli.APIClient, id string, kinds assigneeKinds) (string, string, error) {
	if client.WorkspaceID == "" {
		return "", "", fmt.Errorf("workspace ID is required to resolve assignees; use --workspace-id or set MULTICA_WORKSPACE_ID")
	}
	input := strings.TrimSpace(id)
	if !uuidRegexp.MatchString(input) {
		return "", "", fmt.Errorf("expected a canonical UUID, got %q", id)
	}

	var members []map[string]any
	var memberErr error
	if kinds.member {
		memberErr = client.GetJSON(ctx, "/api/workspaces/"+client.WorkspaceID+"/members", &members)
	}

	var agents []map[string]any
	var agentErr error
	if kinds.agent {
		agentPath := "/api/agents?" + url.Values{"workspace_id": {client.WorkspaceID}}.Encode()
		agentErr = client.GetJSON(ctx, agentPath, &agents)
	}

	var squads []map[string]any
	var squadErr error
	if kinds.squad {
		squadErr = client.GetJSON(ctx, "/api/squads", &squads)
	}

	allFailed := true
	hasFetch := false
	for _, pair := range []struct {
		enabled bool
		err     error
	}{{kinds.member, memberErr}, {kinds.agent, agentErr}, {kinds.squad, squadErr}} {
		if !pair.enabled {
			continue
		}
		hasFetch = true
		if pair.err == nil {
			allFailed = false
		}
	}
	if hasFetch && allFailed {
		return "", "", fmt.Errorf("failed to resolve assignee: %v; %v; %v", memberErr, agentErr, squadErr)
	}

	for _, m := range members {
		if strings.EqualFold(strVal(m, "user_id"), input) {
			return "member", strVal(m, "user_id"), nil
		}
	}
	for _, a := range agents {
		if strings.EqualFold(strVal(a, "id"), input) {
			return "agent", strVal(a, "id"), nil
		}
	}
	for _, s := range squads {
		if strings.EqualFold(strVal(s, "id"), input) {
			return "squad", strVal(s, "id"), nil
		}
	}

	return "", "", fmt.Errorf("no %s found with ID %q", kinds.describe(), input)
}

// pickAssigneeFromFlags reads a (name-flag, id-flag) pair off cmd and resolves
// it to (assignee_type, assignee_id), restricted to the entity types in
// kinds. The third return reports whether either flag was *explicitly set*;
// callers use it to decide whether to write `assignee_*` into the request
// body. The two flags are mutually exclusive — passing both is rejected
// up-front so a script that accidentally sets both never silently applies one
// over the other.
//
// Presence is detected via Flags().Changed (not value-emptiness): a script
// that interpolates an empty env var (`--assignee-id "$MAYBE_UUID"`) must
// fail loudly through resolveAssignee/resolveAssigneeByID rather than silently
// degrade to "no filter / unassigned / subscribe caller", which would defeat
// the strict-UUID guarantee the new flags exist for.
func pickAssigneeFromFlags(ctx context.Context, client *cli.APIClient, cmd *cobra.Command, nameFlag, idFlag string, kinds assigneeKinds) (string, string, bool, error) {
	nameSet := cmd.Flags().Changed(nameFlag)
	idSet := cmd.Flags().Changed(idFlag)
	if nameSet && idSet {
		return "", "", false, fmt.Errorf("--%s and --%s are mutually exclusive", nameFlag, idFlag)
	}
	if idSet {
		idVal, _ := cmd.Flags().GetString(idFlag)
		t, i, err := resolveAssigneeByID(ctx, client, idVal, kinds)
		if err != nil {
			return "", "", true, err
		}
		return t, i, true, nil
	}
	if nameSet {
		name, _ := cmd.Flags().GetString(nameFlag)
		t, i, err := resolveAssignee(ctx, client, name, kinds)
		if err != nil {
			return "", "", true, err
		}
		return t, i, true, nil
	}
	return "", "", false, nil
}

func formatAssignee(issue map[string]any, actors actorDisplayLookup) string {
	aType := strVal(issue, "assignee_type")
	aID := strVal(issue, "assignee_id")
	if aType == "" || aID == "" {
		return ""
	}
	return actors.actor(aType, aID)
}

func truncateID(id string) string {
	if utf8.RuneCountInString(id) > 8 {
		runes := []rune(id)
		return string(runes[:8])
	}
	return id
}
