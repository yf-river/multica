package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestLinkPullRequestToIssue_GongfengURL(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Gongfeng MR link",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue_pull_request WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id = $1 AND repo_owner = $2 AND repo_name = $3 AND pr_number = $4`, testWorkspaceID, "ChainWeaver/ida", "user-center", 61234)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues/"+created.ID+"/pull-requests", map[string]any{
		"provider":      "gongfeng",
		"html_url":      "https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/61234",
		"title":         "GOA-61234 user-center add quick entry API",
		"state":         "opened",
		"source_branch": "goa-61234-usercenter-api",
		"target_branch": "dev_sop",
		"author_login":  "codex",
		"head_sha":      "abc123",
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.LinkPullRequestToIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("LinkPullRequestToIssue: %d %s", w.Code, w.Body.String())
	}
	var linked struct {
		PullRequest GitHubPullRequestResponse `json:"pull_request"`
	}
	if err := json.NewDecoder(w.Body).Decode(&linked); err != nil {
		t.Fatalf("decode link response: %v", err)
	}
	if linked.PullRequest.RepoOwner != "ChainWeaver/ida" || linked.PullRequest.RepoName != "user-center" || linked.PullRequest.Number != 61234 {
		t.Fatalf("unexpected linked PR repository/number: %#v", linked.PullRequest)
	}
	if linked.PullRequest.Branch == nil || *linked.PullRequest.Branch != "goa-61234-usercenter-api" {
		t.Fatalf("branch = %#v, want goa-61234-usercenter-api", linked.PullRequest.Branch)
	}

	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues/"+created.ID+"/pull-requests", nil)
	req = withURLParam(req, "id", created.ID)
	testHandler.ListPullRequestsForIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListPullRequestsForIssue: %d %s", w.Code, w.Body.String())
	}
	var listed struct {
		PullRequests []GitHubPullRequestResponse `json:"pull_requests"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed.PullRequests) != 1 || listed.PullRequests[0].Number != 61234 {
		t.Fatalf("listed pull requests = %#v, want MR 61234", listed.PullRequests)
	}

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues/"+created.ID+"/pull-requests", map[string]any{
		"provider": "gongfeng",
		"html_url": "https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/61234",
		"title":    "GOA-61234 user-center add quick entry API follow-up",
		"state":    "opened",
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.LinkPullRequestToIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("LinkPullRequestToIssue repeat without branch: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues/"+created.ID+"/pull-requests", nil)
	req = withURLParam(req, "id", created.ID)
	testHandler.ListPullRequestsForIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListPullRequestsForIssue after repeat link: %d %s", w.Code, w.Body.String())
	}
	listed.PullRequests = nil
	if err := json.NewDecoder(w.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list after repeat link: %v", err)
	}
	if len(listed.PullRequests) != 1 {
		t.Fatalf("listed pull requests after repeat link = %#v, want one MR", listed.PullRequests)
	}
	if listed.PullRequests[0].Branch == nil || *listed.PullRequests[0].Branch != "goa-61234-usercenter-api" {
		t.Fatalf("branch after repeat link = %#v, want preserved goa-61234-usercenter-api", listed.PullRequests[0].Branch)
	}
	var headSHA string
	if err := testPool.QueryRow(ctx, `
		SELECT head_sha
		FROM github_pull_request
		WHERE workspace_id = $1 AND repo_owner = $2 AND repo_name = $3 AND pr_number = $4
	`, testWorkspaceID, "ChainWeaver/ida", "user-center", 61234).Scan(&headSHA); err != nil {
		t.Fatalf("query head_sha after repeat link: %v", err)
	}
	if headSHA != "abc123" {
		t.Fatalf("head_sha after repeat link = %q, want preserved abc123", headSHA)
	}
}

func TestLinkPullRequestToIssue_NormalizesGongfengDashMergeRequestURL(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Gongfeng MR dash URL link",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue_pull_request WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id = $1 AND repo_owner = $2 AND repo_name = $3 AND pr_number = $4`, testWorkspaceID, "ChainWeaver/ida", "ida-deployment", 61235)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues/"+created.ID+"/pull-requests", map[string]any{
		"provider": "gongfeng",
		"html_url": "https://git.code.tencent.com/ChainWeaver/ida/ida-deployment/-/merge_requests/61235",
		"title":    "AIS dash URL normalization",
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.LinkPullRequestToIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("LinkPullRequestToIssue: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/issues/"+created.ID+"/pull-requests", nil)
	req = withURLParam(req, "id", created.ID)
	testHandler.ListPullRequestsForIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListPullRequestsForIssue: %d %s", w.Code, w.Body.String())
	}
	var listed struct {
		PullRequests []GitHubPullRequestResponse `json:"pull_requests"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed.PullRequests) != 1 {
		t.Fatalf("listed pull requests = %#v, want one", listed.PullRequests)
	}
	wantURL := "https://git.code.tencent.com/ChainWeaver/ida/ida-deployment/merge_requests/61235"
	if listed.PullRequests[0].HtmlURL != wantURL {
		t.Fatalf("html_url = %q, want %q", listed.PullRequests[0].HtmlURL, wantURL)
	}
}

func TestNormalizePullRequestRepositoryFieldsRepairsLegacyGongfengDashRows(t *testing.T) {
	owner, name := normalizePullRequestRepositoryFields(
		"ChainWeaver/ida/ida-deployment",
		"-",
		"https://git.code.tencent.com/ChainWeaver/ida/ida-deployment/-/merge_requests/216",
	)

	if owner != "ChainWeaver/ida" || name != "ida-deployment" {
		t.Fatalf("repo = %q / %q, want ChainWeaver/ida / ida-deployment", owner, name)
	}
}

func TestLinkPullRequestToIssue_RequiresRepositoryAndNumber(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Invalid MR link",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue_pull_request WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues/"+created.ID+"/pull-requests", map[string]any{
		"provider": "gongfeng",
		"html_url": "https://example.com/not-a-gongfeng-mr",
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.LinkPullRequestToIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("LinkPullRequestToIssue: got %d %s, want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "project_path is required") {
		t.Fatalf("error body = %s, want project_path guidance", w.Body.String())
	}
}

func TestDerivePRState(t *testing.T) {
	cases := []struct {
		state  string
		draft  bool
		merged bool
		want   string
	}{
		{"open", false, false, "open"},
		{"open", true, false, "draft"},
		{"closed", false, false, "closed"},
		{"closed", false, true, "merged"},
		{"closed", true, true, "merged"}, // merged trumps draft
	}
	for _, tc := range cases {
		got := derivePRState(tc.state, tc.draft, tc.merged)
		if got != tc.want {
			t.Errorf("derivePRState(%q, draft=%v, merged=%v) = %q, want %q",
				tc.state, tc.draft, tc.merged, got, tc.want)
		}
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	secret := "shared-secret"
	body := []byte(`{"action":"opened"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !verifyWebhookSignature(secret, good, body) {
		t.Error("expected valid signature to verify")
	}
	if verifyWebhookSignature(secret, "sha256=deadbeef", body) {
		t.Error("expected bad hex to fail")
	}
	if verifyWebhookSignature(secret, "", body) {
		t.Error("expected empty header to fail")
	}
	if verifyWebhookSignature(secret, "sha1=whatever", body) {
		t.Error("expected non-sha256 prefix to fail")
	}
	if verifyWebhookSignature("other-secret", good, body) {
		t.Error("expected wrong secret to fail")
	}
}

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "test-secret-123")
	wsID := "11111111-2222-3333-4444-555555555555"

	tok, err := signState(wsID)
	if err != nil {
		t.Fatalf("signState: %v", err)
	}
	got, ok := verifyState(tok)
	if !ok {
		t.Fatal("verifyState rejected a freshly-signed token")
	}
	if got != wsID {
		t.Errorf("verifyState() = %q, want %q", got, wsID)
	}

	// Tampering with the workspace portion must fail (signature is bound
	// to it). Replace the leading UUID's first hex digit.
	tampered := "01111111" + tok[8:]
	if _, ok := verifyState(tampered); ok {
		t.Error("tampered state token should fail to verify")
	}

	// Wrong secret rejects.
	t.Setenv("GITHUB_WEBHOOK_SECRET", "different")
	if _, ok := verifyState(tok); ok {
		t.Error("token signed with old secret should fail under a new one")
	}
}

func TestSignStateRequiresSecret(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	if _, err := signState("ws"); err == nil {
		t.Error("signState should error when secret is unset")
	}
}

func TestDerivePRMergeableState(t *testing.T) {
	cases := []struct {
		name           string
		action         string
		payload        string
		baseRefChanged bool
		wantValid      bool
		wantStr        string
		wantClear      bool
	}{
		{"opened_clears", "opened", "clean", false, false, "", true},
		{"synchronize_clears", "synchronize", "clean", false, false, "", true},
		{"reopened_clears", "reopened", "dirty", false, false, "", true},
		{"edited_base_changed_clears", "edited", "clean", true, false, "", true},
		{"edited_title_only_keeps_value", "edited", "clean", false, true, "clean", false},
		{"labeled_keeps_value", "labeled", "clean", false, true, "clean", false},
		{"labeled_empty_payload_preserves", "labeled", "", false, false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, clear := derivePRMergeableState(tc.action, tc.payload, tc.baseRefChanged)
			if got.Valid != tc.wantValid {
				t.Errorf("Valid=%v want %v", got.Valid, tc.wantValid)
			}
			if got.String != tc.wantStr {
				t.Errorf("String=%q want %q", got.String, tc.wantStr)
			}
			if clear != tc.wantClear {
				t.Errorf("clear=%v want %v", clear, tc.wantClear)
			}
		})
	}
}

func TestAggregateChecksConclusion(t *testing.T) {
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	cases := []struct {
		name                           string
		failed, passed, pending, total int64
		want                           string
	}{
		{"no_suites_nil", 0, 0, 0, 0, "<nil>"},
		{"any_failure_wins", 1, 5, 0, 6, "failed"},
		{"failure_beats_pending", 1, 0, 3, 4, "failed"},
		{"pending_when_no_failure", 0, 1, 2, 3, "pending"},
		{"all_passed", 0, 3, 0, 3, "passed"},
		{"counts_zero_but_total_nonzero_returns_nil", 0, 0, 0, 1, "<nil>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := aggregateChecksConclusion(tc.failed, tc.passed, tc.pending, tc.total)
			if str(got) != tc.want {
				t.Errorf("aggregateChecksConclusion = %s, want %s", str(got), tc.want)
			}
		})
	}
}

// firePullRequestWebhookWithHead is like firePullRequestWebhook but lets the
// caller control the head SHA and mergeable_state on the payload. The CI
// tests need both knobs to exercise head-change semantics.
func firePullRequestWebhookWithHead(t *testing.T, secret string, issue IssueResponse, installationID int64, repo string, prNumber int32, action, headSHA, mergeableState string) {
	t.Helper()
	payload := map[string]any{
		"action": action,
		"pull_request": map[string]any{
			"number":          prNumber,
			"html_url":        "https://github.com/acme/" + repo + "/pull/1",
			"title":           "Fix " + issue.Identifier,
			"body":            "",
			"state":           "open",
			"draft":           false,
			"merged":          false,
			"merged_at":       nil,
			"closed_at":       nil,
			"created_at":      "2026-04-28T00:00:00Z",
			"updated_at":      "2026-04-29T00:00:00Z",
			"mergeable_state": mergeableState,
			"head":            map[string]any{"ref": "fix/foo", "sha": headSHA},
			"user":            map[string]any{"login": "octocat"},
		},
		"repository": map[string]any{
			"name":  repo,
			"owner": map[string]any{"login": "acme"},
		},
		"installation": map[string]any{"id": installationID},
	}
	raw, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	rec := httptest.NewRecorder()
	hookReq := httptest.NewRequest("POST", "/api/webhooks/github", bytes.NewReader(raw))
	hookReq.Header.Set("X-GitHub-Event", "pull_request")
	hookReq.Header.Set("X-Hub-Signature-256", sig)
	testHandler.HandleGitHubWebhook(rec, hookReq)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook %s pr=%d action=%s: expected 202, got %d (%s)",
			repo, prNumber, action, rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	pr, err := testHandler.Queries.GetGitHubPullRequest(ctx, db.GetGitHubPullRequestParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RepoOwner:   "acme",
		RepoName:    repo,
		PrNumber:    prNumber,
	})
	if err != nil {
		t.Fatalf("GetGitHubPullRequest(%s#%d): %v", repo, prNumber, err)
	}
	if err := testHandler.Queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{
		IssueID:             parseUUID(issue.ID),
		PullRequestID:       pr.ID,
		LinkedByType:        textFromNonEmpty("test"),
		LinkedByID:          parseUUID(testUserID),
		CloseIntent:         false,
		PreserveCloseIntent: true,
	}); err != nil {
		t.Fatalf("LinkIssueToPullRequest(%s#%d): %v", repo, prNumber, err)
	}
}

func fireCheckSuiteWebhook(t *testing.T, secret string, installationID int64, repo string, prNumbers []int32, suiteID, appID int64, headSHA, conclusion, updatedAt string) {
	t.Helper()
	fireCheckSuiteWebhookWithStatus(t, secret, installationID, repo, prNumbers,
		suiteID, appID, headSHA, "completed", "completed", conclusion, updatedAt)
}

// fireCheckSuiteWebhookWithStatus is the parametric form of
// fireCheckSuiteWebhook. Tests covering the `requested`/`rerequested` and
// `queued`/`in_progress` matrix use it directly; the legacy completed-only
// helper above wraps it for existing call sites.
func fireCheckSuiteWebhookWithStatus(t *testing.T, secret string, installationID int64, repo string, prNumbers []int32, suiteID, appID int64, headSHA, action, status, conclusion, updatedAt string) {
	t.Helper()
	prRefs := make([]map[string]any, 0, len(prNumbers))
	for _, n := range prNumbers {
		prRefs = append(prRefs, map[string]any{"number": n})
	}
	payload := map[string]any{
		"action": action,
		"check_suite": map[string]any{
			"id":            suiteID,
			"head_sha":      headSHA,
			"status":        status,
			"conclusion":    conclusion,
			"updated_at":    updatedAt,
			"app":           map[string]any{"id": appID},
			"pull_requests": prRefs,
		},
		"repository": map[string]any{
			"name":  repo,
			"owner": map[string]any{"login": "acme"},
		},
		"installation": map[string]any{"id": installationID},
	}
	raw, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	rec := httptest.NewRecorder()
	hookReq := httptest.NewRequest("POST", "/api/webhooks/github", bytes.NewReader(raw))
	hookReq.Header.Set("X-GitHub-Event", "check_suite")
	hookReq.Header.Set("X-Hub-Signature-256", sig)
	testHandler.HandleGitHubWebhook(rec, hookReq)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("check_suite webhook: expected 202, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func setupPRTestIssue(t *testing.T, ctx context.Context, secret string) (IssueResponse, int64) {
	t.Helper()
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "PR CI test",
		"status": "in_progress",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	json.NewDecoder(w.Body).Decode(&created)

	installationID := int64(33445566) + int64(time.Now().UnixNano()%1000000)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request_check_suite WHERE pr_id IN (SELECT id FROM github_pull_request WHERE workspace_id = $1)`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM github_pending_check_suite WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM issue_pull_request WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE installation_id = $1`, installationID)
		testPool.Exec(ctx, `DELETE FROM activity_log WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, created.ID)
	})
	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: installationID,
		AccountLogin:   "ci-acct",
		AccountType:    "User",
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}
	return created, installationID
}

// TestWebhook_CheckSuite_AggregatesAcrossApps ensures the list query reports
// "failed" when one app's latest suite is a failure and another app's is a
// success on the same head. Without per-app aggregation, the last-completed
// suite would silently flip the verdict.
func TestWebhook_CheckSuite_AggregatesAcrossApps(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const secret = "ci-aggregate-secret"
	created, installationID := setupPRTestIssue(t, ctx, secret)

	head := "abc1234567890"
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-a", 11, "opened", head, "")
	// App A → success, App B → failure. The list query must report failed.
	fireCheckSuiteWebhook(t, secret, installationID, "ci-repo-a", []int32{11}, 1001, 7001, head, "success", "2026-05-01T00:00:00Z")
	fireCheckSuiteWebhook(t, secret, installationID, "ci-repo-a", []int32{11}, 1002, 7002, head, "failure", "2026-05-01T00:01:00Z")

	rows, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 PR row, got %d", len(rows))
	}
	got := aggregateChecksConclusion(rows[0].ChecksFailed, rows[0].ChecksPassed, rows[0].ChecksPending, rows[0].ChecksTotal)
	if got == nil || *got != "failed" {
		t.Errorf("expected aggregate failed, got %v (counts: failed=%d passed=%d pending=%d total=%d)",
			got, rows[0].ChecksFailed, rows[0].ChecksPassed, rows[0].ChecksPending, rows[0].ChecksTotal)
	}
}

// TestWebhook_CheckSuite_OldHeadIgnored asserts that a late-arriving
// check_suite for a stale head SHA doesn't contaminate the current head's
// pending view. Without the head_sha filter in the aggregation query, the
// new head would inherit the old head's "passed" verdict.
func TestWebhook_CheckSuite_OldHeadIgnored(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const secret = "ci-oldhead-secret"
	created, installationID := setupPRTestIssue(t, ctx, secret)

	oldHead := "old1111111111"
	newHead := "new2222222222"

	// First: open the PR at old head, run a passing suite.
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-b", 22, "opened", oldHead, "")
	fireCheckSuiteWebhook(t, secret, installationID, "ci-repo-b", []int32{22}, 2001, 8001, oldHead, "success", "2026-05-01T00:00:00Z")

	rows, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	got := aggregateChecksConclusion(rows[0].ChecksFailed, rows[0].ChecksPassed, rows[0].ChecksPending, rows[0].ChecksTotal)
	if got == nil || *got != "passed" {
		t.Fatalf("setup: expected passed on old head, got %v", got)
	}

	// Then: synchronize to new head — no new suite yet. Then a late suite
	// for the OLD head fires (e.g. a delayed delivery). The current aggregate
	// must be nil (no suite for the new head).
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-b", 22, "synchronize", newHead, "")
	fireCheckSuiteWebhook(t, secret, installationID, "ci-repo-b", []int32{22}, 2002, 8001, oldHead, "success", "2026-05-01T00:05:00Z")

	rows, err = testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	got = aggregateChecksConclusion(rows[0].ChecksFailed, rows[0].ChecksPassed, rows[0].ChecksPending, rows[0].ChecksTotal)
	if got != nil {
		t.Errorf("expected no aggregate (nil) after head change, got %v", got)
	}
}

// TestWebhook_CheckSuite_LateOlderEventIgnored guards the single-row ordering
// rule: for the same (pr_id, suite_id) the upsert must not let a later-
// delivered older event overwrite the latest one. We send the newer state
// (failure) first and then the older (success) and assert the row still
// reads failure.
func TestWebhook_CheckSuite_LateOlderEventIgnored(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const secret = "ci-ordering-secret"
	created, installationID := setupPRTestIssue(t, ctx, secret)

	head := "ord1234567890"
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-c", 33, "opened", head, "")
	// Latest event first.
	fireCheckSuiteWebhook(t, secret, installationID, "ci-repo-c", []int32{33}, 3001, 9001, head, "failure", "2026-05-01T01:00:00Z")
	// Late-arriving older event for the same suite.
	fireCheckSuiteWebhook(t, secret, installationID, "ci-repo-c", []int32{33}, 3001, 9001, head, "success", "2026-05-01T00:00:00Z")

	rows, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	got := aggregateChecksConclusion(rows[0].ChecksFailed, rows[0].ChecksPassed, rows[0].ChecksPending, rows[0].ChecksTotal)
	if got == nil || *got != "failed" {
		t.Errorf("expected failure to win against later-delivered older success, got %v", got)
	}
}

// TestWebhook_CheckSuite_QueuedCountsAsPending covers the "CI 跑到一半" path:
// GitHub fires `check_suite.requested` with status `queued` and an empty
// conclusion while CI is still spinning up. The handler must persist these
// non-terminal events so the per-PR `checks_pending` count reflects work in
// progress; otherwise the frontend falls through to the "checks not
// reported yet" placeholder until the first completed suite arrives.
func TestWebhook_CheckSuite_QueuedCountsAsPending(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const secret = "ci-pending-secret"
	created, installationID := setupPRTestIssue(t, ctx, secret)

	head := "pending1234567"
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-pending", 55, "opened", head, "")
	// CI just kicked off — `requested` action, status=queued, no conclusion.
	fireCheckSuiteWebhookWithStatus(t, secret, installationID, "ci-repo-pending", []int32{55}, 4001, 6001, head, "requested", "queued", "", "2026-05-01T00:00:00Z")
	// A second app's suite starts a moment later with status=in_progress.
	fireCheckSuiteWebhookWithStatus(t, secret, installationID, "ci-repo-pending", []int32{55}, 4002, 6002, head, "requested", "in_progress", "", "2026-05-01T00:00:30Z")

	rows, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 PR row, got %d", len(rows))
	}
	if rows[0].ChecksPending != 2 || rows[0].ChecksTotal != 2 ||
		rows[0].ChecksFailed != 0 || rows[0].ChecksPassed != 0 {
		t.Fatalf("expected pending=2 total=2 failed=0 passed=0, got pending=%d total=%d failed=%d passed=%d",
			rows[0].ChecksPending, rows[0].ChecksTotal, rows[0].ChecksFailed, rows[0].ChecksPassed)
	}
	got := aggregateChecksConclusion(rows[0].ChecksFailed, rows[0].ChecksPassed, rows[0].ChecksPending, rows[0].ChecksTotal)
	if got == nil || *got != "pending" {
		t.Errorf("expected aggregate pending while CI is running, got %v", got)
	}

	// Now one app completes successfully — pending count drops to 1 and the
	// aggregate stays pending until the second app finishes.
	fireCheckSuiteWebhookWithStatus(t, secret, installationID, "ci-repo-pending", []int32{55}, 4001, 6001, head, "completed", "completed", "success", "2026-05-01T00:05:00Z")
	rows, err = testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if rows[0].ChecksPending != 1 || rows[0].ChecksPassed != 1 || rows[0].ChecksTotal != 2 {
		t.Fatalf("expected pending=1 passed=1 total=2 after one suite completes, got pending=%d passed=%d total=%d",
			rows[0].ChecksPending, rows[0].ChecksPassed, rows[0].ChecksTotal)
	}
}

// TestWebhook_CheckSuite_OutOfOrderReplaysOnPRUpsert covers the out-of-order
// path: a `check_suite` event arrives before the matching `pull_request`
// row has been mirrored locally (e.g. webhook reordering, or the PR was
// linked to an installation that was suspended/resumed). The handler must
// stash the suite and replay it when the PR upsert arrives, otherwise the
// PR's first observed suite is silently lost and `checks_pending` stays at
// 0 until the next suite ships.
func TestWebhook_CheckSuite_OutOfOrderReplaysOnPRUpsert(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const secret = "ci-oooreplay-secret"
	created, installationID := setupPRTestIssue(t, ctx, secret)

	head := "oo01234567890"
	// Suite event lands FIRST — the PR row does not exist yet.
	fireCheckSuiteWebhookWithStatus(t, secret, installationID, "ci-repo-ooo", []int32{66}, 5001, 7501, head, "requested", "in_progress", "", "2026-05-01T00:00:00Z")

	// Verify nothing landed on the PR table yet (no PR row to land on).
	if rows, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID)); err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	} else if len(rows) != 0 {
		t.Fatalf("expected 0 PR rows before PR webhook, got %d", len(rows))
	}

	// Now the pull_request webhook arrives. The handler must drain the
	// pending stash and replay it onto this PR.
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-ooo", 66, "opened", head, "")

	rows, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 PR row after PR webhook, got %d", len(rows))
	}
	if rows[0].ChecksPending != 1 || rows[0].ChecksTotal != 1 {
		t.Fatalf("expected pending=1 total=1 after replay, got pending=%d total=%d",
			rows[0].ChecksPending, rows[0].ChecksTotal)
	}

	// The next PR upsert (a no-op metadata edit) must NOT re-apply or fail
	// — the drain is one-shot, so the second pull_request webhook drains
	// an empty pending list.
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-ooo", 66, "edited", head, "")
	rows, err = testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if rows[0].ChecksPending != 1 || rows[0].ChecksTotal != 1 {
		t.Fatalf("expected pending=1 total=1 after no-op edit, got pending=%d total=%d",
			rows[0].ChecksPending, rows[0].ChecksTotal)
	}
}

// TestWebhook_CheckSuite_OutOfOrderStashKeepsNewer guards the pending
// stash against the same out-of-order trap the live table already
// handles: while the PR row is still missing, an older event for the
// same suite_id must not overwrite a newer payload that was stashed
// first. Without the suite_updated_at guard on UpsertPendingCheckSuite,
// a late `requested/in_progress` arriving after `completed/success`
// would roll the stash back to pending; the subsequent PR upsert would
// then replay the stale state and the PR card would stay stuck on
// "pending" until the next suite shipped.
func TestWebhook_CheckSuite_OutOfOrderStashKeepsNewer(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const secret = "ci-stash-order-secret"
	created, installationID := setupPRTestIssue(t, ctx, secret)

	head := "stash01234567"
	// Newer event lands FIRST while the PR row does not exist yet.
	fireCheckSuiteWebhookWithStatus(t, secret, installationID, "ci-repo-stash", []int32{77}, 6001, 8001, head, "completed", "completed", "success", "2026-05-01T00:05:00Z")
	// Older event for the SAME suite arrives later (webhook reorder). The
	// pending stash must keep the newer payload.
	fireCheckSuiteWebhookWithStatus(t, secret, installationID, "ci-repo-stash", []int32{77}, 6001, 8001, head, "requested", "in_progress", "", "2026-05-01T00:00:00Z")

	// PR webhook arrives — drain replays the (still newer) stash.
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-stash", 77, "opened", head, "")

	rows, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 PR row after PR webhook, got %d", len(rows))
	}
	if rows[0].ChecksPassed != 1 || rows[0].ChecksPending != 0 || rows[0].ChecksTotal != 1 {
		t.Fatalf("expected passed=1 pending=0 total=1 (newer stash preserved), got passed=%d pending=%d total=%d",
			rows[0].ChecksPassed, rows[0].ChecksPending, rows[0].ChecksTotal)
	}
}

// TestWebhook_PullRequest_SynchronizeClearsMergeable verifies that
// `synchronize` sets mergeable_state to NULL even when the payload still
// carries the previous "clean" verdict — the old answer no longer applies
// to the new head SHA.
func TestWebhook_PullRequest_SynchronizeClearsMergeable(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const secret = "ci-mergeable-secret"
	created, installationID := setupPRTestIssue(t, ctx, secret)

	// Open with no mergeable verdict, then a metadata event populates clean.
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-d", 44, "opened", "head1", "")
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-d", 44, "labeled", "head1", "clean")

	rows, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if !rows[0].MergeableState.Valid || rows[0].MergeableState.String != "clean" {
		t.Fatalf("setup: expected mergeable_state=clean, got %+v", rows[0].MergeableState)
	}

	// Synchronize — payload still claims clean, but we must blank it.
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-d", 44, "synchronize", "head2", "clean")

	rows, err = testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if rows[0].MergeableState.Valid {
		t.Errorf("expected mergeable_state cleared on synchronize, got %q", rows[0].MergeableState.String)
	}
	if rows[0].HeadSha != "head2" {
		t.Errorf("expected head_sha updated to head2, got %q", rows[0].HeadSha)
	}
}

// TestWebhook_PullRequest_MetadataPreservesMergeable verifies that a
// metadata-only event (labeled/assigned/edited-without-base-swap) whose
// payload omits mergeable_state does NOT clobber an existing clean/dirty
// verdict. GitHub re-computes mergeability lazily and metadata events ship
// with the field empty even when the previous verdict is still accurate;
// silently overwriting it with NULL would drop a real signal.
func TestWebhook_PullRequest_MetadataPreservesMergeable(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	const secret = "ci-mergeable-preserve-secret"
	created, installationID := setupPRTestIssue(t, ctx, secret)

	// Open, then set a known verdict via a labeled event carrying clean.
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-e", 55, "opened", "headA", "")
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-e", 55, "labeled", "headA", "clean")

	rows, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if !rows[0].MergeableState.Valid || rows[0].MergeableState.String != "clean" {
		t.Fatalf("setup: expected mergeable_state=clean, got %+v", rows[0].MergeableState)
	}

	// A second labeled event arrives with mergeable_state empty (typical for
	// metadata events). The existing clean must survive.
	firePullRequestWebhookWithHead(t, secret, created, installationID, "ci-repo-e", 55, "labeled", "headA", "")

	rows, err = testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if !rows[0].MergeableState.Valid || rows[0].MergeableState.String != "clean" {
		t.Errorf("expected mergeable_state preserved as clean after metadata event, got %+v", rows[0].MergeableState)
	}
}

// TestListGitHubInstallations_RoleGating covers the read-only relaxation
// in MUL-2413: the endpoint is now reachable by any workspace member, but
// the handler strips the numeric installation_id and reports `can_manage`
// based on the caller's role. Admins / owners still receive the full row.
func TestListGitHubInstallations_RoleGating(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()

	const installationID int64 = 42424242
	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: installationID,
		AccountLogin:   "role-gating-acct",
		AccountType:    "Organization",
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE workspace_id = $1`, testWorkspaceID)
	})

	call := func(t *testing.T, role string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/github/installations", nil)
		req = withURLParam(req, "id", testWorkspaceID)
		req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{Role: role}))
		w := httptest.NewRecorder()
		testHandler.ListGitHubInstallations(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("ListGitHubInstallations(%s): %d %s", role, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body (%s): %v", role, err)
		}
		return body
	}

	t.Run("admin sees installation_id + can_manage true", func(t *testing.T) {
		body := call(t, "admin")
		if got, _ := body["can_manage"].(bool); !got {
			t.Errorf("can_manage = %v, want true", body["can_manage"])
		}
		installs, _ := body["installations"].([]any)
		if len(installs) == 0 {
			t.Fatalf("expected at least one installation row, got %v", installs)
		}
		row, _ := installs[0].(map[string]any)
		gotID, ok := row["installation_id"].(float64)
		if !ok {
			t.Fatalf("admin response missing installation_id: %v", row)
		}
		if int64(gotID) != installationID {
			t.Errorf("installation_id = %v, want %d", gotID, installationID)
		}
	})

	t.Run("owner sees installation_id + can_manage true", func(t *testing.T) {
		body := call(t, "owner")
		if got, _ := body["can_manage"].(bool); !got {
			t.Errorf("can_manage = %v, want true", body["can_manage"])
		}
		installs, _ := body["installations"].([]any)
		row, _ := installs[0].(map[string]any)
		if _, ok := row["installation_id"]; !ok {
			t.Errorf("owner response missing installation_id: %v", row)
		}
	})

	t.Run("member sees row without installation_id and can_manage false", func(t *testing.T) {
		body := call(t, "member")
		canManage, _ := body["can_manage"].(bool)
		if canManage {
			t.Errorf("can_manage = true, want false for non-admin member")
		}
		installs, _ := body["installations"].([]any)
		if len(installs) == 0 {
			t.Fatalf("member should still see installation rows, got %v", installs)
		}
		row, _ := installs[0].(map[string]any)
		if _, present := row["installation_id"]; present {
			t.Errorf("installation_id must be omitted for non-admin members, row=%v", row)
		}
		// Display fields the read-only view still needs must round-trip.
		if got, _ := row["account_login"].(string); got != "role-gating-acct" {
			t.Errorf("account_login = %q, want role-gating-acct", got)
		}
	})

	t.Run("guest is treated as read-only and can_manage is false", func(t *testing.T) {
		body := call(t, "guest")
		if canManage, _ := body["can_manage"].(bool); canManage {
			t.Errorf("can_manage = true, want false for guest")
		}
		installs, _ := body["installations"].([]any)
		row, _ := installs[0].(map[string]any)
		if _, present := row["installation_id"]; present {
			t.Errorf("installation_id must be omitted for guest, row=%v", row)
		}
	})
}

// TestGitHubRoutes_RoleGating exercises the router-level middleware split
// introduced in MUL-2413: GET installations runs under
// RequireWorkspaceMemberFromURL while connect / delete remain behind
// RequireWorkspaceRoleFromURL(owner, admin). The handler-level tests above
// inject a member into context directly and so do not cover the middleware
// itself — a future routing change that accidentally moved one of the
// admin-only routes into the member group would slip past them.
func TestGitHubRoutes_RoleGating(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()

	const slug = "github-routes-role-gating"
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)

	var wsID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name, slug, description, issue_prefix)
VALUES ($1, $2, $3, $4)
RETURNING id
`, "GitHub Routes Role Gating", slug, "github routes role gating", "GRG").Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	// Three workspace members + one outsider. We attach the requesting user
	// via the X-User-ID header so the middleware reads them off the auth
	// boundary just like a real request.
	mkUser := func(t *testing.T, label string) string {
		t.Helper()
		var id string
		account := fmt.Sprintf("github-routes-%s-%s@multica.ai", slug, label)
		if err := testPool.QueryRow(ctx, `
INSERT INTO "user" (name, account) VALUES ($1, $2) RETURNING id
`, "GHR "+label, account).Scan(&id); err != nil {
			t.Fatalf("create user %s: %v", label, err)
		}
		return id
	}
	adminUserID := mkUser(t, "admin")
	memberUserID := mkUser(t, "member")
	outsiderUserID := mkUser(t, "outsider")

	for _, m := range []struct {
		userID, role string
	}{
		{adminUserID, "admin"},
		{memberUserID, "member"},
	} {
		if _, err := testPool.Exec(ctx, `
INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)
`, wsID, m.userID, m.role); err != nil {
			t.Fatalf("insert member (%s): %v", m.role, err)
		}
	}

	const installationID int64 = 90909090
	createdInst, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(wsID),
		InstallationID: installationID,
		AccountLogin:   "routes-acct",
		AccountType:    "User",
	})
	if err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
		for _, uid := range []string{adminUserID, memberUserID, outsiderUserID} {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, uid)
		}
	})

	// Build a router subtree mirroring the production wiring at
	// server/cmd/server/router.go for the workspace-scoped GitHub routes.
	// Mounting the real middleware is what makes this a routing-level test —
	// the role split has to come from the chi groups, not from the handler.
	router := chi.NewRouter()
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
			r.Get("/github/installations", testHandler.ListGitHubInstallations)
		})
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin"))
			r.Get("/github/connect", testHandler.GitHubConnect)
			r.Delete("/github/installations/{installationId}", testHandler.DeleteGitHubInstallation)
		})
	})

	exercise := func(t *testing.T, method, path, userID string) int {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		if userID != "" {
			req.Header.Set("X-User-ID", userID)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	t.Run("GET installations is reachable by members", func(t *testing.T) {
		if code := exercise(t, http.MethodGet, "/api/workspaces/"+wsID+"/github/installations", memberUserID); code != http.StatusOK {
			t.Errorf("member GET installations: want 200, got %d", code)
		}
		if code := exercise(t, http.MethodGet, "/api/workspaces/"+wsID+"/github/installations", adminUserID); code != http.StatusOK {
			t.Errorf("admin GET installations: want 200, got %d", code)
		}
	})

	t.Run("GET installations rejects non-members", func(t *testing.T) {
		// Outsider hits the workspace middleware before the handler — the
		// middleware translates a missing membership row into 404.
		if code := exercise(t, http.MethodGet, "/api/workspaces/"+wsID+"/github/installations", outsiderUserID); code != http.StatusNotFound {
			t.Errorf("outsider GET installations: want 404, got %d", code)
		}
	})

	t.Run("GET connect remains owner/admin only", func(t *testing.T) {
		if code := exercise(t, http.MethodGet, "/api/workspaces/"+wsID+"/github/connect", adminUserID); code != http.StatusOK {
			t.Errorf("admin GET connect: want 200, got %d", code)
		}
		if code := exercise(t, http.MethodGet, "/api/workspaces/"+wsID+"/github/connect", memberUserID); code != http.StatusForbidden {
			t.Errorf("member GET connect: want 403, got %d", code)
		}
		if code := exercise(t, http.MethodGet, "/api/workspaces/"+wsID+"/github/connect", outsiderUserID); code != http.StatusNotFound {
			t.Errorf("outsider GET connect: want 404, got %d", code)
		}
	})

	t.Run("DELETE installation remains owner/admin only", func(t *testing.T) {
		// Member: 403 — middleware rejects before the handler runs.
		if code := exercise(t, http.MethodDelete, "/api/workspaces/"+wsID+"/github/installations/"+uuidToString(createdInst.ID), memberUserID); code != http.StatusForbidden {
			t.Errorf("member DELETE installation: want 403, got %d", code)
		}
		// Outsider: 404 — workspace not found.
		if code := exercise(t, http.MethodDelete, "/api/workspaces/"+wsID+"/github/installations/"+uuidToString(createdInst.ID), outsiderUserID); code != http.StatusNotFound {
			t.Errorf("outsider DELETE installation: want 404, got %d", code)
		}
		// Admin: 204 and the row goes away.
		if code := exercise(t, http.MethodDelete, "/api/workspaces/"+wsID+"/github/installations/"+uuidToString(createdInst.ID), adminUserID); code != http.StatusNoContent {
			t.Errorf("admin DELETE installation: want 204, got %d", code)
		}
		var remaining int
		if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM github_installation WHERE id = $1`, uuidToString(createdInst.ID)).Scan(&remaining); err != nil {
			t.Fatalf("verify deletion: %v", err)
		}
		if remaining != 0 {
			t.Errorf("expected installation row gone after admin DELETE, got %d remaining", remaining)
		}
	})
}

// TestGitHubInstallationBroadcastRedaction guards Emacs' finding on PR #2886:
// the realtime payloads we publish on installation create / uninstall must
// not carry the numeric `installation_id`. The frontend uses these events
// only to invalidate the installations query, so an admin client recovers
// the management handle via the list endpoint — which already gates the
// numeric id by role.
func TestGitHubInstallationBroadcastRedaction(t *testing.T) {
	inst := db.GithubInstallation{
		InstallationID: 123456789,
		AccountLogin:   "broadcast-acct",
		AccountType:    "User",
	}
	got := githubInstallationToBroadcast(inst)
	if got.InstallationID != nil {
		t.Errorf("broadcast payload must omit installation_id, got %v", *got.InstallationID)
	}
	if got.AccountLogin != "broadcast-acct" {
		t.Errorf("expected account_login preserved, got %q", got.AccountLogin)
	}

	// Sanity: the JSON encoding actually drops the field (omitempty + nil
	// pointer). A future change to the response shape could re-introduce
	// the field through a different name; the JSON check is the real
	// assertion against the wire format clients see.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal broadcast payload: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal broadcast payload: %v", err)
	}
	if _, present := generic["installation_id"]; present {
		t.Errorf("installation_id leaked into broadcast JSON: %s", string(raw))
	}
}

// generateTestRSAKeyPEM mints an RSA-2048 key, returns its PKCS#1 PEM
// encoding (the format GitHub hands operators when they create the App)
// and the parsed *rsa.PrivateKey for verification.
func generateTestRSAKeyPEM(t *testing.T) (pemBytes []byte, key *rsa.PrivateKey) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), k
}

func TestSignGitHubAppJWT_NotConfigured(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	tok, err := signGitHubAppJWT(time.Now())
	if err != nil {
		t.Fatalf("expected nil error when env not set, got %v", err)
	}
	if tok != "" {
		t.Errorf("expected empty token when env not set, got %q", tok)
	}

	// Half-configured (one var set, the other empty) is treated the same
	// as fully unset — we never want a partial config to claim the App
	// is wired up.
	t.Setenv("GITHUB_APP_ID", "12345")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	tok, err = signGitHubAppJWT(time.Now())
	if err != nil || tok != "" {
		t.Errorf("partial config should return empty token, got tok=%q err=%v", tok, err)
	}
}

// TestSignGitHubAppJWT_InvalidPEM proves that a malformed private key is
// surfaced as an error, not silently swallowed. The setup-callback path
// catches and logs this so the operator gets a breadcrumb instead of an
// install that quietly never enriches the row.
func TestSignGitHubAppJWT_InvalidPEM(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "12345")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "not a real PEM block")
	if _, err := signGitHubAppJWT(time.Now()); err == nil {
		t.Error("expected error for malformed private key, got nil")
	}
}

// TestSignGitHubAppJWT_ClaimsAndSignature signs a token with a known key
// and verifies (a) the claims GitHub requires (`iss`, `iat`, `exp`) carry
// the values we set, (b) iat is back-dated for clock skew, (c) exp stays
// inside GitHub's 10-minute cap, and (d) the signature verifies against
// the matching public key.
func TestSignGitHubAppJWT_ClaimsAndSignature(t *testing.T) {
	pemBytes, key := generateTestRSAKeyPEM(t)
	t.Setenv("GITHUB_APP_ID", "424242")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", string(pemBytes))

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	tok, err := signGitHubAppJWT(now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if tok == "" {
		t.Fatal("expected non-empty token when fully configured")
	}

	// Inject the same `now` into the parser's clock so default exp/nbf
	// validation is anchored to the test-time, not real wall clock —
	// otherwise the test becomes a time bomb that fails for real once
	// the real time crosses the token's exp (now + 9m).
	parsed, err := jwt.Parse(
		tok,
		func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return &key.PublicKey, nil
		},
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil || !parsed.Valid {
		t.Fatalf("verify token: err=%v valid=%v", err, parsed != nil && parsed.Valid)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims type: %T", parsed.Claims)
	}
	if got, _ := claims["iss"].(string); got != "424242" {
		t.Errorf("iss = %q, want 424242", got)
	}
	iat := int64(claims["iat"].(float64))
	exp := int64(claims["exp"].(float64))
	if iat != now.Add(-60*time.Second).Unix() {
		t.Errorf("iat = %d, want %d (now - 60s for clock skew)", iat, now.Add(-60*time.Second).Unix())
	}
	if exp != now.Add(9*time.Minute).Unix() {
		t.Errorf("exp = %d, want %d (now + 9m, inside GitHub's 10m cap)", exp, now.Add(9*time.Minute).Unix())
	}
	if exp-iat > int64(10*time.Minute/time.Second) {
		t.Errorf("exp-iat = %d s, exceeds GitHub's 10m max", exp-iat)
	}
}

// TestFetchInstallationAccount_AuthenticatedPopulatesRow simulates the
// GitHub `/app/installations/{id}` endpoint with a JWT-gated mock and
// verifies that fetchInstallationAccount, when fully configured,
// (a) sends a Bearer JWT, (b) parses the JSON response, and (c) returns
// the real account login instead of the "unknown" placeholder. This is
// the assertion that nails down the bug fix for MUL-3078.
func TestFetchInstallationAccount_AuthenticatedPopulatesRow(t *testing.T) {
	pemBytes, key := generateTestRSAKeyPEM(t)
	t.Setenv("GITHUB_APP_ID", "11111")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", string(pemBytes))

	const wantInstallationID int64 = 7777777
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		expectedPath := fmt.Sprintf("/app/installations/%d", wantInstallationID)
		if r.URL.Path != expectedPath {
			t.Errorf("unexpected path: got %q want %q", r.URL.Path, expectedPath)
		}
		// Verify JWT signature using the matching public key — this is
		// what GitHub does on the real endpoint.
		bearer := strings.TrimPrefix(sawAuth, "Bearer ")
		if bearer == sawAuth {
			http.Error(w, "missing Bearer prefix", http.StatusUnauthorized)
			return
		}
		if _, err := jwt.Parse(bearer, func(token *jwt.Token) (any, error) {
			return &key.PublicKey, nil
		}); err != nil {
			http.Error(w, "bad jwt: "+err.Error(), http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"account": map[string]any{
				"login":      "octocat",
				"type":       "Organization",
				"avatar_url": "https://example.com/o.png",
			},
		})
	}))
	t.Cleanup(srv.Close)

	oldBase := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = oldBase })

	login, accountType, avatar := fetchInstallationAccount(context.Background(), wantInstallationID)
	if login != "octocat" {
		t.Errorf("login = %q, want %q (the bug repro: stayed as 'unknown' before the fix)", login, "octocat")
	}
	if accountType != "Organization" {
		t.Errorf("accountType = %q, want Organization", accountType)
	}
	if avatar == nil || *avatar != "https://example.com/o.png" {
		t.Errorf("avatar = %v, want pointer to https://example.com/o.png", avatar)
	}
	if !strings.HasPrefix(sawAuth, "Bearer ") {
		t.Errorf("expected Bearer auth header, got %q", sawAuth)
	}
}

// TestFetchInstallationAccount_UnauthenticatedFallsBack documents the
// degraded path: when the operator hasn't set GITHUB_APP_ID/PRIVATE_KEY,
// the call is made unauthenticated, GitHub returns 401, and the function
// returns the "unknown" placeholder. This is the input the webhook then
// upserts over once GitHub delivers `installation.created`.
func TestFetchInstallationAccount_UnauthenticatedFallsBack(t *testing.T) {
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"account": map[string]any{"login": "should-not-see"}})
	}))
	t.Cleanup(srv.Close)
	oldBase := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = oldBase })

	login, _, _ := fetchInstallationAccount(context.Background(), 999)
	if login != "unknown" {
		t.Errorf("login = %q, want unknown placeholder when auth not configured", login)
	}
}

// TestFetchInstallationAccount_EmptyAccountKeepsPlaceholder pins that a 200
// response with a missing `account.login` (e.g. GitHub returned a partial
// payload) still yields the safe "unknown" placeholder rather than writing
// an empty string — the frontend renders the literal value, so an empty
// string would surface as "已连接到 " (the bug we're fixing, in a different
// shape).
func TestFetchInstallationAccount_EmptyAccountKeepsPlaceholder(t *testing.T) {
	pemBytes, _ := generateTestRSAKeyPEM(t)
	t.Setenv("GITHUB_APP_ID", "1")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", string(pemBytes))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"account": map[string]any{}})
	}))
	t.Cleanup(srv.Close)
	oldBase := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = oldBase })

	login, accountType, avatar := fetchInstallationAccount(context.Background(), 12)
	if login != "unknown" {
		t.Errorf("expected 'unknown' placeholder for empty account.login, got %q", login)
	}
	if accountType != "User" {
		t.Errorf("expected default 'User' accountType, got %q", accountType)
	}
	if avatar != nil {
		t.Errorf("expected nil avatar, got %v", *avatar)
	}
}

// TestWebhook_InstallationCreatedRefreshesUnknownLogin guards the fix for
// MUL-3078: when the setup callback persists a row with the "unknown"
// placeholder (because the operator hasn't configured App JWT auth, or
// the API call failed), the subsequent `installation.created` webhook
// must (a) overwrite account_login with the real value from the payload
// and (b) broadcast a `github_installation:created` event so any open
// Settings → GitHub tab re-queries without needing a manual refresh.
func TestWebhook_InstallationCreatedRefreshesUnknownLogin(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	secret := "installation-refresh-secret"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)

	const installationID int64 = 71717171
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE installation_id = $1`, installationID)
	})

	// Seed the row the way the setup callback does today when App JWT
	// auth isn't available: account_login = "unknown".
	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: installationID,
		AccountLogin:   "unknown",
		AccountType:    "User",
	}); err != nil {
		t.Fatalf("seed installation row: %v", err)
	}

	// Subscribe to the bus BEFORE firing the webhook so we can assert the
	// broadcast actually fired. Bus.Subscribe is per-event-type, which
	// matches the realtime hub's downstream filter.
	gotEvent := make(chan events.Event, 1)
	testHandler.Bus.Subscribe(protocol.EventGitHubInstallationCreated, func(e events.Event) {
		select {
		case gotEvent <- e:
		default:
		}
	})

	body, _ := json.Marshal(map[string]any{
		"action": "created",
		"installation": map[string]any{
			"id": installationID,
			"account": map[string]any{
				"login":      "real-octocat",
				"type":       "Organization",
				"avatar_url": "https://example.com/avatar.png",
			},
		},
	})
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "installation")
	req.Header.Set("X-Hub-Signature-256", sig)
	testHandler.HandleGitHubWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook: expected 202, got %d (%s)", rec.Code, rec.Body.String())
	}

	// (a) The row's account_login must be the real login, not "unknown".
	got, err := testHandler.Queries.GetGitHubInstallationByInstallationID(ctx, installationID)
	if err != nil {
		t.Fatalf("get installation: %v", err)
	}
	if got.AccountLogin != "real-octocat" {
		t.Errorf("account_login = %q, want %q (refresh did not overwrite the unknown placeholder)",
			got.AccountLogin, "real-octocat")
	}
	if got.AccountType != "Organization" {
		t.Errorf("account_type = %q, want Organization", got.AccountType)
	}

	// (b) A broadcast must have been emitted on the installation:created
	// channel so the frontend re-queries the list. The realtime listener
	// drops events with empty workspace_id, so we verify both the type
	// AND the workspace scope.
	select {
	case ev := <-gotEvent:
		if ev.WorkspaceID != testWorkspaceID {
			t.Errorf("broadcast WorkspaceID = %q, want %q", ev.WorkspaceID, testWorkspaceID)
		}
		// The payload must carry the redacted installation shape so
		// non-admin clients on the workspace channel can't extract the
		// numeric installation_id from the broadcast itself.
		payload, ok := ev.Payload.(map[string]any)
		if !ok {
			t.Fatalf("broadcast payload type: %T", ev.Payload)
		}
		inst, ok := payload["installation"].(GitHubInstallationResponse)
		if !ok {
			t.Fatalf("installation payload type: %T", payload["installation"])
		}
		if inst.AccountLogin != "real-octocat" {
			t.Errorf("broadcast account_login = %q, want real-octocat", inst.AccountLogin)
		}
		if inst.InstallationID != nil {
			t.Errorf("broadcast must redact installation_id, got %v", *inst.InstallationID)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("expected github_installation:created broadcast after webhook refresh, got none in 2s")
	}
}

// TestSetupCallback_ConsumesPendingInstallationCreated covers the inverse
// race to TestWebhook_InstallationCreatedRefreshesUnknownLogin: GitHub can
// deliver installation.created before the setup callback has created the local
// workspace binding. The webhook cannot broadcast yet, but it must not be lost;
// the callback consumes the pending account metadata even if its direct GitHub
// API lookup falls back to the "unknown" placeholder.
func TestSetupCallback_ConsumesPendingInstallationCreated(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	secret := "pending-installation-secret"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	t.Setenv("FRONTEND_ORIGIN", "https://app.example.test")

	const installationID int64 = 81818181
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE installation_id = $1`, installationID)
		testPool.Exec(ctx, `DELETE FROM github_pending_installation WHERE installation_id = $1`, installationID)
	})

	// Force fetchInstallationAccount to take its degraded path. This pins that
	// the final real account name comes from the earlier webhook, not the
	// setup callback's synchronous GitHub API lookup.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "auth required", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	oldBase := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = oldBase })

	body, _ := json.Marshal(map[string]any{
		"action": "created",
		"installation": map[string]any{
			"id": installationID,
			"account": map[string]any{
				"login":      "pending-octocat",
				"type":       "Organization",
				"avatar_url": "https://example.com/pending.png",
			},
		},
	})
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	rec := httptest.NewRecorder()
	hookReq := httptest.NewRequest("POST", "/api/webhooks/github", bytes.NewReader(body))
	hookReq.Header.Set("X-GitHub-Event", "installation")
	hookReq.Header.Set("X-Hub-Signature-256", sig)
	testHandler.HandleGitHubWebhook(rec, hookReq)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook: expected 202, got %d (%s)", rec.Code, rec.Body.String())
	}

	var pendingLogin string
	if err := testPool.QueryRow(ctx,
		`SELECT account_login FROM github_pending_installation WHERE installation_id = $1`,
		installationID,
	).Scan(&pendingLogin); err != nil {
		t.Fatalf("pending installation row not stored: %v", err)
	}
	if pendingLogin != "pending-octocat" {
		t.Fatalf("pending account_login = %q, want pending-octocat", pendingLogin)
	}

	state, err := signState(testWorkspaceID)
	if err != nil {
		t.Fatalf("signState: %v", err)
	}
	setupReq := httptest.NewRequest("GET",
		fmt.Sprintf("/api/github/setup?installation_id=%d&state=%s", installationID, state),
		nil,
	)
	setupRec := httptest.NewRecorder()
	testHandler.GitHubSetupCallback(setupRec, setupReq)
	if setupRec.Code != http.StatusFound {
		t.Fatalf("setup callback: expected 302, got %d (%s)", setupRec.Code, setupRec.Body.String())
	}
	if loc := setupRec.Header().Get("Location"); !strings.Contains(loc, "github_connected=1") {
		t.Fatalf("setup callback redirect = %q, want github_connected=1", loc)
	}

	got, err := testHandler.Queries.GetGitHubInstallationByInstallationID(ctx, installationID)
	if err != nil {
		t.Fatalf("get installation: %v", err)
	}
	if got.AccountLogin != "pending-octocat" {
		t.Errorf("account_login = %q, want pending-octocat (callback left the unknown placeholder)", got.AccountLogin)
	}
	if got.AccountType != "Organization" {
		t.Errorf("account_type = %q, want Organization", got.AccountType)
	}
	if got.AccountAvatarUrl.String != "https://example.com/pending.png" || !got.AccountAvatarUrl.Valid {
		t.Errorf("account_avatar_url = %+v, want pending avatar", got.AccountAvatarUrl)
	}

	var pendingCount int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM github_pending_installation WHERE installation_id = $1`,
		installationID,
	).Scan(&pendingCount); err != nil {
		t.Fatalf("count pending installation: %v", err)
	}
	if pendingCount != 0 {
		t.Fatalf("pending installation row should be consumed, got count %d", pendingCount)
	}
}
