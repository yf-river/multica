package agent

import "context"

var kiroBlockedArgs = map[string]blockedArgMode{
	"acp":               blockedStandalone,
	"-a":                blockedStandalone,
	"--trust-all-tools": blockedStandalone,
	"--trust-tools":     blockedWithValue,
}

// kiroBackend runs Kiro CLI through the shared ACP lifecycle. Kiro loads
// sessions through session/load and currently accepts both documented content
// and standard ACP prompt fields on session/prompt.
type kiroBackend struct {
	cfg Config
}

func (b *kiroBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	return executeACPBackend(ctx, prompt, opts, b.cfg, acpRuntimeSpec{
		provider:                 "kiro",
		defaultExecutable:        "kiro-cli",
		baseArgs:                 []string{"acp", "--trust-all-tools"},
		blockedArgs:              kiroBlockedArgs,
		resumeMethod:             "session/load",
		resumeResponseOmitsID:    true,
		gateHistoryReplay:        true,
		prependSystemPrompt:      true,
		duplicatePromptAsContent: true,
		normalizeToolName:        kiroToolNameFromTitle,
	})
}

func kiroToolNameFromTitle(title string) string {
	return acpToolNameFromTitle(title, true)
}
