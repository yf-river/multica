package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func createHandlerTestChatSession(t *testing.T, agentID string) string {
	t.Helper()

	var sessionID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, "Handler Test Chat Session").Scan(&sessionID); err != nil {
		t.Fatalf("failed to create handler test chat session: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})
	return sessionID
}

type mockStorage struct {
	mu                  sync.Mutex
	files               map[string][]byte
	presignCalls        []string
	presignDispositions []string
	deleteErr           error
}

func (m *mockStorage) Upload(_ context.Context, key string, data []byte, _ string, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[key] = append([]byte(nil), data...)
	return fmt.Sprintf("https://cdn.example.com/%s", key), nil
}

func (m *mockStorage) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.files, key)
	return nil
}
func (m *mockStorage) KeyFromURL(rawURL string) string {
	for _, prefix := range []string{
		"https://cdn.example.com/",
		"http://rustfs:9000/test-bucket/",
		"https://s3.example.com/test-bucket/",
	} {
		if strings.HasPrefix(rawURL, prefix) {
			return strings.TrimPrefix(rawURL, prefix)
		}
	}
	return rawURL
}
func (m *mockStorage) CdnDomain() string { return "cdn.example.com" }

type mockStorageNoCdn struct{ mockStorage }

func (m *mockStorageNoCdn) CdnDomain() string { return "" }

func newUploadRequest(t *testing.T, filename string, data []byte, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload-file", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("Idempotency-Key", uuid.NewString())
	return req
}

func newIdempotentUploadRequest(t *testing.T, key, filename string, data []byte) *http.Request {
	t.Helper()
	req := newUploadRequest(t, filename, data, nil)
	req.Header.Set("Idempotency-Key", key)
	return req
}
func (m *mockStorage) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if data, ok := m.files[key]; ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return nil, fmt.Errorf("mockStorage GetReader: key not found: %q", key)
}
func (m *mockStorage) PresignGetWithContentDisposition(_ context.Context, key string, _ time.Duration, contentDisposition string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.presignCalls = append(m.presignCalls, key)
	m.presignDispositions = append(m.presignDispositions, contentDisposition)
	u := url.URL{
		Scheme: "https",
		Host:   "signed.example.com",
		Path:   "/" + key,
	}
	q := u.Query()
	q.Set("X-Amz-Signature", "mock")
	if contentDisposition != "" {
		q.Set("response-content-disposition", contentDisposition)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func (m *mockStorage) put(key string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[key] = append([]byte(nil), data...)
}

func TestPersistUploadedAttachmentRollsBackObjectOnDatabaseFailure(t *testing.T) {
	store := &mockStorage{}
	store.put("workspaces/ws-1/file.txt", []byte("private"))

	_, err := persistUploadedAttachment(
		context.Background(),
		store,
		"workspaces/ws-1/file.txt",
		func() (db.Attachment, error) {
			return db.Attachment{}, errors.New("database unavailable")
		},
	)

	if err == nil {
		t.Fatal("expected persistence failure")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.files["workspaces/ws-1/file.txt"]; exists {
		t.Fatal("uploaded object was not rolled back")
	}
}

func TestPersistUploadedAttachmentReportsRollbackFailure(t *testing.T) {
	store := &mockStorage{deleteErr: errors.New("object store unavailable")}

	_, err := persistUploadedAttachment(
		context.Background(),
		store,
		"workspaces/ws-1/file.txt",
		func() (db.Attachment, error) {
			return db.Attachment{}, errors.New("database unavailable")
		},
	)

	if err == nil || !strings.Contains(err.Error(), "rollback uploaded object") {
		t.Fatalf("expected joined rollback error, got %v", err)
	}
}

func TestUploadFileRequiresWorkspaceBeforeWritingObject(t *testing.T) {
	origStorage := testHandler.Storage
	store := &mockStorage{}
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	req := newUploadRequest(t, "orphan.txt", []byte("must not be uploaded"), nil)
	req.Header.Del("X-Workspace-ID")
	w := httptest.NewRecorder()

	testHandler.UploadFile(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("UploadFile without workspace: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "workspace") {
		t.Fatalf("UploadFile without workspace returned unclear error: %s", w.Body.String())
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.files) != 0 {
		t.Fatalf("UploadFile without workspace wrote %d orphaned objects", len(store.files))
	}
}

func TestUploadFileForeignWorkspace(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	foreignWorkspaceID := "00000000-0000-0000-0000-000000000099"
	req := newUploadRequest(t, "test.txt", []byte("hello world"), nil)
	req.Header.Set("X-Workspace-ID", foreignWorkspaceID)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("UploadFile with foreign workspace: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUploadFileReplaysCommittedAttachment(t *testing.T) {
	origStorage := testHandler.Storage
	store := &mockStorage{}
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	key := uuid.NewString()
	call := func() (*httptest.ResponseRecorder, AttachmentResponse) {
		w := httptest.NewRecorder()
		testHandler.UploadFile(w, newIdempotentUploadRequest(t, key, "replay.txt", []byte("same payload")))
		var response AttachmentResponse
		if w.Code == http.StatusOK {
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("decode upload response: %v", err)
			}
		}
		return w, response
	}

	first, created := call()
	if first.Code != http.StatusOK {
		t.Fatalf("first upload: expected 200, got %d: %s", first.Code, first.Body.String())
	}
	replayed, replayBody := call()
	if replayed.Code != http.StatusOK {
		t.Fatalf("replay: expected 200, got %d: %s", replayed.Code, replayed.Body.String())
	}
	if replayBody.ID != created.ID || created.ID != key {
		t.Fatalf("upload identity diverged: key=%s first=%s replay=%s", key, created.ID, replayBody.ID)
	}

	store.mu.Lock()
	objects := len(store.files)
	store.mu.Unlock()
	if objects != 1 {
		t.Fatalf("expected one object after replay, got %d", objects)
	}
	var attachments int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM attachment WHERE id = $1`, key).Scan(&attachments); err != nil {
		t.Fatalf("count attachments: %v", err)
	}
	if attachments != 1 {
		t.Fatalf("expected one attachment after replay, got %d", attachments)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM attachment WHERE id = $1`, key)
		mustExec(t, context.Background(), `DELETE FROM resource_create_request WHERE resource_type = 'attachment' AND idempotency_key = $1`, key)
	})
}

func TestUploadFileRejectsSameKeyWithDifferentContent(t *testing.T) {
	origStorage := testHandler.Storage
	store := &mockStorage{}
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	key := uuid.NewString()
	first := httptest.NewRecorder()
	testHandler.UploadFile(first, newIdempotentUploadRequest(t, key, "conflict.txt", []byte("original")))
	if first.Code != http.StatusOK {
		t.Fatalf("first upload: expected 200, got %d: %s", first.Code, first.Body.String())
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM attachment WHERE id = $1`, key)
		mustExec(t, context.Background(), `DELETE FROM resource_create_request WHERE resource_type = 'attachment' AND idempotency_key = $1`, key)
	})

	conflict := httptest.NewRecorder()
	testHandler.UploadFile(conflict, newIdempotentUploadRequest(t, key, "conflict.txt", []byte("changed")))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting upload: expected 409, got %d: %s", conflict.Code, conflict.Body.String())
	}
	objectKey := "workspaces/" + testWorkspaceID + "/" + key + ".txt"
	store.mu.Lock()
	stored := string(store.files[objectKey])
	store.mu.Unlock()
	if stored != "original" {
		t.Fatalf("conflicting request overwrote object: %q", stored)
	}
}

func TestUploadFileConcurrentSameKeyConverges(t *testing.T) {
	origStorage := testHandler.Storage
	store := &mockStorage{}
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	const callers = 8
	key := uuid.NewString()
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM attachment WHERE id = $1`, key)
		mustExec(t, context.Background(), `DELETE FROM resource_create_request WHERE resource_type = 'attachment' AND idempotency_key = $1`, key)
	})
	type result struct {
		code int
		id   string
		body string
	}
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			testHandler.UploadFile(w, newIdempotentUploadRequest(t, key, "concurrent.txt", []byte("one payload")))
			var response AttachmentResponse
			if w.Code == http.StatusOK {
				_ = json.NewDecoder(w.Body).Decode(&response)
			}
			results <- result{code: w.Code, id: response.ID, body: w.Body.String()}
		}()
	}
	wg.Wait()
	close(results)

	for got := range results {
		if got.code != http.StatusOK || got.id != key {
			t.Fatalf("concurrent upload diverged: code=%d id=%s body=%s", got.code, got.id, got.body)
		}
	}
	var attachments int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM attachment WHERE id = $1`, key).Scan(&attachments); err != nil {
		t.Fatalf("count attachments: %v", err)
	}
	store.mu.Lock()
	objects := len(store.files)
	store.mu.Unlock()
	if attachments != 1 || objects != 1 {
		t.Fatalf("expected one attachment/object, got attachments=%d objects=%d", attachments, objects)
	}
}

func TestUploadFileResolvesWorkspaceViaSlugHeader(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	req := newUploadRequest(t, "slug-upload.txt", []byte("hello via slug"), nil)
	req.Header.Del("X-Workspace-ID")
	req.Header.Set("X-Workspace-Slug", handlerTestWorkspaceSlug)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UploadFile with slug header: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp AttachmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, w.Body.String())
	}
	if resp.ID == "" || resp.WorkspaceID != testWorkspaceID {
		t.Fatalf("attachment identity = %q/%q, want non-empty/%q", resp.ID, resp.WorkspaceID, testWorkspaceID)
	}

	// Verify the row actually exists in the database.
	var count int
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM attachment WHERE workspace_id = $1 AND filename = $2`,
		testWorkspaceID,
		"slug-upload.txt",
	).Scan(&count); err != nil {
		t.Fatalf("query attachment count: %v", err)
	}
	if count != 1 {
		t.Fatalf("attachment row count: want 1, got %d", count)
	}

	// Clean up so reruns don't accumulate rows.
	if _, err := testPool.Exec(
		context.Background(),
		`DELETE FROM attachment WHERE workspace_id = $1 AND filename = $2`,
		testWorkspaceID,
		"slug-upload.txt",
	); err != nil {
		t.Fatalf("cleanup attachment: %v", err)
	}
}

func TestUploadFileResolvesWorkspaceViaIDHeader(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	req := newUploadRequest(t, "uuid-upload.txt", []byte("hello via uuid"), nil)

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UploadFile with UUID header: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Clean up.
	if _, err := testPool.Exec(
		context.Background(),
		`DELETE FROM attachment WHERE workspace_id = $1 AND filename = $2`,
		testWorkspaceID,
		"uuid-upload.txt",
	); err != nil {
		t.Fatalf("cleanup attachment: %v", err)
	}
}

func TestUploadFile_AttachesToChatSession(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	agentID := createHandlerTestAgent(t, "ChatUploadAgent", nil)
	sessionID := createHandlerTestChatSession(t, agentID)

	req := newUploadRequest(t, "chat-upload.png", []byte("\x89PNG\r\n\x1a\nrest-of-bytes"), map[string]string{
		"chat_session_id": sessionID,
	})

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UploadFile with chat_session_id: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp AttachmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, w.Body.String())
	}
	if resp.ChatSessionID == nil || *resp.ChatSessionID != sessionID {
		t.Fatalf("chat_session_id in response: want %s, got %v", sessionID, resp.ChatSessionID)
	}
	if resp.ChatMessageID != nil {
		t.Fatalf("chat_message_id should be NULL before send, got %v", resp.ChatMessageID)
	}
	if resp.IssueID != nil || resp.CommentID != nil {
		t.Fatalf("issue_id/comment_id should be NULL for chat-only upload: %+v", resp)
	}
	if resp.URL == "" {
		t.Fatal("expected non-empty url")
	}

	// Verify the DB row directly.
	var dbSession, dbMessage *string
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT chat_session_id::text, chat_message_id::text FROM attachment WHERE id = $1`,
		resp.ID,
	).Scan(&dbSession, &dbMessage); err != nil {
		t.Fatalf("query attachment row: %v", err)
	}
	if dbSession == nil || *dbSession != sessionID {
		t.Fatalf("DB chat_session_id mismatch: want %s, got %v", sessionID, dbSession)
	}
	if dbMessage != nil {
		t.Fatalf("DB chat_message_id should be NULL, got %v", dbMessage)
	}

	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM attachment WHERE id = $1`, resp.ID)
	})
}

func TestUploadFile_RejectsForeignChatSession(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	req := newUploadRequest(t, "evil.txt", []byte("payload"), map[string]string{
		"chat_session_id": "00000000-0000-0000-0000-0000deadbeef",
	})

	w := httptest.NewRecorder()
	testHandler.UploadFile(w, req)
	if w.Code != http.StatusNotFound && w.Code != http.StatusForbidden && w.Code != http.StatusBadRequest {
		t.Fatalf("UploadFile with unknown chat_session_id: expected 4xx, got %d: %s", w.Code, w.Body.String())
	}
}

func seedPreviewAttachment(t *testing.T, store *mockStorage, key, filename, contentType string, body []byte) string {
	t.Helper()
	// Register the body so GetReader can find it via KeyFromURL → key.
	url, err := store.Upload(context.Background(), key, body, contentType, filename)
	if err != nil {
		t.Fatalf("seed Upload: %v", err)
	}

	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO attachment (workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		VALUES ($1, 'member', $2, $3, $4, $5, $6)
		RETURNING id::text
	`, testWorkspaceID, testUserID, filename, url, contentType, len(body)).Scan(&id); err != nil {
		t.Fatalf("seed attachment row: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM attachment WHERE id = $1`, id)
	})
	return id
}

func seedAttachmentURL(t *testing.T, rawURL, filename, contentType string, sizeBytes int64) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO attachment (workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		VALUES ($1, 'member', $2, $3, $4, $5, $6)
		RETURNING id::text
	`, testWorkspaceID, testUserID, filename, rawURL, contentType, sizeBytes).Scan(&id); err != nil {
		t.Fatalf("seed attachment row: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM attachment WHERE id = $1`, id)
	})
	return id
}

func attachmentResponseForURL(t *testing.T, rawURL, filename string) (string, AttachmentResponse) {
	t.Helper()
	id := seedAttachmentURL(t, rawURL, filename, "image/png", 1)
	att, err := testHandler.Queries.GetAttachment(context.Background(), db.GetAttachmentParams{
		ID:          parseUUID(id),
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	return id, testHandler.attachmentToResponse(att)
}

func useAttachmentDownloadConfig(t *testing.T, store storage.Storage, mode string, signer *auth.CloudFrontSigner) {
	t.Helper()
	origStorage, origCfg, origSigner := testHandler.Storage, testHandler.cfg, testHandler.CFSigner
	testHandler.Storage = store
	testHandler.cfg.AttachmentDownloadMode = mode
	testHandler.CFSigner = signer
	t.Cleanup(func() {
		testHandler.Storage, testHandler.cfg, testHandler.CFSigner = origStorage, origCfg, origSigner
	})
}

func useAttachmentResponseConfig(t *testing.T, store storage.Storage, publicURL string, signer *auth.CloudFrontSigner) {
	t.Helper()
	origStorage, origCfg, origSigner := testHandler.Storage, testHandler.cfg, testHandler.CFSigner
	testHandler.Storage = store
	testHandler.cfg.PublicURL = publicURL
	testHandler.CFSigner = signer
	t.Cleanup(func() {
		testHandler.Storage, testHandler.cfg, testHandler.CFSigner = origStorage, origCfg, origSigner
	})
}

func newPreviewRequest(t *testing.T, attachmentID, workspaceID string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/attachments/"+attachmentID+"/content", nil)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", attachmentID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req, httptest.NewRecorder()
}

func newDownloadRequest(t *testing.T, attachmentID, workspaceID string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/attachments/"+attachmentID+"/download", nil)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", attachmentID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req, httptest.NewRecorder()
}

func newDownloadRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/api/attachments/{id}/download", testHandler.DownloadAttachment)
	return r
}

func testCloudFrontSigner(t *testing.T) *auth.CloudFrontSigner {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CloudFront test key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	t.Setenv("CLOUDFRONT_KEY_PAIR_ID", "KTEST")
	t.Setenv("CLOUDFRONT_DOMAIN", "static.example.test")
	t.Setenv("COOKIE_DOMAIN", ".example.test")
	t.Setenv("CLOUDFRONT_PRIVATE_KEY", base64.StdEncoding.EncodeToString(pemBytes))
	t.Setenv("CLOUDFRONT_PRIVATE_KEY_SECRET", "")
	signer, err := auth.NewCloudFrontSignerFromEnv()
	if err != nil {
		t.Fatalf("create CloudFront signer: %v", err)
	}
	return signer
}

func TestAttachmentToResponse_NonCloudFrontUsesDownloadEndpoint(t *testing.T) {
	origSigner := testHandler.CFSigner
	testHandler.CFSigner = nil
	t.Cleanup(func() { testHandler.CFSigner = origSigner })

	id := seedAttachmentURL(t, "http://rustfs:9000/test-bucket/private.txt", "private.txt", "text/plain", 5)
	att, err := testHandler.Queries.GetAttachment(context.Background(), db.GetAttachmentParams{
		ID:          parseUUID(id),
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}

	resp := testHandler.attachmentToResponse(att)
	if resp.URL != "http://rustfs:9000/test-bucket/private.txt" {
		t.Fatalf("stored url changed: %q", resp.URL)
	}
	if resp.DownloadURL != "/api/attachments/"+id+"/download" {
		t.Fatalf("download_url = %q, want unified endpoint", resp.DownloadURL)
	}
}

func TestDownloadAttachment_CloudFrontRedirectSignsAttachmentDisposition(t *testing.T) {
	useAttachmentDownloadConfig(t, &mockStorage{}, "cloudfront", testCloudFrontSigner(t))

	id := seedAttachmentURL(t, "https://static.example.test/downloads/cloudfront.md", "cloud front.md", "text/markdown", 10)

	req, w := newDownloadRequest(t, id, testWorkspaceID)
	testHandler.DownloadAttachment(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if got := parsed.Query().Get("response-content-disposition"); got != `attachment; filename="cloud front.md"` {
		t.Fatalf("response-content-disposition = %q", got)
	}
	if got := parsed.Query().Get("Key-Pair-Id"); got != "KTEST" {
		t.Fatalf("Key-Pair-Id = %q", got)
	}
}

func TestDownloadAttachment_BareNavigationServesMember(t *testing.T) {
	store := &mockStorage{}
	useAttachmentDownloadConfig(t, store, "proxy", nil)
	key := "downloads/bare-nav.txt"
	body := []byte("download body")
	store.put(key, body)
	id := seedAttachmentURL(t, "https://s3.example.com/test-bucket/"+key, "bare-nav.txt", "text/plain", int64(len(body)))

	for name, query := range map[string]string{
		"without workspace query": "",
		"with workspace slug":     "?workspace_slug=" + url.QueryEscape(handlerTestWorkspaceSlug),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/attachments/"+id+"/download"+query, nil)
			req.Header.Set("X-User-ID", testUserID)
			w := httptest.NewRecorder()
			newDownloadRouter().ServeHTTP(w, req)
			if w.Code != http.StatusOK || w.Body.String() != string(body) {
				t.Fatalf("status/body = %d/%q, want 200/%q", w.Code, w.Body.String(), body)
			}
			if req.Header.Get("X-Workspace-ID") != "" || req.Header.Get("X-Workspace-Slug") != "" {
				t.Fatal("browser navigation must not set custom workspace headers")
			}
		})
	}
}

func TestDownloadAttachment_BareNavigationDeniesNonMemberWith404(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	store := &mockStorage{}
	useAttachmentDownloadConfig(t, store, "proxy", nil)

	// Seed an attachment that lives in a workspace testUserID is NOT
	// a member of. The workspace row has to exist so the FK on
	// attachment.workspace_id resolves; we tear both down on
	// cleanup.
	ctx := context.Background()
	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Bare-Nav Foreign', 'bare-nav-foreign', '', 'BNF')
		RETURNING id::text
	`).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID) })

	key := "downloads/bare-nav-foreign.txt"
	store.put(key, []byte("foreign-body"))
	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO attachment (workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		VALUES ($1, 'member', $2, $3, $4, $5, $6)
		RETURNING id::text
	`, foreignWorkspaceID, testUserID, "foreign.txt", "https://s3.example.com/test-bucket/"+key, "text/plain", 12).Scan(&id); err != nil {
		t.Fatalf("seed foreign attachment: %v", err)
	}
	t.Cleanup(func() { mustExec(t, ctx, `DELETE FROM attachment WHERE id = $1`, id) })

	req := httptest.NewRequest("GET", "/api/attachments/"+id+"/download", nil)
	req.Header.Set("X-User-ID", testUserID)
	w := httptest.NewRecorder()

	newDownloadRouter().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for non-member; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "foreign-body") {
		t.Fatalf("response body leaked file contents: %q", w.Body.String())
	}
}

func TestDownloadAttachmentClientCanceledReturns499(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	attachmentID := "11111111-1111-4111-8111-111111111111"
	req := newRequest(http.MethodGet, "/api/attachments/"+attachmentID+"/download", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	req = withURLParam(req, "id", attachmentID)

	w := httptest.NewRecorder()
	testHandler.DownloadAttachment(w, req)
	if w.Code != 499 {
		t.Fatalf("expected 499 for canceled attachment read, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDownloadAttachment_AutoInternalEndpointProxies(t *testing.T) {
	store := &mockStorage{}
	useAttachmentDownloadConfig(t, store, "auto", nil)

	key := "downloads/proxy-private.txt"
	body := []byte("private object")
	store.put(key, body)
	id := seedAttachmentURL(t, "http://rustfs:9000/test-bucket/"+key, "report.txt", "text/plain", int64(len(body)))

	req, w := newDownloadRequest(t, id, testWorkspaceID)
	testHandler.DownloadAttachment(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != string(body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if got := w.Header().Get("Location"); got != "" {
		t.Fatalf("Location should be empty for proxy download, got %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="report.txt"` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if len(store.presignCalls) != 0 {
		t.Fatalf("internal endpoint should not presign, calls=%v", store.presignCalls)
	}
}

func TestDownloadAttachment_AutoPublicEndpointPresigns(t *testing.T) {
	store := &mockStorage{}
	useAttachmentDownloadConfig(t, store, "auto", nil)

	key := "downloads/public-private.txt"
	id := seedAttachmentURL(t, "https://s3.example.com/test-bucket/"+key, "public.txt", "text/plain", 10)

	req, w := newDownloadRequest(t, id, testWorkspaceID)
	testHandler.DownloadAttachment(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "X-Amz-Signature=mock") {
		t.Fatalf("Location = %q, want fake S3 signature", loc)
	}
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if got := parsed.Query().Get("response-content-disposition"); got != `attachment; filename="public.txt"` {
		t.Fatalf("response-content-disposition = %q", got)
	}
	if len(store.presignCalls) != 1 || store.presignCalls[0] != key {
		t.Fatalf("presign calls = %v, want [%s]", store.presignCalls, key)
	}
	if len(store.presignDispositions) != 1 || store.presignDispositions[0] != `attachment; filename="public.txt"` {
		t.Fatalf("presign dispositions = %v", store.presignDispositions)
	}
}

func TestDownloadAttachment_ExplicitProxyStreamsPublicEndpoint(t *testing.T) {
	store := &mockStorage{}
	useAttachmentDownloadConfig(t, store, "proxy", nil)

	key := "downloads/forced-proxy.png"
	body := []byte("\x89PNG\r\n\x1a\nimage")
	store.put(key, body)
	id := seedAttachmentURL(t, "https://s3.example.com/test-bucket/"+key, "image.png", "image/png", int64(len(body)))

	req, w := newDownloadRequest(t, id, testWorkspaceID)
	testHandler.DownloadAttachment(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.Bytes(); !bytes.Equal(got, body) {
		t.Fatalf("body mismatch: got %q want %q", got, body)
	}
	if got := w.Header().Get("Content-Disposition"); got != `inline; filename="image.png"` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if len(store.presignCalls) != 0 {
		t.Fatalf("forced proxy should not presign, calls=%v", store.presignCalls)
	}
}

func TestShouldProxyAttachmentURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"http://rustfs:9000/test-bucket/file.txt", true},
		{"http://localhost:9000/test-bucket/file.txt", true},
		{"http://127.0.0.1:9000/test-bucket/file.txt", true},
		{"http://10.0.2.15/test-bucket/file.txt", true},
		{"https://minio.internal/test-bucket/file.txt", true},
		{"/uploads/workspaces/abc/file.txt", true},
		{"https://s3.example.com/test-bucket/file.txt", false},
		{"https://bucket.s3.us-east-1.amazonaws.com/file.txt", false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := shouldProxyAttachmentURL(tc.raw); got != tc.want {
				t.Fatalf("shouldProxyAttachmentURL(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestGetAttachmentContent_HappyPath_Markdown(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	body := []byte("# heading\n\nbody text\n")
	id := seedPreviewAttachment(t, store, "preview-md-key.md", "preview.md", "text/markdown", body)

	req, w := newPreviewRequest(t, id, testWorkspaceID)
	testHandler.GetAttachmentContent(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != string(body) {
		t.Errorf("body = %q, want %q", got, body)
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", got)
	}
	if got := w.Header().Get("X-Original-Content-Type"); got != "text/markdown" {
		t.Errorf("X-Original-Content-Type = %q, want text/markdown", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestGetAttachmentContent_AcceptsByExtensionWhenContentTypeIsGeneric(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	body := []byte("package main\n")
	id := seedPreviewAttachment(t, store, "main-go-key.go", "main.go", "application/octet-stream", body)

	req, w := newPreviewRequest(t, id, testWorkspaceID)
	testHandler.GetAttachmentContent(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestGetAttachmentContent_Unsupported_PDF(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	id := seedPreviewAttachment(t, store, "pdf-key.pdf", "manual.pdf", "application/pdf", []byte("%PDF-1.4\n"))

	req, w := newPreviewRequest(t, id, testWorkspaceID)
	testHandler.GetAttachmentContent(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415; body=%s", w.Code, w.Body.String())
	}
}

func TestGetAttachmentContent_TooLarge(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	// One byte over the limit. Allocate ASCII so io.ReadAll has work to do.
	big := bytes.Repeat([]byte("a"), maxPreviewTextSize+1)
	id := seedPreviewAttachment(t, store, "huge-key.txt", "huge.txt", "text/plain", big)

	req, w := newPreviewRequest(t, id, testWorkspaceID)
	testHandler.GetAttachmentContent(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", w.Code, w.Body.String())
	}
}

func TestGetAttachmentContent_ForeignWorkspace(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	id := seedPreviewAttachment(t, store, "ws-mismatch.md", "note.md", "text/markdown", []byte("# secret\n"))

	// Same attachment id, but request comes in scoped to a different workspace.
	foreign := "00000000-0000-0000-0000-000000000099"
	req, w := newPreviewRequest(t, id, foreign)
	testHandler.GetAttachmentContent(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestGetAttachmentContent_NotFound(t *testing.T) {
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	req, w := newPreviewRequest(t, "00000000-0000-0000-0000-000000000abc", testWorkspaceID)
	testHandler.GetAttachmentContent(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestIsTextPreviewable(t *testing.T) {
	t.Helper()
	cases := []struct {
		name        string
		contentType string
		filename    string
		want        bool
	}{
		{"markdown by ext", "application/octet-stream", "README.md", true},
		{"markdown by mime", "text/markdown", "README", true},
		{"plain text", "text/plain", "log.txt", true},
		{"json by mime", "application/json", "data.json", true},
		{"yaml by ext", "application/octet-stream", "config.yml", true},
		{"go source", "text/plain", "main.go", true},
		{"typescript", "application/octet-stream", "index.ts", true},
		{"html", "text/html", "page.html", true},
		{"dockerfile no ext", "application/octet-stream", "Dockerfile", true},
		{"env basename", "application/octet-stream", ".env", true},
		{"gitignore extension", "application/octet-stream", ".gitignore", true},
		{"dockerfile extension", "application/octet-stream", "service.dockerfile", true},

		{"pdf rejected", "application/pdf", "doc.pdf", false},
		{"png rejected", "image/png", "shot.png", false},
		{"video rejected", "video/mp4", "clip.mp4", false},
		{"binary fallthrough", "application/octet-stream", "blob.bin", false},
		{"docx rejected", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "report.docx", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTextPreviewable(tc.contentType, tc.filename); got != tc.want {
				t.Errorf("isTextPreviewable(%q, %q) = %v, want %v", tc.contentType, tc.filename, got, tc.want)
			}
		})
	}
}

func TestBuildMarkdownURL_PublicCdnAbsoluteURLReusedVerbatim(t *testing.T) {
	useAttachmentResponseConfig(t, &mockStorage{}, "https://api.multica.test", nil)
	_, resp := attachmentResponseForURL(t, "https://cdn.multica.test/uploads/abc.png", "abc.png")
	if resp.MarkdownURL != "https://cdn.multica.test/uploads/abc.png" {
		t.Fatalf("markdown_url = %q, want raw a.Url passthrough", resp.MarkdownURL)
	}
}

func TestBuildMarkdownURL_PrivateBucketWithoutCdnDomainRoutesThroughAPIEndpoint(t *testing.T) {
	useAttachmentResponseConfig(t, &mockStorageNoCdn{}, "https://api.multica.test", nil)
	id, resp := attachmentResponseForURL(t, "https://prod.s3.amazonaws.com/key.png", "key.png")
	want := "https://api.multica.test/api/attachments/" + id + "/download"
	if resp.MarkdownURL != want {
		t.Fatalf("markdown_url = %q, want absolute API endpoint %q (private bucket without explicit CDN must not persist raw S3 URL)", resp.MarkdownURL, want)
	}
}

func TestBuildMarkdownURL_CloudFrontSignedModeNeverPersistsRawStorageURL(t *testing.T) {
	useAttachmentResponseConfig(t, testHandler.Storage, "https://api.multica.test", testCloudFrontSigner(t))

	id, resp := attachmentResponseForURL(t, "https://prod.s3.amazonaws.com/key.png", "key.png")
	want := "https://api.multica.test/api/attachments/" + id + "/download"
	if resp.MarkdownURL != want {
		t.Fatalf("markdown_url = %q, want absolute API endpoint %q", resp.MarkdownURL, want)
	}
	if resp.DownloadURL == resp.MarkdownURL {
		t.Fatalf("download_url and markdown_url must differ in CloudFront-signed mode (got identical %q)", resp.DownloadURL)
	}
}

func TestBuildMarkdownURL_RelativeStorageURLPrefixedWithPublicURL(t *testing.T) {
	useAttachmentResponseConfig(t, testHandler.Storage, "https://api.multica.test", nil)

	id, resp := attachmentResponseForURL(t, "/uploads/abc.png", "abc.png")
	want := "https://api.multica.test/api/attachments/" + id + "/download"
	if resp.MarkdownURL != want {
		t.Fatalf("markdown_url = %q, want absolute API endpoint %q", resp.MarkdownURL, want)
	}
}

func TestBuildMarkdownURL_PublicURLUnsetFallsBackToSiteRelative(t *testing.T) {
	useAttachmentResponseConfig(t, testHandler.Storage, "", nil)
	id, resp := attachmentResponseForURL(t, "/uploads/abc.png", "abc.png")
	want := "/api/attachments/" + id + "/download"
	if resp.MarkdownURL != want {
		t.Fatalf("markdown_url = %q, want site-relative fallback %q", resp.MarkdownURL, want)
	}
}

func TestBuildMarkdownURL_StripsTrailingSlashOnPublicURL(t *testing.T) {
	useAttachmentResponseConfig(t, testHandler.Storage, "https://api.multica.test/", nil)
	id, resp := attachmentResponseForURL(t, "/uploads/abc.png", "abc.png")
	want := "https://api.multica.test/api/attachments/" + id + "/download"
	if resp.MarkdownURL != want {
		t.Fatalf("markdown_url = %q, want exactly one separator %q", resp.MarkdownURL, want)
	}
}

func TestIsDurablePublicURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"absolute https no signature", "https://cdn.multica.test/foo.png", true},
		{"absolute http no signature", "http://cdn.multica.test/foo.png", true},
		{"absolute with port + path", "https://cdn.example.test:8080/a/b/c.png", true},
		{"empty string", "", false},
		{"site-relative", "/uploads/abc.png", false},
		{"protocol-relative", "//cdn.example/foo.png", false},
		{"data URL", "data:image/png;base64,abc", false},
		{"blob URL", "blob:https://app/abc", false},
		{"unsupported scheme", "ftp://server/foo", false},
		{"cloudfront-signed Signature", "https://cdn.example/foo.png?Signature=abc&Key-Pair-Id=K1", false},
		{"cloudfront-signed Key-Pair-Id alone", "https://cdn.example/foo.png?Key-Pair-Id=K1", false},
		{"s3-presigned X-Amz-Signature", "https://bucket.s3/foo.png?X-Amz-Signature=abc", false},
		{"s3-presigned X-Amz-Expires alone", "https://bucket.s3/foo.png?X-Amz-Expires=900", false},
		{"plain Expires query", "https://cdn.example/foo.png?Expires=99", false},
		{"unrelated query", "https://cdn.example/foo.png?cache=1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDurablePublicURL(tc.url); got != tc.want {
				t.Errorf("isDurablePublicURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
