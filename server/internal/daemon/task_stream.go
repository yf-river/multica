package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
	"github.com/multica-ai/multica/server/pkg/agent"
)

func (d *Daemon) executeAndDrain(ctx context.Context, backend agent.Backend, prompt string, opts agent.ExecOptions, taskLog *slog.Logger, taskID string) (agent.Result, int32, error) {
	// Wrap the caller's ctx so the idle watchdog (below) can interrupt both
	// the agent subprocess (via the ctx passed to backend.Execute) AND the
	// drain loop with a single cancel. Without this layer the backend would
	// stay tied to the parent ctx and our cancellation could only abort
	// drain, leaving the subprocess running.
	agentCtx, agentCancel := context.WithCancel(ctx)
	defer agentCancel()

	session, err := backend.Execute(agentCtx, prompt, opts)
	if err != nil {
		taskLog.Debug("backend execute returned error", "error", err)
		return agent.Result{}, 0, err
	}
	taskLog.Debug("backend started, draining messages")

	// Bound the drain loop only when there is a wall-clock cap. With a positive
	// opts.Timeout, give the drain a slightly longer deadline than the backend
	// so it can still collect the backend's own timeout Result if the scanner
	// is stuck on a hung stdout pipe (the extra 30 s covers cleanup after the
	// backend's own deadline fires). With no cap (opts.Timeout <= 0) the
	// inactivity watchdog is the only liveness net, so the drain must NOT
	// impose its own deadline either — otherwise an actively streaming long run
	// would be cut off here regardless of progress (MUL-3064).
	var drainCtx context.Context
	var drainCancel context.CancelFunc
	if opts.Timeout > 0 {
		drainCtx, drainCancel = context.WithTimeout(agentCtx, opts.Timeout+30*time.Second)
	} else {
		drainCtx, drainCancel = context.WithCancel(agentCtx)
	}
	defer drainCancel()

	var toolCount atomic.Int32
	// lastActivityAt records (as unix nanos) when the drain loop most
	// recently received a message from the backend. The idle watchdog
	// reads this to decide whether the agent has gone silent for too long.
	// Initialise to the start so a backend that never emits a single
	// message also trips the watchdog.
	var lastActivityAt atomic.Int64
	lastActivityAt.Store(time.Now().UnixNano())
	// inFlightTools counts tool_use messages that haven't yet been paired
	// with a matching tool_result. A non-zero count means the agent is
	// legitimately waiting on a tool (e.g. `npm install`, `docker build`)
	// that may run far longer than the idle window without emitting any
	// message — so while a tool is in flight the watchdog applies the larger
	// AgentToolWatchdog budget instead of treating that silence as a hang.
	var inFlightTools atomic.Int32
	var idleWatchdogFired atomic.Bool
	// idleWatchdogThreshold records (as nanos) which silence budget actually
	// tripped the watchdog — the idle window or the larger in-flight-tool
	// window — so the failure message reports the real duration.
	var idleWatchdogThreshold atomic.Int64
	idleWatchdogThreshold.Store(int64(d.cfg.AgentIdleWatchdog))
	idleWindow := d.cfg.AgentIdleWatchdog
	if idleWindow > 0 {
		go d.runIdleWatchdog(agentCtx, idleWindow, d.cfg.AgentToolWatchdog, &lastActivityAt, &inFlightTools, &idleWatchdogFired, &idleWatchdogThreshold, agentCancel, session.Messages, taskLog, taskID)
	}

	go func() {
		var seq atomic.Int32
		var mu sync.Mutex
		var pendingText strings.Builder
		var pendingThinking strings.Builder
		var batch []TaskMessageData
		callIDToTool := map[string]string{}

		flush := func() {
			mu.Lock()
			if pendingThinking.Len() > 0 {
				s := seq.Add(1)
				batch = append(batch, TaskMessageData{
					Seq:     int(s),
					Type:    "thinking",
					Content: pendingThinking.String(),
				})
				pendingThinking.Reset()
			}
			if pendingText.Len() > 0 {
				s := seq.Add(1)
				batch = append(batch, TaskMessageData{
					Seq:     int(s),
					Type:    "text",
					Content: pendingText.String(),
				})
				pendingText.Reset()
			}
			toSend := batch
			batch = nil
			mu.Unlock()

			if len(toSend) > 0 {
				sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := d.client.ReportTaskMessages(sendCtx, taskID, toSend); err != nil {
					taskLog.Debug("failed to report task messages", "error", err)
				} else {
					taskLog.Debug("reported task messages", "count", len(toSend), "last_seq", toSend[len(toSend)-1].Seq)
				}
				cancel()
			}
		}

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-ticker.C:
					flush()
				case <-done:
					return
				}
			}
		}()

		var sessionPinned atomic.Bool
		for {
			select {
			case msg, ok := <-session.Messages:
				if !ok {
					goto drainDone
				}
				// Stamp activity as soon as a message lands. The idle
				// watchdog reads this to decide whether the backend has
				// gone silent — stamping before processing makes sure a
				// slow downstream call (mu.Lock contention, batch resize)
				// can't be misattributed to backend silence.
				lastActivityAt.Store(time.Now().UnixNano())
				switch msg.Type {
				case agent.MessageStatus:
					// Persist the session/work_dir as soon as the backend
					// reveals them. Without this, a daemon crash mid-run
					// loses the resume pointer and the auto-retry fires
					// without context.
					if msg.SessionID != "" && !sessionPinned.Swap(true) {
						sid := msg.SessionID
						wd := opts.Cwd
						go func() {
							pinCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							defer cancel()
							if err := d.client.PinTaskSession(pinCtx, taskID, sid, wd); err != nil {
								taskLog.Debug("pin session failed", "error", err)
							}
						}()
					}
				case agent.MessageToolUse:
					n := toolCount.Add(1)
					inFlightTools.Add(1)
					taskLog.Info(fmt.Sprintf("tool #%d: %s", n, msg.Tool))
					if msg.CallID != "" {
						mu.Lock()
						callIDToTool[msg.CallID] = msg.Tool
						mu.Unlock()
					}
					s := seq.Add(1)
					mu.Lock()
					batch = append(batch, TaskMessageData{
						Seq:   int(s),
						Type:  "tool_use",
						Tool:  msg.Tool,
						Input: msg.Input,
					})
					mu.Unlock()
				case agent.MessageToolResult:
					// Decrement only when the count would stay >= 0. A stray
					// tool_result with no matching tool_use (backend bug or
					// reconnect mid-stream) shouldn't push the counter
					// negative — that would re-arm the watchdog one tool_use
					// too early on the next call.
					for {
						cur := inFlightTools.Load()
						if cur <= 0 {
							break
						}
						if inFlightTools.CompareAndSwap(cur, cur-1) {
							break
						}
					}
					s := seq.Add(1)
					output := msg.Output
					if len(output) > 8192 {
						output = output[:8192]
					}
					toolName := msg.Tool
					if toolName == "" && msg.CallID != "" {
						mu.Lock()
						toolName = callIDToTool[msg.CallID]
						mu.Unlock()
					}
					taskLog.Info("tool_result observed", "seq", s, "tool", toolName, "call_id", msg.CallID)
					mu.Lock()
					batch = append(batch, TaskMessageData{
						Seq:    int(s),
						Type:   "tool_result",
						Tool:   toolName,
						Output: output,
					})
					mu.Unlock()
				case agent.MessageThinking:
					if msg.Content != "" {
						mu.Lock()
						pendingThinking.WriteString(msg.Content)
						mu.Unlock()
					}
				case agent.MessageText:
					if msg.Content != "" {
						taskLog.Debug("agent", "text", truncateLog(msg.Content, 200))
						mu.Lock()
						pendingText.WriteString(msg.Content)
						mu.Unlock()
					}
				case agent.MessageError:
					taskLog.Error("agent error", "content", msg.Content)
					s := seq.Add(1)
					mu.Lock()
					batch = append(batch, TaskMessageData{
						Seq:     int(s),
						Type:    "error",
						Content: msg.Content,
					})
					mu.Unlock()
				}
			case <-drainCtx.Done():
				goto drainDone
			}
		}
	drainDone:
		close(done)
		flush()
	}()

	select {
	case result := <-session.Result:
		if idleWatchdogFired.Load() {
			// The backend's wait goroutine (e.g. claude.go) translates the
			// SIGKILL we delivered via agentCancel into Status="aborted".
			// Re-tag it as "idle_watchdog" so runTask routes the
			// disposition through a dedicated failure_reason, not the
			// generic "agent_error" bucket the aborted path falls into.
			result.Status = "idle_watchdog"
			if result.Error == "" {
				result.Error = idleWatchdogReason(time.Duration(idleWatchdogThreshold.Load()))
			}
		}
		return result, toolCount.Load(), nil
	case <-drainCtx.Done():
		// Idle watchdog cancels via agentCancel(), which propagates here as
		// context.Canceled. Check this BEFORE the generic cancelled/timeout
		// classifiers so a watchdog-induced stop isn't misreported as
		// "task cancelled by server".
		if idleWatchdogFired.Load() {
			return agent.Result{
				Status: "idle_watchdog",
				Error:  idleWatchdogReason(time.Duration(idleWatchdogThreshold.Load())),
			}, toolCount.Load(), nil
		}
		// Distinguish external cancellation (e.g. server-initiated cancel
		// because the issue was reassigned, or the user invoked CancelTask)
		// from genuine drain-deadline timeouts. context.Canceled means the
		// upstream runCtx fired runCancel(); context.DeadlineExceeded is the
		// drain deadline expiring on its own.
		if errors.Is(drainCtx.Err(), context.Canceled) {
			return agent.Result{
				Status: "cancelled",
				Error:  "task cancelled by upstream context (server cancel or daemon shutdown)",
			}, toolCount.Load(), nil
		}
		return agent.Result{
			Status: "timeout",
			Error:  "agent did not produce result within drain timeout",
		}, toolCount.Load(), nil
	}
}

// idleWatchdogReason formats the human-facing explanation surfaced on
// idle_watchdog dispositions. Centralised so the result-arrival branch and the
// drain-timeout branch in executeAndDrain emit identical wording.
func idleWatchdogReason(window time.Duration) string {
	return fmt.Sprintf("agent produced no new messages for %s and message queue was empty; force-stopped by idle watchdog", window)
}

// runIdleWatchdog ticks until either agentCtx is cancelled or the backend has
// been silent past the applicable budget. On firing, it records the tripped
// threshold, sets fired, and calls cancel, which propagates to the agent
// subprocess (via the ctx passed to backend.Execute) and to drainCtx. The
// silence budget depends on whether a tool call is in flight:
//
//  1. No tool in flight — a silent backend is a hang after `window`.
//  2. A tool in flight (tool_use with no matching tool_result yet) — a real
//     tool (e.g. `npm install`, `docker build`) legitimately runs silently for
//     many minutes, so the larger `toolWindow` applies instead. toolWindow <= 0
//     keeps the historical behavior of never force-stopping while a tool is in
//     flight. Without this in-flight budget a backend that emits tool_use and
//     never the matching tool_result would run forever now that there is no
//     wall-clock cap (MUL-3064).
//
// In both cases the watchdog also requires the session.Messages buffer to be
// empty — a buffered-but-undrained message means the drain loop is behind, not
// the backend.
//
// Tick interval is window/2 (floored at 30 s in production, but the floor only
// kicks in for windows >= 1 min so tests can pass tiny windows like 50 ms and
// see the watchdog fire within a few ticks).
func (d *Daemon) runIdleWatchdog(agentCtx context.Context, window, toolWindow time.Duration, lastActivityAt *atomic.Int64, inFlightTools *atomic.Int32, fired *atomic.Bool, firedThreshold *atomic.Int64, cancel context.CancelFunc, messages <-chan agent.Message, taskLog *slog.Logger, taskID string) {
	interval := window / 2
	if window >= time.Minute && interval < 30*time.Second {
		interval = 30 * time.Second
	}
	if interval <= 0 {
		interval = window
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-agentCtx.Done():
			return
		case <-ticker.C:
			// Pick the silence budget. A tool in flight is expected to be
			// silent (a long build/install/test emits nothing between
			// tool_use and tool_result), so it gets the larger toolWindow;
			// toolWindow <= 0 disables the in-flight bound entirely.
			threshold := window
			toolInFlight := inFlightTools.Load() > 0
			if toolInFlight {
				if toolWindow <= 0 {
					continue
				}
				threshold = toolWindow
			}
			last := time.Unix(0, lastActivityAt.Load())
			idleFor := time.Since(last)
			if idleFor < threshold {
				continue
			}
			// A buffered-but-undrained message means the drain loop is
			// behind, not the backend. Wait one more tick rather than
			// killing a backend that is still producing output.
			if len(messages) > 0 {
				continue
			}
			taskLog.Warn("idle watchdog firing: no agent activity, force-stopping run",
				"task", shortID(taskID),
				"idle_for", idleFor.Round(time.Second).String(),
				"threshold", threshold.String(),
				"tool_in_flight", toolInFlight,
			)
			firedThreshold.Store(int64(threshold))
			fired.Store(true)
			cancel()
			return
		}
	}
}

func mergeUsage(a, b map[string]agent.TokenUsage) map[string]agent.TokenUsage {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make(map[string]agent.TokenUsage, len(a)+len(b))
	for model, u := range a {
		merged[model] = u
	}
	for model, u := range b {
		existing := merged[model]
		existing.InputTokens += u.InputTokens
		existing.OutputTokens += u.OutputTokens
		existing.CacheReadTokens += u.CacheReadTokens
		existing.CacheWriteTokens += u.CacheWriteTokens
		merged[model] = existing
	}
	return merged
}

// repoDataToInfo converts daemon RepoData to repocache RepoInfo.
func repoDataToInfo(repos []RepoData) []repocache.RepoInfo {
	info := make([]repocache.RepoInfo, len(repos))
	for i, r := range repos {
		info[i] = repocache.RepoInfo{URL: r.URL}
	}
	return info
}

func convertReposForEnv(repos []RepoData) []execenv.RepoContextForEnv {
	if len(repos) == 0 {
		return nil
	}
	result := make([]execenv.RepoContextForEnv, len(repos))
	for i, r := range repos {
		result[i] = execenv.RepoContextForEnv{URL: r.URL, Description: r.Description}
	}
	return result
}

func effectiveTaskExecutionPolicy(task Task) TaskExecutionPolicy {
	if task.ExecutionPolicy == nil {
		return TaskExecutionPolicy{
			RoleKind:         "agent",
			CanAccessRepo:    true,
			CanEditRepo:      true,
			ProjectSkillMode: "all",
		}
	}
	policy := *task.ExecutionPolicy
	if strings.TrimSpace(policy.RoleKind) == "" {
		policy.RoleKind = "agent"
	}
	if strings.TrimSpace(policy.ProjectSkillMode) == "" {
		policy.ProjectSkillMode = "all"
	}
	return policy
}

func convertExecutionPolicyForEnv(policy TaskExecutionPolicy) execenv.TaskExecutionPolicyForEnv {
	return execenv.TaskExecutionPolicyForEnv{
		RoleKey:          policy.RoleKey,
		RoleKind:         policy.RoleKind,
		CanAccessRepo:    policy.CanAccessRepo,
		CanEditRepo:      policy.CanEditRepo,
		ProjectSkillMode: policy.ProjectSkillMode,
	}
}

func mergeSkillContexts(base []execenv.SkillContextForEnv, extra []execenv.SkillContextForEnv) []execenv.SkillContextForEnv {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, skill := range base {
		seen[strings.ToLower(strings.TrimSpace(skill.Name))] = struct{}{}
	}
	out := append([]execenv.SkillContextForEnv(nil), base...)
	for _, skill := range extra {
		key := strings.ToLower(strings.TrimSpace(skill.Name))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, skill)
	}
	return out
}

func loadProjectSkillsForPolicy(repoDir string, policy TaskExecutionPolicy) ([]execenv.SkillContextForEnv, error) {
	if strings.TrimSpace(repoDir) == "" || policy.ProjectSkillMode == "none" {
		return nil, nil
	}
	var result []execenv.SkillContextForEnv
	for _, root := range []string{
		filepath.Join(repoDir, ".codebuddy", "skills"),
		filepath.Join(repoDir, ".codex", "skills"),
		filepath.Join(repoDir, ".claude", "skills"),
	} {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !projectSkillAllowedByPolicy(name, policy) {
				continue
			}
			skill, ok, err := readProjectSkill(filepath.Join(root, name), name)
			if err != nil {
				return nil, err
			}
			if ok {
				result = append(result, skill)
			}
		}
	}
	return result, nil
}

func projectSkillAllowedByPolicy(name string, policy TaskExecutionPolicy) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	switch policy.ProjectSkillMode {
	case "none":
		return false
	case "all":
		return true
	case "stage":
		for _, allowed := range policy.AllowedProjectSkills {
			if key == strings.ToLower(strings.TrimSpace(allowed)) {
				return true
			}
		}
		return false
	case "implementation":
		return key == "04-implement" || !isSOPStageSkillName(key)
	case "verification":
		return key == "05-verify" || !isSOPStageSkillName(key)
	default:
		return true
	}
}

func isSOPStageSkillName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "01-clarify", "02-design", "03-task-split", "04-implement", "05-verify":
		return true
	default:
		return false
	}
}

func readProjectSkill(dir string, name string) (execenv.SkillContextForEnv, bool, error) {
	content, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return execenv.SkillContextForEnv{}, false, nil
		}
		return execenv.SkillContextForEnv{}, false, err
	}
	skill := execenv.SkillContextForEnv{Name: name, Content: string(content)}
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "SKILL.md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		skill.Files = append(skill.Files, execenv.SkillFileContextForEnv{Path: rel, Content: string(data)})
		return nil
	})
	if err != nil {
		return execenv.SkillContextForEnv{}, false, err
	}
	return skill, true, nil
}

func gitPorcelainStatus(ctx context.Context, repoDir string) (string, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "git", "-C", repoDir, "status", "--porcelain=v1", "--untracked-files=all")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func convertProjectResourcesForEnv(resources []ProjectResourceData) []execenv.ProjectResourceForEnv {
	if len(resources) == 0 {
		return nil
	}
	result := make([]execenv.ProjectResourceForEnv, len(resources))
	for i, r := range resources {
		result[i] = execenv.ProjectResourceForEnv{
			ID:           r.ID,
			ResourceType: r.ResourceType,
			ResourceRef:  r.ResourceRef,
			Label:        r.Label,
		}
	}
	return result
}

// markActiveEnvRoot records that a task is currently using the given env root,
// so the GC loop won't reclaim its artifacts mid-execution. Calls are
// reference-counted so a reuse path marked twice (predicted + prior) only
// becomes inactive after both unmark calls.
func (d *Daemon) markActiveEnvRoot(envRoot string) {
	if envRoot == "" {
		return
	}
	d.activeEnvRootsMu.Lock()
	defer d.activeEnvRootsMu.Unlock()
	d.activeEnvRoots[envRoot]++
}

func (d *Daemon) unmarkActiveEnvRoot(envRoot string) {
	if envRoot == "" {
		return
	}
	d.activeEnvRootsMu.Lock()
	defer d.activeEnvRootsMu.Unlock()
	if d.activeEnvRoots[envRoot] <= 1 {
		delete(d.activeEnvRoots, envRoot)
		return
	}
	d.activeEnvRoots[envRoot]--
}

func (d *Daemon) isActiveEnvRoot(envRoot string) bool {
	d.activeEnvRootsMu.Lock()
	defer d.activeEnvRootsMu.Unlock()
	return d.activeEnvRoots[envRoot] > 0
}

// shortID returns the first 8 characters of an ID for readable logs.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// truncateLog truncates a string to maxLen, appending "…" if truncated.
// Also collapses newlines to spaces for single-line log output.
func truncateLog(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

func convertSkillsForEnv(skills []SkillData) []execenv.SkillContextForEnv {
	if len(skills) == 0 {
		return nil
	}
	result := make([]execenv.SkillContextForEnv, len(skills))
	for i, s := range skills {
		result[i] = execenv.SkillContextForEnv{
			Name:        s.Name,
			Description: s.Description,
			Content:     s.Content,
		}
		for _, f := range s.Files {
			result[i].Files = append(result[i].Files, execenv.SkillFileContextForEnv{
				Path:    f.Path,
				Content: f.Content,
			})
		}
	}
	return result
}

// composeOpenclawIncludeRoots returns the value the daemon should set for
// OPENCLAW_INCLUDE_ROOTS on the child openclaw process so its `$include`
// loader will follow the wrapper's reference out of envRoot into the
// user's active config directory.
//
// addRoot is the directory we must grant (typically dirname of the user's
// active openclaw.json). userValue is whatever the daemon's own
// environment already has under OPENCLAW_INCLUDE_ROOTS — the user's own
// cross-directory layout. We prepend addRoot, dedupe by string equality,
// drop empty path segments, and return ok=false when there's nothing to
// grant (addRoot is empty — fresh install case), so callers can leave the
// env var alone in that case.
//
// Path separator is the OS-native list separator (`:` on Unix, `;` on
// Windows) to match how OpenClaw splits the env var.
func composeOpenclawIncludeRoots(addRoot, userValue string) (string, bool) {
	if addRoot == "" {
		return "", false
	}
	parts := []string{addRoot}
	seen := map[string]struct{}{addRoot: {}}
	for _, p := range strings.Split(userValue, string(os.PathListSeparator)) {
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		parts = append(parts, p)
	}
	return strings.Join(parts, string(os.PathListSeparator)), true
}

// isBlockedEnvKey returns true if the key must not be overridden by user-
// configured custom_env. This prevents accidental or malicious override of
// daemon-internal variables and critical system paths.
func isBlockedEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "MULTICA_") {
		return true
	}
	switch upper {
	case "HOME", "PATH", "USER", "SHELL", "TERM", "CODEX_HOME", "CURSOR_DATA_DIR", "OPENCLAW_CONFIG_PATH", "OPENCLAW_INCLUDE_ROOTS":
		return true
	}
	return false
}

func defaultArgsForProvider(cfg Config, provider string) []string {
	var args []string
	switch provider {
	case "claude":
		args = cfg.ClaudeArgs
	case "codex":
		args = cfg.CodexArgs
	case "codebuddy":
		args = cfg.CodebuddyArgs
	default:
		return nil
	}
	return append([]string(nil), args...)
}

func executionPolicyForToolEnvelope(task Task, policy TaskExecutionPolicy) TaskExecutionPolicy {
	if strings.TrimSpace(task.SourceSummaryPrompt) != "" {
		return TaskExecutionPolicy{RoleKind: "agent", CanAccessRepo: false, CanEditRepo: false, ProjectSkillMode: "none"}
	}
	return policy
}

func allowedBuiltinToolsForExecutionPolicy(provider string, policy TaskExecutionPolicy) []string {
	if !supportsClaudeFamilyToolEnvelope(provider) {
		return nil
	}
	if isCoordinatorWithoutRepoAccess(policy) {
		return []string{"Bash"}
	}
	if isNoRepoBoundedStage(policy) {
		return nil
	}
	if isBoundedReviewStage(policy) {
		return []string{"Bash", "Read", "Grep", "Glob", "LS"}
	}
	if isImplementationStage(policy) {
		return []string{"Bash", "Read", "Grep", "Glob", "LS", "Edit", "Write", "MultiEdit", "NotebookRead", "NotebookEdit"}
	}
	return nil
}

func allowedToolsForExecutionPolicy(provider string, policy TaskExecutionPolicy) []string {
	if !supportsClaudeFamilyToolEnvelope(provider) {
		return nil
	}
	if isCoordinatorWithoutRepoAccess(policy) {
		return []string{"Bash(multica *)"}
	}
	if isNoRepoBoundedStage(policy) {
		return nil
	}
	return nil
}

func permissionModeForExecutionPolicy(provider string, policy TaskExecutionPolicy) string {
	if !supportsClaudeFamilyToolEnvelope(provider) {
		return ""
	}
	if isCoordinatorWithoutRepoAccess(policy) {
		return "bypassPermissions"
	}
	if isNoRepoBoundedStage(policy) {
		return "default"
	}
	return ""
}

func disallowedToolsForExecutionPolicy(provider string, policy TaskExecutionPolicy) []string {
	if !supportsClaudeFamilyToolEnvelope(provider) {
		return nil
	}
	nativeDelegationTools := []string{
		"Task",
		"TaskCreate",
		"TaskUpdate",
		"Agent",
		"TodoRead",
		"TodoWrite",
	}
	if isCoordinatorWithoutRepoAccess(policy) {
		return append(append([]string{}, nativeDelegationTools...),
			"Read",
			"Edit",
			"Write",
			"MultiEdit",
			"Grep",
			"Glob",
			"LS",
			"NotebookRead",
			"NotebookEdit",
		)
	}
	if isNoRepoBoundedStage(policy) {
		return append(append([]string{}, nativeDelegationTools...),
			"Bash",
			"Read",
			"Edit",
			"Write",
			"MultiEdit",
			"Grep",
			"Glob",
			"LS",
			"NotebookRead",
			"NotebookEdit",
		)
	}
	if isBoundedReviewStage(policy) {
		return append(append([]string{}, nativeDelegationTools...),
			"Edit",
			"Write",
			"MultiEdit",
			"NotebookEdit",
		)
	}
	if isImplementationStage(policy) {
		return nativeDelegationTools
	}
	return nil
}

func maxTurnsForExecutionPolicy(configured int, policy TaskExecutionPolicy) int {
	if configured > 0 {
		return configured
	}
	if isCoordinatorWithoutRepoAccess(policy) {
		return 12
	}
	return 0
}

func supportsClaudeFamilyToolEnvelope(provider string) bool {
	return provider == "claude" || provider == "codebuddy"
}

func isCoordinatorWithoutRepoAccess(policy TaskExecutionPolicy) bool {
	return strings.EqualFold(strings.TrimSpace(policy.RoleKind), "coordinator") && !policy.CanAccessRepo
}

func isNoRepoBoundedStage(policy TaskExecutionPolicy) bool {
	return isBoundedReviewStage(policy) && !policy.CanAccessRepo
}

func isBoundedReviewStage(policy TaskExecutionPolicy) bool {
	switch strings.ToLower(strings.TrimSpace(policy.RoleKind)) {
	case "planning_stage", "verification_stage":
		return true
	default:
		return false
	}
}

func isImplementationStage(policy TaskExecutionPolicy) bool {
	return strings.EqualFold(strings.TrimSpace(policy.RoleKind), "implementation_stage") && policy.CanAccessRepo && policy.CanEditRepo
}
