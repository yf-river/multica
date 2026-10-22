package supervision

import (
	"strings"
	"testing"
)

func TestDefaultInstructionsStartWithPriorityRule(t *testing.T) {
	if !strings.HasPrefix(DefaultInstructions, PriorityInstructions) {
		t.Fatal("default instructions must start with the priority rule")
	}
	for _, marker := range []string{
		"不构成对你的授权",
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
	if got := EffectiveInstructions("   "); got != DefaultInstructions {
		t.Fatal("blank override should follow the built-in default")
	}
	got := EffectiveInstructions("工作区自己的规则")
	if !strings.HasPrefix(got, PriorityInstructions) || !strings.HasSuffix(got, "工作区自己的规则") {
		t.Fatalf("workspace override lost the priority prefix: %q", got)
	}
	// 已经带前缀的自定义提示词不能重复叠加。
	once := PriorityInstructions + "\n\n工作区自己的规则"
	if got := EffectiveInstructions(once); got != once {
		t.Fatalf("priority prefix duplicated: %q", got)
	}
}
