package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAgentToResponseRejectsNonObjectRuntimeConfig(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`null`), []byte(`[]`), []byte(`"string"`)} {
		if _, err := agentToResponse(db.Agent{RuntimeConfig: raw, CustomEnv: []byte(`{}`), CustomArgs: []byte(`[]`)}); err == nil {
			t.Fatalf("agentToResponse runtime_config=%s expected an error", raw)
		}
	}
}

func TestAgentToResponseAcceptsObjectRuntimeConfig(t *testing.T) {
	response, err := agentToResponse(db.Agent{RuntimeConfig: []byte(`{"provider":"codex"}`), CustomEnv: []byte(`{}`), CustomArgs: []byte(`[]`)})
	if err != nil {
		t.Fatalf("agentToResponse: %v", err)
	}
	if response.RuntimeConfig["provider"] != "codex" {
		t.Fatalf("runtime_config = %#v", response.RuntimeConfig)
	}
}

func TestAgentToResponseRejectsNonObjectMCPConfig(t *testing.T) {
	if _, err := agentToResponse(db.Agent{
		RuntimeConfig: []byte(`{}`),
		CustomEnv:     []byte(`{}`),
		CustomArgs:    []byte(`[]`),
		McpConfig:     []byte(`[]`),
	}); err == nil {
		t.Fatal("agentToResponse expected non-object mcp_config error")
	}
}

func TestAgentToResponseRejectsInvalidCustomState(t *testing.T) {
	for _, agent := range []db.Agent{
		{RuntimeConfig: []byte(`{}`), CustomEnv: []byte(`[]`), CustomArgs: []byte(`[]`)},
		{RuntimeConfig: []byte(`{}`), CustomEnv: []byte(`{}`), CustomArgs: []byte(`{}`)},
	} {
		if _, err := agentToResponse(agent); err == nil {
			t.Fatalf("agentToResponse custom_env=%s custom_args=%s expected an error", agent.CustomEnv, agent.CustomArgs)
		}
	}
}

func TestUnmarshalCustomEnvRejectsInvalidPersistence(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`null`), []byte(`[]`), []byte(`{"KEY":1}`)} {
		if _, err := unmarshalCustomEnv(db.Agent{CustomEnv: raw}); err == nil {
			t.Fatalf("unmarshalCustomEnv(%s) expected an error", raw)
		}
	}
	env, err := unmarshalCustomEnv(db.Agent{CustomEnv: []byte(`{"KEY":"value"}`)})
	if err != nil {
		t.Fatalf("unmarshalCustomEnv valid object: %v", err)
	}
	if env["KEY"] != "value" {
		t.Fatalf("custom env = %#v", env)
	}
}

func TestMaskGatewayTokenReplacesNonEmpty(t *testing.T) {
	t.Parallel()

	rc := map[string]any{
		"mode": "gateway",
		"gateway": map[string]any{
			"host":  "gw.internal",
			"port":  float64(18789),
			"token": "real-secret",
			"tls":   true,
		},
	}
	maskGatewayToken(rc)
	gw := rc["gateway"].(map[string]any)
	if gw["token"] != runtimeConfigGatewayTokenMask {
		t.Errorf("token: got %v, want %q", gw["token"], runtimeConfigGatewayTokenMask)
	}
	if gw["host"] != "gw.internal" {
		t.Errorf("host must not be touched, got %v", gw["host"])
	}
}

func TestMaskGatewayTokenSkipsEmptyToken(t *testing.T) {
	t.Parallel()

	rc := map[string]any{
		"gateway": map[string]any{
			"host": "gw.internal",
			"port": float64(18789),
		},
	}
	maskGatewayToken(rc)
	gw := rc["gateway"].(map[string]any)
	if _, present := gw["token"]; present {
		t.Errorf("empty token must not gain a mask, got %v", gw["token"])
	}
}

func TestMaskGatewayTokenNoOpOnNonOpenclawShape(t *testing.T) {
	t.Parallel()

	rc := map[string]any{"some_other_key": "value"}
	maskGatewayToken(rc)
	if _, present := rc["gateway"]; present {
		t.Errorf("must not synthesise gateway key, got %v", rc)
	}
}

func TestPreserveMaskedGatewayTokenRestoresFromPersisted(t *testing.T) {
	t.Parallel()

	persisted := []byte(`{"mode":"gateway","gateway":{"token":"real-secret","host":"gw.internal"}}`)
	incoming := map[string]any{
		"mode": "gateway",
		"gateway": map[string]any{
			"host":  "gw.internal",
			"port":  float64(18789),
			"token": runtimeConfigGatewayTokenMask,
		},
	}
	preserveMaskedGatewayToken(incoming, persisted)
	gw := incoming["gateway"].(map[string]any)
	if gw["token"] != "real-secret" {
		t.Errorf("token should be restored from persisted row, got %v", gw["token"])
	}
}

func TestPreserveMaskedGatewayTokenPassesThroughRealValue(t *testing.T) {
	t.Parallel()

	persisted := []byte(`{"gateway":{"token":"old-secret"}}`)
	incoming := map[string]any{
		"gateway": map[string]any{"token": "rotated-secret"},
	}
	preserveMaskedGatewayToken(incoming, persisted)
	gw := incoming["gateway"].(map[string]any)
	if gw["token"] != "rotated-secret" {
		t.Errorf("real PATCH token must win, got %v", gw["token"])
	}
}

func TestPreserveMaskedGatewayTokenDropsMaskWhenNoPersistedToken(t *testing.T) {
	t.Parallel()

	persisted := []byte(`{"gateway":{"host":"gw.internal"}}`)
	incoming := map[string]any{
		"gateway": map[string]any{"token": runtimeConfigGatewayTokenMask},
	}
	preserveMaskedGatewayToken(incoming, persisted)
	gw := incoming["gateway"].(map[string]any)
	if _, present := gw["token"]; present {
		t.Errorf("token must be dropped, got %v", gw["token"])
	}
}

func TestMaskGatewayTokenRoundTripsAsJSON(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"gateway":{"token":"plaintext","host":"gw"}}`)
	var rc map[string]any
	if err := json.Unmarshal(raw, &rc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	maskGatewayToken(rc)
	out, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("marshal after mask: %v", err)
	}
	if string(out) == string(raw) {
		t.Errorf("mask should change the bytes, got %q", out)
	}
}
