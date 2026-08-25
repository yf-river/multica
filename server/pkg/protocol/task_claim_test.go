package protocol

import (
	"encoding/json"
	"testing"
)

func TestTaskClaimContextJSONContract(t *testing.T) {
	value := TaskSourceContext{
		Provider: "tapd",
		URL:      "https://example.invalid/source",
		TAPD: &TAPDTaskSourceContext{
			WorkspaceID:   "tapd-workspace",
			ResourceType:  "story",
			ResourceID:    "123",
			FetchProvider: "mcp",
			FetchStatus:   "fetched",
		},
		ExternalCredentials: map[string]TaskExternalCredentialContext{
			"tapd": {
				Provider:    "tapd",
				Scope:       "user",
				Inheritance: "task_creator_or_trigger_user",
				UserID:      "user-1",
				ProfileID:   "profile-1",
				MCPServer:   "mcp-server-tapd",
				Configured:  true,
			},
		},
	}

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal task source context: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode task source context: %v", err)
	}
	if got["provider"] != "tapd" || got["url"] != "https://example.invalid/source" {
		t.Fatalf("top-level context = %#v", got)
	}
	tapd, ok := got["tapd"].(map[string]any)
	if !ok || tapd["workspace_id"] != "tapd-workspace" || tapd["fetch_status"] != "fetched" {
		t.Fatalf("tapd context = %#v", got["tapd"])
	}
	credentials, ok := got["external_credentials"].(map[string]any)
	if !ok {
		t.Fatalf("credential context = %#v", got["external_credentials"])
	}
	tapdCredential, ok := credentials["tapd"].(map[string]any)
	if !ok || tapdCredential["configured"] != true || tapdCredential["mcp_server"] != "mcp-server-tapd" {
		t.Fatalf("tapd credential = %#v", credentials["tapd"])
	}
}

func TestChatAttachmentMetaJSONContract(t *testing.T) {
	raw, err := json.Marshal(ChatAttachmentMeta{ID: "attachment-1", Filename: "brief.md", ContentType: "text/markdown"})
	if err != nil {
		t.Fatalf("marshal chat attachment: %v", err)
	}
	want := `{"id":"attachment-1","filename":"brief.md","content_type":"text/markdown"}`
	if string(raw) != want {
		t.Fatalf("chat attachment JSON = %s, want %s", raw, want)
	}
}
