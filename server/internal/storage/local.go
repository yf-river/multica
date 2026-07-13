package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type LocalStorage struct {
	uploadDir string
	baseURL   string
}

// metaSuffix is the on-disk extension for the sidecar JSON file that
// captures an upload's original filename and sniffed content type. The
// sidecar exists so ServeFile can set Content-Disposition the way S3's
// PutObject path already does, instead of letting the browser fall back
// to the storage-key basename for the download filename.
const metaSuffix = ".meta.json"

type localMeta struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

// NewLocalStorageFromEnv creates a LocalStorage from environment variables.
// Returns nil if upload directory cannot be created.
//
// Environment variables:
//   - LOCAL_UPLOAD_DIR (default: "./data/uploads")
//   - LOCAL_UPLOAD_BASE_URL (optional, e.g., "http://localhost:8080")
func NewLocalStorageFromEnv() *LocalStorage {
	uploadDir := os.Getenv("LOCAL_UPLOAD_DIR")
	if uploadDir == "" {
		uploadDir = "./data/uploads"
	}

	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		slog.Error("failed to create upload directory", "dir", uploadDir, "error", err)
		return nil
	}

	baseURL := strings.TrimSuffix(os.Getenv("LOCAL_UPLOAD_BASE_URL"), "/")

	slog.Info("local storage initialized", "dir", uploadDir, "baseURL", baseURL)
	return &LocalStorage{
		uploadDir: uploadDir,
		baseURL:   baseURL,
	}
}

func (s *LocalStorage) CdnDomain() string {
	if s.baseURL == "" {
		return ""
	}
	u, err := url.Parse(s.baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (s *LocalStorage) KeyFromURL(rawURL string) string {
	if s.baseURL != "" && strings.HasPrefix(rawURL, s.baseURL) {
		rawURL = strings.TrimPrefix(rawURL, s.baseURL)
	}

	prefix := "/uploads/"
	if idx := strings.Index(rawURL, prefix); idx >= 0 {
		return rawURL[idx+len(prefix):]
	}
	if i := strings.LastIndex(rawURL, "/"); i >= 0 {
		return rawURL[i+1:]
	}
	return rawURL
}

// GetReader opens the underlying file for streaming. Refuses keys that
// resolve outside uploadDir (defense against a stored key with traversal
// components) and refuses the sidecar suffix so /content can't be coaxed
// into leaking the .meta.json blob.
func (s *LocalStorage) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	filePath, err := s.objectPath(key)
	if err != nil {
		return nil, fmt.Errorf("local GetReader: %w", err)
	}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("local GetReader: %w", err)
	}
	return f, nil
}

func (s *LocalStorage) Delete(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	filePath, err := s.objectPath(key)
	if err != nil {
		return fmt.Errorf("local Delete: %w", err)
	}
	var deleteErr error
	if err := os.Remove(filePath); err != nil {
		if !os.IsNotExist(err) {
			deleteErr = fmt.Errorf("remove object: %w", err)
		}
	}
	if err := os.Remove(filePath + metaSuffix); err != nil && !os.IsNotExist(err) {
		deleteErr = errors.Join(deleteErr, fmt.Errorf("remove metadata: %w", err))
	}
	return deleteErr
}

func (s *LocalStorage) DeleteKeys(ctx context.Context, keys []string) {
	for _, key := range keys {
		if err := s.Delete(ctx, key); err != nil {
			slog.Error("local storage Delete failed", "key", key, "error", err)
		}
	}
}

func (s *LocalStorage) Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error) {
	dest, err := s.objectPath(key)
	if err != nil {
		return "", fmt.Errorf("local Upload: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", fmt.Errorf("local storage MkdirAll: %w", err)
	}
	// Revalidate after creating nested directories so a pre-existing symlinked
	// parent cannot redirect the write outside uploadDir.
	dest, err = s.objectPath(key)
	if err != nil {
		return "", fmt.Errorf("local Upload: %w", err)
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return "", fmt.Errorf("local storage WriteFile: %w", err)
	}
	// The sidecar and object form one upload result. If metadata cannot be
	// persisted, remove the just-written object and fail instead of returning a
	// URL whose download name/content type no longer matches the request. Skip
	// the sidecar only when there is no filename to preserve.
	if filename != "" {
		body, _ := json.Marshal(localMeta{Filename: filename, ContentType: contentType})
		if err := os.WriteFile(dest+metaSuffix, body, 0644); err != nil {
			cleanupErr := os.Remove(dest)
			return "", errors.Join(fmt.Errorf("local storage metadata WriteFile: %w", err), cleanupErr)
		}
	}

	if s.baseURL != "" {
		return fmt.Sprintf("%s/uploads/%s", s.baseURL, key), nil
	}
	return fmt.Sprintf("/uploads/%s", key), nil
}

func (s *LocalStorage) ServeFile(w http.ResponseWriter, r *http.Request, filename string) {
	// The sidecar is an implementation detail of the local backend; refuse
	// to serve it directly so /uploads/<key>.meta.json doesn't become a
	// stable read API. Comes before any disk work so a path-traversal
	// attempt at a .meta.json sibling can't trigger an out-of-tree read.
	if strings.HasSuffix(filename, metaSuffix) {
		http.NotFound(w, r)
		return
	}

	filePath, err := s.objectPath(filename)
	// filepath.Join cleans the path but doesn't enforce containment, so a
	// caller passing "../etc/passwd" lands outside uploadDir. http.ServeFile
	// rejects such requests on r.URL.Path, but readLocalMeta runs first —
	// without this guard a crafted path could trigger a stray disk read on
	// an arbitrary <some-path>.meta.json before the 400 lands.
	if err != nil {
		http.NotFound(w, r)
		return
	}
	slog.Info("serving file", "filename", filename, "filepath", filePath)

	// Mirror the S3 Upload path: when sidecar metadata exists for this key,
	// set Content-Disposition with the original uploaded filename. Without
	// it, browsers download the file under the storage-key basename (the
	// UUID + extension) instead of the human-readable name the uploader
	// chose. Uploads from before the sidecar landed have no .meta.json on
	// disk and fall through to the existing behavior.
	if meta, ok := readLocalMeta(filePath); ok && meta.Filename != "" {
		w.Header().Set("Content-Disposition", ContentDisposition(meta.ContentType, meta.Filename))
	}

	// Use http.ServeFile which has built-in path traversal protection
	// It sanitizes the path and prevents access outside the directory
	http.ServeFile(w, r, filePath)
}

// objectPath is the single key-to-filesystem boundary for local storage.
// Object keys may contain nested directories but must never address the
// metadata sidecar or escape uploadDir.
func (s *LocalStorage) objectPath(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("empty key")
	}
	if strings.HasSuffix(key, metaSuffix) {
		return "", fmt.Errorf("refusing sidecar key %q", key)
	}
	filePath := filepath.Join(s.uploadDir, key)
	if filepath.IsAbs(key) || !isUnder(s.uploadDir, filePath) {
		return "", fmt.Errorf("key escapes upload dir: %q", key)
	}
	resolvedRoot, err := filepath.EvalSymlinks(s.uploadDir)
	if err != nil {
		return "", fmt.Errorf("resolve upload dir: %w", err)
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(filePath))
	if err == nil {
		if !isUnder(resolvedRoot, resolvedParent) {
			return "", fmt.Errorf("key traverses symlink outside upload dir: %q", key)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve key parent: %w", err)
	}
	if info, err := os.Lstat(filePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink object key %q", key)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect key: %w", err)
	}
	return filePath, nil
}

// isUnder reports whether target resolves to a path inside dir (or equal to
// it). Both inputs are passed through filepath.Clean so trailing slashes and
// "." segments don't fool the comparison.
func isUnder(dir, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readLocalMeta(filePath string) (localMeta, bool) {
	body, err := os.ReadFile(filePath + metaSuffix)
	if err != nil {
		return localMeta{}, false
	}
	var meta localMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return localMeta{}, false
	}
	return meta, true
}
