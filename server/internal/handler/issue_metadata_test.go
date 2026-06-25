package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Round-trip: set primitives of each type, list, get them back, delete, confirm gone.
func TestIssueMetadataSetGetDelete(t *testing.T) {
	issueID := createMetadataTestIssue(t, "Metadata round-trip")

	cases := []struct {
		key   string
		value string // raw JSON value
	}{
		{"pipeline_status", `"waiting"`},
		{"pr_number", `482`},
		{"is_blocked", `true`},
		{"is_done", `false`},
	}

	for _, c := range cases {
		w := httptest.NewRecorder()
		req := newRequest("PUT", "/api/issues/"+issueID+"/metadata/"+c.key, json.RawMessage(`{"value":`+c.value+`}`))
		req = withURLParams(req, "id", issueID, "key", c.key)
		testHandler.SetIssueMetadataKey(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("Set %s=%s: expected 200, got %d: %s", c.key, c.value, w.Code, w.Body.String())
		}
	}

	// List returns every key with the right value type.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+issueID+"/metadata", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.ListIssueMetadata(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("List metadata: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if got := resp.Metadata["pipeline_status"]; got != "waiting" {
		t.Errorf("pipeline_status: expected \"waiting\", got %T %v", got, got)
	}
	if got := resp.Metadata["pr_number"]; got != float64(482) {
		t.Errorf("pr_number: expected number 482, got %T %v", got, got)
	}
	if got := resp.Metadata["is_blocked"]; got != true {
		t.Errorf("is_blocked: expected true, got %T %v", got, got)
	}
	if got := resp.Metadata["is_done"]; got != false {
		t.Errorf("is_done: expected false, got %T %v", got, got)
	}

	// Delete a key — refresh confirms it is gone, others remain.
	w = httptest.NewRecorder()
	req = newRequest("DELETE", "/api/issues/"+issueID+"/metadata/pipeline_status", nil)
	req = withURLParams(req, "id", issueID, "key", "pipeline_status")
	testHandler.DeleteIssueMetadataKey(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Delete pipeline_status: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues/"+issueID+"/metadata", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.ListIssueMetadata(w, req)
	// Decode into a fresh struct — json.Decode into a non-nil map merges,
	// it does not replace, so reusing `resp` would keep deleted keys around.
	var afterDelete struct {
		Metadata map[string]any `json:"metadata"`
	}
	json.NewDecoder(w.Body).Decode(&afterDelete)
	if _, present := afterDelete.Metadata["pipeline_status"]; present {
		t.Errorf("after delete, pipeline_status should be gone; got %+v", afterDelete.Metadata)
	}
	if _, present := afterDelete.Metadata["pr_number"]; !present {
		t.Errorf("delete removed unrelated key; got %+v", afterDelete.Metadata)
	}
}

// Invalid keys / values / shapes are rejected with 400 — the regex, primitive,
// and "no null" rules must all hold.
func TestIssueMetadataValidation(t *testing.T) {
	issueID := createMetadataTestIssue(t, "Metadata validation")

	bad := []struct {
		name    string
		key     string
		rawBody string
	}{
		{"key starts with digit", "1attempts", `{"value":"x"}`},
		{"key has space", "foo bar", `{"value":"x"}`},
		{"value is null", "k", `{"value":null}`},
		{"value is array", "k", `{"value":[1,2]}`},
		{"value is object", "k", `{"value":{"a":1}}`},
		{"empty body", "k", ``},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			// chi pulls the key from URL params (injected via withURLParams);
			// the raw URL needs to be a valid request line, so PathEscape any
			// chars (spaces, etc.) that would otherwise break httptest.NewRequest.
			req := newRequest("PUT", "/api/issues/"+issueID+"/metadata/"+url.PathEscape(c.key), json.RawMessage(c.rawBody))
			req = withURLParams(req, "id", issueID, "key", c.key)
			testHandler.SetIssueMetadataKey(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// The 8KB DB CHECK kicks in past a few hundred KV pairs of large strings; we
// blow it deliberately with one giant value to confirm the handler surfaces
// a 400 (not a generic 500).
func TestIssueMetadataSizeLimit(t *testing.T) {
	issueID := createMetadataTestIssue(t, "Metadata size limit")

	huge := strings.Repeat("a", 9000)
	body, _ := json.Marshal(map[string]any{"value": huge})
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID+"/metadata/blob", body)
	req = withURLParams(req, "id", issueID, "key", "blob")
	testHandler.SetIssueMetadataKey(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 from size CHECK, got %d: %s", w.Code, w.Body.String())
	}
}

// The 50-key cap is enforced in the handler with a clear 400.
func TestIssueMetadataKeyCountCap(t *testing.T) {
	issueID := createMetadataTestIssue(t, "Metadata key count cap")

	for i := 0; i < maxIssueMetadataKeys; i++ {
		key := fmt.Sprintf("k_%d", i)
		w := httptest.NewRecorder()
		req := newRequest("PUT", "/api/issues/"+issueID+"/metadata/"+key, json.RawMessage(`{"value":"v"}`))
		req = withURLParams(req, "id", issueID, "key", key)
		testHandler.SetIssueMetadataKey(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("key #%d: expected 200, got %d: %s", i, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID+"/metadata/overflow", json.RawMessage(`{"value":"v"}`))
	req = withURLParams(req, "id", issueID, "key", "overflow")
	testHandler.SetIssueMetadataKey(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("overflow key: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// Updating an existing key past the cap is still allowed — only new keys
	// are blocked.
	w = httptest.NewRecorder()
	req = newRequest("PUT", "/api/issues/"+issueID+"/metadata/k_0", json.RawMessage(`{"value":"v2"}`))
	req = withURLParams(req, "id", issueID, "key", "k_0")
	testHandler.SetIssueMetadataKey(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update existing at cap: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ListIssues with `metadata` query param does JSONB containment filtering and
// returns only matching issues — the killer use case for autopilot.
func TestListIssuesMetadataFilter(t *testing.T) {
	waitingID := createMetadataTestIssue(t, "Waiting issue")
	doneID := createMetadataTestIssue(t, "Done issue")

	for issueID, status := range map[string]string{waitingID: "waiting_review", doneID: "deployed"} {
		w := httptest.NewRecorder()
		req := newRequest("PUT", "/api/issues/"+issueID+"/metadata/pipeline_status",
			json.RawMessage(`{"value":"`+status+`"}`))
		req = withURLParams(req, "id", issueID, "key", "pipeline_status")
		testHandler.SetIssueMetadataKey(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("seed %s: %d %s", issueID, w.Code, w.Body.String())
		}
	}

	w := httptest.NewRecorder()
	req := newRequest("GET", `/api/issues?metadata={"pipeline_status":"waiting_review"}`, nil)
	testHandler.ListIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("List with filter: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Issues []IssueResponse `json:"issues"`
	}
	json.NewDecoder(w.Body).Decode(&listResp)

	foundWaiting := false
	for _, iss := range listResp.Issues {
		if iss.ID == doneID {
			t.Errorf("filter leaked: deployed issue %s appeared in waiting_review result set", doneID)
		}
		if iss.ID == waitingID {
			foundWaiting = true
			if got, _ := iss.Metadata["pipeline_status"].(string); got != "waiting_review" {
				t.Errorf("waiting issue: pipeline_status not surfaced; got %v", iss.Metadata)
			}
		}
	}
	if !foundWaiting {
		t.Errorf("waiting issue %s missing from filter result; got %d issues", waitingID, len(listResp.Issues))
	}

	// Malformed filter → 400.
	w = httptest.NewRecorder()
	req = newRequest("GET", `/api/issues?metadata={not-json}`, nil)
	testHandler.ListIssues(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed metadata: expected 400, got %d", w.Code)
	}
}

// New issues default to an empty metadata object — never null — so frontend
// reads like `issue.metadata[key]` never NPE.
func TestNewIssueDefaultsToEmptyMetadata(t *testing.T) {
	issueID := createMetadataTestIssue(t, "Default empty metadata")

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+issueID, nil)
	req = withURLParam(req, "id", issueID)
	testHandler.GetIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetIssue: %d %s", w.Code, w.Body.String())
	}
	var got IssueResponse
	json.NewDecoder(w.Body).Decode(&got)
	if got.Metadata == nil {
		t.Fatalf("Metadata is nil on a fresh issue; expected empty object")
	}
	if len(got.Metadata) != 0 {
		t.Fatalf("Metadata: expected empty, got %v", got.Metadata)
	}
}

func TestCreateIssueWithMetadata(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":           "TAPD linked issue",
		"status":          "todo",
		"priority":        "medium",
		"allow_duplicate": true,
		"metadata": map[string]any{
			"source_provider": "tapd",
			"tapd_workspace":  "47654106",
			"tapd_wiki_id":    "1147654106001004154",
			"source_title":    "User quick entry requirement",
			"source_synced":   true,
			"source_version":  1,
		},
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if got := created.Metadata["source_provider"]; got != "tapd" {
		t.Fatalf("create response metadata source_provider = %T %v", got, got)
	}
	if got := created.Metadata["source_version"]; got != float64(1) {
		t.Fatalf("create response metadata source_version = %T %v", got, got)
	}
	if got := created.Metadata["source_synced"]; got != true {
		t.Fatalf("create response metadata source_synced = %T %v", got, got)
	}

	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues/"+created.ID, nil)
	req = withURLParam(req, "id", created.ID)
	testHandler.GetIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var fetched IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.Metadata["tapd_wiki_id"] != "1147654106001004154" {
		t.Fatalf("metadata not persisted: %+v", fetched.Metadata)
	}

	w = httptest.NewRecorder()
	req = newRequest("GET", `/api/issues?metadata={"source_provider":"tapd","tapd_wiki_id":"1147654106001004154"}`, nil)
	testHandler.ListIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssues metadata filter: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Issues []IssueResponse `json:"issues"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	found := false
	for _, issue := range listResp.Issues {
		if issue.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("created TAPD issue missing from metadata filter result")
	}
}

func TestCreateIssueMetadataValidation(t *testing.T) {
	cases := []struct {
		name     string
		metadata map[string]any
	}{
		{"bad key", map[string]any{"1bad": "x"}},
		{"null value", map[string]any{"source_provider": nil}},
		{"object value", map[string]any{"source_provider": map[string]any{"nested": true}}},
		{"array value", map[string]any{"source_provider": []string{"tapd"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
				"title":    "Invalid create metadata " + c.name,
				"status":   "todo",
				"priority": "medium",
				"metadata": c.metadata,
			})
			testHandler.CreateIssue(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}

	tooMany := make(map[string]any, maxIssueMetadataKeys+1)
	for i := 0; i <= maxIssueMetadataKeys; i++ {
		tooMany[fmt.Sprintf("k_%d", i)] = "v"
	}
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":    "Invalid create metadata too many",
		"status":   "todo",
		"priority": "medium",
		"metadata": tooMany,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("too many keys: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func createMetadataTestIssue(t *testing.T, title string) string {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":    title,
		"status":   "todo",
		"priority": "medium",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("createMetadataTestIssue: %d %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	return issue.ID
}
