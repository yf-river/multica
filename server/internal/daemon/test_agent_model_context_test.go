package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTestAgentModelContextPreservesResolvedSelection(t *testing.T) {
	dir := t.TempDir()
	sel := taskModelSelection{Model: "gpt-6.1-sol", ThinkingLevel: "low", ServiceTier: "default"}
	for _, args := range [][]string{nil, {"--model=other"}, {"-c", "model=other"}, {"--enable", "fast_mode"}} {
		if err := writeTestAgentModelContext(dir, "run", "codex", sel, args); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "multica-execution-model.json"))
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got["resolved"] != (len(args) == 0) {
			t.Fatalf("override must invalidate model provenance: %v", got)
		}
		if got["model"] != sel.Model || got["task_id"] != "run" || got["thinking_level"] != "low" || got["service_tier"] != "default" {
			t.Fatalf("lost execution config: %v", got)
		}
	}
}
