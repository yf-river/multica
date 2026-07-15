package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-shellwords"

	"github.com/multica-ai/multica/server/internal/cli"
)

const (
	DefaultServerURL         = "ws://localhost:8080/ws"
	DefaultPollInterval      = 30 * time.Second
	DefaultHeartbeatInterval = 15 * time.Second
	// A zero agent timeout leaves liveness to the idle and tool watchdogs.
	DefaultAgentTimeout                   = 0
	DefaultCodexSemanticInactivityTimeout = 10 * time.Minute
	// Idle applies only when no message is emitted and the queue is empty.
	DefaultAgentIdleWatchdog = 30 * time.Minute
	// Tool watchdog applies while a tool_use awaits its matching tool_result.
	DefaultAgentToolWatchdog     = 2 * time.Hour
	DefaultRuntimeName           = "Local Agent"
	DefaultWorkspaceSyncInterval = 30 * time.Second
	DefaultHealthPort            = 19514
	DefaultMaxConcurrentTasks    = 20
	DefaultCodexMinTaskInterval  = 10 * time.Second
	DefaultGCInterval            = 1 * time.Hour
	DefaultGCTTL                 = 24 * time.Hour // 1 day — AI-coding issues rarely stay open long
	DefaultGCOrphanTTL           = 72 * time.Hour // 3 days — orphans with no meta (crashes, pre-GC leftovers)
	DefaultGCArtifactTTL         = 12 * time.Hour // 12h — drop regenerable artifacts on completed but still-open issues
)

// DefaultGCArtifactPatterns lists basename matches that the GC loop treats as
// regenerable build artifacts. Kept conservative: only directories that are
// always cheap to recreate (`pnpm install`, `next build`, `turbo build`). Things
// like `dist/`, `build/`, `.cache/` or `.venv/` may legitimately hold source or
// release output in some repos and are NOT included by default — set
// MULTICA_GC_ARTIFACT_PATTERNS to extend the list per deployment.
var DefaultGCArtifactPatterns = []string{"node_modules", ".next", ".turbo"}

type agentProviderSpec struct {
	provider string
	pathEnv  string
	command  string
	modelEnv string
}

var agentProviderSpecs = []agentProviderSpec{
	{provider: "claude", pathEnv: "MULTICA_CLAUDE_PATH", command: "claude", modelEnv: "MULTICA_CLAUDE_MODEL"},
	{provider: "codex", pathEnv: "MULTICA_CODEX_PATH", command: "codex", modelEnv: "MULTICA_CODEX_MODEL"},
	{provider: "opencode", pathEnv: "MULTICA_OPENCODE_PATH", command: "opencode", modelEnv: "MULTICA_OPENCODE_MODEL"},
	{provider: "openclaw", pathEnv: "MULTICA_OPENCLAW_PATH", command: "openclaw", modelEnv: "MULTICA_OPENCLAW_MODEL"},
	{provider: "hermes", pathEnv: "MULTICA_HERMES_PATH", command: "hermes", modelEnv: "MULTICA_HERMES_MODEL"},
	{provider: "gemini", pathEnv: "MULTICA_GEMINI_PATH", command: "gemini", modelEnv: "MULTICA_GEMINI_MODEL"},
	{provider: "pi", pathEnv: "MULTICA_PI_PATH", command: "pi", modelEnv: "MULTICA_PI_MODEL"},
	{provider: "cursor", pathEnv: "MULTICA_CURSOR_PATH", command: "cursor-agent", modelEnv: "MULTICA_CURSOR_MODEL"},
	{provider: "copilot", pathEnv: "MULTICA_COPILOT_PATH", command: "copilot", modelEnv: "MULTICA_COPILOT_MODEL"},
	{provider: "kimi", pathEnv: "MULTICA_KIMI_PATH", command: "kimi", modelEnv: "MULTICA_KIMI_MODEL"},
	{provider: "kiro", pathEnv: "MULTICA_KIRO_PATH", command: "kiro-cli", modelEnv: "MULTICA_KIRO_MODEL"},
	{provider: "codebuddy", pathEnv: "MULTICA_CODEBUDDY_PATH", command: "codebuddy", modelEnv: "MULTICA_CODEBUDDY_MODEL"},
	{provider: "antigravity", pathEnv: "MULTICA_ANTIGRAVITY_PATH", command: "agy", modelEnv: "MULTICA_ANTIGRAVITY_MODEL"},
}

// Config holds all daemon configuration.
type Config struct {
	ServerBaseURL                  string
	DaemonID                       string
	DeviceName                     string
	RuntimeName                    string
	CLIVersion                     string                // multica CLI version (e.g. "0.1.13")
	LaunchedBy                     string                // "desktop" when spawned by the Electron app, empty for standalone
	Profile                        string                // profile name (empty = default)
	Agents                         map[string]AgentEntry // keyed by provider: claude, codebuddy, codex, copilot, opencode, openclaw, hermes, gemini, pi, cursor, kimi, kiro, antigravity
	WorkspacesRoot                 string                // base path for execution envs (default: ~/multica_workspaces)
	KeepEnvAfterTask               bool                  // preserve env after task for debugging
	HealthPort                     int                   // local HTTP port for health checks (default: 19514)
	MaxConcurrentTasks             int                   // max tasks running in parallel (default: 20)
	GCEnabled                      bool                  // enable periodic workspace garbage collection (default: true)
	GCInterval                     time.Duration         // how often the GC loop runs (default: 1h)
	GCTTL                          time.Duration         // clean dirs whose issue is done/cancelled and updated_at < now()-TTL (default: 24h)
	GCOrphanTTL                    time.Duration         // clean orphan dirs with no meta, or dirs whose issue gc-check returns 404, once they exceed this age (default: 72h). The 404 path uses the same TTL — a scoped-down token can't instantly wipe live workspaces.
	GCArtifactTTL                  time.Duration         // when a task has been completed for at least this long but its issue is still open, drop regenerable artifacts (default: 12h, set 0 to disable)
	GCArtifactPatterns             []string              // basename patterns whose subtrees are removed during artifact cleanup (default: node_modules, .next, .turbo)
	PollInterval                   time.Duration
	HeartbeatInterval              time.Duration
	AgentTimeout                   time.Duration
	CodexSemanticInactivityTimeout time.Duration
	CodexMinTaskInterval           time.Duration // minimum spacing between Codex task starts on the same runtime (0 = disabled)
	AgentIdleWatchdog              time.Duration // force-stop a run when the backend goes silent this long with an empty queue (0 = disabled)
	AgentToolWatchdog              time.Duration // force-stop a run when a single tool call stays in flight (silent) this long (0 = disabled); backstop for hung tools now that there is no wall-clock cap
	ClaudeArgs                     []string
	CodexArgs                      []string
	CodebuddyArgs                  []string

	// ProfileCommandOverrides maps a custom runtime profile_id -> the absolute
	// executable path to use for that profile on THIS machine (MUL-3284).
	// Sourced from the local CLI config (cli.CLIConfig.ProfileCommandOverrides),
	// written by `multica runtime profile set-path`. appendProfileRuntimes
	// prefers a matching, executable override over resolving the profile's
	// command_name on PATH. nil/empty means "always resolve via PATH".
	ProfileCommandOverrides map[string]string
}

// Overrides allows CLI flags to override environment variables and defaults.
// Zero values are ignored and the env/default value is used instead.
type Overrides struct {
	ServerURL         string
	WorkspacesRoot    string
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	// AgentTimeout is a pointer so an explicit `--agent-timeout 0` (no cap) is
	// distinguishable from "flag not passed". nil = use env/default.
	AgentTimeout                   *time.Duration
	CodexSemanticInactivityTimeout time.Duration
	MaxConcurrentTasks             int
	DaemonID                       string
	DeviceName                     string
	RuntimeName                    string
	Profile                        string // profile name (empty = default)
	HealthPort                     int    // health check port (0 = use default)
}

// LoadConfig builds the daemon configuration from environment variables
// and optional CLI flag overrides.
func LoadConfig(overrides Overrides) (Config, error) {
	// Server URL: override > env > default
	rawServerURL := envOrDefault("MULTICA_SERVER_URL", DefaultServerURL)
	if overrides.ServerURL != "" {
		rawServerURL = overrides.ServerURL
	}
	serverBaseURL, err := NormalizeServerBaseURL(rawServerURL)
	if err != nil {
		return Config{}, err
	}

	// Local config is optional. Explicit process environment still wins over
	// OpenClaw config, and a malformed config does not prevent daemon startup.
	var profileCommandOverrides map[string]string
	if cliCfg, err := cli.LoadCLIConfigForProfile(overrides.Profile); err != nil {
		slog.Warn("could not load CLI config for backend overrides; proceeding without",
			"profile", overrides.Profile, "err", err)
	} else {
		if cliCfg.Backends != nil {
			applyOpenclawOverride(cliCfg.Backends.OpenClaw)
		}
		// Copy machine-local paths so loaded config cannot alias daemon state.
		if len(cliCfg.ProfileCommandOverrides) > 0 {
			profileCommandOverrides = make(map[string]string, len(cliCfg.ProfileCommandOverrides))
			for id, path := range cliCfg.ProfileCommandOverrides {
				if id == "" || strings.TrimSpace(path) == "" {
					continue
				}
				profileCommandOverrides[id] = path
			}
		}
	}

	// Resolve normal PATH entries first. Query a login shell lazily only when
	// a bare command is absent from a GUI-launched daemon's PATH.
	agentCommandNames := make([]string, 0, len(agentProviderSpecs))
	for _, spec := range agentProviderSpecs {
		agentCommandNames = append(agentCommandNames, spec.command)
	}
	var (
		shellResolveOnce sync.Once
		shellResolved    map[string]string
	)
	getShellResolved := func() map[string]string {
		shellResolveOnce.Do(func() {
			shellResolved = resolveAgentsViaLoginShell(agentCommandNames)
		})
		return shellResolved
	}
	probe := func(envVar, defaultCmd, modelEnv string) (AgentEntry, bool) {
		cmd := envOrDefault(envVar, defaultCmd)
		if _, err := exec.LookPath(cmd); err == nil {
			return AgentEntry{
				Path:  cmd,
				Model: strings.TrimSpace(os.Getenv(modelEnv)),
			}, true
		}
		// An invalid explicit path must not silently select another binary.
		if strings.ContainsAny(cmd, "/\\") {
			return AgentEntry{}, false
		}
		if path, ok := getShellResolved()[cmd]; ok {
			return AgentEntry{
				Path:  path,
				Model: strings.TrimSpace(os.Getenv(modelEnv)),
			}, true
		}
		if defaultCmd == "codex" && cmd == defaultCmd {
			// Codex Desktop bundles its CLI inside the macOS app instead of
			// installing it onto PATH.
			for _, p := range codexDesktopAppBundlePaths() {
				if _, err := os.Stat(p); err == nil {
					return AgentEntry{
						Path:  p,
						Model: strings.TrimSpace(os.Getenv(modelEnv)),
					}, true
				}
			}
		}
		return AgentEntry{}, false
	}

	agents := make(map[string]AgentEntry, len(agentProviderSpecs))
	for _, spec := range agentProviderSpecs {
		if entry, ok := probe(spec.pathEnv, spec.command, spec.modelEnv); ok {
			agents[spec.provider] = entry
		}
	}
	agents = filterAgentsByProviderEnv(agents)
	if len(agents) == 0 {
		return Config{}, fmt.Errorf("no agent CLI found: install claude, codebuddy, codex, copilot, opencode, openclaw, hermes, gemini, pi, cursor-agent, kimi, kiro-cli, or agy and ensure it is on PATH")
	}

	claudeArgs, err := shellArgsFromEnv("MULTICA_CLAUDE_ARGS")
	if err != nil {
		return Config{}, err
	}
	codexArgs, err := shellArgsFromEnv("MULTICA_CODEX_ARGS")
	if err != nil {
		return Config{}, err
	}
	codebuddyArgs, err := shellArgsFromEnv("MULTICA_CODEBUDDY_ARGS")
	if err != nil {
		return Config{}, err
	}

	// Host info
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "local-machine"
	}

	// Durations: override > env > default
	pollInterval, err := durationFromEnv("MULTICA_DAEMON_POLL_INTERVAL", DefaultPollInterval)
	if err != nil {
		return Config{}, err
	}
	if overrides.PollInterval > 0 {
		pollInterval = overrides.PollInterval
	}

	heartbeatInterval, err := durationFromEnv("MULTICA_DAEMON_HEARTBEAT_INTERVAL", DefaultHeartbeatInterval)
	if err != nil {
		return Config{}, err
	}
	if overrides.HeartbeatInterval > 0 {
		heartbeatInterval = overrides.HeartbeatInterval
	}

	agentTimeout, err := durationFromEnv("MULTICA_AGENT_TIMEOUT", DefaultAgentTimeout)
	if err != nil {
		return Config{}, err
	}
	if overrides.AgentTimeout != nil {
		agentTimeout = *overrides.AgentTimeout
	}

	codexSemanticInactivityTimeout, err := durationFromEnv("MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT", DefaultCodexSemanticInactivityTimeout)
	if err != nil {
		return Config{}, err
	}
	if overrides.CodexSemanticInactivityTimeout > 0 {
		codexSemanticInactivityTimeout = overrides.CodexSemanticInactivityTimeout
	}

	codexMinTaskInterval, err := durationFromEnv("MULTICA_CODEX_MIN_TASK_INTERVAL", DefaultCodexMinTaskInterval)
	if err != nil {
		return Config{}, err
	}

	// MULTICA_AGENT_IDLE_WATCHDOG=0 disables the per-task idle watchdog. We
	// route 0 through durationFromEnv so the operator can opt out without
	// patching the binary; any positive duration overrides DefaultAgentIdleWatchdog.
	agentIdleWatchdog, err := durationFromEnv("MULTICA_AGENT_IDLE_WATCHDOG", DefaultAgentIdleWatchdog)
	if err != nil {
		return Config{}, err
	}

	// MULTICA_AGENT_TOOL_WATCHDOG=0 disables the in-flight-tool backstop; any
	// positive duration overrides DefaultAgentToolWatchdog.
	agentToolWatchdog, err := durationFromEnv("MULTICA_AGENT_TOOL_WATCHDOG", DefaultAgentToolWatchdog)
	if err != nil {
		return Config{}, err
	}

	maxConcurrentTasks, err := intFromEnv("MULTICA_DAEMON_MAX_CONCURRENT_TASKS", DefaultMaxConcurrentTasks)
	if err != nil {
		return Config{}, err
	}
	if overrides.MaxConcurrentTasks > 0 {
		maxConcurrentTasks = overrides.MaxConcurrentTasks
	}

	// Profile
	profile := overrides.Profile

	// daemon_id resolution: override > env > persistent UUID on disk.
	// The persistent UUID is written once to ~/.multica/daemon.id and then
	// reused forever across hostname and profile changes.
	// Callers may still pin a specific id via MULTICA_DAEMON_ID or the
	// override field (e.g. for tests or embedded environments).
	daemonID := strings.TrimSpace(os.Getenv("MULTICA_DAEMON_ID"))
	if overrides.DaemonID != "" {
		daemonID = overrides.DaemonID
	}
	if daemonID == "" {
		persisted, err := EnsureDaemonID()
		if err != nil {
			return Config{}, fmt.Errorf("ensure daemon id: %w", err)
		}
		daemonID = persisted
	}
	deviceName := envOrDefault("MULTICA_DAEMON_DEVICE_NAME", host)
	if overrides.DeviceName != "" {
		deviceName = overrides.DeviceName
	}

	runtimeName := envOrDefault("MULTICA_AGENT_RUNTIME_NAME", DefaultRuntimeName)
	if overrides.RuntimeName != "" {
		runtimeName = overrides.RuntimeName
	}

	// Workspaces root: override > env > default (~/multica_workspaces or ~/multica_workspaces_<profile>)
	workspacesRoot, err := ResolveWorkspacesRoot(profile, overrides.WorkspacesRoot)
	if err != nil {
		return Config{}, err
	}
	if _, ok := agents["codex"]; ok {
		if err := ensureCodexRuntimeProfile(daemonID); err != nil {
			return Config{}, fmt.Errorf("ensure codex runtime profile: %w", err)
		}
	}

	// Health port: override > default
	healthPort := DefaultHealthPort
	if overrides.HealthPort > 0 {
		healthPort = overrides.HealthPort
	}

	// Keep env after task: env > default (false)
	keepEnv := os.Getenv("MULTICA_KEEP_ENV_AFTER_TASK") == "true" || os.Getenv("MULTICA_KEEP_ENV_AFTER_TASK") == "1"

	// GC config: env > defaults
	gcEnabled := true
	if v := os.Getenv("MULTICA_GC_ENABLED"); v == "false" || v == "0" {
		gcEnabled = false
	}
	gcInterval, err := durationFromEnv("MULTICA_GC_INTERVAL", DefaultGCInterval)
	if err != nil {
		return Config{}, err
	}
	gcTTL, err := durationFromEnv("MULTICA_GC_TTL", DefaultGCTTL)
	if err != nil {
		return Config{}, err
	}
	gcOrphanTTL, err := durationFromEnv("MULTICA_GC_ORPHAN_TTL", DefaultGCOrphanTTL)
	if err != nil {
		return Config{}, err
	}
	gcArtifactTTL, err := durationFromEnv("MULTICA_GC_ARTIFACT_TTL", DefaultGCArtifactTTL)
	if err != nil {
		return Config{}, err
	}
	gcArtifactPatterns := patternsFromEnv(DefaultGCArtifactPatterns)

	return Config{
		ServerBaseURL:                  serverBaseURL,
		DaemonID:                       daemonID,
		DeviceName:                     deviceName,
		RuntimeName:                    runtimeName,
		Profile:                        profile,
		Agents:                         agents,
		WorkspacesRoot:                 workspacesRoot,
		KeepEnvAfterTask:               keepEnv,
		GCEnabled:                      gcEnabled,
		GCInterval:                     gcInterval,
		GCTTL:                          gcTTL,
		GCOrphanTTL:                    gcOrphanTTL,
		GCArtifactTTL:                  gcArtifactTTL,
		GCArtifactPatterns:             gcArtifactPatterns,
		HealthPort:                     healthPort,
		MaxConcurrentTasks:             maxConcurrentTasks,
		PollInterval:                   pollInterval,
		HeartbeatInterval:              heartbeatInterval,
		AgentTimeout:                   agentTimeout,
		CodexSemanticInactivityTimeout: codexSemanticInactivityTimeout,
		CodexMinTaskInterval:           codexMinTaskInterval,
		AgentIdleWatchdog:              agentIdleWatchdog,
		AgentToolWatchdog:              agentToolWatchdog,
		ClaudeArgs:                     claudeArgs,
		CodexArgs:                      codexArgs,
		CodebuddyArgs:                  codebuddyArgs,
		ProfileCommandOverrides:        profileCommandOverrides,
	}, nil
}

// NormalizeServerBaseURL converts a WebSocket or HTTP URL to a base HTTP URL.
func NormalizeServerBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid MULTICA_SERVER_URL: %w", err)
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
	default:
		return "", fmt.Errorf("MULTICA_SERVER_URL must use ws, wss, http, or https")
	}
	if u.Path == "/ws" {
		u.Path = ""
	}
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

// ResolveWorkspacesRoot returns the absolute path that the daemon and CLI
// should treat as the workspaces root. Resolution order: explicit override >
// MULTICA_WORKSPACES_ROOT env > default ($HOME/multica_workspaces, or
// $HOME/multica_workspaces_<profile> for a named profile). Read-only callers
// (e.g. `multica daemon disk-usage`) use this directly so they pick the same
// directory the running daemon would have picked.
func ResolveWorkspacesRoot(profile, override string) (string, error) {
	root := strings.TrimSpace(os.Getenv("MULTICA_WORKSPACES_ROOT"))
	if override != "" {
		root = override
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w (set MULTICA_WORKSPACES_ROOT to override)", err)
		}
		if profile != "" {
			root = filepath.Join(home, "multica_workspaces_"+profile)
		} else {
			root = filepath.Join(home, "multica_workspaces")
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve absolute workspaces root: %w", err)
	}
	return abs, nil
}

// ArtifactPatternsFromEnv returns the configured artifact patternSet — the
// same list the GC loop consults when it runs the artifact-only cleanup. The
// disk-usage CLI uses this to make sure the "artifact size" it reports
// matches what the GC would actually reclaim.
func ArtifactPatternsFromEnv() []string {
	return patternsFromEnv(DefaultGCArtifactPatterns)
}

// patternsFromEnv reads a comma-separated list from env. Patterns containing
// path separators are silently dropped — the GC artifact cleanup only matches
// directory basenames, never paths, so a pattern like "foo/bar" is meaningless
// and accepting it would just be a footgun.
func patternsFromEnv(defaults []string) []string {
	raw := strings.TrimSpace(os.Getenv("MULTICA_GC_ARTIFACT_PATTERNS"))
	if raw == "" {
		out := make([]string, len(defaults))
		copy(out, defaults)
		return out
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.ContainsAny(p, "/\\") {
			continue
		}
		out = append(out, p)
	}
	return out
}

func filterAgentsByProviderEnv(agents map[string]AgentEntry) map[string]AgentEntry {
	raw := strings.TrimSpace(os.Getenv("MULTICA_AGENT_PROVIDERS"))
	if raw == "" {
		return agents
	}
	allowed := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		provider := strings.ToLower(strings.TrimSpace(part))
		if provider == "" || strings.ContainsAny(provider, "/\\") {
			continue
		}
		allowed[provider] = struct{}{}
	}
	if len(allowed) == 0 {
		return map[string]AgentEntry{}
	}
	filtered := make(map[string]AgentEntry, len(allowed))
	for provider, entry := range agents {
		if _, ok := allowed[provider]; ok {
			filtered[provider] = entry
		}
	}
	return filtered
}

func shellArgsFromEnv(name string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	args, err := shellwords.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", name, err)
	}
	return args, nil
}

var codexDesktopAppBundlePaths = func() []string {
	paths := []string{
		"/Applications/Codex.app/Contents/Resources/codex",
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, "Applications", "Codex.app", "Contents", "Resources", "codex"))
	}
	return paths
}

// A slow or broken shell configuration must not block daemon startup.
const loginShellResolveTimeout = 3 * time.Second

// WaitDelay also bounds inherited pipes kept open by background rc processes.
const loginShellResolveWaitDelay = 2 * time.Second

// supportedLoginShells limits which interpreters we will invoke via
// `<shell> -ilc <script>`. Sticking to POSIX-compatible shells means the
// resolver script below works unchanged. Notably absent: fish (uses
// `command -s` and a different syntax for command substitution).
var supportedLoginShells = map[string]struct{}{
	"bash": {},
	"zsh":  {},
	"sh":   {},
	"dash": {},
	"ksh":  {},
}

// resolveAgentsViaLoginShell discovers canonical executable paths from a
// supported interactive login shell. Names are allowlisted before entering
// the script, and results must remain absolute and executable afterward.
func resolveAgentsViaLoginShell(names []string) map[string]string {
	out := map[string]string{}
	if len(names) == 0 {
		return out
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return out
	}
	if _, ok := supportedLoginShells[filepath.Base(shell)]; !ok {
		return out
	}

	safe := make([]string, 0, len(names))
	for _, n := range names {
		if isSafeAgentName(n) {
			safe = append(safe, n)
		}
	}
	if len(safe) == 0 {
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginShellResolveTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-ilc", buildLoginShellResolveScript(safe))
	cmd.WaitDelay = loginShellResolveWaitDelay
	raw, err := cmd.Output()
	if err != nil {
		return out
	}

	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, path := parts[0], strings.TrimSpace(parts[1])
		if !filepath.IsAbs(path) {
			continue
		}
		// Reject paths that disappeared with the helper shell.
		if _, err := exec.LookPath(path); err != nil {
			continue
		}
		out[name] = path
	}
	return out
}

// buildLoginShellResolveScript removes alias/function shadows, resolves each
// allowlisted name through PATH, canonicalizes its directory and prints a
// tab-separated name/path pair. Non-absolute results are rejected.
func buildLoginShellResolveScript(names []string) string {
	var b strings.Builder
	b.WriteString("for n in")
	for _, n := range names {
		b.WriteByte(' ')
		b.WriteString(n)
	}
	b.WriteString("; do\n")
	b.WriteString("  unalias \"$n\" 2>/dev/null\n")
	b.WriteString("  unset -f \"$n\" 2>/dev/null\n")
	b.WriteString("  p=$(command -v \"$n\" 2>/dev/null) || continue\n")
	b.WriteString("  [ -n \"$p\" ] || continue\n")
	b.WriteString("  case \"$p\" in /*) ;; *) continue ;; esac\n")
	b.WriteString("  d=$(dirname \"$p\") && f=$(basename \"$p\") && c=$(cd \"$d\" 2>/dev/null && pwd -P) || continue\n")
	b.WriteString("  printf '%s\\t%s\\n' \"$n\" \"$c/$f\"\n")
	b.WriteString("done\n")
	return b.String()
}

// isSafeAgentName restricts shell-script input to a bare ASCII command name.
func isSafeAgentName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// applyOpenclawOverride exposes machine-local config through the environment
// already consumed by discovery and child processes. Explicit env wins.
func applyOpenclawOverride(oc *cli.OpenClawOverride) {
	if oc == nil {
		return
	}
	if oc.BinaryPath != "" {
		if _, set := os.LookupEnv("MULTICA_OPENCLAW_PATH"); !set {
			_ = os.Setenv("MULTICA_OPENCLAW_PATH", oc.BinaryPath)
		}
	}
	if oc.StateDir != "" {
		if _, set := os.LookupEnv("OPENCLAW_STATE_DIR"); !set {
			_ = os.Setenv("OPENCLAW_STATE_DIR", oc.StateDir)
		}
	}
}
