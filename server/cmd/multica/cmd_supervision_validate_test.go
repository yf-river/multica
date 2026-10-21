package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/supervision"
	"github.com/spf13/cobra"
)

func TestSupervisionValidationReturnsCanonicalResultWithoutRepairingRawOutput(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bundle.json")
	output := `{"summary":"范围内核验完成","efficiency":"clear","correctness":"clear","gaps":[],"covered":[],"findings":[],"resolutions":[]}`
	validate := func(raw string) ([]byte, error) {
		t.Helper()
		data, err := json.Marshal(map[string]any{"output": raw, "materials": []any{}, "known_findings": []string{}})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		var buffer bytes.Buffer
		command := &cobra.Command{}
		command.SetOut(&buffer)
		err = supervisionValidateCmd.RunE(command, []string{file})
		return buffer.Bytes(), err
	}
	data, err := validate("```json\n" + output + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	var result supervision.Result
	if err = json.Unmarshal(data, &result); err != nil || result.Summary != "范围内核验完成" {
		t.Fatalf("canonical output missing: %s %v", data, err)
	}
	if data, err = validate("```json\n" + output + "\n```\n这是额外说明"); err == nil || len(data) != 0 {
		t.Fatal("trailing content was silently removed before product validation")
	}
}
