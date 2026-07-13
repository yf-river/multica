package agent

import "context"

var kimiBlockedArgs = map[string]blockedArgMode{
	"acp": blockedStandalone,
}

// kimiBackend runs Kimi Code CLI through the shared ACP lifecycle. Kimi uses
// session/resume and receives Multica's system prompt inline with the turn.
type kimiBackend struct {
	cfg Config
}

func (b *kimiBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	return executeACPBackend(ctx, prompt, opts, b.cfg, acpRuntimeSpec{
		provider:            "kimi",
		defaultExecutable:   "kimi",
		baseArgs:            []string{"acp"},
		blockedArgs:         kimiBlockedArgs,
		resumeMethod:        "session/resume",
		prependSystemPrompt: true,
		normalizeToolName:   kimiToolNameFromTitle,
	})
}

func kimiToolNameFromTitle(title string) string {
	return acpToolNameFromTitle(title, false)
}
