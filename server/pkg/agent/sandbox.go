package agent

// Agent run filesystem stances. The literals are the runtimes' own vocabulary
// (DSH and Codex both speak read-only / workspace-write / danger-full-access)
// so the value the user picks on the agent is the value the runtime receives —
// no cross-provider translation table to drift.
const (
	SandboxReadOnly         = "read-only"
	SandboxWorkspaceWrite   = "workspace-write"
	SandboxDangerFullAccess = "danger-full-access"
)

// ValidSandboxMode reports whether mode is one of the closed set of stances.
func ValidSandboxMode(mode string) bool {
	switch mode {
	case SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess:
		return true
	default:
		return false
	}
}

// SandboxModeOrDefault normalises an absent or unknown stance to the documented
// default: Multica does not sandbox the filesystem for agents
// (docs/content/docs/security-model.zh.mdx). Servers older than the
// sandbox_mode column omit it, and those runs must keep today's behaviour
// rather than inherit a provider's own stricter default.
func SandboxModeOrDefault(mode string) string {
	if ValidSandboxMode(mode) {
		return mode
	}
	return SandboxDangerFullAccess
}
