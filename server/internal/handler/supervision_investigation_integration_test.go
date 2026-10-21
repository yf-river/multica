package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/supervision"
	dbfixture "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/investigation"
)

func TestIntSupervisionInvestigationScopeAndPersistentBudget(t *testing.T) {
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
	f.UserID = f.User(t, "调查作用域验收", "investigation-"+stamp+"@example.test", dbfixture.Cols{"account": "investigation-" + stamp})
	f.WorkspaceID = f.Workspace(t, "调查验收", "investigation-"+stamp)
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	runtime := f.Runtime(t, "调查夹具", dbfixture.Cols{"status": "offline", "daemon_id": "investigation-" + stamp})
	agent := f.Agent(t, "调查夹具", runtime)
	f.InsertNoID(t, "supervision_config", dbfixture.Cols{"workspace_id": f.WorkspaceID, "owner_id": f.UserID, "agent_id": agent, "enabled": true, "investigation_enabled": true}, "workspace_id=$1", f.WorkspaceID)
	project := f.Project(t, "调查项目")
	issue := f.Issue(t, "调查目标", dbfixture.Cols{"project_id": project})
	directory := t.TempDir()
	resource := f.Insert(t, "project_resource", dbfixture.Cols{"project_id": project, "workspace_id": f.WorkspaceID, "resource_type": "local_directory", "label": "关联源码", "resource_ref": map[string]any{"daemon_id": "investigation-" + stamp, "local_path": directory}})
	repository := f.Insert(t, "repository", dbfixture.Cols{"workspace_id": f.WorkspaceID, "identity": "fixture-" + stamp, "config": map[string]any{}})
	f.Insert(t, "repository_location", dbfixture.Cols{"workspace_id": f.WorkspaceID, "repository_id": repository, "daemon_id": "investigation-" + stamp, "local_path": directory + "/repo"})
	repoResource := f.Insert(t, "project_resource", dbfixture.Cols{"project_id": project, "workspace_id": f.WorkspaceID, "resource_type": "gongfeng_repo", "label": "项目代码仓库", "resource_ref": map[string]any{"repository_id": repository}})
	other := f.Issue(t, "另一业务任务")
	source := f.Task(t, agent, dbfixture.Cols{"runtime_id": runtime, "issue_id": issue, "status": "completed", "completed_at": dbfixture.Raw("now()"), "work_dir": directory + "/run"})
	message := f.Insert(t, "task_message", dbfixture.Cols{"task_id": source, "seq": 1, "type": "text", "content": "mapping evidence"})
	foreign := f.Task(t, agent, dbfixture.Cols{"runtime_id": runtime, "issue_id": other, "status": "completed", "completed_at": dbfixture.Raw("now()")})
	foreignMessage := f.Insert(t, "task_message", dbfixture.Cols{"task_id": foreign, "seq": 1, "type": "text", "content": "foreign issue"})
	review := f.Task(t, agent, dbfixture.Cols{"runtime_id": runtime, "status": "running"})
	batch := f.Insert(t, "supervision_batch", dbfixture.Cols{"workspace_id": f.WorkspaceID, "issue_id": issue, "task_id": review, "target_version": 1, "status": "running"})
	f.Exec(t, "UPDATE agent_task_queue SET context=jsonb_build_object('type','quality_supervision','batch_id',$2::text) WHERE id=$1", review, batch)
	t.Cleanup(func() {
		for _, table := range []string{"attachment", "supervision_investigation", "supervision_material", "supervision_subject"} {
			_, e := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", f.WorkspaceID)
			if e != nil {
				t.Error(e)
			}
		}
	})
	h := &Handler{DB: pool, TxStarter: pool, Queries: db.New(pool), Supervision: supervision.New(pool, nil)}
	roots, err := h.supervisionRoots(ctx, f.WorkspaceID, batch, runtime)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]investigation.Root{
		"run:" + source:            {Path: directory + "/run", RunID: source, Label: "运行目录当前内容"},
		"resource:" + resource:     {Path: directory, Label: "关联源码"},
		"resource:" + repoResource: {Path: directory + "/repo", Label: "项目代码仓库"},
		"evidence:" + resource:     {Path: directory + "/.run/evidence", Label: "本任务验收材料"},
		"evidence:" + repoResource: {Path: directory + "/repo/.run/evidence", Label: "本任务验收材料"},
	}
	for _, root := range roots {
		expected, ok := wanted[root.ID]
		if !ok || root.Path != expected.Path || root.RunID != expected.RunID || root.Label != expected.Label {
			t.Fatalf("unexpected root: %+v", root)
		}
		delete(wanted, root.ID)
	}
	if len(wanted) != 0 {
		t.Fatalf("missing roots: %+v", wanted)
	}

	// 恢复失败任务不重扫已完成任务，也不清零已经读过的原文。
	f.Exec(t, "INSERT INTO supervision_subject(workspace_id,issue_id,failures,desired_version,reviewed_version) VALUES($1,$2,3,2,1),($1,$3,0,1,1) ON CONFLICT(workspace_id,issue_id) DO UPDATE SET failures=EXCLUDED.failures,desired_version=EXCLUDED.desired_version,reviewed_version=EXCLUDED.reviewed_version", f.WorkspaceID, issue, other)
	f.Exec(t, "INSERT INTO supervision_material(workspace_id,issue_id,kind,source_id,generation,reviewed_generation,reviewed_offset) VALUES($1,$2,'message',$3,1,0,4000) ON CONFLICT(workspace_id,kind,source_id) DO UPDATE SET reviewed_offset=4000", f.WorkspaceID, issue, message)
	recovery := httptest.NewRequest(http.MethodPost, "/api/supervision/check", bytes.NewBufferString(`{"retry_failed":true}`))
	recovery.Header.Set("X-User-ID", f.UserID)
	recovery = recovery.WithContext(middleware.SetMemberContext(recovery.Context(), f.WorkspaceID, db.Member{UserID: parseUUID(f.UserID), WorkspaceID: parseUUID(f.WorkspaceID), Role: "owner"}))
	dbfixture.Call(t, h.CheckSupervision, recovery).Want(202)
	var failures, offset int
	var forced, otherForced bool
	if err = pool.QueryRow(ctx, "SELECT failures,force_review,(SELECT reviewed_offset FROM supervision_material WHERE workspace_id=$1 AND source_id=$3),(SELECT force_review FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$4) FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issue, message, other).Scan(&failures, &forced, &offset, &otherForced); err != nil {
		t.Fatal(err)
	}
	if failures != 0 || !forced || offset != 4000 || otherForced {
		t.Fatalf("recovery changed unrelated progress: %d %v %d %v", failures, forced, offset, otherForced)
	}

	// 校验工具不受主动调查开关限制，也不能保存调查记录或推进游标。
	f.Exec(t, "UPDATE supervision_config SET investigation_enabled=false WHERE workspace_id=$1", f.WorkspaceID)
	validation := httptest.NewRequest(http.MethodPost, "/api/supervision/validate", bytes.NewBufferString(`{"summary":"范围已核对","efficiency":"uncertain","correctness":"uncertain","gaps":[],"covered":[],"findings":[],"resolutions":[],"read_receipts":[]}`))
	setTaskTokenActor(validation, agent, review)
	validation.Header.Set("X-User-ID", f.UserID)
	validation.Header.Set("X-Workspace-ID", f.WorkspaceID)
	validationResponse := httptest.NewRecorder()
	h.ValidateSupervisionResult(validationResponse, validation)
	if validationResponse.Code != 200 || !strings.Contains(validationResponse.Body.String(), `"valid":true`) {
		t.Fatalf("validation: %d %s", validationResponse.Code, validationResponse.Body.String())
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_investigation WHERE batch_id=$1", batch); n != 0 {
		t.Fatal("validation mutated evidence")
	}
	f.Exec(t, "UPDATE supervision_config SET investigation_enabled=true WHERE workspace_id=$1", f.WorkspaceID)
	call := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		setTaskTokenActor(r, agent, review)
		r.Header.Set("X-User-ID", f.UserID)
		r.Header.Set("X-Workspace-ID", f.WorkspaceID)
		w := httptest.NewRecorder()
		h.ReadSupervisionInvestigationMaterial(w, r)
		return w
	}
	path := "/api/supervision/investigation/material?kind=message&id=" + message + "&offset=0&reason=verify"
	if w := call(path); w.Code != 200 {
		t.Fatalf("read=%d %s", w.Code, w.Body.String())
	}
	if w := call("/api/supervision/investigation/material?kind=message&id=" + foreignMessage + "&offset=0&reason=verify"); w.Code != 404 {
		t.Fatalf("foreign=%d", w.Code)
	}

	if w := call("/api/supervision/investigation/material?kind=index&id=&offset=0&reason=inspect"); w.Code != 200 {
		t.Fatalf("index=%d %s", w.Code, w.Body.String())
	}
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "http://fixture/uploads")
	h.Storage = storage.NewLocalStorageFromEnv()
	evidence := roots[len(roots)-2]
	folder := strings.ToLower(strings.ReplaceAll(evidence.ArtifactPrefix, "-", "")) + "-acceptance"
	artifact := filepath.Join(evidence.Path, folder, "report.json")
	if err = os.MkdirAll(filepath.Dir(artifact), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(artifact, []byte(`{"checks":["passed"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	session := investigation.Session{Roots: []investigation.Root{evidence}}
	session.Save = func(ctx context.Context, request investigation.Request, result *investigation.Result) error {
		body, _ := json.Marshal(map[string]any{"request": request, "result": result})
		req := httptest.NewRequest(http.MethodPost, "/api/supervision/investigation", bytes.NewReader(body))
		setTaskTokenActor(req, agent, review)
		req.Header.Set("X-User-ID", f.UserID)
		req.Header.Set("X-Workspace-ID", f.WorkspaceID)
		reply := httptest.NewRecorder()
		h.SaveSupervisionInvestigation(reply, req)
		if reply.Code != 200 {
			return fmt.Errorf("save=%d %s", reply.Code, reply.Body.String())
		}
		return nil
	}
	if _, err = session.Call(ctx, "supervision_read", investigation.Request{Root: evidence.ID, Path: folder + "/report.json", Reason: "核对真实验收报告"}); err != nil {
		t.Fatal(err)
	}
	var attachment string
	f.QueryRow(t, "SELECT id::text FROM attachment WHERE issue_id=$1", issue).Scan(&attachment)
	if attachment == "" {
		t.Fatal("evidence not archived in existing attachment storage")
	}
	if err = os.RemoveAll(evidence.Path); err != nil {
		t.Fatal(err)
	}
	if w := call("/api/supervision/investigation/material?kind=attachments&id=&offset=0&reason=locate"); w.Code != 200 || !strings.Contains(w.Body.String(), attachment) {
		t.Fatalf("attachment index=%d %s", w.Code, w.Body.String())
	}
	if w := call("/api/supervision/investigation/material?kind=attachment&id=" + attachment + "&offset=0&reason=recheck"); w.Code != 200 || !strings.Contains(w.Body.String(), "passed") {
		t.Fatalf("archived read=%d %s", w.Code, w.Body.String())
	}
	f.Exec(t, "UPDATE supervision_batch SET status='cancelled' WHERE id=$1", batch)
	if w := call(path); w.Code != 403 {
		t.Fatalf("cancelled=%d", w.Code)
	}
	f.Exec(t, "UPDATE supervision_batch SET status='running' WHERE id=$1", batch)
	f.Exec(t, "UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2", f.WorkspaceID, f.UserID)
	if w := call(path); w.Code != 403 {
		t.Fatalf("lost owner=%d", w.Code)
	}
	f.Exec(t, "UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2", f.WorkspaceID, f.UserID)
	f.Exec(t, "INSERT INTO supervision_investigation(workspace_id,issue_id,batch_id,task_id,request,result) SELECT $1,$2,$3,$4,'{}','{\"text\":\"\"}' FROM generate_series(1,39)", f.WorkspaceID, issue, batch, review)
	if w := call(path); w.Code != 409 {
		t.Fatalf("persistent budget=%d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodPost, "/api/supervision/investigation", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()
	h.SaveSupervisionInvestigation(w, r)
	if w.Code != 403 {
		t.Fatalf("human headers accepted=%d", w.Code)
	}
}
