package supervision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const agentLensEndpoint = "http://openapi.agentlens.woa.com/agentlens/llm/"
const agentLensSpace = "codebuddy-model-request-prod"

// AgentLensScope binds server-controlled credentials to one workspace.
// 1. Credentials never come from the model, request URL, or trace metadata.
type AgentLensScope struct {
	Workspace string `json:"workspace_id"`
	Token     string `json:"token"`
}

type agentLensFailure struct {
	Kind       string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *agentLensFailure) Error() string { return "AgentLens: " + e.Kind }

// lensBudget is shared by every query, including retries, in one preparation.
// 2. A preparation is bounded independently of the model investigation budget.
type lensBudget struct {
	Requests int
	Started  time.Time
	Reserve  func(context.Context) error
}

type agentLensClient struct {
	HTTP  *http.Client
	Scope AgentLensScope
	// endpoint is only overridden by package tests; production has no URL setting.
	endpoint string
}

// loadAgentLensScope rereads a private mounted file so rotation needs no restart.
// 3. Reject broad credentials rather than silently sharing private trace bodies.
func loadAgentLensScope(path, workspace string) (AgentLensScope, error) {
	var scope AgentLensScope
	if path == "" {
		return scope, &agentLensFailure{Kind: "credential_unconfigured"}
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return scope, &agentLensFailure{Kind: "credential_unavailable"}
	}
	f, err := os.Open(path)
	if err != nil {
		return scope, &agentLensFailure{Kind: "credential_unavailable"}
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 || stat.Size() > 16384 {
		return scope, &agentLensFailure{Kind: "credential_file_permissions"}
	}
	dec := json.NewDecoder(io.LimitReader(f, 16385))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&scope); err != nil || scope.Workspace != workspace || strings.TrimSpace(scope.Token) == "" {
		return AgentLensScope{}, &agentLensFailure{Kind: "credential_scope_mismatch"}
	}
	if err = dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return AgentLensScope{}, &agentLensFailure{Kind: "credential_invalid"}
	}
	return scope, nil
}

// query returns raw provider data; callers preserve unknown fields and units.
// 4. No redirect, credential-bearing diagnostic, or unbounded response is allowed.
func (c *agentLensClient) query(ctx context.Context, budget *lensBudget, method string, fields map[string]any) (json.RawMessage, error) {
	if method != "traceDetail" && method != "spanDetail" && method != "traceList" {
		return nil, &agentLensFailure{Kind: "unsupported_method"}
	}
	if strings.TrimSpace(c.Scope.Token) == "" {
		return nil, &agentLensFailure{Kind: "credential_unconfigured"}
	}
	if budget.Started.IsZero() {
		budget.Started = time.Now()
	}
	deadline := budget.Started.Add(120 * time.Second)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	params := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		params[k] = v
	}
	params["space_enname"] = agentLensSpace
	body, err := json.Marshal(params)
	if err != nil {
		return nil, &agentLensFailure{Kind: "invalid_parameters"}
	}
	endpoint := c.endpoint
	if endpoint == "" {
		endpoint = agentLensEndpoint
	}
	client := http.Client{Timeout: 15 * time.Second}
	if c.HTTP != nil {
		client.Transport = c.HTTP.Transport
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for attempt := 0; attempt < 3; attempt++ {
		if budget.Requests >= 40 || ctx.Err() != nil {
			return nil, &agentLensFailure{Kind: "budget_exhausted"}
		}
		if budget.Reserve != nil {
			if err := budget.Reserve(ctx); err != nil {
				return nil, err
			}
		}
		budget.Requests++
		requestCtx, requestCancel := context.WithTimeout(ctx, 15*time.Second)
		req, reqErr := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint+method, bytes.NewReader(body))
		if reqErr != nil {
			requestCancel()
			return nil, &agentLensFailure{Kind: "invalid_parameters"}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Agentlens-Token", c.Scope.Token)
		resp, callErr := client.Do(req)
		var result json.RawMessage
		var failure *agentLensFailure
		if callErr != nil {
			failure = &agentLensFailure{Kind: "transport_error", Retryable: true}
		} else {
			result, failure = readLensResponse(resp)
			if failure != nil {
				failure.RetryAfter = lensRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			}
			resp.Body.Close()
		}
		requestCancel()
		if failure == nil {
			return result, nil
		}
		if !failure.Retryable || attempt == 2 {
			return nil, failure
		}
		// Respect provider backoff without allowing it to extend the round deadline.
		delay := time.Duration(attempt+1) * 100 * time.Millisecond
		if failure.RetryAfter > delay {
			delay = failure.RetryAfter
		}
		if delay >= time.Until(deadline) {
			return nil, &agentLensFailure{Kind: "budget_exhausted"}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, &agentLensFailure{Kind: "budget_exhausted"}
		case <-timer.C:
		}
	}
	return nil, &agentLensFailure{Kind: "request_failed"}
}

// 5. Provider messages can contain private identifiers and credentials; expose categories only.
func readLensResponse(resp *http.Response) (json.RawMessage, *agentLensFailure) {
	if resp.StatusCode != http.StatusOK {
		return nil, &agentLensFailure{Kind: fmt.Sprintf("http_%d", resp.StatusCode), Retryable: resp.StatusCode == 429 || resp.StatusCode >= 500}
	}
	const limit = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, &agentLensFailure{Kind: "response_read", Retryable: true}
	}
	if len(body) > limit {
		return nil, &agentLensFailure{Kind: "response_too_large"}
	}
	var envelope struct {
		Code json.RawMessage `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Code) == 0 {
		return nil, &agentLensFailure{Kind: "unknown_response_format"}
	}
	code := strings.Trim(string(envelope.Code), "\"")
	if code != "0" {
		return nil, &agentLensFailure{Kind: "provider_error_" + safeLensCode(code), Retryable: code == "50001" || code == "ErrLimit"}
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(envelope.Data, &data) != nil || len(data) == 0 {
		return nil, &agentLensFailure{Kind: "unknown_response_format"}
	}
	if _, ok := data["fallback_text"]; ok {
		return nil, &agentLensFailure{Kind: "space_unavailable"}
	}
	return envelope.Data, nil
}

func safeLensCode(code string) string {
	switch code {
	case "220002", "ErrAuth", "ErrParam", "ErrLimit", "50001", "10000", "-1":
		return code
	default:
		return "unknown"
	}
}

// 6. Retry-After supports both HTTP forms; excessive delays exhaust the existing budget.
func lensRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}
