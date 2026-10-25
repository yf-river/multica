package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRepairKeepsUnknownFieldsAndRedactsBothCopies(t *testing.T) {
	fake := "SYNTHETIC_ONLY_01234567890123456789"
	raw, _ := json.Marshal(map[string]any{"future": true, "code_diff": map[string]any{"future": 42, "files": []any{map[string]any{"path": "config.json", "patch": `+{"token":"` + fake + `"}`, "future": "kept"}}}})
	after, changed := cleanResult(raw)
	if !changed || strings.Contains(string(after), fake) || !strings.Contains(string(after), "kept") {
		t.Fatalf("invalid repair: %s", after)
	}
	if _, changed = cleanResult(after); changed {
		t.Fatal("repair is not idempotent")
	}
}
