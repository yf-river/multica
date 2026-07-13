package agent

import (
	"errors"
	"sync"
	"time"
)

// codexBlockedArgs are flags hardcoded by the daemon that must not be
// overridden by user-configured custom_args. The mcp_servers config keys
// live in the per-task `$CODEX_HOME/config.toml` (written by
// ensureCodexMcpConfig); user-supplied `-c mcp_servers.…` overrides are
// stripped separately by filterCodexCustomConfigOverrides because they
// share the `-c` flag with legitimate non-MCP overrides like `-c model=…`.
var codexBlockedArgs = map[string]blockedArgMode{
	"--listen": blockedWithValue, // stdio:// transport for daemon communication
}

// codexStderrTailBytes bounds the stderr tail captured for inclusion in
// error messages when codex exits before the JSON-RPC handshake (e.g. the
// user supplied a custom_args flag that the `app-server` subcommand
// rejects). Kept as its own constant so bumping codex independently of
// other agents stays easy if codex starts shipping longer failure traces.
const (
	codexStderrTailBytes                   = 2048
	defaultCodexSemanticInactivityTimeout  = 10 * time.Minute
	defaultCodexFirstTurnNoProgressTimeout = 30 * time.Second
	codexVersionDiagnosticTimeout          = 2 * time.Second
	codexModelCapabilityTimeout            = 3 * time.Second
	// codexGracefulShutdownTimeout bounds how long the lifecycle goroutine
	// waits for codex to exit on its own after stdin is closed, before forcing
	// a context-cancel kill. A clean exit lets codex run its shutdown path and
	// flush buffered telemetry — OTEL batch exporters only force-flush on
	// graceful shutdown, so killing it immediately (the prior behavior) drops
	// the task's spans/metrics/logs.
	codexGracefulShutdownTimeout = 10 * time.Second
)

// CodexSemanticInactivityMarker prefixes timeout errors emitted when Codex
// stops making semantic progress while the process is still alive.
const CodexSemanticInactivityMarker = "codex semantic inactivity timeout"

// CodexFirstTurnNoProgressMarker identifies the app-server failure mode where
// Codex accepts a turn and then never emits any item, completion, or error.
const CodexFirstTurnNoProgressMarker = "codex app-server no progress timeout"

const codexModelCatalogRefreshTimeoutSignal = "failed to refresh available models: timeout waiting for child process to exit"

var errCodexProcessExited = errors.New("codex process exited")

type codexImageGenerationPolicy string

const (
	codexImageGenerationAuto codexImageGenerationPolicy = "auto"
	codexImageGenerationOn   codexImageGenerationPolicy = "on"
	codexImageGenerationOff  codexImageGenerationPolicy = "off"
)

type codexModelCapability struct {
	InputModalities                []string
	ExperimentalSupportedTools     []string
	SupportsImageDetailOriginalSet bool
	SupportsImageDetailOriginal    bool
}

type codexModelCapabilityCacheEntry struct {
	models    map[string]codexModelCapability
	ok        bool
	expiresAt time.Time
}

var (
	codexModelCapabilityCacheMu sync.Mutex
	codexModelCapabilityCache   = map[string]codexModelCapabilityCacheEntry{}
)

type codexTimeoutKind int

const (
	codexTimeoutNone codexTimeoutKind = iota
	codexTimeoutSemanticInactivity
	codexTimeoutFirstTurnNoProgress
)

type codexTimeoutDiagnostic struct {
	Kind         codexTimeoutKind
	Timeout      time.Duration
	LastActivity string
	ThreadID     string
	TurnID       string
	Model        string
	CodexVersion string
}
