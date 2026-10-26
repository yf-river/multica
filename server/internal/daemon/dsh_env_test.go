package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// dshRuntimeEnv 是 multica profile 的档位入口：任务继承的必须是 agent 配置的
// 档位，缺省时必须回到「不做文件系统沙箱」的默认，否则 go/npm 这类工具又会
// 像 GONG-55 那样撞上只读缓存与不存在的 TMPDIR。
func TestDshRuntimeEnvCarriesSandboxMode(t *testing.T) {
	for _, mode := range []string{
		agent.SandboxReadOnly,
		agent.SandboxWorkspaceWrite,
		agent.SandboxDangerFullAccess,
	} {
		env := dshRuntimeEnv("/root/.multica/profile/dsh-sessions/runtime/agent", mode)
		if got := env["DSH_PERMISSION_MODE"]; got != mode {
			t.Fatalf("mode %q: DSH_PERMISSION_MODE=%q", mode, got)
		}
		if env["MULTICA_DSH_SESSION_ROOT"] == "" {
			t.Fatalf("mode %q: session root missing", mode)
		}
		if env["DSH_TELEMETRY_DISABLED"] != "1" {
			t.Fatalf("mode %q: telemetry switch missing", mode)
		}
	}
}

func TestDshRuntimeEnvFallsBackToFullAccess(t *testing.T) {
	// 老服务端不下发 sandbox_mode；空值和未知值都必须回到默认，绝不能悄悄
	// 落进 provider 自己的 workspace-write 默认。
	for _, mode := range []string{"", "future-mode"} {
		if got := dshRuntimeEnv("/tmp/root", mode)["DSH_PERMISSION_MODE"]; got != agent.SandboxDangerFullAccess {
			t.Fatalf("mode %q: DSH_PERMISSION_MODE=%q, want %q", mode, got, agent.SandboxDangerFullAccess)
		}
	}
}
