package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ClientVersion is the CLI version sent on every request as X-Client-Version.
// Set by the multica binary at init() so the package doesn't depend on the
// concrete cmd package. Defaults to "dev" when running unset (e.g. tests).
var ClientVersion = "dev"

// APIClient is a REST client for the Multica server API.
// Used by ctrl subcommands (agent, runtime, status, etc.). Requests
// automatically include auth and execution context headers when configured.
type APIClient struct {
	BaseURL     string
	WorkspaceID string
	Token       string
	AgentID     string // When set, requests are attributed to this agent instead of the user.
	TaskID      string // When set, sent as X-Task-ID for agent-task validation.
	HTTPClient  *http.Client

	// Identity overrides. Empty values use CLI/runtime defaults; ClientVersion
	// remains package-level because the binary injects its build version.
	Platform string
	Version  string
	OS       string
}

type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, strings.TrimSpace(e.Body))
}

// newHTTPError builds a *HTTPError from an error response (status >= 400),
// reading a capped slice of the body. Every Multica API helper funnels its
// >= 400 responses through this so the top-level FormatError / ExitCodeFor can
// classify the failure via errors.As(err, **HTTPError) regardless of which
// HTTP verb the command used.
func newHTTPError(method, path string, resp *http.Response) *HTTPError {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &HTTPError{
		Method:     method,
		Path:       path,
		StatusCode: resp.StatusCode,
		Body:       strings.TrimSpace(string(data)),
	}
}

// defaultHTTPTimeout is the per-request timeout for the CLI's HTTP client.
// It can be overridden with the MULTICA_HTTP_TIMEOUT environment variable
// (see httpTimeout). 30s is chosen over the historical 15s because complex
// networks (notably in mainland China) routinely need more than 15s to
// complete the TLS handshake plus request round-trip, which surfaced as an
// opaque "context deadline exceeded" to users.
const defaultHTTPTimeout = 30 * time.Second

// httpTimeout returns the HTTP client timeout, honoring MULTICA_HTTP_TIMEOUT.
// The value may be a Go duration string ("45s", "2m") or a plain integer
// number of seconds ("45"). Invalid or non-positive values fall back to the
// default.
func httpTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv("MULTICA_HTTP_TIMEOUT"))
	if v == "" {
		return defaultHTTPTimeout
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return defaultHTTPTimeout
}

// apiContextGrace is added on top of the HTTP transport timeout when deriving
// a command-level context deadline, so the transport timeout (which produces a
// clean, classifiable "request timed out" error) is the one that fires rather
// than the outer context being canceled first.
const apiContextGrace = 5 * time.Second

// APITimeout returns the deadline budget for a single CLI API command. It is
// always at least the configured HTTP transport timeout (see httpTimeout,
// which honors MULTICA_HTTP_TIMEOUT) plus a small grace margin, so a
// command-level context never truncates an in-flight request below the timeout
// the user configured. This is the fix for command contexts that previously
// hardcoded a 15s deadline shorter than the 30s/env transport timeout.
func APITimeout() time.Duration {
	return AtLeastAPITimeout(0)
}

// AtLeastAPITimeout returns max(min, APITimeout()). Use it for commands that
// need a larger floor than usual (for example file uploads, which historically
// used a 60s budget).
func AtLeastAPITimeout(min time.Duration) time.Duration {
	budget := httpTimeout() + apiContextGrace
	if min > budget {
		return min
	}
	return budget
}

// APIContext derives a command-scoped context whose deadline is APITimeout().
// The returned cancel func must be called (typically via defer) to release
// resources. Commands should use this instead of context.WithTimeout with a
// hardcoded duration so the deadline always respects MULTICA_HTTP_TIMEOUT.
func APIContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, APITimeout())
}

// NewAPIClient creates a new API client for ctrl commands.
func NewAPIClient(baseURL, workspaceID, token string) *APIClient {
	return &APIClient{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		WorkspaceID: workspaceID,
		Token:       token,
		HTTPClient:  &http.Client{Timeout: httpTimeout()},
	}
}

func (c *APIClient) setHeaders(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.WorkspaceID != "" {
		req.Header.Set("X-Workspace-ID", c.WorkspaceID)
	}
	if c.AgentID != "" {
		req.Header.Set("X-Agent-ID", c.AgentID)
	}
	if c.TaskID != "" {
		req.Header.Set("X-Task-ID", c.TaskID)
	}

	platform := c.Platform
	if platform == "" {
		platform = "cli"
	}
	if platform != "" {
		req.Header.Set("X-Client-Platform", platform)
	}
	version := c.Version
	if version == "" {
		version = ClientVersion
	}
	if version != "" {
		req.Header.Set("X-Client-Version", version)
	}
	osName := c.OS
	if osName == "" {
		osName = protocol.NormalizeGOOS(runtime.GOOS)
	}
	if osName != "" {
		req.Header.Set("X-Client-OS", osName)
	}
}

func (c *APIClient) executeJSON(client *http.Client, req *http.Request, errorPath string, out any) (http.Header, error) {
	resp, err := client.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return nil, newHTTPError(req.Method, errorPath, resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.Header, err
		}
	}
	return resp.Header, nil
}

// GetJSON performs a GET request and decodes the JSON response.
//
// On an HTTP error response (status >= 400) the returned error is a
// *HTTPError so callers can use errors.As to inspect the status code
// (for example to recognize a 404 from a server that does not expose a
// given endpoint and degrade gracefully). The error string format
// ("GET <path> returned <code>: <body>") is preserved by HTTPError.Error().
func (c *APIClient) GetJSON(ctx context.Context, path string, out any) error {
	_, err := c.GetJSONWithHeaders(ctx, path, out)
	return err
}

// GetJSONWithHeaders performs a GET request, decodes the JSON response, and
// returns the response headers. Useful when callers need header values like
// X-Total-Count for pagination.
func (c *APIClient) GetJSONWithHeaders(ctx context.Context, path string, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	return c.executeJSON(c.HTTPClient, req, path, out)
}

// DeleteJSON performs a DELETE request.
func (c *APIClient) DeleteJSON(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)
	_, err = c.executeJSON(c.HTTPClient, req, path, nil)
	return err
}

// DeleteJSONWithBody performs a DELETE request with a JSON body.
func (c *APIClient) DeleteJSONWithBody(ctx context.Context, path string, body any) error {
	return c.sendJSON(ctx, http.MethodDelete, path, body, "", nil)
}

// PostJSON performs a POST request with a JSON body.
func (c *APIClient) PostJSON(ctx context.Context, path string, body any, out any) error {
	return c.sendJSON(ctx, http.MethodPost, path, body, "", out)
}

// PostJSONWithIdempotencyKey performs a POST for a caller-owned logical
// operation. Reusing the key lets the server return the committed response
// when the first response was lost.
func (c *APIClient) PostJSONWithIdempotencyKey(
	ctx context.Context,
	path string,
	body any,
	idempotencyKey string,
	out any,
) error {
	if out == nil {
		err := c.sendJSON(ctx, http.MethodPost, path, body, idempotencyKey, nil)
		if err == nil || isHTTPResponseError(err) {
			return err
		}
		return c.sendJSON(ctx, http.MethodPost, path, body, idempotencyKey, nil)
	}

	var raw json.RawMessage
	err := c.sendJSON(ctx, http.MethodPost, path, body, idempotencyKey, &raw)
	if err != nil && !isHTTPResponseError(err) {
		raw = nil
		err = c.sendJSON(ctx, http.MethodPost, path, body, idempotencyKey, &raw)
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func isHTTPResponseError(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr)
}

// sendJSON is the single transport path for HTTP methods that carry a JSON
// request body. Public verb-specific helpers keep the caller-facing API small;
// serialization, identity headers, HTTP error typing and response decoding stay
// identical across those verbs.
func (c *APIClient) sendJSON(
	ctx context.Context,
	method string,
	path string,
	body any,
	idempotencyKey string,
	out any,
) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	_, err = c.executeJSON(c.HTTPClient, req, path, out)
	return err
}

// PutJSON performs a PUT request with a JSON body.
func (c *APIClient) PutJSON(ctx context.Context, path string, body any, out any) error {
	return c.sendJSON(ctx, http.MethodPut, path, body, "", out)
}

// PatchJSON performs a PATCH request with a JSON body.
func (c *APIClient) PatchJSON(ctx context.Context, path string, body any, out any) error {
	return c.sendJSON(ctx, http.MethodPatch, path, body, "", out)
}

type uploadResponse struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func (c *APIClient) uploadFile(ctx context.Context, client *http.Client, fileData []byte, filename, issueID string) (uploadResponse, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return uploadResponse{}, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return uploadResponse{}, fmt.Errorf("write file data: %w", err)
	}

	if issueID != "" {
		if err := writer.WriteField("issue_id", issueID); err != nil {
			return uploadResponse{}, fmt.Errorf("write issue_id field: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return uploadResponse{}, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/upload-file", &body)
	if err != nil {
		return uploadResponse{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)

	var result uploadResponse
	if _, err := c.executeJSON(client, req, "/api/upload-file", &result); err != nil {
		var networkErr *networkError
		if isHTTPResponseError(err) || errors.As(err, &networkErr) {
			return uploadResponse{}, err
		}
		return uploadResponse{}, fmt.Errorf("decode upload response: %w", err)
	}
	return result, nil
}

// UploadFile uploads a file via multipart form to /api/upload-file.
// It returns the attachment ID from the server response.
func (c *APIClient) UploadFile(ctx context.Context, fileData []byte, filename string, issueID string) (string, error) {
	result, err := c.uploadFile(ctx, c.HTTPClient, fileData, filename, issueID)
	if err != nil {
		return "", err
	}
	if result.ID == "" {
		return "", fmt.Errorf("upload response missing attachment id")
	}
	return result.ID, nil
}

// UploadFileWithURL uploads a file via multipart form to /api/upload-file
// without associating it with an issue or comment. It returns the attachment
// ID and URL.
func (c *APIClient) UploadFileWithURL(ctx context.Context, fileData []byte, filename string) (string, string, error) {
	httpClient := c.HTTPClient
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > httpClient.Timeout {
			clientCopy := *httpClient
			clientCopy.Timeout = remaining
			httpClient = &clientCopy
		}
	}
	result, err := c.uploadFile(ctx, httpClient, fileData, filename, "")
	if err != nil {
		return "", "", err
	}
	if result.URL == "" {
		return "", "", fmt.Errorf("upload response missing attachment url")
	}
	if result.ID == "" {
		return "", "", fmt.Errorf("upload response missing attachment id")
	}
	return result.ID, result.URL, nil
}

// DownloadFile downloads a file from the given URL and returns the response body.
// This is used for downloading attachments via their signed download_url.
// Downloads are limited to 100 MB to match the upload size limit.
//
// The URL may be absolute (a signed CloudFront/S3 URL) or relative
// (a server-relative path like "/api/attachments/{id}/download" or
// "/uploads/...") depending on how the
// server is configured. Relative URLs are resolved against the client's
// BaseURL and sent with the standard auth headers; absolute URLs are
// used as-is so that their query-string signatures are not disturbed.
func (c *APIClient) DownloadFile(ctx context.Context, downloadURL string) ([]byte, error) {
	isRelative := !strings.HasPrefix(downloadURL, "http://") && !strings.HasPrefix(downloadURL, "https://")
	if isRelative {
		if c.BaseURL == "" {
			return nil, fmt.Errorf("download URL %q is relative but client has no BaseURL", downloadURL)
		}
		downloadURL = c.BaseURL + downloadURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	if isRelative {
		c.setHeaders(req)
	}

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return nil, newHTTPError(http.MethodGet, downloadURL, resp)
	}

	const maxDownloadSize = 100 << 20 // 100 MB
	return io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize))
}
