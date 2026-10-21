package supervision_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/supervision"
	dbfixture "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestIntSupervisionPreservesNewHumanDecisions(t *testing.T) {
	if os.Getenv("MULTICA_SUPERVISION_INT_TEST") != "1" {
		t.Skip("requires explicit fixed-int validation")
	}
	ctx := context.Background()
	raw, err := os.ReadFile("/data/ida/multica/.run/env/int-services.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]map[string]string
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["server"]["MULTICA_ENV_ID"] != "int" {
		t.Fatal("not int")
	}
	pool, err := pgxpool.New(ctx, cfg["server"]["DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := dbfixture.New(pool, "", "")
	stamp := fmt.Sprint(time.Now().UnixNano())
	f.UserID = f.User(t, "人工处理保护验收", "decision-"+stamp+"@example.test", dbfixture.Cols{"account": "decision-" + stamp})
	f.WorkspaceID = f.Workspace(t, "人工处理保护验收", "decision-"+stamp)
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	runtime := f.Runtime(t, "人工处理夹具", dbfixture.Cols{"status": "offline"})
	agent := f.Agent(t, "人工处理夹具", runtime)
	f.InsertNoID(t, "supervision_config", dbfixture.Cols{"workspace_id": f.WorkspaceID, "owner_id": f.UserID, "agent_id": agent, "enabled": false, "investigation_enabled": true}, "workspace_id=$1", f.WorkspaceID)
	svc := supervision.New(pool, nil)
	h := &handler.Handler{DB: pool, Queries: db.New(pool), TxStarter: pool, Supervision: svc}
	call := func(t *testing.T, fn http.HandlerFunc, method, path string, body any, params ...string) *dbfixture.Response {
		r := dbfixture.WithURLParams(dbfixture.WithHeaders(dbfixture.JSONRequest(method, path, body), "X-User-ID", f.UserID), params...)
		r = r.WithContext(middleware.SetMemberContext(r.Context(), f.WorkspaceID, db.Member{}))
		return dbfixture.Call(t, fn, r)
	}
	for _, mode := range []string{"finding", "fingerprint", "resolution"} {
		for _, status := range []string{"resolved", "ignored", "open", "in_progress"} {
			t.Run(mode+"/"+status, func(t *testing.T) {
				issue := f.Issue(t, "人工处理目标")
				review := f.Task(t, agent, dbfixture.Cols{"runtime_id": runtime, "status": "completed", "context": map[string]any{"type": "quality_supervision"}})
				var generation, target int64
				f.QueryRow(t, "SELECT generation FROM supervision_material WHERE workspace_id=$1 AND kind='issue' AND source_id=$2", f.WorkspaceID, issue).Scan(&generation)
				f.QueryRow(t, "SELECT desired_version FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issue).Scan(&target)
				material := supervision.Material{Evidence: supervision.Evidence{Kind: "issue", ID: issue}, Generation: generation, Text: "original", Offset: 0, End: 8, Length: 8}
				materials, _ := json.Marshal([]supervision.Material{material})
				hash := sha256.Sum256([]byte("mapping:issue:" + issue))
				finding := f.Insert(t, "supervision_finding", dbfixture.Cols{"workspace_id": f.WorkspaceID, "issue_id": issue, "fingerprint": hex.EncodeToString(hash[:]), "batch_id": dbid.NewV7().String(), "title": "原问题", "description": "原描述", "impact": "影响", "recommendation": "核验", "confidence": "confirmed", "evidence": []any{}, "first_seen_at": dbfixture.Raw("now()-interval '1 hour'")})
				newBatch := func() string {
					return f.Insert(t, "supervision_batch", dbfixture.Cols{"workspace_id": f.WorkspaceID, "issue_id": issue, "target_version": target, "task_id": review, "status": "running", "materials": materials, "created_at": dbfixture.Raw("clock_timestamp()")})
				}
				batch := newBatch()
				t.Cleanup(func() {
					for _, sql := range []string{
						"DELETE FROM supervision_action WHERE workspace_id=$1 AND finding_id IN(SELECT id FROM supervision_finding WHERE issue_id=$2)",
						"DELETE FROM supervision_finding WHERE workspace_id=$1 AND issue_id=$2",
						"DELETE FROM supervision_batch WHERE workspace_id=$1 AND issue_id=$2",
						"DELETE FROM supervision_material WHERE workspace_id=$1 AND issue_id=$2",
						"DELETE FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2",
					} {
						if _, e := pool.Exec(ctx, sql, f.WorkspaceID, issue); e != nil {
							t.Error(e)
						}
					}
				})
				call(t, h.UpdateSupervisionFinding, http.MethodPatch, "/finding", map[string]any{"status": status, "reason": "用户的最新决定", "operation_id": dbid.NewV7().String()}, "findingId", finding).Want(200)
				result := supervision.Result{Summary: "旧批次结论", Efficiency: "clear", Correctness: "clear", Gaps: []string{}, Covered: []supervision.Evidence{material.Evidence}, Findings: []supervision.Finding{}, Resolutions: []supervision.Resolution{}}
				if mode == "resolution" {
					result.Resolutions = []supervision.Resolution{{ID: finding, Reason: "模型认为已修复", Evidence: []supervision.Evidence{material.Evidence}}}
				} else {
					id := finding
					if mode == "fingerprint" {
						id = ""
					}
					result.Findings = []supervision.Finding{{ID: id, Code: "mapping", Title: "模型旧判断", Description: "描述", Impact: "影响", Recommendation: "核验", Confidence: "confirmed", Evidence: []supervision.Evidence{material.Evidence}}}
				}
				if err = svc.Commit(ctx, f.WorkspaceID, batch, review, result); err != nil {
					t.Fatal(err)
				}
				var got string
				var human bool
				f.QueryRow(t, "SELECT status,human_resolution FROM supervision_finding WHERE id=$1", finding).Scan(&got, &human)
				if got != status || human != (status == "resolved") {
					t.Fatalf("human choice overwritten: %s/%v", got, human)
				}
				var summary, correctness, history string
				f.QueryRow(t, "SELECT summary,correctness FROM supervision_subject WHERE issue_id=$1", issue).Scan(&summary, &correctness)
				f.QueryRow(t, "SELECT result->>'summary' FROM supervision_batch WHERE id=$1", batch).Scan(&history)
				if summary == result.Summary || correctness != "uncertain" || history != result.Summary {
					t.Fatal("stale summary replaced current conclusion or history was lost")
				}
				if mode == "finding" && status == "resolved" || mode == "resolution" && status == "open" {
					fresh := newBatch()
					if err = svc.Commit(ctx, f.WorkspaceID, fresh, review, result); err != nil {
						t.Fatal(err)
					}
					f.QueryRow(t, "SELECT status,human_resolution FROM supervision_finding WHERE id=$1", finding).Scan(&got, &human)
					want := "open"
					if mode == "resolution" {
						want = "resolved"
					}
					if got != want || human {
						t.Fatal("fresh review was permanently blocked by a prior human decision")
					}
				}
			})
		}
	}
	t.Run("paused-recheck", func(t *testing.T) {
		issue := f.Issue(t, "暂停复查目标")
		t.Cleanup(func() {
			for _, table := range []string{"supervision_material", "supervision_subject"} {
				_, e := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issue)
				if e != nil {
					t.Error(e)
				}
			}
		})
		var before, after int64
		f.QueryRow(t, "SELECT generation FROM supervision_material WHERE workspace_id=$1 AND source_id=$2", f.WorkspaceID, issue).Scan(&before)
		call(t, h.CheckSupervision, http.MethodPost, "/check", map[string]any{"issue_id": issue, "full": true}).Want(409)
		f.QueryRow(t, "SELECT generation FROM supervision_material WHERE workspace_id=$1 AND source_id=$2", f.WorkspaceID, issue).Scan(&after)
		if before != after {
			t.Fatal("paused recheck consumed or reset material progress")
		}
	})
	t.Run("paused-active-investigation", func(t *testing.T) {
		issue := f.Issue(t, "暂停收尾目标")
		review := f.Task(t, agent, dbfixture.Cols{"runtime_id": runtime, "status": "running", "context": map[string]any{"type": "quality_supervision"}})
		batch := f.Insert(t, "supervision_batch", dbfixture.Cols{"workspace_id": f.WorkspaceID, "issue_id": issue, "task_id": review, "status": "running", "target_version": 1})
		f.Exec(t, "UPDATE agent_task_queue SET context=jsonb_build_object('type','quality_supervision','batch_id',$2::text) WHERE id=$1", review, batch)
		t.Cleanup(func() {
			for _, table := range []string{"supervision_investigation", "supervision_material", "supervision_subject"} {
				_, e := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issue)
				if e != nil {
					t.Error(e)
				}
			}
		})
		read := func() *dbfixture.Response {
			r := dbfixture.WithHeaders(dbfixture.JSONRequest(http.MethodGet, "/api/supervision/investigation/material?kind=issue&id="+issue+"&offset=0&reason=verify", nil), "X-Workspace-ID", f.WorkspaceID, "X-User-ID", f.UserID, "X-Actor-Source", "task_token", "X-Agent-ID", agent, "X-Task-ID", review)
			r = r.WithContext(middleware.SetMemberContext(r.Context(), f.WorkspaceID, db.Member{}))
			return dbfixture.Call(t, h.ReadSupervisionInvestigationMaterial, r)
		}
		read().Want(200)
		f.Exec(t, "UPDATE supervision_batch SET status='cancelled' WHERE id=$1", batch)
		read().Want(403)
		f.Exec(t, "UPDATE supervision_batch SET status='running' WHERE id=$1", batch)
		f.Exec(t, "UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2", f.WorkspaceID, f.UserID)
		read().Want(403)
		f.Exec(t, "UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2", f.WorkspaceID, f.UserID)
	})

}
