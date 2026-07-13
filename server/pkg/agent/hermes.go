package agent

import "context"

var hermesBlockedArgs = map[string]blockedArgMode{
	"acp": blockedStandalone,
}

// hermesBackend runs Hermes through the shared ACP lifecycle. Hermes keeps its
// model in session/new, loads system context from cwd, and replays history when
// resuming, so current-turn notifications remain gated until session/prompt.
type hermesBackend struct {
	cfg Config
}

func (b *hermesBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	return executeACPBackend(ctx, prompt, opts, b.cfg, acpRuntimeSpec{
		provider:              "hermes",
		defaultExecutable:     "hermes",
		baseArgs:              []string{"acp"},
		blockedArgs:           hermesBlockedArgs,
		resumeMethod:          "session/resume",
		extraEnv:              []string{"HERMES_YOLO_MODE=1"},
		gateHistoryReplay:     true,
		passModelOnSessionNew: true,
		inferModelFromSession: true,
		mergeCacheReadUsage:   true,
		usesCWDContextFiles:   true,
	})
}
