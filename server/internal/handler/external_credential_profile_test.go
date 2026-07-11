package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExternalCredentialProfileSecretRefRedactedAndListed(t *testing.T) {
	name := fmt.Sprintf("tapd-profile-%d", time.Now().UnixNano())

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles", map[string]any{
		"provider":     "tapd",
		"name":         name,
		"secret_ref":   "env:TAPD_TOKEN",
		"capabilities": map[string]any{"markdown_wiki": true},
	})
	testHandler.CreateExternalCredentialProfile(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateExternalCredentialProfile: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "env:TAPD_TOKEN") {
		t.Fatalf("response leaked raw secret_ref: %s", w.Body.String())
	}

	var created ExternalCredentialProfileResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM external_credential_profile WHERE id = $1`, created.ID)
	})
	if created.Scope != "account" {
		t.Fatalf("scope = %q, want account", created.Scope)
	}
	if created.Provider != "tapd" {
		t.Fatalf("provider = %q, want tapd", created.Provider)
	}
	if created.SecretBinding["mode"] != "secret_ref" {
		t.Fatalf("secret_binding mode = %+v", created.SecretBinding)
	}
	if created.SecretBinding["redacted"] != true {
		t.Fatalf("secret_binding should be redacted: %+v", created.SecretBinding)
	}

	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/external-credential-profiles?provider=tapd", nil)
	testHandler.ListExternalCredentialProfiles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListExternalCredentialProfiles: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		Profiles []ExternalCredentialProfileResponse `json:"profiles"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	found := false
	for _, profile := range listResp.Profiles {
		if profile.ID == created.ID {
			found = true
			if profile.UserID != testUserID {
				t.Fatalf("listed profile user_id = %q, want %q", profile.UserID, testUserID)
			}
		}
	}
	if !found {
		t.Fatalf("created profile missing from list response: %+v", listResp.Profiles)
	}
}

func TestExternalCredentialProfileRawTokenRequiresEncryption(t *testing.T) {
	previous := testHandler.ExternalCredentialBox
	testHandler.ExternalCredentialBox = nil
	t.Cleanup(func() { testHandler.ExternalCredentialBox = previous })

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles", map[string]any{
		"provider": "tapd",
		"name":     fmt.Sprintf("tapd-token-%d", time.Now().UnixNano()),
		"token":    "tapd-secret-token",
	})
	testHandler.CreateExternalCredentialProfile(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when encryption is not configured, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "tapd-secret-token") {
		t.Fatalf("error response leaked raw token: %s", w.Body.String())
	}
}

func TestExternalCredentialProfileTestVerifiesTAPDConnection(t *testing.T) {
	var sawAuth bool
	tapdAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workspaces" {
			t.Fatalf("unexpected TAPD path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer tapd-verified-token" && r.Header.Get("Via") == "mcp" {
			sawAuth = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer tapdAPI.Close()
	t.Setenv("TAPD_API_BASE_URL", tapdAPI.URL)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles/test", map[string]any{
		"provider": "tapd",
		"token":    "tapd-verified-token",
	})
	testHandler.TestExternalCredentialProfile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("TestExternalCredentialProfile: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !sawAuth {
		t.Fatal("TAPD credential test did not send credential-backed authorization headers")
	}
	var resp TestExternalCredentialProfileResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "verified" || resp.LastError != "" {
		t.Fatalf("unexpected TAPD test response: %+v", resp)
	}
	if strings.Contains(w.Body.String(), "tapd-verified-token") {
		t.Fatalf("response leaked raw token: %s", w.Body.String())
	}
}

func TestExternalCredentialProfileTestReportsTAPDUnauthorized(t *testing.T) {
	tapdAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer tapdAPI.Close()
	t.Setenv("TAPD_API_BASE_URL", tapdAPI.URL)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles/test", map[string]any{
		"provider": "tapd",
		"token":    "expired-token",
	})
	testHandler.TestExternalCredentialProfile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("TestExternalCredentialProfile: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp TestExternalCredentialProfileResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "failed" || !strings.Contains(resp.LastError, "无效或已过期") {
		t.Fatalf("unexpected TAPD unauthorized response: %+v", resp)
	}
	if strings.Contains(w.Body.String(), "expired-token") {
		t.Fatalf("response leaked raw token: %s", w.Body.String())
	}
}

func TestMergeMCPServerEnvCreatesDefaultServerEntry(t *testing.T) {
	config := normalizeMCPConfigForInjection(nil)
	changed := mergeMCPServerEnv(config, tapdMCPServerName, map[string]string{"TAPD_ACCESS_TOKEN": "tapd-secret"})
	if !changed {
		t.Fatal("mergeMCPServerEnv should report a change")
	}
	servers := config["mcpServers"].(map[string]any)
	entry := servers[tapdMCPServerName].(map[string]any)
	if entry["command"] != "uvx" {
		t.Fatalf("command = %v, want uvx", entry["command"])
	}
	args, ok := entry["args"].([]any)
	if !ok || len(args) == 0 || args[0] != "mcp-server-tapd" {
		t.Fatalf("args = %#v, want mcp-server-tapd command args", entry["args"])
	}
	env := entry["env"].(map[string]any)
	if env["TAPD_ACCESS_TOKEN"] != "tapd-secret" {
		t.Fatalf("TAPD_ACCESS_TOKEN = %v, want injected token", env["TAPD_ACCESS_TOKEN"])
	}
}

func TestExternalCredentialProfileSupportsGongfengProvider(t *testing.T) {
	name := fmt.Sprintf("gongfeng-profile-%d", time.Now().UnixNano())

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles", map[string]any{
		"provider":   "gongfeng",
		"name":       name,
		"secret_ref": "env:GONGFENG_TOKEN",
		"capabilities": map[string]any{
			"repository_read": true,
			"branch_read":     true,
		},
	})
	testHandler.CreateExternalCredentialProfile(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateExternalCredentialProfile(gongfeng): expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "env:GONGFENG_TOKEN") {
		t.Fatalf("response leaked raw gongfeng secret_ref: %s", w.Body.String())
	}

	var created ExternalCredentialProfileResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM external_credential_profile WHERE id = $1`, created.ID)
	})
	if created.Provider != "gongfeng" || created.Scope != "account" {
		t.Fatalf("created profile = %+v", created)
	}
	if created.SecretBinding["mode"] != "secret_ref" || created.SecretBinding["redacted"] != true {
		t.Fatalf("secret_binding = %+v", created.SecretBinding)
	}
}

func TestExternalCredentialProfileVerifyMissingEnvSecretRef(t *testing.T) {
	t.Setenv("GONGFENG_ACCESS_TOKEN_EXPECTED_MISSING", "")
	name := fmt.Sprintf("gongfeng-missing-env-%d", time.Now().UnixNano())

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles", map[string]any{
		"provider":   "gongfeng",
		"name":       name,
		"secret_ref": "env:GONGFENG_ACCESS_TOKEN_EXPECTED_MISSING",
		"verify_now": true,
	})
	testHandler.CreateExternalCredentialProfile(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateExternalCredentialProfile(gongfeng): expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "env:GONGFENG_ACCESS_TOKEN_EXPECTED_MISSING") {
		t.Fatalf("response leaked raw secret_ref: %s", w.Body.String())
	}

	var created ExternalCredentialProfileResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM external_credential_profile WHERE id = $1`, created.ID)
	})
	if created.Status != "failed" {
		t.Fatalf("status = %q, want failed; response=%+v", created.Status, created)
	}
	if !strings.Contains(created.LastError, "服务器环境变量 GONGFENG_ACCESS_TOKEN_EXPECTED_MISSING 未设置") {
		t.Fatalf("last_error = %q", created.LastError)
	}
}

func TestExternalCredentialProfileTestMissingEnvDoesNotPersist(t *testing.T) {
	t.Setenv("GONGFENG_ACCESS_TOKEN_TEST_MISSING", "")
	var before int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM external_credential_profile WHERE user_id = $1 AND provider = 'gongfeng'`, testUserID).Scan(&before); err != nil {
		t.Fatalf("count profiles before: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles/test", map[string]any{
		"provider":   "gongfeng",
		"secret_ref": "env:GONGFENG_ACCESS_TOKEN_TEST_MISSING",
	})
	testHandler.TestExternalCredentialProfile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("TestExternalCredentialProfile: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "env:GONGFENG_ACCESS_TOKEN_TEST_MISSING") {
		t.Fatalf("response leaked raw secret_ref: %s", w.Body.String())
	}
	var result TestExternalCredentialProfileResponse
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode test response: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed; response=%+v", result.Status, result)
	}
	if !strings.Contains(result.LastError, "服务器环境变量 GONGFENG_ACCESS_TOKEN_TEST_MISSING 未设置") {
		t.Fatalf("last_error = %q", result.LastError)
	}
	var after int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM external_credential_profile WHERE user_id = $1 AND provider = 'gongfeng'`, testUserID).Scan(&after); err != nil {
		t.Fatalf("count profiles after: %v", err)
	}
	if after != before {
		t.Fatalf("test endpoint persisted profile rows: before=%d after=%d", before, after)
	}
}

func TestExternalCredentialProfileTestGongfengEnvTokenHitsAPI(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Fatalf("unexpected probe path: %s", r.URL.Path)
		}
		if r.Header.Get("PRIVATE-TOKEN") != "gongfeng-ok" {
			t.Fatalf("PRIVATE-TOKEN header = %q, want gongfeng-ok", r.Header.Get("PRIVATE-TOKEN"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"username":"tester"}`))
	}))
	t.Cleanup(api.Close)
	t.Setenv("GONGFENG_API_BASE", api.URL)
	t.Setenv("GONGFENG_ACCESS_TOKEN_TEST_OK", "gongfeng-ok")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles/test", map[string]any{
		"provider":   "gongfeng",
		"secret_ref": "env:GONGFENG_ACCESS_TOKEN_TEST_OK",
	})
	testHandler.TestExternalCredentialProfile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("TestExternalCredentialProfile: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "env:GONGFENG_ACCESS_TOKEN_TEST_OK") || strings.Contains(w.Body.String(), "gongfeng-ok") {
		t.Fatalf("response leaked credential material: %s", w.Body.String())
	}
	var result TestExternalCredentialProfileResponse
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode test response: %v", err)
	}
	if result.Status != "verified" {
		t.Fatalf("status = %q, want verified; response=%+v", result.Status, result)
	}
	if result.LastError != "" {
		t.Fatalf("last_error = %q, want empty", result.LastError)
	}
}

func TestCreateTapdIssueInheritsAccountCredentialProfile(t *testing.T) {
	name := fmt.Sprintf("tapd-default-%d", time.Now().UnixNano())

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles", map[string]any{
		"provider":   "tapd",
		"name":       name,
		"secret_ref": "env:TAPD_TOKEN",
	})
	testHandler.CreateExternalCredentialProfile(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateExternalCredentialProfile: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var profile ExternalCredentialProfileResponse
	if err := json.NewDecoder(w.Body).Decode(&profile); err != nil {
		t.Fatalf("decode profile response: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM external_credential_profile WHERE id = $1`, profile.ID)
	})

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":    "TAPD profile inherited issue",
		"status":   "todo",
		"priority": "medium",
		"metadata": map[string]any{
			"source_provider": "tapd",
			"tapd_workspace":  "47654106",
			"tapd_wiki_id":    "1147654106001004154",
		},
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "env:TAPD_TOKEN") {
		t.Fatalf("issue response leaked profile secret_ref: %s", w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	if got := issue.Metadata["source_credential_scope"]; got != "account" {
		t.Fatalf("source_credential_scope = %T %v", got, got)
	}
	if got := issue.Metadata["source_credential_inheritance"]; got != "task_creator_or_trigger_user" {
		t.Fatalf("source_credential_inheritance = %T %v", got, got)
	}
	if got := issue.Metadata["source_credential_profile_id"]; got != profile.ID {
		t.Fatalf("source_credential_profile_id = %T %v, want %s", got, got, profile.ID)
	}
	if got := issue.Metadata["source_fetch_provider"]; got != "tapd_mcp" {
		t.Fatalf("source_fetch_provider = %T %v", got, got)
	}
	if got := issue.Metadata["source_fetch_status"]; got != "pending_mcp_fetch" {
		t.Fatalf("source_fetch_status = %T %v", got, got)
	}
}

func TestCreateTapdIssueMarksMissingAccountCredentialProfile(t *testing.T) {
	if _, err := testPool.Exec(context.Background(),
		`DELETE FROM external_credential_profile WHERE user_id = $1 AND provider = 'tapd'`,
		testUserID,
	); err != nil {
		t.Fatalf("clear tapd profiles: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":    "TAPD missing profile issue",
		"status":   "todo",
		"priority": "medium",
		"metadata": map[string]any{
			"source_provider": "tapd",
			"tapd_workspace":  "47654106",
			"tapd_wiki_id":    "1147654106001004154",
		},
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	if got := issue.Metadata["source_fetch_status"]; got != "blocked_missing_profile" {
		t.Fatalf("source_fetch_status = %T %v", got, got)
	}
	if got := issue.Metadata["source_credential_scope"]; got != "account" {
		t.Fatalf("source_credential_scope = %T %v", got, got)
	}
}

func TestClaimTaskIncludesTapdSourceContextWithAccountCredential(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TAPD_TOKEN", "tapd-claim-secret")
	runtimeID := createClaimReclaimRuntime(t, ctx, "tapd source context runtime")

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id, mcp_config
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'personal', 1, $4, $5::jsonb)
		RETURNING id
	`, testWorkspaceID, "tapd source context agent", runtimeID, testUserID, `{
		"mcpServers": {
			"mcp-server-tapd": {
				"command": "uvx",
				"args": ["mcp-server-tapd"]
			}
		}
	}`).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { mustExec(t, context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })

	profileName := fmt.Sprintf("tapd-claim-%d", time.Now().UnixNano())
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles", map[string]any{
		"provider":   "tapd",
		"name":       profileName,
		"secret_ref": "env:TAPD_TOKEN",
	})
	testHandler.CreateExternalCredentialProfile(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateExternalCredentialProfile: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var profile ExternalCredentialProfileResponse
	if err := json.NewDecoder(w.Body).Decode(&profile); err != nil {
		t.Fatalf("decode profile response: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM external_credential_profile WHERE id = $1`, profile.ID)
	})

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "TAPD source context claim issue",
		"status":        "todo",
		"priority":      "medium",
		"assignee_type": "agent",
		"assignee_id":   agentID,
		"metadata": map[string]any{
			"source_provider":    "tapd",
			"source_url":         "https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004154",
			"tapd_workspace_id":  "47654106",
			"tapd_resource_type": "markdown_wiki",
			"tapd_resource_id":   "1147654106001004154",
		},
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil,
		testWorkspaceID, "tapd-source-context-claim")
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "env:TAPD_TOKEN") {
		t.Fatalf("claim response leaked raw secret_ref: %s", w.Body.String())
	}
	var claim struct {
		Task *struct {
			SourceContext *TaskSourceContext `json:"source_context"`
			Agent         *TaskAgentData     `json:"agent"`
		} `json:"task"`
	}
	if err := json.NewDecoder(w.Body).Decode(&claim); err != nil {
		t.Fatalf("decode claim response: %v", err)
	}
	if claim.Task == nil || claim.Task.SourceContext == nil {
		t.Fatalf("claim response missing source_context: %s", w.Body.String())
	}
	source := claim.Task.SourceContext
	if source.Provider != "tapd" {
		t.Fatalf("source provider = %q", source.Provider)
	}
	if source.TAPD == nil {
		t.Fatalf("source tapd context missing: %+v", source)
	}
	if source.TAPD.WorkspaceID != "47654106" || source.TAPD.ResourceType != "markdown_wiki" || source.TAPD.ResourceID != "1147654106001004154" {
		t.Fatalf("unexpected TAPD context: %+v", source.TAPD)
	}
	if source.TAPD.FetchProvider != "tapd_mcp" || source.TAPD.FetchStatus != "pending_mcp_fetch" {
		t.Fatalf("unexpected TAPD fetch context: %+v", source.TAPD)
	}
	credential := source.ExternalCredentials["tapd"]
	if !credential.Configured {
		t.Fatalf("tapd credential not configured: %+v", credential)
	}
	if credential.Scope != "account" || credential.Inheritance != "task_creator_or_trigger_user" {
		t.Fatalf("unexpected credential scope/inheritance: %+v", credential)
	}
	if credential.ProfileID != profile.ID || credential.MCPServer != "mcp-server-tapd" {
		t.Fatalf("unexpected credential context: %+v, profile=%s", credential, profile.ID)
	}
	sourceSerialized, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}
	if strings.Contains(string(sourceSerialized), "tapd-claim-secret") || strings.Contains(string(sourceSerialized), "env:TAPD_TOKEN") {
		t.Fatalf("source_context leaked credential material: %s", sourceSerialized)
	}
	if claim.Task.Agent == nil {
		t.Fatalf("claim response missing agent")
	}
	var mcp struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(claim.Task.Agent.McpConfig, &mcp); err != nil {
		t.Fatalf("decode injected mcp_config: %v: %s", err, string(claim.Task.Agent.McpConfig))
	}
	server := mcp.MCPServers["mcp-server-tapd"]
	if server.Command != "uvx" || len(server.Args) != 1 || server.Args[0] != "mcp-server-tapd" {
		t.Fatalf("mcp server command was not preserved: %+v", server)
	}
	if got := server.Env["TAPD_ACCESS_TOKEN"]; got != "tapd-claim-secret" {
		t.Fatalf("TAPD_ACCESS_TOKEN injection = %q, want resolved env token", got)
	}
}
