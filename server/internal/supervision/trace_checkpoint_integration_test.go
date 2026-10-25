package supervision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	dbfx "github.com/multica-ai/multica/server/internal/testutil"
)

type traceTestTransport func(*http.Request) (*http.Response, error)

func (f traceTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 1. Forty reserved calls stop inside a page and retain its exact next detail and reference.
func TestTraceDiscoveryBudgetCheckpoint(t *testing.T) {
	x := newRoundFixture(t)
	ctx := context.Background()
	x.f.Cleanup(t, "DELETE FROM supervision_trace_material WHERE workspace_id=$1", x.workspace)
	x.f.Exec(t, "UPDATE supervision_config SET investigation_enabled=true WHERE workspace_id=$1", x.workspace)
	issue := x.issue(t, "done")
	started := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	completed := started.Add(20 * time.Minute)
	run := x.f.Task(t, x.agent, dbfx.Cols{"issue_id": issue, "runtime_id": x.runtime, "status": "completed", "started_at": started, "completed_at": completed, "session_id": "owned-session"})
	x.f.Insert(t, "task_provider_trace", dbfx.Cols{"workspace_id": x.workspace, "task_id": run, "provider": "codebuddy", "provider_trace_id": "owned-cid", "session_id": "owned-session", "correlation": "exact", "source": "fixture"})
	round, err := x.svc.OpenRound(ctx, x.workspace, issue, true)
	if err != nil {
		t.Fatal(err)
	}
	id := x.f.Insert(t, "supervision_trace_material", dbfx.Cols{"id": dbfx.Raw("gen_random_uuid()"), "workspace_id": x.workspace, "issue_id": issue, "round_id": round.ID, "run_id": run, "runtime": "codebuddy", "source": "execution", "status": "processing", "attempts": 1})
	credential := filepath.Join(t.TempDir(), "scope.json")
	encoded, _ := json.Marshal(AgentLensScope{Workspace: x.workspace, Token: "fixture-not-a-real-credential"})
	if err = os.WriteFile(credential, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_AGENTLENS_CREDENTIAL_FILE", credential)
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	calls := 0
	http.DefaultTransport = traceTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		body := `{"code":0,"data":{"trace_detail":{}}}`
		if strings.HasSuffix(req.URL.Path, "traceList") {
			var fields map[string]any
			if err = json.NewDecoder(req.Body).Decode(&fields); err != nil {
				t.Fatal(err)
			}
			wantStart, wantEnd := traceQueryRange(started, completed)
			if fields["start_time"] != strconv.FormatInt(wantStart.UnixNano(), 10) || fields["end_time"] != strconv.FormatInt(wantEnd.UnixNano(), 10) || fields["session_id"] != "owned-session" || !strings.Contains(fields["tags"].(string), "owned-cid") || fields["page_num"] != float64(1) {
				t.Fatalf("wrong discovery bounds/identity/page: %+v", fields)
			}
			traces := make([]map[string]string, 50)
			for i := range traces {
				traces[i] = map[string]string{"traceID": strconv.Itoa(i)}
			}
			b, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"list": map[string]any{"total": 51, "page_num": 1, "page_size": 50, "traces": traces}}})
			body = string(b)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if err = x.svc.prepareTrace(ctx, tracePreparation{ID: id, Workspace: x.workspace, Issue: issue, Round: round.ID, Run: run, Runtime: "codebuddy", Attempts: 1, Version: 1}); err != nil {
		t.Fatal(err)
	}
	var cursor, report []byte
	var status string
	x.f.QueryRow(t, "SELECT cursor,report,status FROM supervision_trace_material WHERE id=$1", id).Scan(&cursor, &report, &status)
	var progress struct {
		Reference, Detail, Page int
		Candidates              []string
	}
	if err = json.Unmarshal(cursor, &progress); err != nil {
		t.Fatal(err)
	}
	var result traceReport
	if err = json.Unmarshal(report, &result); err != nil {
		t.Fatal(err)
	}
	if calls != 40 || status != "partial" || progress.Reference != 0 || progress.Page != 1 || progress.Detail != 39 || len(progress.Candidates) != 50 || !result.DiscoveryPending {
		t.Fatalf("lost checkpoint: calls=%d status=%s cursor=%s pending=%v", calls, status, cursor, result.DiscoveryPending)
	}
	if x.svc.reserveTraceRequest(ctx, x.workspace, round.ID) == nil {
		t.Fatal("checkpoint replenished budget")
	}
	// A new immutable recheck version resumes the checkpoint but cannot replenish this round's allowance.
	next := x.f.Insert(t, "supervision_trace_material", dbfx.Cols{"id": dbfx.Raw("gen_random_uuid()"), "workspace_id": x.workspace, "issue_id": issue, "round_id": round.ID, "run_id": run, "runtime": "codebuddy", "source": "execution", "status": "pending", "material_version": 2, "created_at": time.Now().Add(-2 * time.Minute), "cursor": json.RawMessage(cursor), "report": json.RawMessage(report)})
	if err = x.svc.prepareTraceMaterials(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	var resumed []byte
	x.f.QueryRow(t, "SELECT cursor FROM supervision_trace_material WHERE id=$1", next).Scan(&resumed)
	var resumedProgress struct {
		Reference, Detail, Page int
		Candidates              []string
	}
	if err = json.Unmarshal(resumed, &resumedProgress); err != nil {
		t.Fatal(err)
	}
	if calls != 40 || resumedProgress.Detail != 39 || resumedProgress.Reference != 0 || resumedProgress.Page != 1 || len(resumedProgress.Candidates) != 50 {
		t.Fatalf("resume lost page or spent new quota: calls=%d cursor=%s", calls, resumed)
	}

}
