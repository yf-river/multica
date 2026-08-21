package util

import "regexp"

// Mention represents a parsed @mention from markdown content.
type Mention struct {
	Type string // "member", "agent", "issue", or "all"
	ID   string // user_id, agent_id, issue_id, or "all"
}

// MentionRe matches [@Label](mention://type/id) or [Label](mention://issue/id) in markdown.
// The @ prefix is optional to support issue mentions which use [MUL-123](mention://issue/...).
// Uses .+? (non-greedy) instead of [^\]]* so labels containing square brackets
// (e.g. "David[TF]") are matched correctly — the ](mention:// anchor is specific
// enough to prevent over-matching.
var MentionRe = regexp.MustCompile(`\[@?(.+?)\]\(mention://(member|agent|squad|issue|all)/([0-9a-fA-F-]+|all)\)`)

// BareAgentUUIDMentionRe is a compatibility fallback for agent-authored
// routing comments that accidentally emit "@<agent-uuid>" instead of the rich
// markdown mention. It intentionally only supports agent UUIDs.
var BareAgentUUIDMentionRe = regexp.MustCompile(`(?:^|[^\w])@([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})(?:\b|$)`)

// AgentIDParenMentionRe accepts natural-language fallback mentions such as
// "@01-需求澄清(agent_id=<uuid>)" or "@01-需求澄清 (agent:<uuid>)" emitted by
// some model runtimes.
var AgentIDParenMentionRe = regexp.MustCompile(`@[^()\r\n]{1,80}[ \t]*\((?:agent_id=|agent:)([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\)`)

// AgentUUIDParenMentionRe accepts natural-language fallback mentions such as
// "@01-需求澄清 (<uuid>)" or "@01-需求澄清（<uuid>）" emitted by some model runtimes.
var AgentUUIDParenMentionRe = regexp.MustCompile(`@[^()\r\n（）]{1,80}[ \t]*[（(]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})[）)]`)

// AgentSchemeMentionRe accepts shorthand markdown links such as
// "[01-需求澄清](agent://<uuid>)" or "[01-需求澄清](agent:<uuid>)".
var AgentSchemeMentionRe = regexp.MustCompile(`\[[^\]\r\n]{1,120}\]\(agent:(?://)?([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\)`)

// BareMarkdownUUIDMentionRe accepts simplified markdown such as
// "@[01-需求澄清](<uuid>)".
var BareMarkdownUUIDMentionRe = regexp.MustCompile(`@?\[[^\]\r\n]{1,120}\]\(([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\)`)

// IsMentionAll returns true if the mention is an @all mention.
func (m Mention) IsMentionAll() bool {
	return m.Type == "all"
}

// ParseMentions extracts deduplicated mentions from markdown content.
func ParseMentions(content string) []Mention {
	matches := MentionRe.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool)
	var result []Mention
	add := func(typ, id string) {
		key := typ + ":" + id
		if seen[key] {
			return
		}
		seen[key] = true
		result = append(result, Mention{Type: typ, ID: id})
	}
	for _, m := range matches {
		add(m[2], m[3])
	}
	for _, m := range BareAgentUUIDMentionRe.FindAllStringSubmatch(content, -1) {
		add("agent", m[1])
	}
	for _, m := range AgentIDParenMentionRe.FindAllStringSubmatch(content, -1) {
		add("agent", m[1])
	}
	for _, m := range AgentUUIDParenMentionRe.FindAllStringSubmatch(content, -1) {
		add("agent", m[1])
	}
	for _, m := range AgentSchemeMentionRe.FindAllStringSubmatch(content, -1) {
		add("agent", m[1])
	}
	for _, m := range BareMarkdownUUIDMentionRe.FindAllStringSubmatch(content, -1) {
		add("agent", m[1])
	}
	return result
}

// HasMentionAll returns true if any mention in the slice is an @all mention.
func HasMentionAll(mentions []Mention) bool {
	for _, m := range mentions {
		if m.IsMentionAll() {
			return true
		}
	}
	return false
}
