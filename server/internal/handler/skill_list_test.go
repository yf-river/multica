package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCreateSkillRejectsNonObjectConfig(t *testing.T) {
	name := "invalid-config-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM skill WHERE name = $1`, name)
	})
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/skills?workspace_id="+testWorkspaceID, map[string]any{
		"name":    name,
		"content": "# Invalid config fixture",
		"config":  []any{"not", "an", "object"},
	})
	testHandler.CreateSkill(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("CreateSkill non-object config: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)::int FROM skill WHERE workspace_id = $1 AND name = $2
	`, testWorkspaceID, name).Scan(&count); err != nil {
		t.Fatalf("count invalid-config skill: %v", err)
	}
	if count != 0 {
		t.Fatalf("CreateSkill persisted %d invalid-config rows", count)
	}
}

func TestUpdateSkillRejectsNonObjectConfig(t *testing.T) {
	skillID := insertHandlerTestSkill(t, "update-invalid-config", "# Existing skill")
	w := httptest.NewRecorder()
	req := newRequest("PATCH", "/api/skills/"+skillID+"?workspace_id="+testWorkspaceID, map[string]any{
		"config": []any{"not", "an", "object"},
	})
	req = withURLParam(req, "id", skillID)
	testHandler.UpdateSkill(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("UpdateSkill non-object config: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var config string
	if err := testPool.QueryRow(context.Background(), `SELECT config::text FROM skill WHERE id = $1`, skillID).Scan(&config); err != nil {
		t.Fatalf("read skill config: %v", err)
	}
	if config != "{}" {
		t.Fatalf("UpdateSkill changed config to %s", config)
	}
}

func TestDecodeSkillConfigRejectsNonObjectValues(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`null`), []byte(`[]`), []byte(`"string"`)} {
		if _, err := decodeSkillConfig(raw); err == nil {
			t.Fatalf("decodeSkillConfig(%s) expected an error", raw)
		}
	}
	config, err := decodeSkillConfig([]byte(`{"origin":{"type":"manual"}}`))
	if err != nil {
		t.Fatalf("decode object config: %v", err)
	}
	if _, ok := config["origin"]; !ok {
		t.Fatalf("decoded config = %#v, want origin", config)
	}
}

func TestListSkills_OmitsContent(t *testing.T) {
	skillID := insertHandlerTestSkill(t, "list-omits-content", strings.Repeat("a", 4096))

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/skills?workspace_id="+testWorkspaceID, nil)
	testHandler.ListSkills(w, req)
	if w.Code != 200 {
		t.Fatalf("ListSkills: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	assertSkillListSummary(t, "ListSkills", w, skillID)
}

func TestGetSkill_IncludesContent(t *testing.T) {
	body := "# detail body\nstill served on /api/skills/{id}"
	skillID := insertHandlerTestSkill(t, "detail-includes-content", body)

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/skills/"+skillID, nil)
	req = withURLParam(req, "id", skillID)
	testHandler.GetSkill(w, req)
	if w.Code != 200 {
		t.Fatalf("GetSkill: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GetSkill: failed to decode body: %v", err)
	}
	if got, _ := resp["content"].(string); got != body {
		t.Fatalf("GetSkill: expected content %q, got %q", body, got)
	}
}

func TestListAgentSkills_OmitsContent(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Handler Skill Summary Test", nil)
	skillID := insertHandlerTestSkill(t, "agent-skill-omits-content", strings.Repeat("b", 1024))
	if _, err := testPool.Exec(context.Background(),
		`INSERT INTO agent_skill (agent_id, skill_id) VALUES ($1, $2)`,
		agentID, skillID,
	); err != nil {
		t.Fatalf("attach skill to agent: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/agents/"+agentID+"/skills", nil)
	req = withURLParam(req, "id", agentID)
	testHandler.ListAgentSkills(w, req)
	if w.Code != 200 {
		t.Fatalf("ListAgentSkills: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	assertSkillListSummary(t, "ListAgentSkills", w, skillID)
}

func TestGetSkill_MalformedUUIDReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/skills/not-a-uuid", nil)
	req = withURLParam(req, "id", "not-a-uuid")
	testHandler.GetSkill(w, req)
	if w.Code != 400 {
		t.Fatalf("GetSkill malformed uuid: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func assertSkillListSummary(t *testing.T, operation string, w *httptest.ResponseRecorder, skillID string) {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("%s: failed to decode body: %v", operation, err)
	}
	for _, row := range rows {
		if row["id"] != skillID {
			continue
		}
		if _, ok := row["content"]; ok {
			t.Fatalf("%s: response must not include content: %v", operation, row)
		}
		for _, key := range []string{"id", "name", "description", "config", "created_at", "updated_at", "workspace_id"} {
			if _, ok := row[key]; !ok {
				t.Fatalf("%s: missing expected field %q: %v", operation, key, row)
			}
		}
		return
	}
	t.Fatalf("%s: inserted skill %s not in response", operation, skillID)
}

func insertHandlerTestSkill(t *testing.T, namePrefix, content string) string {
	t.Helper()
	name := namePrefix + "-" + t.Name()
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO skill (workspace_id, name, description, content, config, created_by)
		VALUES ($1, $2, $3, $4, '{}'::jsonb, $5)
		RETURNING id
	`, testWorkspaceID, name, "fixture", content, testUserID).Scan(&id); err != nil {
		t.Fatalf("insert skill: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM skill WHERE id = $1`, id)
	})
	return id
}
