package handler

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/codediff"
	"strings"
	"testing"
)

func TestCompletionSanitizesAndPersistsCodeDiff(t *testing.T) {
	req := TaskCompleteRequest{CodeDiff: &codediff.Diff{Before: "before", After: "after", Files: []codediff.File{{Path: "file\x00.go", Patch: "+new\x00line"}}}}
	sanitizeTaskCompleteRequest(&req)
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `\u0000`) {
		t.Fatal("NUL in JSONB payload")
	}
	var stored TaskCompleteRequest
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.CodeDiff == nil || stored.CodeDiff.Files[0].Patch != "+newline" {
		t.Fatalf("snapshot lost: %s", data)
	}
}

func TestGong78TerminalRedactsQuotedSecretsFromOldDaemon(t *testing.T) {
	fake := "SYNTHETIC_NOT_A_REAL_TOKEN_01234567890123456789"
	req := TaskCompleteRequest{CodeDiff: &codediff.Diff{Version: 2, Roots: []codediff.Root{{Files: []codediff.File{{Path: "config.json", Patch: `+{"token":"` + fake + `"}`}}}}}}
	sanitizeTaskCompleteRequest(&req)
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), fake) {
		t.Fatal("caller supplied secret persisted")
	}
}
