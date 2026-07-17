package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStorage_Upload(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	_ = os.Unsetenv("LOCAL_UPLOAD_BASE_URL")

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	ctx := context.Background()
	data := []byte("hello world")
	contentType := "text/plain"
	filename := "test.txt"

	link, err := store.Upload(ctx, "test-key.txt", data, contentType, filename)
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	expectedLink := "/uploads/test-key.txt"
	if link != expectedLink {
		t.Errorf("link = %q, want %q", link, expectedLink)
	}

	filePath := filepath.Join(tmpDir, "test-key.txt")
	stored, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read uploaded file: %v", err)
	}
	if string(stored) != string(data) {
		t.Errorf("stored data = %q, want %q", stored, data)
	}
}

func TestLocalStorage_Upload_WithBaseURL(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "http://localhost:8080")

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	ctx := context.Background()
	data := []byte("hello world")
	contentType := "text/plain"
	filename := "test.txt"

	link, err := store.Upload(ctx, "test-key.txt", data, contentType, filename)
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	expectedLink := "http://localhost:8080/uploads/test-key.txt"
	if link != expectedLink {
		t.Errorf("link = %q, want %q", link, expectedLink)
	}

	filePath := filepath.Join(tmpDir, "test-key.txt")
	stored, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read uploaded file: %v", err)
	}
	if string(stored) != string(data) {
		t.Errorf("stored data = %q, want %q", stored, data)
	}
}

func TestLocalStorage_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	ctx := context.Background()
	data := []byte("hello world")

	_, err := store.Upload(ctx, "delete-me.txt", data, "text/plain", "delete-me.txt")
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	filePath := filepath.Join(tmpDir, "delete-me.txt")
	if _, err := os.ReadFile(filePath); err != nil {
		t.Fatalf("file should exist: %v", err)
	}

	_ = store.Delete(ctx, "delete-me.txt")

	if _, err := os.ReadFile(filePath); !os.IsNotExist(err) {
		t.Errorf("file should be deleted")
	}
}

func TestLocalStorage_WriteAndDeleteRejectTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}
	outside := filepath.Join(filepath.Dir(tmpDir), "outside-"+filepath.Base(tmpDir)+".txt")
	t.Cleanup(func() { _ = os.Remove(outside) })
	if _, err := store.Upload(context.Background(), "../"+filepath.Base(outside), []byte("leak"), "text/plain", "leak.txt"); err == nil {
		t.Fatal("traversal upload was accepted")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("traversal upload touched %s: %v", outside, err)
	}
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "../"+filepath.Base(outside)); err == nil {
		t.Fatal("traversal delete was accepted")
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "keep" {
		t.Fatalf("outside file changed: body=%q err=%v", body, err)
	}
}

func TestLocalStorage_UploadRollsBackWhenMetadataWriteFails(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}
	key := "metadata-failure.txt"
	if err := os.Mkdir(filepath.Join(tmpDir, key+metaSuffix), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upload(context.Background(), key, []byte("body"), "text/plain", "report.txt"); err == nil {
		t.Fatal("metadata failure returned upload success")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, key)); !os.IsNotExist(err) {
		t.Fatalf("object was not rolled back: %v", err)
	}
}

func TestLocalStorage_RejectsSymlinkEscape(t *testing.T) {
	uploadDir := t.TempDir()
	outsideDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", uploadDir)
	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}
	if err := os.Symlink(outsideDir, filepath.Join(uploadDir, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.Upload(context.Background(), "escape/leak.txt", []byte("leak"), "text/plain", "leak.txt"); err == nil {
		t.Fatal("symlinked-parent upload was accepted")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "leak.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink upload touched outside path: %v", err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(uploadDir, "secret-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetReader(context.Background(), "secret-link"); err == nil {
		t.Fatal("symlink object read was accepted")
	}
	if err := store.Delete(context.Background(), "secret-link"); err == nil {
		t.Fatal("symlink object delete was accepted")
	}
	if body, err := os.ReadFile(outsideFile); err != nil || string(body) != "secret" {
		t.Fatalf("outside target changed: body=%q err=%v", body, err)
	}
}

func TestLocalStorage_KeyFromURL(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	// No baseURL set

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	tests := []struct {
		name     string
		rawURL   string
		expected string
	}{
		{"local URL format", "/uploads/abc123.png", "abc123.png"},
		{"local URL with subdir", "/uploads/2024/01/image.jpg", "2024/01/image.jpg"},
		{"local URL with workspace prefix", "/uploads/workspaces/ws-123/abc.png", "workspaces/ws-123/abc.png"},
		{"just filename", "abc123.png", "abc123.png"},
		{"full path", "/some/path/to/file.pdf", "file.pdf"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := store.KeyFromURL(tc.rawURL)
			if got != tc.expected {
				t.Errorf("KeyFromURL(%q) = %q, want %q", tc.rawURL, got, tc.expected)
			}
		})
	}
}

func TestLocalStorage_KeyFromURL_WithBaseURL(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "http://localhost:8080")

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	tests := []struct {
		name     string
		rawURL   string
		expected string
	}{
		{"full URL format", "http://localhost:8080/uploads/abc123.png", "abc123.png"},
		{"full URL with subdir", "http://localhost:8080/uploads/2024/01/image.jpg", "2024/01/image.jpg"},
		{"local URL format still works", "/uploads/abc123.png", "abc123.png"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := store.KeyFromURL(tc.rawURL)
			if got != tc.expected {
				t.Errorf("KeyFromURL(%q) = %q, want %q", tc.rawURL, got, tc.expected)
			}
		})
	}
}

func TestDeleteKeys(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	ctx := context.Background()
	data := []byte("hello world")

	keys := []string{"file1.txt", "file2.txt", "file3.txt"}
	for _, key := range keys {
		_, err := store.Upload(ctx, key, data, "text/plain", key)
		if err != nil {
			t.Fatalf("Upload %s failed: %v", key, err)
		}
	}

	DeleteKeys(ctx, store, keys)

	for _, key := range keys {
		filePath := filepath.Join(tmpDir, key)
		if _, err := os.ReadFile(filePath); !os.IsNotExist(err) {
			t.Errorf("file %s should be deleted", key)
		}
	}
}

func TestLocalStorage_KeyFromURL_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	if got := store.KeyFromURL(""); got != "" {
		t.Errorf("KeyFromURL(\"\") = %q, want empty string", got)
	}
}

func TestLocalStorage_ServeFile_ContentDispositionFromSidecar(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	cases := []struct {
		name        string
		key         string
		contentType string
		filename    string
		wantHeader  string
	}{
		{
			name:        "attachment for non-inline type",
			key:         "workspaces/ws-1/abc.xlsx",
			contentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			filename:    "Bwave JE_V1.xlsx",
			wantHeader:  `attachment; filename="Bwave JE_V1.xlsx"`,
		},
		{
			name:        "inline for image",
			key:         "workspaces/ws-1/def.png",
			contentType: "image/png",
			filename:    "screenshot 2026-05-11.png",
			wantHeader:  `inline; filename="screenshot 2026-05-11.png"`,
		},
		{
			name:        "filename with header-injection characters is sanitized",
			key:         "workspaces/ws-1/ghi.txt",
			contentType: "text/plain",
			filename:    "weird\";name.txt",
			wantHeader:  `attachment; filename="weird__name.txt"`,
		},
		{
			// SVG can carry <script>/onload — never serve inline.
			name:        "attachment for svg (stored-XSS prevention)",
			key:         "workspaces/ws-1/jkl.svg",
			contentType: "image/svg+xml",
			filename:    "logo.svg",
			wantHeader:  `attachment; filename="logo.svg"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := store.Upload(ctx, tc.key, []byte("body"), tc.contentType, tc.filename); err != nil {
				t.Fatalf("Upload failed: %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, "/uploads/"+tc.key, nil)
			rec := httptest.NewRecorder()
			store.ServeFile(rec, req, tc.key)

			got := rec.Header().Get("Content-Disposition")
			if got != tc.wantHeader {
				t.Errorf("Content-Disposition = %q, want %q", got, tc.wantHeader)
			}
		})
	}
}

func TestLocalStorage_ServeFile_RequiresSidecar(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	key := "missing-sidecar.txt"
	if err := os.WriteFile(filepath.Join(tmpDir, key), []byte("body"), 0644); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/"+key, nil)
	rec := httptest.NewRecorder()
	store.ServeFile(rec, req, key)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestLocalStorage_ServeFile_RejectsSidecarSuffix(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	ctx := context.Background()
	if _, err := store.Upload(ctx, "abc.xlsx", []byte("body"), "text/plain", "real.xlsx"); err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	sidecarKey := "abc.xlsx" + metaSuffix
	req := httptest.NewRequest(http.MethodGet, "/uploads/"+sidecarKey, nil)
	rec := httptest.NewRecorder()
	store.ServeFile(rec, req, sidecarKey)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q, want empty", got)
	}
}

func TestLocalStorage_ServeFile_RejectsPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	parentDir := filepath.Dir(tmpDir)
	leakedBase := filepath.Join(parentDir, "leaked-target")
	if err := os.WriteFile(leakedBase+metaSuffix, []byte(`{"filename":"leaked.xlsx","content_type":"text/plain"}`), 0644); err != nil {
		t.Fatalf("seed leaked sidecar failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(leakedBase + metaSuffix)
	})

	traversal := "../" + filepath.Base(leakedBase)
	req := httptest.NewRequest(http.MethodGet, "/uploads/"+traversal, nil)
	rec := httptest.NewRecorder()
	store.ServeFile(rec, req, traversal)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q, want empty (sidecar must not leak)", got)
	}
}

func TestLocalStorage_Upload_RejectsEmptyFilename(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	key := "no-filename.bin"
	if _, err := store.Upload(context.Background(), key, []byte("body"), "application/octet-stream", ""); err == nil {
		t.Fatal("Upload should reject an empty filename")
	}

	if _, err := os.Stat(filepath.Join(tmpDir, key)); !os.IsNotExist(err) {
		t.Errorf("object should not exist after rejected upload, got err=%v", err)
	}
}

func TestLocalStorage_Delete_RemovesSidecar(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	ctx := context.Background()
	key := "deleteme.txt"
	if _, err := store.Upload(ctx, key, []byte("body"), "text/plain", "original.txt"); err != nil {
		t.Fatalf("Upload failed: %v", err)
	}
	sidecar := filepath.Join(tmpDir, key+metaSuffix)
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("sidecar should exist after Upload: %v", err)
	}

	_ = store.Delete(ctx, key)

	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Errorf("sidecar should be removed after Delete, got err=%v", err)
	}
}

func TestLocalStorage_GetReader_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	ctx := context.Background()
	key := "preview.md"
	body := []byte("# hello\nworld\n")
	if _, err := store.Upload(ctx, key, body, "text/markdown", "preview.md"); err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	rc, err := store.GetReader(ctx, key)
	if err != nil {
		t.Fatalf("GetReader: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("body = %q, want %q", got, body)
	}
}

func TestLocalStorage_GetReader_RejectsTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	if rc, err := store.GetReader(context.Background(), "../../../etc/passwd"); err == nil {
		_ = rc.Close()
		t.Fatal("GetReader should refuse traversal keys")
	}
}

func TestLocalStorage_GetReader_RejectsSidecarSuffix(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	if rc, err := store.GetReader(context.Background(), "some-key.txt"+metaSuffix); err == nil {
		_ = rc.Close()
		t.Fatal("GetReader should refuse sidecar keys")
	}
}

func TestLocalStorage_GetReader_MissingKey(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", tmpDir)

	store := NewLocalStorageFromEnv()
	if store == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}

	if rc, err := store.GetReader(context.Background(), "nonexistent.txt"); err == nil {
		_ = rc.Close()
		t.Fatal("GetReader should error on missing key")
	}
}
