package execenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexSupervisionBriefUsesNativeInstructions(t *testing.T) {
	work, root := t.TempDir(), t.TempDir()
	const human = "人工指令，保留原始字节\r\n"
	path := filepath.Join(work, "AGENTS.md")
	if err := os.WriteFile(path, []byte(human), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := TaskContextForEnv{AgentInstructions: "OLD_MANAGED_BRIEF"}
	if _, err := InjectRuntimeConfig(work, "codex", ctx); err != nil {
		t.Fatal(err)
	}
	ctx.CodexBriefViaAPI, ctx.AgentInstructions = true, "PRIVATE_SUPERVISION_ROLE_AND_BOUND_SKILL"
	ctx.AgentSkills = []SkillContextForEnv{{Name: "delivery-supervision", Content: "bound"}, {Name: "unrelated", Content: "other"}}
	brief, err := InjectRuntimeConfig(work, "codex", ctx)
	if err != nil || brief != ctx.AgentInstructions {
		t.Fatalf("native role brief mismatch: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != human {
		t.Fatalf("human instruction file changed: %v", err)
	}
	if err := WriteRuntimeProvenance(root, work, "codex", brief, ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "runtime-instructions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Delivery    string   `json:"delivery"`
		RuntimeFile string   `json:"runtime_file"`
		SHA256      string   `json:"sha256"`
		Skills      []string `json:"skills"`
		Ancestors   []string `json:"ancestor_instruction_candidates"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(brief))
	if record.Delivery != "native_developer" || record.RuntimeFile != "" || record.SHA256 != hex.EncodeToString(hash[:]) || len(record.Ancestors) != 0 || len(record.Skills) != 1 || record.Skills[0] != "delivery-supervision" || strings.Contains(string(raw), brief) {
		t.Fatal("provenance does not match the private native role delivery")
	}
	work = t.TempDir()
	if _, err := InjectRuntimeConfig(work, "codex", ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("native role delivery created a runtime file")
	}
}
