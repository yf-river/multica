package agent

import "testing"

func TestValidSandboxMode(t *testing.T) {
	for _, mode := range []string{SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess} {
		if !ValidSandboxMode(mode) {
			t.Fatalf("%q should be valid", mode)
		}
	}
	for _, mode := range []string{"", "full", "workspace_write", "DANGER-FULL-ACCESS"} {
		if ValidSandboxMode(mode) {
			t.Fatalf("%q should be rejected", mode)
		}
	}
}

func TestSandboxModeOrDefault(t *testing.T) {
	// 缺省与未知取值都必须回到「不做文件系统沙箱」的文档默认；否则老服务端
	// 的任务会悄悄落进 provider 自己的更严格默认（GONG-55 的 TMPDIR 事故）。
	if got := SandboxModeOrDefault(""); got != SandboxDangerFullAccess {
		t.Fatalf("empty: %q", got)
	}
	if got := SandboxModeOrDefault("future-mode"); got != SandboxDangerFullAccess {
		t.Fatalf("unknown: %q", got)
	}
	if got := SandboxModeOrDefault(SandboxReadOnly); got != SandboxReadOnly {
		t.Fatalf("read-only: %q", got)
	}
}
