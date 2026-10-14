package handler

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestSkillAssetRoundTripAndAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", dir)
	h := *testHandler
	h.Storage = storage.NewLocalStorageFromEnv()
	sk := dbfx.Insert(t, "skill", testutil.Cols{"workspace_id": testWorkspaceID, "created_by": testUserID, "name": t.Name(), "content": "original"})
	dbfx.Cleanup(t, "DELETE FROM skill_asset WHERE workspace_id=$1", testWorkspaceID)
	upload := func(data []byte, want int) SkillFileResponse {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		f, err := mw.CreateFormFile("file", "diagram.png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(data)
		_ = mw.WriteField("path", "assets/diagram.png")
		_ = mw.Close()
		req := newRequest("POST", "/api/skills/"+sk+"/files/upload", nil)
		req.Body = io.NopCloser(&body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req = withURLParam(req, "id", sk)
		var result SkillFileResponse
		response := testutil.Call(t, h.UploadSkillFile, req).Want(want)
		if want == 200 {
			response.JSON(&result)
		}
		return result
	}
	original := []byte("\x89PNG\r\n\x1a\n\xff\x00asset")
	f := upload(original, 200)
	if f.Kind != "binary" || f.Content != "" || f.SHA256 != contentHash(string(original)) || f.ContentType != "image/png" {
		t.Fatalf("bad metadata: %+v", f)
	}
	get := func(skillID, fileID string, want int) *testutil.Response {
		req := newRequest("GET", "/api/skills/"+skillID+"/files/"+fileID+"/content", nil)
		req = testutil.WithURLParams(req, "id", skillID, "fileId", fileID)
		return testutil.Call(t, h.GetSkillFileContent, req).Want(want)
	}
	response := get(sk, f.ID, 200)
	if !bytes.Equal(response.Body.Bytes(), original) {
		t.Fatal("binary bytes changed")
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	other := dbfx.Insert(t, "skill", testutil.Cols{"workspace_id": testWorkspaceID, "created_by": testUserID, "name": t.Name() + "-other"})
	get(other, f.ID, 404)
	req := newRequest("PUT", "/api/skills/"+sk, map[string]any{"files": []CreateSkillFileRequest{{Path: f.Path, Content: ""}}})
	req = withURLParam(req, "id", sk)
	testutil.Call(t, h.UpdateSkill, req).Want(400)
	get(sk, f.ID, 200)
	// 存储不可写时，旧文件和元数据必须完整保留。
	h.Storage = nil
	upload([]byte("new"), 400)
	h.Storage = storage.NewLocalStorageFromEnv()
	get(sk, f.ID, 200)
	row, err := h.Queries.GetSkillFile(context.Background(), parseUUID(f.ID))
	if err != nil {
		t.Fatal(err)
	}
	raw := newRequest("GET", "/uploads/"+row.StorageKey, nil)
	testutil.Call(t, h.ServeLocalUpload, raw).Want(404)
	if err = os.WriteFile(filepath.Join(dir, row.StorageKey), []byte("corrupt"), 0644); err != nil {
		t.Fatal(err)
	}
	get(sk, f.ID, 503)
	// 替换后更新引用；旧对象保留至清理器确认不再被使用。
	replacement := upload(original, 200)
	get(sk, replacement.ID, 200)
	row2, err := h.Queries.GetSkillFile(context.Background(), parseUUID(replacement.ID))
	if err != nil {
		t.Fatal(err)
	}
	if row.StorageKey == row2.StorageKey {
		t.Fatal("replacement reused mutable key")
	}
	unused, err := h.Queries.ListUnusedSkillAssets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range unused {
		if key == row.StorageKey {
			t.Fatal("fresh object eligible for premature cleanup")
		}
	}
	dbfx.Exec(t, "UPDATE skill_asset SET created_at=now()-interval '2 days' WHERE storage_key=$1", row.StorageKey)
	unused, err = h.Queries.ListUnusedSkillAssets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, key := range unused {
		if key == row.StorageKey {
			found = true
		}
		if key == row2.StorageKey {
			t.Fatal("live object marked for cleanup")
		}
	}
	if !found {
		t.Fatal("orphan not scheduled for cleanup")
	}
	if err = h.cleanupSkillAssets(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, row.StorageKey)); !os.IsNotExist(err) {
		t.Fatalf("orphan still exists after cleanup: %v", err)
	}
	get(sk, replacement.ID, 200)
	if err = h.Queries.DeleteSkillFile(context.Background(), parseUUID(replacement.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestSkillAssetTaskScopeAndOldDaemon(t *testing.T) {
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	h := *testHandler
	h.Storage = storage.NewLocalStorageFromEnv()
	sk := dbfx.Insert(t, "skill", testutil.Cols{"workspace_id": testWorkspaceID, "created_by": testUserID, "name": t.Name(), "content": "main"})
	files := []CreateSkillFileRequest{{Path: "image.png", data: []byte("\x89PNG\x00\xff")}}
	ctx := context.Background()
	if err := h.prepareSkillFiles(ctx, parseUUID(testWorkspaceID), parseUUID(sk), files); err != nil {
		t.Fatal(err)
	}
	file, err := upsertPreparedSkillFile(ctx, h.Queries, parseUUID(sk), files[0])
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Cleanup(t, "DELETE FROM skill_asset WHERE workspace_id=$1", testWorkspaceID)
	rt := dbfx.Runtime(t, t.Name())
	agent := dbfx.Agent(t, t.Name(), rt)
	chat := dbfx.ChatSession(t, agent)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": rt, "status": "dispatched", "chat_session_id": chat})
	get := func(hash string, want int) {
		req := newRequest("GET", "/file?sha256="+hash, nil)
		req = testutil.WithURLParams(req, "runtimeId", rt, "taskId", task, "fileId", uuidToString(file.ID))
		testutil.Call(t, h.GetTaskSkillFileContent, req).Want(want)
	}
	get("sha256:"+file.Sha256, 404)
	dbfx.InsertNoID(t, "agent_skill", testutil.Cols{"agent_id": agent, "skill_id": sk}, "agent_id=$1 AND skill_id=$2", agent, sk)
	get("sha256:"+file.Sha256, 200)
	get("wrong-version", 409)
	resolve := func(capability string, want int) {
		req := newRequest("POST", "/resolve", map[string]any{"skills": []map[string]string{{"id": sk, "source": "workspace", "hash": "outdated"}}})
		req = testutil.WithURLParams(req, "runtimeId", rt, "taskId", task)
		req.Header.Set("X-Client-Capabilities", capability)
		testutil.Call(t, h.ResolveTaskSkillBundles, req).Want(want)
	}
	resolve("", 409)
	resolve("skill-assets-v1", 200)
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='running' WHERE id=$1", task)
	get("sha256:"+file.Sha256, 409)
}
