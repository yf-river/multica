package agent

import (
	"context"
	"log/slog"
)

// CodeBuddy is a Claude Code fork and speaks the same stream-json protocol.
// Its backend therefore differs only by executable name and the absence of
// Claude's background-tool rejection policy.
type codebuddyBackend struct {
	cfg Config
}

var codebuddyBlockedArgs = claudeBlockedArgs

func buildCodebuddyArgs(opts ExecOptions, logger *slog.Logger) []string {
	return buildClaudeStreamArgs(opts, codebuddyBlockedArgs, logger)
}

func (b *codebuddyBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	return executeClaudeStreamBackend(ctx, prompt, opts, b.cfg, claudeStreamBackendSpec{
		provider: "codebuddy", defaultExecutable: "codebuddy",
		buildArgs: buildCodebuddyArgs,
	})
}
