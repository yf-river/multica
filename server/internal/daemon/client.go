package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// requestError is returned by postJSON/getJSON when the server responds with an error status.
type requestError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *requestError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// isTaskNotFoundError returns true if the error is a 404 with "task not found"
// body. The daemon uses this to detect that a task was deleted server-side
// (issue removed, agent reassigned, ...) while the local agent was still
// running, so it can interrupt the agent rather than letting it keep
// emitting tool calls against a dead task.
func isTaskNotFoundError(err error) bool {
	return matchesRequestError(err, http.StatusNotFound, "task not found")
}

func isTaskStartConflictError(err error) bool {
	return matchesRequestError(err, http.StatusConflict, "task is no longer startable")
}

func matchesRequestError(err error, status int, bodySubstring string) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.StatusCode == status && strings.Contains(strings.ToLower(reqErr.Body), bodySubstring)
}

// isUnauthorizedError returns true if the error is a 401 from the server.
// Used by the token-renewal loop to surface a clear "re-login required"
// message instead of a generic transport-level retry.
func isUnauthorizedError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.StatusCode == http.StatusUnauthorized
}

// isRuntimeNotFoundError returns true if the error is a 404 with "runtime not
// found" body. The daemon uses this to detect that the runtime row was deleted
// server-side (UI Delete, 7-day offline GC) while the daemon was still
// heartbeating against the dead UUID, so it can prune the stale runtime from
// its local state and re-register instead of looping on the dead ID forever.
//
// Server-side, this body is paired with pgx.ErrNoRows specifically (other DB
// errors return 500), so a transient DB hiccup cannot make the daemon
// self-cleanup.
func isRuntimeNotFoundError(err error) bool {
	return matchesRequestError(err, http.StatusNotFound, "runtime not found")
}

// Client handles HTTP communication with the Multica server daemon API.
type Client struct {
	baseURL string
	token   string
	client  *http.Client

	// Identity headers sent on every request as X-Client-*. Populated by
	// SetIdentity(); empty values are simply omitted.
	platform string
	version  string
	os       string
}

// NewClient creates a new daemon API client.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:  baseURL,
		client:   &http.Client{Timeout: 30 * time.Second},
		platform: "daemon",
		os:       protocol.NormalizeGOOS(runtime.GOOS),
	}
}

// SetVersion records the daemon's CLI version, sent as X-Client-Version.
// Called by Daemon.Run after config is loaded.
func (c *Client) SetVersion(v string) {
	c.version = v
}

// setIdentityHeaders attaches X-Client-Platform/Version/OS to req when set.
func (c *Client) setIdentityHeaders(req *http.Request) {
	if c.platform != "" {
		req.Header.Set("X-Client-Platform", c.platform)
	}
	if c.version != "" {
		req.Header.Set("X-Client-Version", c.version)
	}
	if c.os != "" {
		req.Header.Set("X-Client-OS", c.os)
	}
}

// SetToken sets the auth token for authenticated requests.
func (c *Client) SetToken(token string) {
	c.token = token
}

func (c *Client) ClaimTask(ctx context.Context, runtimeID string) (*Task, error) {
	var resp struct {
		Task *Task `json:"task"`
	}
	if err := c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/tasks/claim", runtimeID), map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp.Task, nil
}

func (c *Client) StartTask(ctx context.Context, taskID string) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/start", taskID), map[string]any{}, nil)
}

func (c *Client) ReportProgress(ctx context.Context, taskID, summary string, step, total int) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/progress", taskID), map[string]any{
		"summary": summary,
		"step":    step,
		"total":   total,
	}, nil)
}

// TaskMessageData represents a single agent execution message for batch reporting.
type TaskMessageData struct {
	Seq     int            `json:"seq"`
	Type    string         `json:"type"`
	Tool    string         `json:"tool,omitempty"`
	Content string         `json:"content,omitempty"`
	Input   map[string]any `json:"input,omitempty"`
	Output  string         `json:"output,omitempty"`
}

func (c *Client) ReportTaskMessages(ctx context.Context, taskID string, messages []TaskMessageData) error {
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/messages", taskID), map[string]any{
		"messages": messages,
	}, taskReportRetrySchedule)
}

func (c *Client) CompleteTask(ctx context.Context, taskID, output, branchName, sessionID, workDir string) error {
	body := map[string]any{"output": output}
	if branchName != "" {
		body["branch_name"] = branchName
	}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/complete", taskID), body, defaultTerminalRetrySchedule)
}

func (c *Client) ReportTaskUsage(ctx context.Context, taskID string, usage []TaskUsageEntry) error {
	if len(usage) == 0 {
		return nil
	}
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/usage", taskID), map[string]any{
		"usage": usage,
	}, taskReportRetrySchedule)
}

func (c *Client) FailTask(ctx context.Context, taskID, errMsg, sessionID, workDir, failureReason string) error {
	body := map[string]any{"error": errMsg}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	if failureReason != "" {
		body["failure_reason"] = failureReason
	}
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/fail", taskID), body, defaultTerminalRetrySchedule)
}

// PinTaskSession persists the agent's session_id and work_dir on the task
// row mid-flight so a daemon crash doesn't lose the resume pointer.
func (c *Client) PinTaskSession(ctx context.Context, taskID, sessionID, workDir string) error {
	if sessionID == "" && workDir == "" {
		return nil
	}
	body := map[string]any{}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/session", taskID), body, nil)
}

// RecoverOrphans tells the server to fail any dispatched/running tasks the
// previous daemon process for this runtime left behind. The server will
// auto-retry eligible tasks.
func (c *Client) RecoverOrphans(ctx context.Context, runtimeID string) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/recover-orphans", runtimeID), map[string]any{}, nil)
}

// GetTaskStatus returns the current status of a task. Used by the daemon to
// detect terminal/interruption signals (cancelled, failed, completed, or a
// 404 task-not-found) while a task is executing.
func (c *Client) GetTaskStatus(ctx context.Context, taskID string) (string, error) {
	var resp struct {
		Status string `json:"status"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/status", taskID), &resp); err != nil {
		return "", err
	}
	return resp.Status, nil
}

func (c *Client) SendHeartbeat(ctx context.Context, runtimeID string, metadata json.RawMessage) (*protocol.DaemonHeartbeatAckPayload, error) {
	var resp protocol.DaemonHeartbeatAckPayload
	body := map[string]any{
		"runtime_id": runtimeID,
	}
	if len(metadata) > 0 {
		body["metadata"] = metadata
	}
	if err := c.postJSON(ctx, "/api/daemon/heartbeat", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// WorkspaceInfo holds minimal workspace metadata returned by the API.
type WorkspaceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RenewTokenResponse mirrors handler.RenewPATResponse — kept loose (string +
// bool) because the daemon never parses the timestamp itself; it just logs it
// for operator visibility.
type RenewTokenResponse struct {
	ExpiresAt string `json:"expires_at"`
	Renewed   bool   `json:"renewed"`
}

// RenewToken asks the server to extend the daemon's current PAT in place when
// it's within the server-side renewal window. The server is authoritative on
// the threshold — the daemon doesn't know the token's expires_at locally —
// so this is safe to call on any cadence; the only thing extra calls cost is
// one round trip and one cheap SELECT.
func (c *Client) RenewToken(ctx context.Context) (*RenewTokenResponse, error) {
	var resp RenewTokenResponse
	if err := c.postJSON(ctx, "/api/tokens/current/renew", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListWorkspaces fetches all workspaces the authenticated user belongs to.
func (c *Client) ListWorkspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	var workspaces []WorkspaceInfo
	if err := c.getJSON(ctx, "/api/workspaces", &workspaces); err != nil {
		return nil, err
	}
	return workspaces, nil
}

type gcStatus struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (c *Client) getGCStatus(ctx context.Context, path string) (*gcStatus, error) {
	var resp gcStatus
	if err := c.getJSON(ctx, path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Deregister(ctx context.Context, runtimeIDs []string) error {
	return c.postJSON(ctx, "/api/daemon/deregister", map[string]any{
		"runtime_ids": runtimeIDs,
	}, nil)
}

// RegisterResponse holds the server's response to a daemon registration.
type RegisterResponse struct {
	Runtimes     []Runtime       `json:"runtimes"`
	Repos        []RepoData      `json:"repos"`
	ReposVersion string          `json:"repos_version"`
	Settings     json.RawMessage `json:"settings,omitempty"`
}

func (c *Client) Register(ctx context.Context, req map[string]any) (*RegisterResponse, error) {
	var resp RegisterResponse
	if err := c.postJSON(ctx, "/api/daemon/register", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type WorkspaceReposResponse struct {
	WorkspaceID  string          `json:"workspace_id"`
	Repos        []RepoData      `json:"repos"`
	ReposVersion string          `json:"repos_version"`
	Settings     json.RawMessage `json:"settings,omitempty"`
}

func (c *Client) GetWorkspaceRepos(ctx context.Context, workspaceID string) (*WorkspaceReposResponse, error) {
	var resp WorkspaceReposResponse
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/workspaces/%s/repos", workspaceID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RuntimeProfile mirrors the server's workspace custom runtime profile
// (MUL-3284). protocol_family is the provider used for task routing (it
// selects the agent backend), while command_name is the actual executable
// the daemon resolves on PATH and launches. fixed_args are launch arguments
// every agent on this runtime inherits.
type RuntimeProfile struct {
	ID             string   `json:"id"`
	WorkspaceID    string   `json:"workspace_id"`
	DisplayName    string   `json:"display_name"`
	ProtocolFamily string   `json:"protocol_family"`
	CommandName    string   `json:"command_name"`
	Description    *string  `json:"description"`
	FixedArgs      []string `json:"fixed_args"`
	Enabled        bool     `json:"enabled"`
}

// RuntimeProfilesResponse is the body of
// GET /api/daemon/workspaces/{workspaceID}/runtime-profiles. The server only
// returns enabled profiles for the workspace.
type RuntimeProfilesResponse struct {
	WorkspaceID     string           `json:"workspace_id"`
	RuntimeProfiles []RuntimeProfile `json:"runtime_profiles"`
}

// GetRuntimeProfiles fetches the workspace's enabled custom runtime profiles.
// Mirrors GetWorkspaceRepos. A fetch failure is isolated from built-in runtime
// registration so a transient profile-service outage does not take the whole
// daemon offline.
func (c *Client) GetRuntimeProfiles(ctx context.Context, workspaceID string) (*RuntimeProfilesResponse, error) {
	var resp RuntimeProfilesResponse
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/workspaces/%s/runtime-profiles", workspaceID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// defaultTerminalRetrySchedule is the backoff used by postJSONWithRetry for
// terminal task callbacks (CompleteTask / FailTask). N entries → N+1 attempts
// in the worst case (one immediate + N retries). Five backoffs totalling
// 124s is wide enough to ride out the short upstream blips we've seen
// (MUL-2780) without leaving the task stuck if the outage outlives the
// window.
var defaultTerminalRetrySchedule = []time.Duration{
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	64 * time.Second,
}

// Task messages and usage are reported on the execution path, so retries must
// fit inside the message flush call's five-second context. Both server writes
// are idempotent, making response-loss retries safe.
var taskReportRetrySchedule = []time.Duration{
	200 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
}

// retrySleep is the sleep used between retry attempts. Pulled into a package
// variable so tests can swap in an instant sleep without rewriting the
// caller's schedule.
var retrySleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isTransientError reports whether err looks like a hiccup that's likely to
// resolve on retry: connection / TLS / I/O errors at the transport layer
// (including client timeouts surfacing as context.DeadlineExceeded inside
// http.Client.Do), 5xx server responses, and 408/429 rate-limit-style 4xx
// codes. Other 4xx codes are treated as permanent — retrying a 400 (bad
// body) or 404 (task not found) only burns time.
//
// The caller is responsible for separately bailing on parent-context
// cancellation; this predicate cannot distinguish "the daemon is shutting
// down" from "the HTTP client timed out a single attempt" because both
// reach here as context errors wrapped by net/http.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	var reqErr *requestError
	if errors.As(err, &reqErr) {
		if reqErr.StatusCode >= 500 {
			return true
		}
		if reqErr.StatusCode == http.StatusRequestTimeout || reqErr.StatusCode == http.StatusTooManyRequests {
			return true
		}
		return false
	}
	return true
}

// postJSONWithRetry posts a JSON body with bounded exponential backoff,
// intended for "must reach the server" terminal callbacks (CompleteTask /
// FailTask). It retries transient errors per isTransientError and stops
// immediately on permanent 4xx responses so we don't burn the schedule on
// requests the server has already rejected.
//
// schedule controls the sleeps between attempts. With N entries the helper
// performs N+1 attempts in the worst case (one initial + N retries). The
// returned error is the last response from the server, so callers can still
// inspect it with isTransientError to decide whether to fall back to a
// different terminal call (e.g. complete → fail on permanent error only).
//
// The server-side CompleteTask / FailTask treat "already terminal" as an
// idempotent success (see service/task.go), so a duplicate replay from a
// retry is safe even if the server's prior response was lost in transit.
func (c *Client) postJSONWithRetry(ctx context.Context, path string, reqBody any, schedule []time.Duration) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return lastErr
			}
			return err
		}
		err := c.postJSON(ctx, path, reqBody, nil)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientError(err) {
			return err
		}
		if attempt >= len(schedule) {
			return err
		}
		if sleepErr := retrySleep(ctx, schedule[attempt]); sleepErr != nil {
			return err
		}
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, reqBody any, respBody any) error {
	var body io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	c.setIdentityHeaders(req)

	return c.executeRequest(req, path, respBody, "")
}

func requestResponseError(resp *http.Response, method, path string) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &requestError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
}

func (c *Client) executeRequest(req *http.Request, path string, respBody any, decodeContext string) error {
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return requestResponseError(resp, req.Method, path)
	}
	if respBody == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	err = json.NewDecoder(resp.Body).Decode(respBody)
	if err != nil && decodeContext != "" {
		return fmt.Errorf("%s: %w", decodeContext, err)
	}
	return err
}

func (c *Client) postJSON(ctx context.Context, path string, reqBody any, respBody any) error {
	return c.doJSON(ctx, http.MethodPost, path, reqBody, respBody)
}

func (c *Client) getJSON(ctx context.Context, path string, respBody any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, respBody)
}
