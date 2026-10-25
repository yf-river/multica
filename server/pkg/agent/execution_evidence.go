package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/redact"
)

// ExecutionEvidence contains visible session records, never hidden model reasoning or credentials.
// 1. Each snapshot is extracted by the owning daemon from an explicitly selected session.
type ExecutionEvidence struct {
	Runtime  string            `json:"runtime"`
	Session  string            `json:"session_id"`
	CID      string            `json:"provider_trace_id,omitempty"`
	SDK      string            `json:"sdk_trace_id,omitempty"`
	SHA256   string            `json:"source_sha256,omitempty"`
	Started  time.Time         `json:"started_at"`
	Ended    time.Time         `json:"ended_at"`
	Requests []EvidenceRequest `json:"requests"`
	Events   []json.RawMessage `json:"events"`
	Usage    *TokenUsage       `json:"normalized_run_usage,omitempty"`
	Gaps     []string          `json:"gaps"`
}

type EvidenceRequest struct {
	CID      string          `json:"provider_trace_id"`
	Session  string          `json:"session_id"`
	SDK      string          `json:"sdk_trace_id"`
	Message  string          `json:"message_id"`
	Model    string          `json:"model"`
	RawUsage json.RawMessage `json:"raw_usage"`
}

var evidenceSessionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var evidenceSecretAssignment = regexp.MustCompile(`(?i)(\b(?:api[_ -]?key|access[_ -]?token|x-agentlens-token|password|secret|authorization|token)\b["']?\s*[:=]\s*["']?)[A-Za-z0-9._/+=-]{8,}`)

// CollectExecutionEvidence reuses the provider usage parsers without writing usage back to the platform.
// 2. Exact filenames and session headers prevent fallback scans through unrelated session bodies.
func CollectExecutionEvidence(provider string, env map[string]string, session, cwd, cid string, started time.Time, resumed bool, ended ...time.Time) ExecutionEvidence {
	e := ExecutionEvidence{Runtime: provider, Session: session, CID: cid, Started: started, Ended: time.Now().UTC(), Requests: []EvidenceRequest{}, Events: []json.RawMessage{}, Gaps: []string{}}
	if len(ended) > 0 && !ended[0].IsZero() {
		e.Ended = ended[0]
	}
	if !evidenceSessionID.MatchString(session) {
		e.Gaps = append(e.Gaps, "session_identity_unavailable")
		return e
	}
	var path string
	switch provider {
	case "codebuddy":
		var source string
		e.SDK, e.SHA256, source, path = codebuddySDKProvenanceWithSource(env, session, cwd, cid)
		if source == "" || path == "" {
			e.Gaps = append(e.Gaps, "exact_sdk_session_source_unavailable")
			return e
		}
	case "codex":
		home := strings.TrimSpace(env["CODEX_HOME"])
		if home == "" {
			e.Gaps = append(e.Gaps, "task_codex_home_unavailable")
			return e
		}
		root := filepath.Join(home, "sessions")
		var matches []string
		for _, pattern := range []string{filepath.Join(root, "rollout-*-"+session+".jsonl"), filepath.Join(root, "*", "*", "*", "rollout-*-"+session+".jsonl")} {
			paths, _ := filepath.Glob(pattern)
			matches = append(matches, paths...)
		}
		if len(matches) != 1 {
			e.Gaps = append(e.Gaps, "exact_codex_session_source_ambiguous_or_missing")
			return e
		}
		path = matches[0]

	default:
		e.Gaps = append(e.Gaps, "runtime_session_format_unsupported")
		return e
	}
	raw, ok := readRegularFileBounded(path)
	if !ok {
		e.Gaps = append(e.Gaps, "session_file_unreadable_or_over_limit")
		return e
	}
	digest := sha256.Sum256(raw)
	e.SHA256 = hex.EncodeToString(digest[:])
	if provider == "codex" {
		first, _, _ := bytes.Cut(raw, []byte("\n"))
		var header struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if json.Unmarshal(first, &header) != nil || header.Type != "session_meta" || header.Payload.ID != session {
			e.Gaps = append(e.Gaps, "codex_session_header_mismatch")
			return e
		}
		if usage := parseCodexSessionFileBetween(path, started, e.Ended, resumed); usage != nil {
			e.Usage = &usage.usage
		}
		e.Gaps = append(e.Gaps, "codex_token_events_are_run_level_not_proven_request_usage")
	}
	e.extract(raw, cwd)
	if provider == "codebuddy" {
		// 3. Child files are selected only by actual Agent/Task results in this root CID.
		for _, child := range agentToolChildIDs(raw, cid) {
			childRaw, ok := readRegularFileBounded(filepath.Join(filepath.Dir(path), session, "subagents", child+".jsonl"))
			if !ok {
				e.Gaps = append(e.Gaps, "child_source_unavailable")
				continue
			}
			_, trace, _, calls, valid := parseSubagentFile(childRaw, child)
			if !valid {
				e.Gaps = append(e.Gaps, "child_identity_unproven")
				continue
			}
			childEvidence := ExecutionEvidence{Runtime: provider, Session: trace.SessionID, CID: trace.TraceID, SDK: trace.SDKTraceID, Started: started, Ended: e.Ended}
			childEvidence.extract(childRaw, cwd)
			proven := map[string]bool{}
			for _, call := range calls {
				proven[call.CID+"/"+call.MessageID] = true
			}
			for _, request := range childEvidence.Requests {
				if proven[request.CID+"/"+request.Message] {
					e.Requests = append(e.Requests, request)
				}
			}
			e.Events = append(e.Events, childEvidence.Events...)
			e.Gaps = append(e.Gaps, childEvidence.Gaps...)
		}
	}
	return e
}

// 4. Retain bounded visible events, provenance and raw counters; truncation is an explicit gap.
func (e *ExecutionEvidence) extract(raw []byte, cwd string) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 65536), 16<<20)
	requestIndex := map[string]int{}
	used := 0
	for scanner.Scan() {
		var row map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			e.Gaps = append(e.Gaps, "malformed_session_record")
			break
		}
		var kind string
		_ = json.Unmarshal(row["type"], &kind)
		selected := map[string]json.RawMessage{"type": row["type"]}
		if e.Runtime == "codebuddy" {
			var record subagentRecord
			if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Provider.ConversationRequestID != e.CID {
				continue
			}
			if record.SessionID != e.Session || (record.Cwd != "" && record.Cwd != cwd) {
				e.Gaps = append(e.Gaps, "session_record_identity_mismatch")
				continue
			}
			pd := record.Provider
			if pd.MessageID != "" && len(pd.RawUsage) > 0 && pd.Model != "" {
				r := EvidenceRequest{CID: e.CID, Session: e.Session, SDK: pd.TraceID, Message: pd.MessageID, Model: pd.Model, RawUsage: pd.RawUsage}
				if i, found := requestIndex[pd.MessageID]; found {
					e.Requests[i] = r
				} else {
					requestIndex[pd.MessageID] = len(e.Requests)
					e.Requests = append(e.Requests, r)
				}
			}
			selected["providerData"], _ = json.Marshal(record.Provider)
			for _, key := range []string{"message", "content", "name", "callId", "arguments", "output"} {
				if v, ok := row[key]; ok {
					selected[key] = v
				}
			}
		} else {
			var at time.Time
			_ = json.Unmarshal(row["timestamp"], &at)
			if at.IsZero() || at.Before(e.Started) || at.After(e.Ended) {
				continue
			}
			if kind != "event_msg" && kind != "response_item" && kind != "compacted" && kind != "turn_context" {
				continue
			}
			selected["timestamp"] = row["timestamp"]
			selected["payload"] = row["payload"]
		}
		body, err := json.Marshal(selected)
		if err != nil {
			continue
		}
		if len(body) > 16384 {
			digest := sha256.Sum256(body)
			body, _ = json.Marshal(map[string]any{"type": kind, "truncated": true, "length": len(body), "sha256": hex.EncodeToString(digest[:]), "visible_prefix": redact.Text(string(body[:8192]))})
			e.Gaps = append(e.Gaps, "visible_event_truncated")
		}
		if used+len(body) > 1<<20 {
			e.Gaps = append(e.Gaps, "visible_events_byte_limit")
			break
		}
		// Apply structured redaction before text masking; no environment/config fields are selected.
		var safe map[string]any
		if json.Unmarshal(body, &safe) != nil {
			continue
		}
		safe = redact.InputMap(safe)
		redactEvidenceValues(safe)
		body, _ = json.Marshal(safe)
		body = []byte(redact.Text(string(body)))
		if !json.Valid(body) {
			e.Gaps = append(e.Gaps, "event_redaction_changed_json")
			continue
		}
		e.Events = append(e.Events, body)
		used += len(body)
	}
	if scanner.Err() != nil {
		e.Gaps = append(e.Gaps, "session_scan_incomplete")
	}
}

// 5. Tool-return strings may contain serialized headers or credential assignments rather than structured keys.
func redactEvidenceValues(value any) {
	switch item := value.(type) {
	case map[string]any:
		for key, v := range item {
			if text, ok := v.(string); ok {
				item[key] = evidenceSecretAssignment.ReplaceAllString(redact.Text(text), "${1}[REDACTED]")
			} else {
				redactEvidenceValues(v)
			}
		}
	case []any:
		for i, v := range item {
			if text, ok := v.(string); ok {
				item[i] = evidenceSecretAssignment.ReplaceAllString(redact.Text(text), "${1}[REDACTED]")
			} else {
				redactEvidenceValues(v)
			}
		}
	}
}
