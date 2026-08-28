package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLifeMCPServerExposesOnlyGovernedTools(t *testing.T) {
	input := strings.NewReader("" +
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n")
	var output bytes.Buffer
	if err := runLifeMCPServer(lifeMCPCmd, input, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var initialized lifeMCPResponse
	if err := decoder.Decode(&initialized); err != nil {
		t.Fatal(err)
	}
	result, ok := initialized.Result.(map[string]any)
	if !ok || result["protocolVersion"] != lifeMCPProtocolVersion {
		t.Fatalf("unexpected initialize response: %#v", initialized.Result)
	}
	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := decoder.Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Result.Tools) != 2 || listed.Result.Tools[0].Name != "life_evidence_resolve" || listed.Result.Tools[1].Name != "life_job_complete" {
		t.Fatalf("unexpected life MCP tools: %+v", listed.Result.Tools)
	}
}

func TestLifeMCPServerRejectsUnknownMethods(t *testing.T) {
	response := handleLifeMCPRequest(lifeMCPCmd, lifeMCPRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/list"})
	if response.Error == nil || response.Error.Code != -32601 {
		t.Fatalf("unexpected response: %+v", response)
	}
}
