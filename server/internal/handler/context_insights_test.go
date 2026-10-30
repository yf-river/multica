package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func insightRequest(t *testing.T, task string, body any) *http.Request {
	t.Helper()
	r := testutil.JSONRequest(http.MethodPost, "/api/daemon/tasks/"+task+"/insights", body)
	r = testutil.WithURLParams(r, "taskId", task)
	r.Header.Set("X-User-ID", testUserID)
	return r
}
func TestContextInsightsDuplicateChunkAndObjectBoundary(t *testing.T) {
	activateInsightTest(t)
	task := seedBatchTask(t, "insight-original")
	session := "insight-" + task
	frame := map[string]any{"type": "archive", "session_id": session, "event": map[string]any{"type": "user/message", "seq": 0, "time": time.Now().UnixMilli(), "data": map[string]any{"content": strings.Repeat("original", 100000), "scope": "tool\x00scope"}}}
	raw, _ := json.Marshal(frame)
	sum := sha256.Sum256(raw)
	id := hex.EncodeToString(sum[:])
	total := (len(raw) + 262143) / 262144
	for attempt := 0; attempt < 2; attempt++ {
		for part := 0; part < total; part++ {
			end := min(len(raw), (part+1)*262144)
			body := map[string]any{"chunk": map[string]any{"id": id, "part": part, "total": total, "data": base64.StdEncoding.EncodeToString(raw[part*262144 : end])}}
			testutil.Call(t, testHandler.ReportContextInsights, insightRequest(t, task, body)).Want(200)
		}
	}
	var count int
	var content []byte
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM context_insights_event WHERE task_id=$1`, task).Scan(&count); err != nil || count != 1 {
		t.Fatalf("dedup: count=%d err=%v", count, err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT content FROM context_insights_event WHERE task_id=$1`, task).Scan(&content); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	json.Unmarshal(content, &decoded)
	if stringValue := decoded["event"].(map[string]any)["data"].(map[string]any)["content"].(string); len(stringValue) != 800000 {
		t.Fatal("original content truncated")
	}
	other := seedBatchTask(t, "insight-other-object")
	testutil.Call(t, testHandler.ReportContextInsights, insightRequest(t, other, map[string]any{"frames": []any{frame}})).Want(409)
}
func TestContextInsightsActivationAndPrivateVisibility(t *testing.T) {
	activateInsightTest(t)
	task := seedBatchTask(t, "insight-activation")
	session := "insight-" + task
	var since time.Time
	testPool.QueryRow(context.Background(), `SELECT enabled_since FROM context_insights_config WHERE singleton`).Scan(&since)
	frames := []any{map[string]any{"type": "archive", "session_id": session, "event": map[string]any{"type": "user/message", "seq": 0, "time": since.UnixMilli() - 1}}, map[string]any{"type": "insights", "session_id": session, "seq": 1, "projections": map[string]any{"contextTimeline": map[string]any{"ok": true, "current": map[string]int{"total": 10}}}}}
	testutil.Call(t, testHandler.ReportContextInsights, insightRequest(t, task, map[string]any{"frames": frames})).Want(200)
	var count int
	testPool.QueryRow(context.Background(), `SELECT count(*) FROM context_insights_event WHERE task_id=$1`, task).Scan(&count)
	if count != 0 {
		t.Fatal("pre-activation usage retained")
	}
	var id string
	testPool.QueryRow(context.Background(), `SELECT session_key FROM context_insights_run WHERE task_id=$1`, task).Scan(&id)
	req := testutil.JSONRequest("GET", "/api/insights/"+id, nil)
	req = testutil.WithURLParams(req, "insightId", id)
	req.Header.Set("X-User-ID", testUserID)
	testutil.Call(t, testHandler.GetContextInsights, req).Want(200)
	req.Header.Set("X-User-ID", "00000000-0000-0000-0000-000000000001")
	testutil.Call(t, testHandler.GetContextInsights, req).Want(404)
}

func TestContextInsightsRunCostIdempotent(t *testing.T) {
	activateInsightTest(t)
	task := seedBatchTask(t, "insight-run-cost")
	cost := map[string]any{"p": map[string]any{"m": map[string]any{"peak": map[string]int{"uncached": 7, "cacheRead": 0, "cacheWrite": 0, "output": 2}}}}
	for _, seq := range []int{5, 5, 4} {
		frame := map[string]any{"type": "insights", "session_id": "run-cost-" + task, "seq": seq, "projections": map[string]any{"multicaRunCost": cost}}
		testutil.Call(t, testHandler.ReportContextInsights, insightRequest(t, task, map[string]any{"frames": []any{frame}})).Want(200)
	}
	resp := []AgentTaskResponse{{ID: task}}
	testHandler.hydrateInsightCosts(context.Background(), resp)
	if len(resp[0].InsightCosts) != 1 {
		t.Fatalf("costs=%v", resp[0].InsightCosts)
	}
	var observed map[string]any
	if json.Unmarshal(resp[0].InsightCosts[0], &observed) != nil || observed["p"] == nil {
		t.Fatal("run cost not preserved")
	}
	var seq int
	if err := testPool.QueryRow(context.Background(), `SELECT seq FROM context_insights_run WHERE task_id=$1`, task).Scan(&seq); err != nil || seq != 5 {
		t.Fatalf("seq=%d err=%v", seq, err)
	}
}

func TestContextInsightsRequiresExplicitActivation(t *testing.T) {
	ctx := context.Background()
	var before pgtype.Timestamptz
	if err := testPool.QueryRow(ctx, `SELECT enabled_since FROM context_insights_config WHERE singleton`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `UPDATE context_insights_config SET enabled_since=$1 WHERE singleton`, before)
	})
	if _, err := testPool.Exec(ctx, `UPDATE context_insights_config SET enabled_since=NULL WHERE singleton`); err != nil {
		t.Fatal(err)
	}
	recorder := testutil.Call(t, testHandler.GetInsightsCaptureConfig, testutil.JSONRequest("GET", "/api/daemon/insights/config", nil)).Want(200)
	_ = recorder
	task := seedBatchTask(t, "insight-before-activation")
	frame := map[string]any{"type": "insights", "session_id": "before-activation-" + task, "seq": 1, "projections": map[string]any{"contextTimeline": map[string]any{}}}
	testutil.Call(t, testHandler.ReportContextInsights, insightRequest(t, task, map[string]any{"frames": []any{frame}})).Want(409)
}

func TestContextInsightsAttachmentOriginalAndDeletion(t *testing.T) {
	activateInsightTest(t)
	task := seedBatchTask(t, "insight-image-original")
	session := "image-" + task
	original := []byte("\x89PNG\r\n\x1a\noriginal\x00binary")
	sum := sha256.Sum256(original)
	id := "sha256:" + hex.EncodeToString(sum[:])
	frame := map[string]any{"type": "insight_attachment", "session_id": session, "attachment_id": id, "media_type": "image/png", "filename": "image.png", "content": original}
	for i := 0; i < 2; i++ {
		testutil.Call(t, testHandler.ReportContextInsights, insightRequest(t, task, map[string]any{"frames": []any{frame}})).Want(200)
	}
	var key, issue string
	if err := testPool.QueryRow(context.Background(), `SELECT r.session_key,s.issue_id::text FROM context_insights_run r JOIN context_insights_session s ON s.id=r.session_key WHERE r.task_id=$1`, task).Scan(&key, &issue); err != nil {
		t.Fatal(err)
	}
	req := testutil.WithURLParams(testutil.JSONRequest("GET", "/api/insights/"+key+"/attachment?id="+id, nil), "insightId", key)
	req.Header.Set("X-User-ID", testUserID)
	response := testutil.Call(t, testHandler.GetContextInsightAttachment, req).Want(200)
	if !bytes.Equal(response.Body.Bytes(), original) || response.Header().Get("Content-Type") != "image/png" {
		t.Fatal("download changed original")
	}
	req.Header.Set("X-User-ID", "00000000-0000-0000-0000-000000000001")
	testutil.Call(t, testHandler.GetContextInsightAttachment, req).Want(404)
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = deleteContextInsightData(context.Background(), tx, testWorkspaceID, issue, false); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(context.Background())
	req.Header.Set("X-User-ID", testUserID)
	testutil.Call(t, testHandler.GetContextInsightAttachment, req).Want(200)
	tx, err = testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = deleteContextInsightData(context.Background(), tx, testWorkspaceID, issue, false); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	testutil.Call(t, testHandler.GetContextInsightAttachment, req).Want(404)
}

func TestContextInsightsLateReplayKeepsCurrentSession(t *testing.T) {
	activateInsightTest(t)
	ctx := context.Background()
	task := seedBatchTask(t, "insight-late-replay")
	var since time.Time
	if err := testPool.QueryRow(ctx, `SELECT enabled_since FROM context_insights_config WHERE singleton`).Scan(&since); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET created_at=$2 WHERE id=$1`, task, since.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id string
		at time.Time
	}{{"new-" + task, since.Add(2 * time.Second)}, {"old-" + task, since.Add(time.Second)}} {
		frames := []any{map[string]any{"type": "archive", "session_id": item.id, "event": map[string]any{"seq": 1, "time": item.at.UnixMilli(), "type": "user/message"}}, map[string]any{"type": "insights", "session_id": item.id, "seq": 2, "projections": map[string]any{}}}
		testutil.Call(t, testHandler.ReportContextInsights, insightRequest(t, task, map[string]any{"frames": frames})).Want(200)
	}
	var selected string
	if err := testPool.QueryRow(ctx, `SELECT session_id FROM context_insights_session s JOIN context_insights_run r ON r.session_key=s.id WHERE r.task_id=$1 ORDER BY s.updated_at DESC LIMIT 1`, task).Scan(&selected); err != nil || selected != "new-"+task {
		t.Fatalf("late replay replaced current session: %s %v", selected, err)
	}
}

func activateInsightTest(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE context_insights_config SET enabled_since=COALESCE(enabled_since,now()) WHERE singleton`); err != nil {
		t.Fatal(err)
	}
}

func TestContextInsightsCompleteHeadersExceedPageSize(t *testing.T) {
	activateInsightTest(t)
	task := seedBatchTask(t, "insight-all-heads")
	for start := 0; start < 112; start += 32 {
		frames := []any{}
		for i := start; i < start+32 && i < 112; i++ {
			frames = append(frames, map[string]any{"type": "insights", "session_id": fmt.Sprintf("all-%s-%03d", task, i), "seq": 1, "projections": map[string]any{}})
		}
		testutil.Call(t, testHandler.ReportContextInsights, insightRequest(t, task, map[string]any{"frames": frames})).Want(200)
	}
	for _, all := range []bool{false, true} {
		req := testutil.JSONRequest("GET", fmt.Sprintf("/api/insights?workspace_id=%s&all=%t", testWorkspaceID, all), nil)
		req.Header.Set("X-User-ID", testUserID)
		res := testutil.Call(t, testHandler.ListContextInsights, req).Want(200)
		var body struct {
			Sessions []insightSession `json:"sessions"`
			Total    int              `json:"total"`
		}
		if json.Unmarshal(res.Body.Bytes(), &body) != nil {
			t.Fatal("invalid response")
		}
		own := 0
		for _, s := range body.Sessions {
			if strings.HasPrefix(s.SessionID, "all-"+task) {
				own++
			}
		}
		if all && own != 112 {
			t.Fatalf("complete overview lost headers: %d", own)
		}
		if !all && len(body.Sessions) > 100 {
			t.Fatal("ordinary pagination ignored its limit")
		}
	}
}
