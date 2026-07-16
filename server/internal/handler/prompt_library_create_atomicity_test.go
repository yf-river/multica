package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestPromptLibraryCreateCompletionFailuresRollbackDomainWrites(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM prompt_library_item WHERE workspace_id = $1`, testWorkspaceID)
	})

	itemKey := uuid.NewString()
	itemName := "atomic-item-" + uuid.NewString()
	installResourceCreateCompletionFailure(t, resourceTypePromptLibraryItem, itemKey)
	itemReq := newRequest(http.MethodPost, "/api/prompt-library", map[string]any{
		"name": itemName, "content": "must roll back",
	})
	itemReq.Header.Set("Idempotency-Key", itemKey)
	itemW := httptest.NewRecorder()
	testHandler.CreatePromptLibraryItem(itemW, itemReq)
	if itemW.Code != http.StatusInternalServerError {
		t.Fatalf("item completion failure = %d %s, want 500", itemW.Code, itemW.Body.String())
	}
	var itemCount, itemRequestCount int
	if err := testPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM prompt_library_item WHERE workspace_id = $1 AND name = $2),
		(SELECT count(*) FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'prompt_library_item' AND idempotency_key = $3)
	`, testWorkspaceID, itemName, itemKey).Scan(&itemCount, &itemRequestCount); err != nil {
		t.Fatal(err)
	}
	if itemCount != 0 || itemRequestCount != 0 {
		t.Fatalf("failed item left writes: items=%d requests=%d", itemCount, itemRequestCount)
	}

	createW := httptest.NewRecorder()
	testHandler.CreatePromptLibraryItem(createW, newRequest(http.MethodPost, "/api/prompt-library", map[string]any{
		"name": "atomic-version-" + uuid.NewString(), "content": "version one",
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create fixture = %d %s", createW.Code, createW.Body.String())
	}
	var item PromptLibraryItemResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	versionKey := uuid.NewString()
	installResourceCreateCompletionFailure(t, resourceTypePromptLibraryVersion, versionKey)
	versionReq := withURLParam(newRequest(http.MethodPost, "/api/prompt-library/"+item.ID+"/versions", map[string]any{
		"content": "version two", "change_note": "must roll back",
	}), "id", item.ID)
	versionReq.Header.Set("Idempotency-Key", versionKey)
	versionW := httptest.NewRecorder()
	testHandler.CreatePromptLibraryVersion(versionW, versionReq)
	if versionW.Code != http.StatusInternalServerError {
		t.Fatalf("version completion failure = %d %s, want 500", versionW.Code, versionW.Body.String())
	}
	var storedContent string
	var versionCount, versionRequestCount int
	if err := testPool.QueryRow(ctx, `SELECT
		(SELECT content FROM prompt_library_item WHERE id = $1),
		(SELECT count(*) FROM prompt_library_version WHERE prompt_id = $1),
		(SELECT count(*) FROM resource_create_request WHERE resource_type = 'prompt_library_version' AND idempotency_key = $2)
	`, item.ID, versionKey).Scan(&storedContent, &versionCount, &versionRequestCount); err != nil {
		t.Fatal(err)
	}
	if storedContent != "version one" || versionCount != 1 || versionRequestCount != 0 {
		t.Fatalf("failed version left writes: content=%q versions=%d requests=%d", storedContent, versionCount, versionRequestCount)
	}
}
