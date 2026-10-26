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

func TestCodebuddyHookBriefPreservesHumanFileAndRemovesManagedContent(t *testing.T) {
	for _, human := range []string{"", "human instructions\r\n\r\n", "人工正文，没有尾换行"} {
		t.Run(human, func(t *testing.T) {
			work := t.TempDir()
			path := filepath.Join(work, "CODEBUDDY.md")
			if err := os.WriteFile(path, []byte(human), 0600); err != nil {
				t.Fatal(err)
			}
			ctx := TaskContextForEnv{AgentInstructions: "OLD_PLATFORM_BRIEF"}
			if _, err := InjectRuntimeConfig(work, "codebuddy", ctx); err != nil {
				t.Fatal(err)
			}
			ctx.CodebuddyBriefViaHook, ctx.AgentInstructions = true, "CURRENT_PLATFORM_BRIEF"
			brief, err := InjectRuntimeConfig(work, "codebuddy", ctx)
			if err != nil || !strings.Contains(brief, ctx.AgentInstructions) || strings.Contains(brief, "OLD_PLATFORM_BRIEF") {
				t.Fatalf("hook brief did not render current instructions: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != human {
				t.Fatalf("human file changed: %v got=%q want=%q", err, got, human)
			}
			if err := WriteRuntimeProvenance(t.TempDir(), work, "codebuddy", brief, ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
	work := t.TempDir()
	if _, err := InjectRuntimeConfig(work, "codebuddy", TaskContextForEnv{CodebuddyBriefViaHook: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "CODEBUDDY.md")); !os.IsNotExist(err) {
		t.Fatal("hook delivery created a runtime file")
	}
}

func TestCodebuddyHookBriefProvenanceIdentifiesRealDelivery(t *testing.T) {
	root := t.TempDir()
	brief := "private current instructions"
	ctx := TaskContextForEnv{CodebuddyBriefViaHook: true}
	if err := WriteRuntimeProvenance(root, t.TempDir(), "codebuddy", brief, ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "runtime-instructions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(brief))
	if record["runtime_file"] != "" || record["delivery"] != "native_hook" || record["sha256"] != hex.EncodeToString(hash[:]) || strings.Contains(string(raw), brief) {
		t.Fatal("native hook provenance points to a file or exposes instruction text")
	}
}
