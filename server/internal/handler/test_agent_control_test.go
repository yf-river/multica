package handler

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/testagents"
	"github.com/multica-ai/multica/server/internal/testutil"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTestAgentControlRevisionAndScope(t *testing.T) {
	if testHandler == nil {
		t.Fatal("需要真实隔离数据库")
	}
	t.Setenv("MULTICA_ENV_ID", "prod")
	t.Setenv("MULTICA_TEST_AGENT_CONTROL_WORKSPACE_ID", testWorkspaceID)
	dbfx.Cleanup(t, `DELETE FROM test_agent_control WHERE workspace_id=$1`, testWorkspaceID)
	call := func(body map[string]any) *testutil.Response {
		return testutil.Call(t, testHandler.TestAgentControl, newRequest("POST", "/api/test-agent-control", body))
	}
	previousRefresh := testHandler.RefreshTestAgents
	t.Cleanup(func() { testHandler.RefreshTestAgents = previousRefresh })
	testHandler.RefreshTestAgents = nil
	call(map[string]any{"action": "refresh", "name": "default"}).Want(503)
	refreshes := 0
	testHandler.RefreshTestAgents = func() { refreshes++ }
	call(map[string]any{"action": "refresh", "name": "default"}).Want(202)
	if refreshes != 1 {
		t.Fatal("refresh did not wake control")
	}

	flash := testAgentPolicy{Provider: "codebuddy", Model: "deepseek-v4.1-flash"}
	codex := testAgentPolicy{Provider: "codex", Model: "gpt-6.1-sol", Thinking: "low", Tier: "default"}
	call(map[string]any{"action": "report", "name": "prod", "observed": map[string]any{}}).Want(400)
	call(map[string]any{"action": "configure", "name": "default", "revision": 1, "policy": flash}).Want(200)
	call(map[string]any{"action": "report", "name": "verify-1", "observed": map[string]any{"agents": []map[string]string{{"id": "known"}}}}).Want(200)
	call(map[string]any{"action": "configure", "name": "verify-1", "revision": 1, "agent_id": "foreign", "policy": codex}).Want(400)
	call(map[string]any{"action": "configure", "name": "verify-1", "revision": 1, "agent_id": "known", "policy": codex}).Want(200)
	call(map[string]any{"action": "configure", "name": "verify-1", "revision": 1, "policy": flash}).Want(409)
	call(map[string]any{"action": "configure", "name": "default", "revision": 2, "policy": codex}).Want(200)
	call(map[string]any{"action": "report", "name": "verify-1", "observed": map[string]any{"revision": 2, "status": "applied"}}).Want(200)
	var raw []byte
	dbfx.QueryRow(t, `SELECT desired FROM test_agent_control WHERE workspace_id=$1 AND name='verify-1'`, testWorkspaceID).Scan(&raw)
	var desired struct {
		Policy    testAgentPolicy            `json:"policy"`
		Overrides map[string]testAgentPolicy `json:"overrides"`
	}
	json.Unmarshal(raw, &desired)
	if desired.Policy != codex || len(desired.Overrides) != 0 {
		t.Fatal("新默认必须覆盖所有环境且迟到回报不能回滚配置")
	}
	req := newRequest("GET", "/api/test-agent-control", nil)
	req.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.TestAgentControl, req).Want(403)
	dbfx.Exec(t, `UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID)
	call(map[string]any{"action": "configure", "name": "default", "revision": 3, "policy": flash}).Want(403)
	dbfx.Exec(t, `UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID)
	t.Setenv("MULTICA_TEST_AGENT_CONTROL_WORKSPACE_ID", "other")
	call(map[string]any{"action": "configure", "name": "default", "revision": 3, "policy": flash}).Want(http.StatusForbidden)
	t.Setenv("MULTICA_TEST_AGENT_CONTROL_WORKSPACE_ID", testWorkspaceID)
	t.Setenv("MULTICA_ENV_ID", "verify-1")
	call(map[string]any{"action": "configure", "name": "default", "revision": 3, "policy": flash}).Want(http.StatusForbidden)
}
func TestTestAgentPolicyAllowlist(t *testing.T) {
	for _, p := range []testAgentPolicy{{Provider: "codex", Model: ""}, {Provider: "codex", Model: "gpt 6.1"}, {Provider: "codebuddy", Runtime: "CodeBuddy", Model: "pro"}} {
		if p.Valid() {
			t.Fatal("接受了非法配置", p)
		}
	}
	for _, p := range []testAgentPolicy{
		{Provider: "codebuddy", Model: "deepseek-v4.1-flash"},
		{Provider: "codex", Model: "gpt-6.1-sol", Thinking: "low", Tier: "default"},
		{Provider: "dsh", Runtime: "dsh", Model: "codebuddy/deepseek-v4.1-flash"},
	} {
		if !p.Valid() {
			t.Fatal("拒绝了合法配置", p)
		}
	}
}

func TestTestAgentCreationUsesAppliedDefault(t *testing.T) {
	if testHandler == nil {
		t.Fatal("需要真实隔离数据库")
	}
	t.Setenv("MULTICA_ENV_ID", "verify-2")
	dbfx.Cleanup(t, `DELETE FROM test_agent_control WHERE workspace_id=$1`, testWorkspaceID)
	dbfx.Cleanup(t, `UPDATE agent_runtime SET daemon_id=NULL WHERE id=$1`, testRuntimeID)
	dbfx.Exec(t, `UPDATE agent_runtime SET daemon_id='test-local-daemon' WHERE id=$1`, testRuntimeID)
	dbfx.Exec(t, `INSERT INTO test_agent_control(workspace_id,name,desired,observed) VALUES($1,'verify-2','{"policy":{"provider":"codebuddy","model":"deepseek-v4.1-flash","thinking_level":"","service_tier":""},"overrides":{}}','{"daemon_id":"test-local-daemon"}')`, testWorkspaceID)
	body := map[string]any{"name": "central-default-test", "runtime_id": testRuntimeID, "model": "unapproved-model", "thinking_level": "high", "service_tier": "priority"}
	response := testutil.Call(t, testHandler.CreateAgent, newRequest("POST", "/api/agents", body)).Want(201)
	_ = response
	var model, thinking, tier string
	dbfx.QueryRow(t, `SELECT model,COALESCE(thinking_level,''),COALESCE(service_tier,'') FROM agent WHERE workspace_id=$1 AND name='central-default-test'`, testWorkspaceID).Scan(&model, &thinking, &tier)
	if model != "deepseek-v4.1-flash" || thinking != "" || tier != "" {
		t.Fatal("新建测试 Agent 未使用环境默认值", model, thinking, tier)
	}
	body["name"] = "central-default-override"
	body["custom_args"] = []string{"--model=pro"}
	testutil.Call(t, testHandler.CreateAgent, newRequest("POST", "/api/agents", body)).Want(400)
	var codexRuntime, createdID string
	dbfx.QueryRow(t, `INSERT INTO agent_runtime(workspace_id,daemon_id,name,runtime_mode,provider,status,owner_id) VALUES($1,'test-local-daemon','Codex control test','cloud','codex','online',$2) RETURNING id`, testWorkspaceID, testUserID).Scan(&codexRuntime)
	dbfx.QueryRow(t, `SELECT id::text FROM agent WHERE workspace_id=$1 AND name='central-default-test'`, testWorkspaceID).Scan(&createdID)
	dbfx.Exec(t, `UPDATE test_agent_control SET desired='{"policy":{"provider":"codex","model":"gpt-6.1-sol","thinking_level":"low","service_tier":"default"},"overrides":{}}' WHERE workspace_id=$1 AND name='verify-2'`, testWorkspaceID)
	if err := testagents.Apply(context.Background(), testPool, testWorkspaceID, createdID); err != nil {
		t.Fatal(err)
	}
	var actualRuntime string
	dbfx.QueryRow(t, `SELECT runtime_id::text,model,thinking_level,service_tier FROM agent WHERE id=$1`, createdID).Scan(&actualRuntime, &model, &thinking, &tier)
	if actualRuntime != codexRuntime || model != "gpt-6.1-sol" || thinking != "low" || tier != "default" {
		t.Fatal("托管保存入口未应用当前配置")
	}
	delete(body, "custom_args")
	body["name"] = "codex-default-created"
	testutil.Call(t, testHandler.CreateAgent, newRequest("POST", "/api/agents", body)).Want(201)
	cleanupMika(t)
	recorder := httptest.NewRecorder()
	mika, created, ok := testHandler.resolveMikaAgent(recorder, newRequest("POST", "/api/agents/mika", nil), testWorkspaceID, testUserID, createMikaAgentRequest{RuntimeID: testRuntimeID, Model: "deepseek-v4.1-flash"})
	if !ok || !created || mika.Model.String != "gpt-6.1-sol" || mika.ThinkingLevel.String != "low" || mika.ServiceTier.String != "default" {
		t.Fatalf("内置 Mika 未采用测试默认：%s", recorder.Body.String())
	}
	// 新工作区由本环境配置提供默认值，目标 runtime 仍严格属于当前工作区。
	otherWorkspace := "00000000-0000-0000-0000-000000000085"
	dbfx.Cleanup(t, `DELETE FROM test_agent_control WHERE workspace_id=$1`, otherWorkspace)
	dbfx.Exec(t, `UPDATE test_agent_control SET workspace_id=$2 WHERE workspace_id=$1 AND name='verify-2'`, testWorkspaceID, otherWorkspace)
	selection, enabled, err := testagents.Resolve(context.Background(), testPool, testWorkspaceID, "")
	if err != nil || !enabled || selection.RuntimeID != codexRuntime || selection.Model != "gpt-6.1-sol" {
		t.Fatal("新工作区未继承同一环境默认", err)
	}
	t.Setenv("MULTICA_ENV_ID", "prod")
	request := CreateAgentRequest{Model: "user-managed", RuntimeID: testRuntimeID}
	if !testHandler.applyTestAgentDefault(nil, newRequest("POST", "/api/agents", nil), testWorkspaceID, &request) || request.Model != "user-managed" {
		t.Fatal("影响了生产默认值")
	}
}
