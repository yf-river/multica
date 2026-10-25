package supervision

import (
	"encoding/json"
	"fmt"

	"github.com/multica-ai/multica/server/pkg/agent"
)

type requestComparison struct {
	CID              string          `json:"provider_trace_id"`
	Session          string          `json:"session_id"`
	Message          string          `json:"response_id"`
	Span             string          `json:"span_id"`
	Trace            string          `json:"external_trace_id"`
	SDK              string          `json:"local_sdk_trace_id"`
	ExternalSDK      string          `json:"external_sdk_trace_id"`
	SDKMatches       bool            `json:"sdk_matches"`
	ParentSDKMatches bool            `json:"parent_sdk_matches"`
	Model            string          `json:"model"`
	Input            string          `json:"input_including_cache_comparison"`
	Output           string          `json:"output_comparison"`
	CacheRead        string          `json:"cache_read_comparison"`
	RawLocal         json.RawMessage `json:"raw_local_usage"`
	RawSpan          json.RawMessage `json:"verified_span"`
}

// 1. CID, SDK trace, external trace and response IDs remain distinct throughout discovery.
func lensCandidates(data []byte) ([]string, bool) {
	var response struct {
		List struct {
			Total  *int `json:"total"`
			Traces []struct {
				ID string `json:"traceID"`
			} `json:"traces"`
		} `json:"list"`
	}
	if json.Unmarshal(data, &response) != nil || response.List.Total == nil {
		return nil, false
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, trace := range response.List.Traces {
		if trace.ID != "" && !seen[trace.ID] {
			seen[trace.ID] = true
			ids = append(ids, trace.ID)
		}
	}
	return ids, *response.List.Total == len(response.List.Traces)
}

// 2. Only spans corroborated by local response/session/CID and SDK provenance may enter evidence.
func mapLensRequests(data []byte, ref traceReference, local []agent.ExecutionEvidence) ([]requestComparison, []string) {
	var response struct {
		Detail struct {
			ID    string            `json:"trace_id"`
			Spans []json.RawMessage `json:"spans"`
		} `json:"trace_detail"`
	}
	if json.Unmarshal(data, &response) != nil || response.Detail.ID == "" {
		return nil, []string{"unknown_external_trace_format"}
	}
	var metadata struct {
		SDK    string `json:"sdk_trace_id"`
		Source string `json:"sdk_trace_source_path"`
		Hash   string `json:"sdk_trace_source_sha256"`
	}
	if json.Unmarshal(ref.Metadata, &metadata) != nil || metadata.SDK == "" || metadata.Source != agent.SDKTraceSourceMarker || len(metadata.Hash) != 64 {
		return nil, []string{"exact_sdk_provenance_unavailable"}
	}
	requests := map[string]agent.EvidenceRequest{}
	parentSDK := map[string]bool{}
	for _, e := range local {
		for _, r := range e.Requests {
			if r.CID == ref.ID && r.Session == ref.Session && r.SDK == metadata.SDK && r.Message != "" {
				requests[r.Message] = r
				if e.SDK != "" {
					parentSDK[e.SDK] = true
				}
			}
		}
	}
	if len(requests) == 0 {
		return nil, []string{"local_request_identity_unavailable"}
	}
	comparisons := []requestComparison{}
	gaps := []string{}
	seen := map[string]bool{}
	for _, raw := range response.Detail.Spans {
		var span struct {
			ID         string `json:"spanID"`
			Trace      string `json:"traceID"`
			Session    string `json:"sessionID"`
			Input      *int64 `json:"inputTokens"`
			Output     *int64 `json:"outputTokens"`
			CacheRead  *int64 `json:"cacheReadTokens"`
			Attributes []struct {
				Key   string `json:"key"`
				Value any    `json:"value"`
			} `json:"attributes"`
		}
		if json.Unmarshal(raw, &span) != nil {
			gaps = append(gaps, "malformed_external_span")
			continue
		}
		attrs := map[string]string{}
		for _, attr := range span.Attributes {
			if value, ok := attr.Value.(string); ok {
				attrs[attr.Key] = value
			}
		}
		if attrs["message_id"] != ref.ID {
			continue
		}
		message := attrs["gen_ai.response.id"]
		r, ok := requests[message]
		if !ok || span.ID == "" || span.Trace != response.Detail.ID || attrs["gen_ai.session.id"] != ref.Session || (span.Session != "" && span.Session != ref.Session) {
			gaps = append(gaps, "external_request_session_or_response_mismatch")
			continue
		}
		externalSDK := attrs["codebuddy.trace_id"]
		if externalSDK != metadata.SDK && !parentSDK[externalSDK] {
			gaps = append(gaps, "external_sdk_identity_unproven")
			continue
		}
		if seen[message] {
			gaps = append(gaps, "duplicate_external_response_identity")
			continue
		}
		seen[message] = true
		model := attrs["gen_ai.response.model"]
		if model == "" {
			model = attrs["gen_ai.request.model"]
		}
		if model == "" || model != r.Model {
			gaps = append(gaps, "external_model_differs_from_local_request")
		}
		var usage struct {
			Input     *int64 `json:"prompt_tokens"`
			Output    *int64 `json:"completion_tokens"`
			CacheRead *int64 `json:"prompt_cache_hit_tokens"`
		}
		_ = json.Unmarshal(r.RawUsage, &usage)
		if usage.Input == nil || usage.Output == nil || usage.CacheRead == nil || span.Input == nil || span.Output == nil || span.CacheRead == nil {
			gaps = append(gaps, "request_token_fields_unavailable_or_unknown_format")
		}
		comparisons = append(comparisons, requestComparison{CID: ref.ID, Session: ref.Session, Message: message, Span: span.ID, Trace: span.Trace, SDK: r.SDK, ExternalSDK: externalSDK, SDKMatches: externalSDK == r.SDK, ParentSDKMatches: externalSDK != r.SDK && parentSDK[externalSDK], Model: model, Input: compareCounter(usage.Input, span.Input), Output: compareCounter(usage.Output, span.Output), CacheRead: compareCounter(usage.CacheRead, span.CacheRead), RawLocal: r.RawUsage, RawSpan: raw})
	}
	if len(seen) != len(requests) {
		gaps = append(gaps, fmt.Sprintf("request_coverage_%d_of_%d", len(seen), len(requests)))
	}
	return comparisons, gaps
}

func compareCounter(local, external *int64) string {
	if local == nil || external == nil {
		return "unavailable"
	}
	if *local == *external {
		return "match"
	}
	return "different"
}
