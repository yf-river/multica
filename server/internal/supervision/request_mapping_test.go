package supervision

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// 1. CID, local SDK and external trace may differ; child SDK inheritance needs independent response proof.
func TestRequestMappingDistinctIdentities(t *testing.T) {
	ref := traceReference{ID: "cid", Session: "session", Metadata: json.RawMessage(`{"sdk_trace_id":"child-sdk","sdk_trace_source_path":"providerData.traceId","sdk_trace_source_sha256":"` + strings.Repeat("a", 64) + `"}`)}
	local := []agent.ExecutionEvidence{{SDK: "root-sdk", Requests: []agent.EvidenceRequest{{CID: "cid", Session: "session", SDK: "child-sdk", Message: "response", Model: "actual", RawUsage: json.RawMessage(`{"prompt_tokens":100,"completion_tokens":4,"prompt_cache_hit_tokens":80}`)}}}}
	span := map[string]any{"traceID": "external-trace", "spanID": "span", "sessionID": "session", "inputTokens": 100, "outputTokens": 4, "cacheReadTokens": 80, "attributes": []map[string]string{
		{"key": "message_id", "value": "cid"}, {"key": "gen_ai.session.id", "value": "session"}, {"key": "gen_ai.response.id", "value": "response"}, {"key": "gen_ai.response.model", "value": "actual"}, {"key": "codebuddy.trace_id", "value": "root-sdk"},
	}}
	wrap := func() []byte {
		data, _ := json.Marshal(map[string]any{"trace_detail": map[string]any{"trace_id": "external-trace", "spans": []any{span}}})
		return data
	}
	comparisons, gaps := mapLensRequests(wrap(), ref, local)
	if len(gaps) != 0 || len(comparisons) != 1 || comparisons[0].SDKMatches || !comparisons[0].ParentSDKMatches || comparisons[0].Input != "match" {
		t.Fatalf("mapping %+v gaps %v", comparisons, gaps)
	}
	span["sessionID"] = "foreign"
	comparisons, gaps = mapLensRequests(wrap(), ref, local)
	if len(comparisons) != 0 || len(gaps) == 0 {
		t.Fatal("foreign session accepted")
	}
	span["sessionID"] = "session"
	ref.Metadata = json.RawMessage(`{}`)
	if comparisons, gaps = mapLensRequests(wrap(), ref, local); len(comparisons) != 0 || len(gaps) == 0 {
		t.Fatal("missing SDK provenance accepted")
	}
}

// 2. Discovery reports truncated/unknown response formats instead of claiming complete trace coverage.
func TestLensDiscoveryCoverage(t *testing.T) {
	ids, complete := lensCandidates([]byte(`{"list":{"total":2,"traces":[{"traceID":"one"}]}}`))
	if complete || len(ids) != 1 {
		t.Fatal("partial list accepted as complete")
	}
	if _, complete = lensCandidates([]byte(`{"list":[]}`)); complete {
		t.Fatal("unknown format accepted")
	}
	if compareCounter(nil, new(int64)) != "unavailable" {
		t.Fatal("unknown treated as zero")
	}
}
