package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func enableQuickCreateRuntime(t *testing.T, ctx context.Context) string {
	t.Helper()

	var agentID, runtimeID string
	var previousMetadata []byte
	if err := testPool.QueryRow(ctx, `
		SELECT a.id, r.id, r.metadata
		FROM agent a
		JOIN agent_runtime r ON r.id = a.runtime_id
		WHERE a.workspace_id = $1 AND a.name = 'Handler Test Agent'
	`, testWorkspaceID).Scan(&agentID, &runtimeID, &previousMetadata); err != nil {
		t.Fatalf("fetch quick-create agent runtime: %v", err)
	}
	if _, err := testPool.Exec(ctx,
		`UPDATE agent_runtime SET metadata = jsonb_build_object('cli_version', $1::text) WHERE id = $2`,
		agent.MinQuickCreateCLIVersion, runtimeID,
	); err != nil {
		t.Fatalf("bump runtime cli_version: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(),
			`UPDATE agent_runtime SET metadata = $1::jsonb WHERE id = $2`, previousMetadata, runtimeID)
	})

	return agentID
}

// TestQuickCreateIssueParentTrustBoundary locks the server-side trust boundary
// for the optional parent_issue_id field on POST /api/issues/quick-create.
//
// The frontend seeds parent_issue_id from the "Add sub issue" entry point and
// otherwise leaves it empty. The handler is the trust boundary: a forged
// request must not be able to smuggle a foreign parent UUID through to the
// quick-create task context, and the same-workspace happy path must thread
// the resolved UUID into QuickCreateContext.ParentIssueID so the daemon claim
// step can resolve the identifier and emit `--parent <uuid>` in the prompt.
//
// Three branches are covered:
//
//  1. Same-workspace parent → 202 Accepted, task enqueued with
//     QuickCreateContext.ParentIssueID populated.
//  2. Foreign-workspace parent → 400 Bad Request, no task enqueued.
//  3. Bogus UUID parent → 400 Bad Request, no task enqueued.
func TestQuickCreateIssueParentTrustBoundary(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Resolve the seeded runtime + agent for this workspace, then bump the
	// runtime metadata to a CLI version that clears MinQuickCreateCLIVersion.
	// The seed runtime uses metadata '{}'::jsonb which would otherwise trip
	// the daemon-version gate before we ever reach the parent_issue_id check.
	agentID := enableQuickCreateRuntime(t, ctx)

	// Same-workspace parent — must be accepted and threaded through.
	var localParentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_id, creator_type, number)
		VALUES ($1, 'quick-create parent (local)', $2, 'member',
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1))
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&localParentID); err != nil {
		t.Fatalf("create local parent issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, localParentID)
	})

	// Foreign-workspace parent — must be rejected.
	var foreignWorkspaceID, foreignUserID, foreignParentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, account) VALUES ($1, $2) RETURNING id
	`, "QuickCreate Foreign", "quickcreate-foreign@multica.ai").Scan(&foreignUserID); err != nil {
		t.Fatalf("create foreign user: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, foreignUserID)
	})
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4) RETURNING id
	`, "QuickCreate Foreign WS", "quickcreate-foreign-ws", "", "QCF").Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_id, creator_type, number)
		VALUES ($1, 'quick-create parent (foreign)', $2, 'member',
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1))
		RETURNING id
	`, foreignWorkspaceID, foreignUserID).Scan(&foreignParentID); err != nil {
		t.Fatalf("create foreign parent issue: %v", err)
	}
	// The foreign workspace cleanup above cascades, but the issue row also
	// needs a direct cleanup in case workspace deletion ordering changes.
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, foreignParentID)
	})

	// Helper for the "must not enqueue" assertions. Each rejection subtest
	// snapshots the count immediately before the request and re-checks after
	// so sibling subtests (and their t.Cleanup deletions) can't false-positive
	// or false-negative this assertion.
	countQuickCreateTasks := func(t *testing.T) int {
		t.Helper()
		var count int
		if err := testPool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM agent_task_queue WHERE agent_id = $1 AND context->>'type' = 'quick_create'`,
			agentID,
		).Scan(&count); err != nil {
			t.Fatalf("count quick-create tasks: %v", err)
		}
		return count
	}

	t.Run("same workspace parent enqueues with context", func(t *testing.T) {
		attachmentID := "019ec09d-6222-722b-bdfa-427b105d80be"
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/quick-create", map[string]any{
			"agent_id":        agentID,
			"prompt":          "Create a follow-up issue for the local parent",
			"parent_issue_id": localParentID,
			"attachment_ids":  []string{attachmentID},
		})
		testHandler.QuickCreateIssue(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
		}
		var resp QuickCreateIssueResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, resp.TaskID)
		})

		// QuickCreateContext.ParentIssueID must contain the resolved UUID —
		// the daemon claim step reads this field to attach the parent
		// identifier and to inject `--parent <uuid>` into the prompt.
		var contextJSON []byte
		if err := testPool.QueryRow(context.Background(),
			`SELECT context FROM agent_task_queue WHERE id = $1`, resp.TaskID,
		).Scan(&contextJSON); err != nil {
			t.Fatalf("load task context: %v", err)
		}
		var qc service.QuickCreateContext
		if err := json.Unmarshal(contextJSON, &qc); err != nil {
			t.Fatalf("unmarshal context: %v", err)
		}
		if qc.Type != service.QuickCreateContextType {
			t.Fatalf("expected type=%q, got %q", service.QuickCreateContextType, qc.Type)
		}
		if qc.ParentIssueID != localParentID {
			t.Fatalf("expected parent_issue_id=%q in context, got %q", localParentID, qc.ParentIssueID)
		}
		if len(qc.AttachmentIDs) != 1 || qc.AttachmentIDs[0] != attachmentID {
			t.Fatalf("expected attachment_ids=[%q] in context, got %#v", attachmentID, qc.AttachmentIDs)
		}
	})

	t.Run("foreign workspace parent is rejected", func(t *testing.T) {
		before := countQuickCreateTasks(t)
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/quick-create", map[string]any{
			"agent_id":        agentID,
			"prompt":          "Try to smuggle a foreign parent",
			"parent_issue_id": foreignParentID,
		})
		testHandler.QuickCreateIssue(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for foreign parent, got %d: %s", w.Code, w.Body.String())
		}
		if got := countQuickCreateTasks(t); got != before {
			// Any increase means the foreign-parent request enqueued a
			// task despite the 400 — the trust boundary leaked.
			t.Fatalf("foreign parent must not enqueue a task: expected %d quick-create tasks, got %d", before, got)
		}
	})

	t.Run("bogus uuid parent is rejected", func(t *testing.T) {
		before := countQuickCreateTasks(t)
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/quick-create", map[string]any{
			"agent_id":        agentID,
			"prompt":          "Try a malformed parent UUID",
			"parent_issue_id": "not-a-uuid",
		})
		testHandler.QuickCreateIssue(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for bogus parent, got %d: %s", w.Code, w.Body.String())
		}
		if got := countQuickCreateTasks(t); got != before {
			t.Fatalf("bogus parent must not enqueue a task: expected %d quick-create tasks, got %d", before, got)
		}
	})

	t.Run("bogus uuid attachment id is rejected", func(t *testing.T) {
		before := countQuickCreateTasks(t)
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/quick-create", map[string]any{
			"agent_id":       agentID,
			"prompt":         "Try a malformed attachment UUID",
			"attachment_ids": []string{"not-a-uuid"},
		})
		testHandler.QuickCreateIssue(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for bogus attachment id, got %d: %s", w.Code, w.Body.String())
		}
		if got := countQuickCreateTasks(t); got != before {
			t.Fatalf("bogus attachment id must not enqueue a task: expected %d quick-create tasks, got %d", before, got)
		}
	})
}

func TestQuickCreateIssueTapdWikiCreatesFetchedIssueDirectly(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID := enableQuickCreateRuntime(t, ctx)

	if _, err := testPool.Exec(ctx, `DELETE FROM external_credential_profile WHERE user_id = $1 AND provider = 'tapd'`, testUserID); err != nil {
		t.Fatalf("clear tapd profiles: %v", err)
	}
	t.Setenv("TAPD_QUICK_CREATE_TEST_TOKEN", "tapd-quick-create-token")

	title := fmt.Sprintf("TAPD 直建测试 %d", time.Now().UnixNano())
	var sawAuth bool
	tapdAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tapd_wikis" {
			t.Fatalf("unexpected TAPD path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("workspace_id") != "47654106" || r.URL.Query().Get("id") != "1147654106001004223" {
			t.Fatalf("unexpected TAPD query: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") == "Bearer tapd-quick-create-token" && r.Header.Get("Via") == "mcp" {
			sawAuth = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{map[string]any{
				"TWiki": map[string]any{
					"id":                   "1147654106001004223",
					"name":                 title,
					"markdown_description": "这是从 TAPD Wiki 抓取的真实需求正文。需要进入 SOP 小队处理。",
					"modified":             "2026-07-02 10:00:00",
				},
			}},
		})
	}))
	defer tapdAPI.Close()
	t.Setenv("TAPD_API_BASE_URL", tapdAPI.URL)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles", map[string]any{
		"provider":   "tapd",
		"name":       fmt.Sprintf("tapd-quick-create-%d", time.Now().UnixNano()),
		"secret_ref": "env:TAPD_QUICK_CREATE_TEST_TOKEN",
	})
	testHandler.CreateExternalCredentialProfile(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateExternalCredentialProfile: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var quickTasksBefore int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_task_queue WHERE context->>'type' = 'quick_create' AND agent_id = $1`,
		agentID,
	).Scan(&quickTasksBefore); err != nil {
		t.Fatalf("count quick-create tasks before: %v", err)
	}

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues/quick-create", map[string]any{
		"agent_id": agentID,
		"prompt":   "根据 TAPD Wiki 文档创建需求：https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004223",
	})
	testHandler.QuickCreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("QuickCreateIssue TAPD: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if !sawAuth {
		t.Fatal("TAPD quick-create did not use credential-backed auto_fetch")
	}
	var resp QuickCreateIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode quick-create response: %v", err)
	}
	if resp.TaskID != "" {
		t.Fatalf("TAPD direct create must not return quick-create task_id, got %q", resp.TaskID)
	}
	if resp.IssueID == "" || resp.SourceFetchStatus != "fetched" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, resp.IssueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, resp.IssueID)
	})

	var quickTasksAfter int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_task_queue WHERE context->>'type' = 'quick_create' AND agent_id = $1`,
		agentID,
	).Scan(&quickTasksAfter); err != nil {
		t.Fatalf("count quick-create tasks after: %v", err)
	}
	if quickTasksAfter != quickTasksBefore {
		t.Fatalf("TAPD direct create enqueued quick-create task: before=%d after=%d", quickTasksBefore, quickTasksAfter)
	}

	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(resp.IssueID))
	if err != nil {
		t.Fatalf("load created issue: %v", err)
	}
	metadata := parseIssueMetadata(issue.Metadata)
	if metadata["source_fetch_status"] != "fetched" || metadata["source_fetch_title"] != title {
		t.Fatalf("metadata missing fetched TAPD source: %+v", metadata)
	}
	if metadata["source_summary_status"] != "pending" || metadata["source_summary_task_id"] == "" {
		t.Fatalf("metadata missing pending source summary task: %+v", metadata)
	}
	if !strings.Contains(issue.Description.String, "摘要生成中") {
		t.Fatalf("description should be a summary placeholder before source summary completion: %s", issue.Description.String)
	}
	if strings.Contains(issue.Description.String, "真实需求正文") {
		t.Fatalf("description should not copy fetched TAPD body before LLM summary: %s", issue.Description.String)
	}
	source := testHandler.buildIssueSourceContext(ctx, issue, parseUUID(testUserID))
	if source == nil || source.TAPD == nil || source.TAPD.Title != title || !strings.Contains(source.TAPD.BodyExcerpt, "真实需求正文") {
		t.Fatalf("source_context missing fetched fields: %+v", source)
	}

	var summaryTaskID string
	if err := testPool.QueryRow(ctx,
		`SELECT id::text FROM agent_task_queue WHERE issue_id = $1 AND context->>'type' = 'issue_source_summary'`,
		resp.IssueID,
	).Scan(&summaryTaskID); err != nil {
		t.Fatalf("load source summary task: %v", err)
	}
	var formalTasksBefore int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_task_queue WHERE issue_id = $1 AND context IS NULL`,
		resp.IssueID,
	).Scan(&formalTasksBefore); err != nil {
		t.Fatalf("count formal tasks before summary completion: %v", err)
	}
	if formalTasksBefore != 0 {
		t.Fatalf("formal execution task should wait for source summary, got %d", formalTasksBefore)
	}

	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, summaryTaskID); err != nil {
		t.Fatalf("mark source summary task running: %v", err)
	}
	summaryOutput := "## 需求摘要\n租户创建校验场景需要通过租户 ID 查询初始化管理员信息。\n\n## 验收要点\n- 已初始化管理员时返回用户基础信息。\n- 未初始化管理员时返回明确业务错误。"
	result, _ := json.Marshal(map[string]string{"output": summaryOutput})
	gotIssueUpdated := make(chan map[string]any, 1)
	testHandler.Bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		issuePayload, ok := payload["issue"].(map[string]any)
		if !ok || issuePayload["id"] != resp.IssueID {
			return
		}
		select {
		case gotIssueUpdated <- issuePayload:
		default:
		}
	})
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(summaryTaskID), result, "", ""); err != nil {
		t.Fatalf("complete source summary task: %v", err)
	}
	select {
	case issuePayload := <-gotIssueUpdated:
		description, ok := issuePayload["description"].(*string)
		if !ok || description == nil || *description != summaryOutput {
			t.Fatalf("issue:updated description = %#v, want pointer to source summary output", issuePayload["description"])
		}
		metadata, ok := issuePayload["metadata"].(map[string]any)
		if !ok {
			t.Fatalf("issue:updated payload missing metadata: %+v", issuePayload)
		}
		if metadata["source_summary_status"] != "completed" {
			t.Fatalf("issue:updated metadata source_summary_status = %v, want completed; metadata=%+v", metadata["source_summary_status"], metadata)
		}
	default:
		t.Fatal("source summary completion did not publish matching issue:updated event")
	}
	issue, err = testHandler.Queries.GetIssue(ctx, parseUUID(resp.IssueID))
	if err != nil {
		t.Fatalf("reload summarized issue: %v", err)
	}
	if issue.Description.String != summaryOutput {
		t.Fatalf("description not replaced by source summary output:\n%s", issue.Description.String)
	}
	metadata = parseIssueMetadata(issue.Metadata)
	if metadata["source_summary_status"] != "completed" {
		t.Fatalf("source summary status not completed: %+v", metadata)
	}
	var formalTasksAfter int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_task_queue WHERE issue_id = $1 AND context IS NULL AND status = 'queued'`,
		resp.IssueID,
	).Scan(&formalTasksAfter); err != nil {
		t.Fatalf("count formal tasks after summary completion: %v", err)
	}
	if formalTasksAfter != 1 {
		t.Fatalf("expected formal execution task after source summary completion, got %d", formalTasksAfter)
	}
}

func TestQuickCreateIssueTapdStoryPreviewCreatesFetchedIssueDirectly(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID := enableQuickCreateRuntime(t, ctx)

	if _, err := testPool.Exec(ctx, `DELETE FROM external_credential_profile WHERE user_id = $1 AND provider = 'tapd'`, testUserID); err != nil {
		t.Fatalf("clear tapd profiles: %v", err)
	}
	t.Setenv("TAPD_QUICK_CREATE_STORY_TEST_TOKEN", "tapd-story-token")

	var sawStoryFetch int
	tapdAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stories" {
			t.Fatalf("unexpected TAPD path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("workspace_id") != "51081496" || r.URL.Query().Get("id") != "1151081496001028216" {
			t.Fatalf("unexpected TAPD query: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") == "Bearer tapd-story-token" && r.Header.Get("Via") == "mcp" {
			sawStoryFetch++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{map[string]any{
				"Story": map[string]any{
					"id":          "1151081496001028216",
					"name":        "【DSM】【系统管理】公告管理",
					"description": "<p>公告列表提供公告管理查询功能。</p><p>发布公告支持管理员填写标题和内容。</p>",
					"modified":    "2026-05-19 18:54:01",
				},
			}},
		})
	}))
	defer tapdAPI.Close()
	t.Setenv("TAPD_API_BASE_URL", tapdAPI.URL)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles", map[string]any{
		"provider":   "tapd",
		"name":       fmt.Sprintf("tapd-story-%d", time.Now().UnixNano()),
		"secret_ref": "env:TAPD_QUICK_CREATE_STORY_TEST_TOKEN",
	})
	testHandler.CreateExternalCredentialProfile(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateExternalCredentialProfile: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	prompt := "TAPD Story 1151081496001028216\n\nUser request\nhttps://www.tapd.cn/tapd_fe/51081496/story/list?categoryId=1151081496001000494&useScene=storyList&groupType=&conf_id=1151081496001024908&page=1&dialog_preview_id=story_1151081496001028216"
	createFromStory := func(t *testing.T) QuickCreateIssueResponse {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/quick-create", map[string]any{
			"agent_id": agentID,
			"prompt":   prompt,
		})
		testHandler.QuickCreateIssue(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("QuickCreateIssue TAPD story: expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var resp QuickCreateIssueResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode quick-create response: %v", err)
		}
		if resp.TaskID != "" {
			t.Fatalf("TAPD story direct create must not return quick-create task_id, got %q", resp.TaskID)
		}
		if resp.IssueID == "" || resp.SourceFetchStatus != "fetched" {
			t.Fatalf("unexpected response: %+v", resp)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, resp.IssueID)
			testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, resp.IssueID)
		})
		return resp
	}

	first := createFromStory(t)
	second := createFromStory(t)
	if first.IssueID == second.IssueID {
		t.Fatalf("duplicate TAPD story create reused issue id %s", first.IssueID)
	}
	if sawStoryFetch != 2 {
		t.Fatalf("story fetch count = %d, want 2", sawStoryFetch)
	}

	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(first.IssueID))
	if err != nil {
		t.Fatalf("load created issue: %v", err)
	}
	if issue.Title != "【DSM】【系统管理】公告管理" {
		t.Fatalf("issue title = %q", issue.Title)
	}
	if !strings.Contains(issue.Description.String, "摘要生成中") ||
		strings.Contains(issue.Description.String, "公告列表提供公告管理查询功能") {
		t.Fatalf("story description should be a summary placeholder before source summary completion: %s", issue.Description.String)
	}
	metadata := parseIssueMetadata(issue.Metadata)
	if metadata["source_url"] != "https://www.tapd.cn/51081496/prong/stories/view/1151081496001028216" ||
		metadata["tapd_workspace_id"] != "51081496" ||
		metadata["tapd_resource_type"] != "story" ||
		metadata["tapd_resource_id"] != "1151081496001028216" ||
		metadata["source_fetch_resource_type"] != "story" ||
		metadata["source_fetch_title"] != "【DSM】【系统管理】公告管理" ||
		metadata["source_summary_status"] != "pending" ||
		metadata["source_summary_task_id"] == "" {
		t.Fatalf("metadata missing fetched TAPD story source: %+v", metadata)
	}
	var summaryTaskCount int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_task_queue WHERE issue_id = $1 AND context->>'type' = 'issue_source_summary'`,
		first.IssueID,
	).Scan(&summaryTaskCount); err != nil {
		t.Fatalf("count story source summary tasks: %v", err)
	}
	if summaryTaskCount != 1 {
		t.Fatalf("story create should enqueue one source summary task, got %d", summaryTaskCount)
	}
	source := testHandler.buildIssueSourceContext(ctx, issue, parseUUID(testUserID))
	if source == nil || source.TAPD == nil || source.TAPD.ResourceType != "story" || source.TAPD.ResourceID != "1151081496001028216" {
		t.Fatalf("source_context missing story fields: %+v", source)
	}
}
