package supervision

import (
	"strings"
	"testing"
)

func TestDefaultInstructionsKeepResultContract(t *testing.T) {
	for _, marker := range []string{
		`"summary": ""`,
		`supervision_validate`,
		`covered`,
		`/note`,
	} {
		if !strings.Contains(DefaultInstructions, marker) {
			t.Fatalf("default instructions lost marker %q", marker)
		}
	}
	if InstructionsVersion == "" || !strings.HasPrefix(InstructionsVersion, "default-") {
		t.Fatalf("unexpected instructions version %q", InstructionsVersion)
	}
}

func TestEffectiveInstructionsPrefersWorkspaceOverride(t *testing.T) {
	if got := EffectiveInstructions(""); got != DefaultInstructions {
		t.Fatal("empty override should follow the built-in default")
	}
	if got := EffectiveInstructions("工作区自己的规则"); got != "工作区自己的规则" {
		t.Fatalf("workspace override ignored: %q", got)
	}
	if got := EffectiveInstructions("   "); got != DefaultInstructions {
		t.Fatal("blank override should follow the built-in default")
	}
}
