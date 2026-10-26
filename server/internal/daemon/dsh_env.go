package daemon

import "github.com/multica-ai/multica/server/pkg/agent"

// dshRuntimeEnv 是 DSH 后端启动前要落进子进程环境的固定几项。它是 multica
// profile 唯一的档位入口：DSH 的组合默认（sandbox-policy.mode = workspace-write）
// 比 Multica 文档承诺的「不替用户做文件系统沙箱」更严格，过去因此把任务的
// TMPDIR 和构建缓存挡在沙箱外（GONG-55：go vet 报只读文件系统与 TMPDIR 不存在）。
//
// sessionRoot 是每 runtime/agent 私有的 DSH session 目录；sandboxMode 归一到
// pkg/agent 的封闭集合，非法或缺失一律取默认完全访问。
func dshRuntimeEnv(sessionRoot, sandboxMode string) map[string]string {
	return map[string]string{
		"MULTICA_DSH_SESSION_ROOT": sessionRoot,
		"DSH_TELEMETRY_DISABLED":   "1",
		// DSH 用同一个变量表达沙箱档位与审批配对（danger-full-access ⇒
		// approval never），字面量就是它的 permission preset 名称。
		"DSH_PERMISSION_MODE": agent.SandboxModeOrDefault(sandboxMode),
	}
}
